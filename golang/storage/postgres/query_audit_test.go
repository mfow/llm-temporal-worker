package postgres

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func validQueryAuditRecord() control.QueryAuditRecord {
	request := []byte(`{"api_version":"llm.temporal/v1","operation_key":"query-1","context":{"tenant":"tenant","project":"project","actor":"workflow"},"kind":"provider_status","query":{"page_size":10}}`)
	response := []byte(`{"api_version":"llm.temporal/v1","operation_key":"query-1","query_execution_id":"execution-1","kind":"provider_status","observed_at":"2026-07-20T00:00:00Z","source":"persisted","freshness":"current","complete":true,"result":{"routes":[]},"cost_status":"exact","actual_cost_usd":"0","cost_method":"control_query_zero"}`)
	canonicalRequest, _ := llm.CanonicalJSONWithLimits(request, control.MaxQueryAuditJSONBytes, 64)
	canonicalResponse, _ := llm.CanonicalJSONWithLimits(response, control.MaxQueryAuditJSONBytes, 64)
	return control.QueryAuditRecord{
		Tenant: "tenant", Project: "project", OperationKey: "query-1",
		RequestFingerprint: sha256Digest(canonicalRequest), APIVersion: llm.QueryAPIVersion,
		Kind: llm.QueryProviderStatus, RequestJSON: request, ResponseJSON: response,
		ResponseDigest: sha256Digest(canonicalResponse), Source: "persisted",
		ActualCostUSD: stringPointer("0"), CostStatus: "exact", CostMethod: "control_query_zero",
		StartedAt:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, 7, 20, 0, 0, 1, 0, time.UTC),
	}
}

func sha256Digest(value []byte) [32]byte { return sha256.Sum256(value) }

func stringPointer(value string) *string { return &value }

func TestQueryExecutionRequestFromAuditConvertsExactCost(t *testing.T) {
	request, err := queryExecutionRequestFromAudit(validQueryAuditRecord())
	if err != nil {
		t.Fatalf("queryExecutionRequestFromAudit() error = %v", err)
	}
	if request.ActualCostUSD == nil || request.ActualCostUSD.String() != "0.000000000000000000" {
		t.Fatalf("actual cost = %#v", request.ActualCostUSD)
	}
	if request.RequestFingerprint == ([32]byte{}) || request.ResponseDigest == ([32]byte{}) {
		t.Fatal("audit digests were lost")
	}
}

func TestQueryExecutionRequestFromAuditPreservesUnknownCost(t *testing.T) {
	audit := validQueryAuditRecord()
	audit.ActualCostUSD = nil
	audit.CostStatus = "unknown"
	audit.CostMethod = ""
	audit.CostUnknownReasonCode = "catalog_missing"
	request, err := queryExecutionRequestFromAudit(audit)
	if err != nil {
		t.Fatalf("queryExecutionRequestFromAudit() error = %v", err)
	}
	if request.ActualCostUSD != nil || request.CostStatus != "unknown" || request.CostUnknownReasonCode != "catalog_missing" {
		t.Fatalf("unknown cost = %#v", request)
	}
}

func TestQueryExecutionRequestFromAuditRejectsFingerprintAndCostDrift(t *testing.T) {
	audit := validQueryAuditRecord()
	audit.RequestFingerprint[0]++
	if _, err := queryExecutionRequestFromAudit(audit); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("fingerprint error = %v", err)
	}
	audit = validQueryAuditRecord()
	audit.ActualCostUSD = stringPointer("0.0000000000000000001")
	if _, err := queryExecutionRequestFromAudit(audit); err == nil || !strings.Contains(err.Error(), "actual cost") {
		t.Fatalf("cost error = %v", err)
	}
}
