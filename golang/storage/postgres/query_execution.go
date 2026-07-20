package postgres

// Query execution persistence is the small audit boundary for llm.query.v1.
// It is deliberately independent of the Activity and query handlers: callers
// provide already-validated, redacted control JSON and this repository binds
// the scoped idempotency key, exact-or-unknown cost, and retention contract.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

const (
	DefaultQueryExecutionRetention = 24 * time.Hour
	MaxQueryExecutionJSONBytes     = 256 * 1024
)

var ErrQueryExecutionConflict = errors.New("query execution idempotency conflict")

// QueryExecutionRequest is the repository input after the query boundary has
// validated its closed request and response models. RequestJSON and
// ResponseJSON must contain only bounded, redacted control data; raw prompts,
// model output, provider bodies, and credentials are not valid audit fields.
type QueryExecutionRequest struct {
	Tenant                string
	Project               string
	OperationKey          string
	RequestFingerprint    [32]byte
	APIVersion            string
	Kind                  llm.QueryKind
	RequestJSON           []byte
	ResponseJSON          []byte
	ResponseDigest        [32]byte
	Source                string
	ActualCostUSD         *pricing.USD
	CostStatus            string
	CostMethod            string
	CostUnknownReasonCode string
	StartedAt             time.Time
	CompletedAt           time.Time
	RetentionExpiresAt    time.Time
}

// QueryExecutionRecord is the persisted, replayable audit row. Raw scope and
// operation values are intentionally absent; only their keyed digests remain.
type QueryExecutionRecord struct {
	ID                     uuid.UUID
	ScopeID                uuid.UUID
	APIVersion             string
	OperationKeyHMAC       [32]byte
	RequestFingerprintHMAC [32]byte
	Kind                   llm.QueryKind
	RequestJSON            []byte
	ResponseJSON           []byte
	ResponseDigest         [32]byte
	Source                 string
	ActualCostUSD          *pricing.USD
	CostStatus             string
	CostMethod             string
	CostUnknownReasonCode  string
	StartedAt              time.Time
	CompletedAt            time.Time
	RetentionExpiresAt     time.Time
}

type QueryExecutionRepository struct {
	Pool      *pgxpool.Pool
	Namespace Namespace
	Keys      Keyring
	Scopes    ScopeRepository
	Retention time.Duration
	NewID     func() (uuid.UUID, error)
	Now       func() time.Time
}

func DefaultQueryExecutionRepository(pool *pgxpool.Pool, namespace Namespace, keys Keyring, scopes ScopeRepository) QueryExecutionRepository {
	return QueryExecutionRepository{
		Pool: pool, Namespace: namespace, Keys: keys, Scopes: scopes,
		Retention: DefaultQueryExecutionRetention, NewID: UUIDv7, Now: time.Now,
	}
}

func (repository QueryExecutionRepository) validate() error {
	if repository.Pool == nil {
		return errors.New("query execution repository pool is nil")
	}
	if err := repository.Namespace.Validate(); err != nil {
		return err
	}
	if _, _, err := repository.Keys.activeKey(); err != nil {
		return err
	}
	if err := repository.Scopes.validate(); err != nil {
		return err
	}
	if repository.Retention <= 0 {
		return errors.New("query execution retention must be positive")
	}
	if repository.NewID == nil {
		return errors.New("query execution UUID generator is nil")
	}
	return nil
}

func (repository QueryExecutionRepository) clock() time.Time {
	if repository.Now != nil {
		return repository.Now().UTC()
	}
	return time.Now().UTC()
}

func canonicalQueryExecutionJSON(name string, data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxQueryExecutionJSONBytes {
		return nil, fmt.Errorf("query execution %s JSON must be between 1 and %d bytes", name, MaxQueryExecutionJSONBytes)
	}
	canonical, err := llm.CanonicalJSONWithLimits(data, MaxQueryExecutionJSONBytes, 64)
	if err != nil {
		return nil, fmt.Errorf("query execution %s JSON cannot be canonicalized: %w", name, err)
	}
	var value any
	if err := json.Unmarshal(canonical, &value); err != nil {
		return nil, fmt.Errorf("query execution %s canonical JSON is invalid: %w", name, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("query execution %s JSON must be an object", name)
	}
	if err := rejectSensitiveAuditFields(value); err != nil {
		return nil, fmt.Errorf("query execution %s JSON is not redacted: %w", name, err)
	}
	if len(canonical) > MaxQueryExecutionJSONBytes {
		return nil, fmt.Errorf("query execution %s canonical JSON exceeds %d bytes", name, MaxQueryExecutionJSONBytes)
	}
	return canonical, nil
}

func validateQueryExecutionJSON(name string, data []byte) error {
	_, err := canonicalQueryExecutionJSON(name, data)
	return err
}

var forbiddenAuditFieldNames = map[string]struct{}{
	"authorization": {}, "credential": {}, "credentials": {}, "model_output": {},
	"prompt": {}, "raw_provider_error": {}, "raw_response": {}, "response_body": {},
	"tool": {}, "tool_input": {}, "tool_output": {},
}

func rejectSensitiveAuditFields(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, forbidden := forbiddenAuditFieldNames[strings.ToLower(key)]; forbidden {
				return fmt.Errorf("field %q is forbidden", key)
			}
			if err := rejectSensitiveAuditFields(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectSensitiveAuditFields(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validQueryExecutionKind(kind llm.QueryKind) bool {
	switch kind {
	case llm.QueryProviderStatus, llm.QueryModelInventory, llm.QueryCreditStatus, llm.QueryBudgetStatus, llm.QuerySpendSummary:
		return true
	default:
		return false
	}
}

func validQueryExecutionSource(source string) bool {
	switch source {
	case "persisted", "persisted_and_refreshed", "redis_budget_generation":
		return true
	default:
		return false
	}
}

func validateQueryExecutionRequest(request QueryExecutionRequest, now time.Time) error {
	if request.Tenant == "" || request.Project == "" || strings.TrimSpace(request.Tenant) != request.Tenant || strings.TrimSpace(request.Project) != request.Project {
		return errors.New("query execution tenant and project are required and must be trimmed")
	}
	if request.OperationKey == "" || len(request.OperationKey) > 256 || strings.TrimSpace(request.OperationKey) != request.OperationKey || strings.ContainsAny(request.OperationKey, "\x00\r\n") {
		return errors.New("query execution operation key is empty or unsafe")
	}
	if request.RequestFingerprint == ([32]byte{}) {
		return errors.New("query execution request fingerprint is required")
	}
	if request.APIVersion != llm.QueryAPIVersion {
		return fmt.Errorf("query execution api version %q is unsupported", request.APIVersion)
	}
	if !validQueryExecutionKind(request.Kind) {
		return fmt.Errorf("query execution kind %q is invalid", request.Kind)
	}
	if err := validateQueryExecutionJSON("request", request.RequestJSON); err != nil {
		return err
	}
	if err := validateQueryExecutionJSON("response", request.ResponseJSON); err != nil {
		return err
	}
	if request.ResponseDigest == ([32]byte{}) || sha256.Sum256(request.ResponseJSON) != request.ResponseDigest {
		return errors.New("query execution response digest does not match response JSON")
	}
	if !validQueryExecutionSource(request.Source) {
		return fmt.Errorf("query execution source %q is invalid", request.Source)
	}
	if request.CostStatus != "exact" && request.CostStatus != "unknown" {
		return fmt.Errorf("query execution cost status %q is invalid", request.CostStatus)
	}
	if request.CostStatus == "exact" {
		if request.ActualCostUSD == nil || request.CostMethod == "" || request.CostUnknownReasonCode != "" {
			return errors.New("exact query execution cost requires amount and method only")
		}
		if err := request.ActualCostUSD.Validate(); err != nil {
			return err
		}
		switch request.CostMethod {
		case "control_query_zero", "provider_reported", "catalog_usage":
		default:
			return fmt.Errorf("query execution cost method %q is invalid", request.CostMethod)
		}
		if request.CostMethod == "control_query_zero" && !request.ActualCostUSD.IsZero() {
			return errors.New("control_query_zero requires zero actual cost")
		}
	} else if request.ActualCostUSD != nil || request.CostMethod != "" || !safeQueryExecutionCode(request.CostUnknownReasonCode) {
		return errors.New("unknown query execution cost requires reason and no amount or method")
	}
	if now.IsZero() {
		return errors.New("query execution clock is zero")
	}
	started := request.StartedAt.UTC()
	completed := request.CompletedAt.UTC()
	retention := request.RetentionExpiresAt.UTC()
	if started.IsZero() || completed.IsZero() || retention.IsZero() || completed.Before(started) || !retention.After(completed) {
		return errors.New("query execution timestamps are invalid")
	}
	if completed.After(now.Add(2 * time.Minute)) {
		return errors.New("query execution completion is too far in the future")
	}
	return nil
}

func safeQueryExecutionCode(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func (repository QueryExecutionRepository) Record(ctx context.Context, request QueryExecutionRequest) (QueryExecutionRecord, error) {
	var record QueryExecutionRecord
	if err := repository.validate(); err != nil {
		return record, err
	}
	now := repository.clock()
	if request.StartedAt.IsZero() {
		request.StartedAt = now
	}
	if request.CompletedAt.IsZero() {
		request.CompletedAt = now
	}
	if request.RetentionExpiresAt.IsZero() {
		request.RetentionExpiresAt = request.CompletedAt.Add(repository.Retention)
	}
	canonicalRequest, err := canonicalQueryExecutionJSON("request", request.RequestJSON)
	if err != nil {
		return record, err
	}
	canonicalResponse, err := canonicalQueryExecutionJSON("response", request.ResponseJSON)
	if err != nil {
		return record, err
	}
	request.RequestJSON = canonicalRequest
	request.ResponseJSON = canonicalResponse
	if err := validateQueryExecutionRequest(request, now); err != nil {
		return record, err
	}
	scope, err := repository.Scopes.Ensure(ctx, request.Tenant, request.Project)
	if err != nil {
		return record, err
	}
	_, key, err := repository.Keys.activeKey()
	if err != nil {
		return record, err
	}
	operationKeyHMAC := operationHMAC(key, "query-operation-key", []byte(request.OperationKey))
	requestFingerprintHMAC := operationHMAC(key, "query-request-fingerprint", request.RequestFingerprint[:])
	queryExecutions, err := repository.Namespace.Render("query_executions")
	if err != nil {
		return record, err
	}
	newID, err := repository.NewID()
	if err != nil {
		return record, fmt.Errorf("generate query execution id: %w", err)
	}
	if newID == uuid.Nil {
		return record, errors.New("query execution UUID generator returned nil")
	}
	actualCost, err := EncodeNullableUSD(request.ActualCostUSD)
	if err != nil {
		return record, err
	}
	insert := "INSERT INTO " + queryExecutions + " (query_execution_id, scope_id, api_version, operation_key_hmac, request_fingerprint_hmac, query_kind, request_jsonb, response_jsonb, response_digest, source, actual_cost_usd, cost_status, cost_method, cost_unknown_reason_code, started_at, completed_at, retention_expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$11,$12,NULLIF($13,''),NULLIF($14,''),$15,$16,$17) ON CONFLICT (scope_id, operation_key_hmac) DO NOTHING RETURNING query_execution_id"
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		var insertedID uuid.UUID
		err := tx.QueryRow(ctx, insert, newID, scope.ID, request.APIVersion, operationKeyHMAC[:], requestFingerprintHMAC[:], request.Kind, request.RequestJSON, request.ResponseJSON, request.ResponseDigest[:], request.Source, actualCost, request.CostStatus, request.CostMethod, request.CostUnknownReasonCode, request.StartedAt.UTC(), request.CompletedAt.UTC(), request.RetentionExpiresAt.UTC()).Scan(&insertedID)
		if err == nil {
			if insertedID != newID {
				return errors.New("PostgreSQL query execution identity changed")
			}
			record = queryExecutionRecordFromRequest(newID, scope.ID, operationKeyHMAC, requestFingerprintHMAC, request)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return redactPostgresError(fmt.Errorf("insert query execution: %w", err))
		}
		var existing QueryExecutionRecord
		var storedOperationKey, storedFingerprint, storedRequest, storedResponse, storedDigest []byte
		var storedMethod, storedReason *string
		var actualText *string
		if err := tx.QueryRow(ctx, "SELECT query_execution_id, api_version, operation_key_hmac, request_fingerprint_hmac, query_kind, request_jsonb, response_jsonb, response_digest, source, actual_cost_usd::text, cost_status, cost_method, cost_unknown_reason_code, started_at, completed_at, retention_expires_at FROM "+queryExecutions+" WHERE scope_id=$1 AND operation_key_hmac=$2 FOR UPDATE", scope.ID, operationKeyHMAC[:]).Scan(&existing.ID, &existing.APIVersion, &storedOperationKey, &storedFingerprint, &existing.Kind, &storedRequest, &storedResponse, &storedDigest, &existing.Source, &actualText, &existing.CostStatus, &storedMethod, &storedReason, &existing.StartedAt, &existing.CompletedAt, &existing.RetentionExpiresAt); err != nil {
			return redactPostgresError(fmt.Errorf("read existing query execution: %w", err))
		}
		if len(storedOperationKey) != len(existing.OperationKeyHMAC) || len(storedFingerprint) != len(existing.RequestFingerprintHMAC) {
			return errors.New("PostgreSQL query execution has invalid HMAC length")
		}
		copy(existing.OperationKeyHMAC[:], storedOperationKey)
		copy(existing.RequestFingerprintHMAC[:], storedFingerprint)
		if !hmac.Equal(storedFingerprint, requestFingerprintHMAC[:]) {
			return ErrQueryExecutionConflict
		}
		if err := hydrateQueryExecutionRecord(&existing, scope.ID, storedRequest, storedResponse, storedDigest, actualText, storedMethod, storedReason); err != nil {
			return err
		}
		record = existing
		return nil
	})
	if err != nil {
		return QueryExecutionRecord{}, err
	}
	return record, nil
}

func queryExecutionRecordFromRequest(id, scopeID uuid.UUID, operationKeyHMAC, requestFingerprintHMAC [32]byte, request QueryExecutionRequest) QueryExecutionRecord {
	return QueryExecutionRecord{ID: id, ScopeID: scopeID, APIVersion: request.APIVersion, OperationKeyHMAC: operationKeyHMAC, RequestFingerprintHMAC: requestFingerprintHMAC, Kind: request.Kind, RequestJSON: append([]byte(nil), request.RequestJSON...), ResponseJSON: append([]byte(nil), request.ResponseJSON...), ResponseDigest: request.ResponseDigest, Source: request.Source, ActualCostUSD: request.ActualCostUSD, CostStatus: request.CostStatus, CostMethod: request.CostMethod, CostUnknownReasonCode: request.CostUnknownReasonCode, StartedAt: request.StartedAt.UTC(), CompletedAt: request.CompletedAt.UTC(), RetentionExpiresAt: request.RetentionExpiresAt.UTC()}
}

func hydrateQueryExecutionRecord(record *QueryExecutionRecord, scopeID uuid.UUID, requestJSON, responseJSON, responseDigest []byte, actualText *string, method, reason *string) error {
	if record == nil || record.ID == uuid.Nil || scopeID == uuid.Nil || len(record.OperationKeyHMAC) != 32 || len(record.RequestFingerprintHMAC) != 32 || len(responseDigest) != 32 {
		return errors.New("PostgreSQL query execution has invalid digest or identity length")
	}
	canonicalRequest, err := canonicalQueryExecutionJSON("stored request", requestJSON)
	if err != nil {
		return err
	}
	canonicalResponse, err := canonicalQueryExecutionJSON("stored response", responseJSON)
	if err != nil {
		return err
	}
	record.ScopeID = scopeID
	record.RequestJSON = canonicalRequest
	record.ResponseJSON = canonicalResponse
	copy(record.ResponseDigest[:], responseDigest)
	if sha256.Sum256(record.ResponseJSON) != record.ResponseDigest {
		return errors.New("PostgreSQL query execution response digest does not match stored response JSON")
	}
	if actualText != nil {
		actual, err := DecodeUSD(*actualText)
		if err != nil {
			return err
		}
		record.ActualCostUSD = &actual
	}
	if method != nil {
		record.CostMethod = *method
	}
	if reason != nil {
		record.CostUnknownReasonCode = *reason
	}
	record.StartedAt = record.StartedAt.UTC()
	record.CompletedAt = record.CompletedAt.UTC()
	record.RetentionExpiresAt = record.RetentionExpiresAt.UTC()
	return nil
}
