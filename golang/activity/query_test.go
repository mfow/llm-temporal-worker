package activity

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

type queryServiceStub struct {
	called bool
}

func (stub *queryServiceStub) Execute(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	stub.called = true
	return llm.QueryResponseV1{
		APIVersion: llm.QueryAPIVersion, OperationKey: request.OperationKey, QueryExecutionID: "query-execution-1", Kind: request.Kind,
		ObservedAt: "2026-07-20T00:00:00Z", Source: "persisted", Freshness: "current", Complete: true,
		Result: llm.ProviderStatusPage{Routes: []json.RawMessage{}},
		Cost:   llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "control_query_zero"},
	}, nil
}

func TestQueryV1UsesIndependentQueryService(t *testing.T) {
	service := &queryServiceStub{}
	activities := &Activities{QueryService: service}
	response, err := activities.QueryV1(context.Background(), validQueryV1Request())
	if err != nil {
		t.Fatalf("QueryV1 error = %v", err)
	}
	if !service.called || response == nil || response.QueryExecutionID != "query-execution-1" {
		t.Fatalf("service called = %v, response = %#v", service.called, response)
	}
	if _, err := activities.GenerateV1(context.Background(), validGenerateV1Request()); err == nil {
		t.Fatal("GenerateV1 unexpectedly succeeded without a durable V1 runtime")
	}
}

func TestRegisterIncludesQueryActivityForQueryOnlyService(t *testing.T) {
	registry := &v1Registry{}
	activities := &Activities{QueryService: &queryServiceStub{}}
	activities.Register(registry)
	if len(registry.names) != 3 || registry.names[2] != QueryActivityName {
		t.Fatalf("registered names = %v, want v1 activities including %q", registry.names, QueryActivityName)
	}
}
