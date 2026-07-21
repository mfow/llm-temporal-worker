package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/mfow/llm-temporal-worker/golang/control"
	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

var _ control.AuditFunc = (QueryExecutionRepository{}).RecordAudit

// RecordAudit adapts the storage-neutral control-plane audit callback to the
// PostgreSQL query-execution ledger. It is safe to assign directly to
// control.QueryService.Audit. Record remains the single persistence path, so
// the adapter inherits its redaction, digest, retention, and idempotency
// checks.
func (repository QueryExecutionRepository) RecordAudit(ctx context.Context, audit control.QueryAuditRecord) error {
	request, err := queryExecutionRequestFromAudit(audit)
	if err != nil {
		return err
	}
	_, err = repository.Record(ctx, request)
	return err
}

func queryExecutionRequestFromAudit(audit control.QueryAuditRecord) (QueryExecutionRequest, error) {
	canonicalRequest, err := llm.CanonicalJSONWithLimits(audit.RequestJSON, control.MaxQueryAuditJSONBytes, 64)
	if err != nil {
		return QueryExecutionRequest{}, fmt.Errorf("query audit request JSON: %w", err)
	}
	if sha256.Sum256(canonicalRequest) != audit.RequestFingerprint {
		return QueryExecutionRequest{}, fmt.Errorf("query audit request fingerprint does not match request JSON")
	}
	var actualCost *pricing.USD
	if audit.ActualCostUSD != nil {
		parsed, err := pricing.ParseUSD(*audit.ActualCostUSD)
		if err != nil {
			return QueryExecutionRequest{}, fmt.Errorf("query audit actual cost: %w", err)
		}
		actualCost = &parsed
	}
	return QueryExecutionRequest{
		Tenant:                audit.Tenant,
		Project:               audit.Project,
		OperationKey:          audit.OperationKey,
		RequestFingerprint:    audit.RequestFingerprint,
		APIVersion:            audit.APIVersion,
		Kind:                  audit.Kind,
		RequestJSON:           canonicalRequest,
		ResponseJSON:          audit.ResponseJSON,
		ResponseDigest:        audit.ResponseDigest,
		Source:                audit.Source,
		ActualCostUSD:         actualCost,
		CostStatus:            audit.CostStatus,
		CostMethod:            audit.CostMethod,
		CostUnknownReasonCode: audit.CostUnknownReasonCode,
		StartedAt:             audit.StartedAt,
		CompletedAt:           audit.CompletedAt,
	}, nil
}
