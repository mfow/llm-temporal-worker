package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

type queryHandlerFunc func(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error)

func (handler queryHandlerFunc) ExecuteQuery(ctx context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	return handler(ctx, request)
}

func queryRequest() llm.QueryRequestV1 {
	return llm.QueryRequestV1{
		APIVersion: llm.QueryAPIVersion, OperationKey: "query-1", Kind: llm.QueryProviderStatus,
		Context: llm.RequestContext{Tenant: "tenant", Project: "project", Actor: "workflow", Tags: map[string]string{"env": "test"}},
		Query:   json.RawMessage(`{"page_size":10}`),
	}
}

func queryResponse(request llm.QueryRequestV1) llm.QueryResponseV1 {
	return llm.QueryResponseV1{
		APIVersion: llm.QueryAPIVersion, OperationKey: request.OperationKey, QueryExecutionID: "query-execution-1",
		Kind: request.Kind, ObservedAt: "2026-07-20T00:00:00Z", Source: "persisted", Freshness: "current", Complete: true,
		Result: llm.ProviderStatusPage{Routes: []json.RawMessage{}},
		Cost:   llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "control_query_zero"},
	}
}

func stringPointer(value string) *string { return &value }

func testQueryService(handler Handler, authorize AuthorizeFunc) *QueryService {
	return &QueryService{Handler: handler, Authorize: authorize, CursorKey: []byte("query-test-key"), Clock: func() time.Time { return time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC) }}
}

func TestQueryServiceAuthorizesAndValidatesResponse(t *testing.T) {
	request := queryRequest()
	var seen Authorization
	service := testQueryService(queryHandlerFunc(func(_ context.Context, got llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return queryResponse(got), nil
	}), func(_ context.Context, authorization Authorization) error {
		seen = authorization
		return nil
	})
	response, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if response.QueryExecutionID == "" || seen.Tenant != "tenant" || seen.Project != "project" || seen.Actor != "workflow" || seen.Kind != llm.QueryProviderStatus {
		t.Fatalf("response/authorization = %#v / %#v", response, seen)
	}
}

func TestQueryServiceRejectsUnsupportedKindAndAuthorization(t *testing.T) {
	request := queryRequest()
	request.Kind = llm.QueryBudgetStatus
	service := testQueryService(queryHandlerFunc(func(_ context.Context, got llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return queryResponse(got), nil
	}), func(context.Context, Authorization) error { return nil })
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, ErrQueryUnsupported) {
		t.Fatalf("unsupported kind error = %v", err)
	}
	request = queryRequest()
	service.Authorize = func(context.Context, Authorization) error { return errors.New("missing role") }
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, ErrQueryAuthorization) {
		t.Fatalf("authorization error = %v", err)
	}
	request = queryRequest()
	request.Query = json.RawMessage(`{"page_size":1001}`)
	service.Authorize = func(context.Context, Authorization) error { return nil }
	if _, err := service.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "page_size") {
		t.Fatalf("page-size bound error = %v", err)
	}
}

func TestQueryServiceCursorsAreBoundToScopeAndFilter(t *testing.T) {
	service := testQueryService(queryHandlerFunc(func(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return queryResponse(request), nil
	}), func(context.Context, Authorization) error { return nil })
	request := queryRequest()
	issued := service.now()
	token, err := service.SignCursor(request, "route-2", issued)
	if err != nil {
		t.Fatalf("SignCursor() error = %v", err)
	}
	if err := service.ValidateCursor(request, token, issued.Add(time.Minute)); err != nil {
		t.Fatalf("ValidateCursor() error = %v", err)
	}
	request.Query = json.RawMessage(`{"page_size":20}`)
	if err := service.ValidateCursor(request, token, issued.Add(time.Minute)); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("filter-bound cursor error = %v", err)
	}
	request = queryRequest()
	request.Context.Tenant = "other-tenant"
	if err := service.ValidateCursor(request, token, issued.Add(time.Minute)); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("scope-bound cursor error = %v", err)
	}
	if err := service.ValidateCursor(queryRequest(), token, issued.Add(16*time.Minute)); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("expired cursor error = %v", err)
	}
}

func TestQueryServiceValidatesOutgoingCursorAgainstFreshTime(t *testing.T) {
	request := queryRequest()
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	clockCalls := 0
	var service *QueryService
	service = &QueryService{
		Authorize: func(context.Context, Authorization) error { return nil },
		CursorKey: []byte("query-test-key"),
		Clock: func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return base
			}
			return base.Add(3 * time.Minute)
		},
		Handler: queryHandlerFunc(func(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
			cursor, err := service.SignCursor(request, "route-2", base.Add(3*time.Minute))
			if err != nil {
				return llm.QueryResponseV1{}, err
			}
			response := queryResponse(request)
			response.NextCursor = &cursor
			return response, nil
		}),
	}
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() rejected a cursor signed after a slow handler: %v", err)
	}
	if clockCalls != 2 {
		t.Fatalf("clock calls = %d, want pre-handler and post-handler samples", clockCalls)
	}
}

func TestQueryServiceRejectsResponseMismatchAndUnsafeScope(t *testing.T) {
	request := queryRequest()
	service := testQueryService(queryHandlerFunc(func(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		response := queryResponse(request)
		response.OperationKey = "other"
		return response, nil
	}), func(context.Context, Authorization) error { return nil })
	if _, err := service.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("response mismatch error = %v", err)
	}
	request = queryRequest()
	request.Context.Actor = " workflow"
	if _, err := service.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe scope error = %v", err)
	}
}
