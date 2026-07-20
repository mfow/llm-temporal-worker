package control

// QueryService is the deliberately small control-plane boundary used by the
// llm.query.v1 Activity. It validates the closed wire request, authorizes the
// tenant scope, verifies pagination cursors, and validates the handler result.
// Persisted provider/model/credit reads, budget reads, and spend aggregation
// are supplied by a later composition layer; this package does not perform
// storage or provider calls.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

var (
	ErrQueryAuthorization = errors.New("query authorization denied")
	ErrQueryCursor        = errors.New("query cursor is invalid")
	ErrQueryUnsupported   = errors.New("query kind is not implemented")
)

// Authorization is the stable, content-free input to a query authorizer.
// Query payloads are intentionally not passed to authorization callbacks.
type Authorization struct {
	Tenant  string
	Project string
	Actor   string
	Kind    llm.QueryKind
}

type AuthorizeFunc func(context.Context, Authorization) error

// Handler owns the actual read implementation. Keeping it behind this
// interface lets the Activity boundary be tested without a database and keeps
// storage-specific query plans out of the wire adapter.
type Handler interface {
	ExecuteQuery(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error)
}

// QueryService is safe to share between Activity invocations. Clock is a test
// seam and must return UTC time. CursorKey is an HMAC key kept in worker
// configuration; cursors are never accepted without one.
type QueryService struct {
	Handler   Handler
	Authorize AuthorizeFunc
	CursorKey []byte
	CursorTTL time.Duration
	Clock     func() time.Time
}

func (service *QueryService) Execute(ctx context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	if service == nil || service.Handler == nil || service.Authorize == nil {
		return llm.QueryResponseV1{}, fmt.Errorf("query service is not configured")
	}
	if ctx == nil {
		return llm.QueryResponseV1{}, fmt.Errorf("query context is nil")
	}
	if err := ctx.Err(); err != nil {
		return llm.QueryResponseV1{}, err
	}
	if request.APIVersion != llm.QueryAPIVersion {
		return llm.QueryResponseV1{}, fmt.Errorf("query api version %q is unsupported", request.APIVersion)
	}
	if !supportedQueryKind(request.Kind) {
		return llm.QueryResponseV1{}, fmt.Errorf("%w: %s", ErrQueryUnsupported, request.Kind)
	}
	if err := validatePageSize(request.Query); err != nil {
		return llm.QueryResponseV1{}, err
	}
	// MarshalJSON is the closed-request validator: it rejects unknown query
	// fields, non-object filters, invalid enums, and page sizes outside the
	// 1..1000 bound before a handler can touch storage.
	if _, err := json.Marshal(request); err != nil {
		return llm.QueryResponseV1{}, fmt.Errorf("query request is invalid: %w", err)
	}
	if err := validateScope(request.Context); err != nil {
		return llm.QueryResponseV1{}, err
	}
	if err := service.Authorize(ctx, Authorization{
		Tenant: request.Context.Tenant, Project: request.Context.Project,
		Actor: request.Context.Actor, Kind: request.Kind,
	}); err != nil {
		return llm.QueryResponseV1{}, fmt.Errorf("%w: %v", ErrQueryAuthorization, err)
	}
	now := service.now()
	if cursor, ok, err := requestCursor(request.Query); err != nil {
		return llm.QueryResponseV1{}, err
	} else if ok {
		if err := service.ValidateCursor(request, cursor, now); err != nil {
			return llm.QueryResponseV1{}, err
		}
	}
	response, err := service.Handler.ExecuteQuery(ctx, request)
	if err != nil {
		return llm.QueryResponseV1{}, err
	}
	if err := validateResponse(request, response); err != nil {
		return llm.QueryResponseV1{}, err
	}
	if response.NextCursor != nil {
		if err := service.ValidateCursor(request, *response.NextCursor, now); err != nil {
			return llm.QueryResponseV1{}, fmt.Errorf("next_cursor: %w", err)
		}
	}
	return response, nil
}

func supportedQueryKind(kind llm.QueryKind) bool {
	return kind == llm.QueryProviderStatus || kind == llm.QueryModelInventory || kind == llm.QueryCreditStatus
}

func validateScope(scope llm.RequestContext) error {
	for name, value := range map[string]string{"tenant": scope.Tenant, "project": scope.Project, "actor": scope.Actor} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("query %s scope is empty or unsafe", name)
		}
	}
	if len(scope.Tags) > 32 {
		return fmt.Errorf("query scope has too many tags")
	}
	for key, value := range scope.Tags {
		if value == "" || len(key) > 128 || len(value) > 256 || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value || strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("query scope tag is unsafe")
		}
	}
	return nil
}

type cursorEnvelope struct {
	Kind      llm.QueryKind `json:"kind"`
	Scope     string        `json:"scope"`
	QueryHash string        `json:"query_hash"`
	Position  string        `json:"position"`
	IssuedAt  int64         `json:"issued_at"`
	ExpiresAt int64         `json:"expires_at"`
}

// SignCursor creates a scope- and filter-bound cursor for a handler's opaque
// position. The position is never interpreted by this package.
func (service *QueryService) SignCursor(request llm.QueryRequestV1, position string, issuedAt time.Time) (string, error) {
	if service == nil || len(service.CursorKey) == 0 || position == "" {
		return "", ErrQueryCursor
	}
	if request.APIVersion != llm.QueryAPIVersion || !supportedQueryKind(request.Kind) || validateScope(request.Context) != nil {
		return "", ErrQueryCursor
	}
	issuedAt = issuedAt.UTC()
	ttl := service.CursorTTL
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 15 * time.Minute
	}
	envelope := cursorEnvelope{Kind: request.Kind, Scope: scopeString(request.Context), QueryHash: queryHash(request.Query), Position: position, IssuedAt: issuedAt.Unix(), ExpiresAt: issuedAt.Add(ttl).Unix()}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", ErrQueryCursor
	}
	signature := hmac.New(sha256.New, service.CursorKey)
	_, _ = signature.Write(payload)
	encode := base64.RawURLEncoding
	return encode.EncodeToString(payload) + "." + encode.EncodeToString(signature.Sum(nil)), nil
}

func (service *QueryService) ValidateCursor(request llm.QueryRequestV1, token string, now time.Time) error {
	if service == nil || len(service.CursorKey) == 0 || len(token) > 2048 {
		return ErrQueryCursor
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ErrQueryCursor
	}
	decode := base64.RawURLEncoding
	payload, err := decode.DecodeString(parts[0])
	if err != nil {
		return ErrQueryCursor
	}
	provided, err := decode.DecodeString(parts[1])
	if err != nil {
		return ErrQueryCursor
	}
	signature := hmac.New(sha256.New, service.CursorKey)
	_, _ = signature.Write(payload)
	if !hmac.Equal(provided, signature.Sum(nil)) {
		return ErrQueryCursor
	}
	var envelope cursorEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Kind != request.Kind || envelope.Scope != scopeString(request.Context) || envelope.QueryHash != queryHash(request.Query) || envelope.Position == "" {
		return ErrQueryCursor
	}
	now = now.UTC()
	if now.IsZero() || envelope.IssuedAt > now.Add(2*time.Minute).Unix() || envelope.ExpiresAt <= now.Unix() || envelope.ExpiresAt <= envelope.IssuedAt {
		return ErrQueryCursor
	}
	return nil
}

func requestCursor(raw json.RawMessage) (string, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", false, fmt.Errorf("query object is invalid: %w", err)
	}
	value, ok := fields["cursor"]
	if !ok || string(value) == "null" {
		return "", false, nil
	}
	var cursor string
	if err := json.Unmarshal(value, &cursor); err != nil || cursor == "" {
		return "", false, ErrQueryCursor
	}
	return cursor, true, nil
}

func validatePageSize(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("query object is invalid: %w", err)
	}
	value, ok := fields["page_size"]
	if !ok {
		return nil
	}
	var pageSize int
	if err := json.Unmarshal(value, &pageSize); err != nil || pageSize < 1 || pageSize > 1000 {
		return fmt.Errorf("query page_size must be between 1 and 1000")
	}
	return nil
}

func queryHash(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	delete(fields, "cursor")
	canonical, _ := json.Marshal(fields)
	digest := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func scopeString(scope llm.RequestContext) string {
	canonical, _ := json.Marshal(struct {
		Tenant  string            `json:"tenant"`
		Project string            `json:"project"`
		Actor   string            `json:"actor"`
		Tags    map[string]string `json:"tags,omitempty"`
	}{scope.Tenant, scope.Project, scope.Actor, scope.Tags})
	return base64.RawURLEncoding.EncodeToString(canonical)
}

func (service *QueryService) now() time.Time {
	if service != nil && service.Clock != nil {
		return service.Clock().UTC()
	}
	return time.Now().UTC()
}

func validateResponse(request llm.QueryRequestV1, response llm.QueryResponseV1) error {
	if response.APIVersion != llm.QueryAPIVersion || response.OperationKey != request.OperationKey || response.Kind != request.Kind {
		return fmt.Errorf("query response does not match request")
	}
	if _, err := json.Marshal(response); err != nil {
		return fmt.Errorf("query response is invalid: %w", err)
	}
	return nil
}
