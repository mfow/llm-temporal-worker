package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
)

type queryHandlerFunc func(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error)

func (handler queryHandlerFunc) ExecuteQuery(ctx context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	return handler(ctx, request)
}

type typedQueryHandlerFunc func(context.Context, QueryRequest, *BoundCursorClaims) (QueryResponse, error)

func (handler typedQueryHandlerFunc) ExecuteTypedQuery(ctx context.Context, request QueryRequest, claims *BoundCursorClaims) (QueryResponse, error) {
	return handler(ctx, request, claims)
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

func typedProviderRequest() QueryRequest {
	return QueryRequest{
		OperationKey: "query-1",
		Scope: QueryScope{
			Tenant: "tenant", Project: "project", Actor: "workflow",
			Tags: map[string]string{"env": "test"},
		},
		Kind:   llm.QueryProviderStatus,
		Filter: ProviderStatusQuery{Page: QueryPage{Size: 10}},
	}
}

func typedModelRequest() QueryRequest {
	request := typedProviderRequest()
	request.Kind = llm.QueryModelInventory
	request.Filter = ModelInventoryQuery{Page: QueryPage{Size: 10}}
	return request
}

func typedRequestWire(t *testing.T, request QueryRequest) llm.QueryRequestV1 {
	t.Helper()
	wire, err := EncodeQueryRequest(request)
	if err != nil {
		t.Fatalf("EncodeQueryRequest() error = %v", err)
	}
	return wire
}

func typedProviderResponse(request QueryRequest, next *QueryCursor) QueryResponse {
	return QueryResponse{
		OperationKey: request.OperationKey, ExecutionID: "query-execution-typed", Kind: request.Kind,
		Provenance: QueryProvenance{Source: QuerySourcePersisted, Freshness: QueryFreshCurrent, ObservedAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)},
		Complete:   true, NextCursor: next,
		Result: ProviderStatusResult{Routes: []ProviderStatusRow{}},
		Cost:   QueryCost{Status: QueryCostExact, ActualUSD: decimalUSDPointer("0"), Method: QueryCostControlZero},
	}
}

func decimalUSDPointer(value string) *DecimalUSD {
	amount := DecimalUSD(value)
	return &amount
}

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

func TestQueryServiceDecodeCursorReturnsAuthenticatedPosition(t *testing.T) {
	service := testQueryService(queryHandlerFunc(func(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return queryResponse(request), nil
	}), func(context.Context, Authorization) error { return nil })
	request := queryRequest()
	issued := service.now()
	token, err := service.SignCursor(request, `{"after":"provider\u0000endpoint","horizon":"2026-07-20T00:00:00Z"}`, issued)
	if err != nil {
		t.Fatalf("SignCursor() error = %v", err)
	}
	claims, err := service.DecodeCursor(request, token, issued.Add(time.Minute))
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if claims.Kind != request.Kind || claims.Position == "" || !claims.IssuedAt.Equal(issued) || !claims.ExpiresAt.After(issued) {
		t.Fatalf("claims = %#v", claims)
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

func TestQueryServiceClassifiesRawHandlerFailureAsRetryableStateUnavailable(t *testing.T) {
	request := queryRequest()
	backendErr := errors.New("postgres unavailable: dsn=postgres://secret")
	service := testQueryService(queryHandlerFunc(func(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return llm.QueryResponseV1{}, backendErr
	}), func(context.Context, Authorization) error { return nil })

	_, err := service.Execute(context.Background(), request)
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want provider.Error", err, err)
	}
	if providerErr.Code != provider.CodeStateUnavailable || providerErr.Phase != provider.PhaseStateLoad || providerErr.Dispatch != provider.DispatchNotDispatched || providerErr.Retry != provider.RetrySameOperation {
		t.Fatalf("provider error = %#v", providerErr)
	}
	if !errors.Is(err, backendErr) {
		t.Fatalf("wrapped error = %v, want original cause for local diagnostics", err)
	}
	if strings.Contains(err.Error(), "postgres") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked backend details: %v", err)
	}
}

func TestQueryServicePreservesPreclassifiedProviderFailure(t *testing.T) {
	request := queryRequest()
	providerErr := provider.NewError(provider.CodePermissionDenied, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetryNever, "query access denied")
	service := testQueryService(queryHandlerFunc(func(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return llm.QueryResponseV1{}, providerErr
	}), func(context.Context, Authorization) error { return nil })

	_, err := service.Execute(context.Background(), request)
	if err != providerErr {
		t.Fatalf("error = %p, want original provider error %p", err, providerErr)
	}
}

func TestQueryServicePreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := testQueryService(queryHandlerFunc(func(handlerCtx context.Context, _ llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		cancel()
		return llm.QueryResponseV1{}, handlerCtx.Err()
	}), func(context.Context, Authorization) error { return nil })
	_, err := service.Execute(ctx, queryRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want caller cancellation", err)
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		t.Fatalf("error = %#v, caller cancellation must not be remapped", providerErr)
	}
}

func TestQueryServiceMapsChildDeadlineToRetryableStateUnavailable(t *testing.T) {
	service := testQueryService(queryHandlerFunc(func(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error) {
		return llm.QueryResponseV1{}, context.DeadlineExceeded
	}), func(context.Context, Authorization) error { return nil })
	_, err := service.Execute(context.Background(), queryRequest())
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want provider.Error", err, err)
	}
	if providerErr.Code != provider.CodeStateUnavailable || providerErr.Phase != provider.PhaseStateLoad || providerErr.Retry != provider.RetrySameOperation {
		t.Fatalf("provider error = %#v", providerErr)
	}
}

func TestQueryServiceTypedHandlerBindsCursorClaimsAndValidatesOutgoingCursor(t *testing.T) {
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	horizon := base.Add(-time.Minute)
	codec := &CursorCodec{Key: []byte("typed-query-key"), TTL: 15 * time.Minute}
	request := typedProviderRequest()
	var incoming *BoundCursorClaims
	service := &QueryService{
		TypedHandler: typedQueryHandlerFunc(func(_ context.Context, got QueryRequest, claims *BoundCursorClaims) (QueryResponse, error) {
			incoming = claims
			return typedProviderResponse(got, func() *QueryCursor {
				next, err := codec.Sign(got, "route-2", horizon, base)
				if err != nil {
					t.Fatalf("CursorCodec.Sign() error = %v", err)
				}
				return &next
			}()), nil
		}),
		Authorize:   func(context.Context, Authorization) error { return nil },
		CursorCodec: codec,
		Clock:       func() time.Time { return base },
	}
	first, err := service.Execute(context.Background(), typedRequestWire(t, request))
	if err != nil {
		t.Fatalf("first typed Execute() error = %v", err)
	}
	if first.NextCursor == nil {
		t.Fatal("first typed response did not contain a cursor")
	}
	claims, err := codec.Decode(request, QueryCursor(*first.NextCursor), base)
	if err != nil || claims.Position != "route-2" || !claims.Horizon.Equal(horizon) {
		t.Fatalf("outgoing claims = %#v, error = %v", claims, err)
	}

	request.Filter = ProviderStatusQuery{Page: QueryPage{Size: 10, Cursor: (*QueryCursor)(first.NextCursor)}}
	second, err := service.Execute(context.Background(), typedRequestWire(t, request))
	if err != nil {
		t.Fatalf("second typed Execute() error = %v", err)
	}
	if second.NextCursor == nil || incoming == nil || incoming.Position != "route-2" || !incoming.Horizon.Equal(horizon) {
		t.Fatalf("incoming claims = %#v, response = %#v", incoming, second)
	}
}

func TestQueryServiceRawHandlerAcceptsTypedCursorCodec(t *testing.T) {
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	codec := &CursorCodec{Key: []byte("typed-query-key")}
	typed := typedProviderRequest()
	token, err := codec.Sign(typed, "route-2", base.Add(-time.Minute), base)
	if err != nil {
		t.Fatalf("CursorCodec.Sign() error = %v", err)
	}
	typed.Filter = ProviderStatusQuery{Page: QueryPage{Size: 10, Cursor: &token}}
	request := typedRequestWire(t, typed)
	service := &QueryService{
		Handler: queryHandlerFunc(func(_ context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
			return queryResponse(request), nil
		}),
		Authorize:   func(context.Context, Authorization) error { return nil },
		CursorCodec: codec,
		Clock:       func() time.Time { return base },
	}
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("raw Handler with typed cursor error = %v", err)
	}
}

func TestQueryServiceTypedCursorBridgeRejectsInvalidClaims(t *testing.T) {
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	codec := &CursorCodec{Key: []byte("typed-query-key"), TTL: time.Minute}
	service := &QueryService{
		TypedHandler: typedQueryHandlerFunc(func(_ context.Context, request QueryRequest, _ *BoundCursorClaims) (QueryResponse, error) {
			return typedProviderResponse(request, nil), nil
		}),
		Authorize:   func(context.Context, Authorization) error { return nil },
		CursorCodec: codec,
		Clock:       func() time.Time { return base },
	}
	valid := typedProviderRequest()
	validToken, err := codec.Sign(valid, "route-2", base.Add(-time.Minute), base)
	if err != nil {
		t.Fatalf("CursorCodec.Sign() error = %v", err)
	}
	withCursor := func(request QueryRequest, token QueryCursor) llm.QueryRequestV1 {
		switch request.Kind {
		case llm.QueryProviderStatus:
			filter := request.Filter.(ProviderStatusQuery)
			filter.Page.Cursor = &token
			request.Filter = filter
		case llm.QueryModelInventory:
			filter := request.Filter.(ModelInventoryQuery)
			filter.Page.Cursor = &token
			request.Filter = filter
		case llm.QueryCreditStatus:
			filter := request.Filter.(CreditStatusQuery)
			filter.Page.Cursor = &token
			request.Filter = filter
		}
		return typedRequestWire(t, request)
	}
	mutateToken := func(token QueryCursor) QueryCursor {
		value := string(token)
		replacement := byte('A')
		if value[0] == replacement {
			replacement = 'B'
		}
		return QueryCursor(string(replacement) + value[1:])
	}
	tests := []struct {
		name    string
		request llm.QueryRequestV1
		wantErr error
	}{
		{name: "tamper", request: withCursor(valid, mutateToken(validToken)), wantErr: ErrQueryCursor},
		{name: "scope", request: withCursor(func() QueryRequest { copy := valid; copy.Scope.Tenant = "other"; return copy }(), validToken), wantErr: ErrQueryCursor},
		{name: "tag", request: withCursor(func() QueryRequest {
			copy := valid
			copy.Scope.Tags = map[string]string{"env": "production"}
			return copy
		}(), validToken), wantErr: ErrQueryCursor},
		{name: "filter", request: withCursor(func() QueryRequest {
			copy := valid
			copy.Filter = ProviderStatusQuery{Page: QueryPage{Size: 11}}
			return copy
		}(), validToken), wantErr: ErrQueryCursor},
		{name: "cross-kind", request: withCursor(typedModelRequest(), validToken), wantErr: ErrQueryCursor},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Execute(context.Background(), test.request); !errors.Is(err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, test.wantErr)
			}
		})
	}

	expired, err := codec.Sign(valid, "route-2", base.Add(-2*time.Minute), base.Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("expired CursorCodec.Sign() error = %v", err)
	}
	if _, err := service.Execute(context.Background(), withCursor(valid, expired)); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("expired cursor error = %v", err)
	}
	future, err := codec.Sign(valid, "route-2", base, base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("future CursorCodec.Sign() error = %v", err)
	}
	if _, err := service.Execute(context.Background(), withCursor(valid, future)); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("future cursor error = %v", err)
	}
	if _, err := codec.Sign(valid, "route-2", time.Time{}, base); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("zero-horizon sign error = %v", err)
	}
	limited := *codec
	limited.MaxPosition = 4
	if _, err := limited.Sign(valid, "route-2", base, base); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("oversized position sign error = %v", err)
	}
}

func TestQueryServiceTypedCursorBridgeRejectsInvalidOutgoingCursor(t *testing.T) {
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	service := &QueryService{
		TypedHandler: typedQueryHandlerFunc(func(_ context.Context, request QueryRequest, _ *BoundCursorClaims) (QueryResponse, error) {
			invalid := QueryCursor("not-a-signed-cursor")
			return typedProviderResponse(request, &invalid), nil
		}),
		Authorize:   func(context.Context, Authorization) error { return nil },
		CursorCodec: &CursorCodec{Key: []byte("typed-query-key")},
		Clock:       func() time.Time { return base },
	}
	if _, err := service.Execute(context.Background(), typedRequestWire(t, typedProviderRequest())); !errors.Is(err, ErrQueryCursor) {
		t.Fatalf("invalid outgoing cursor error = %v", err)
	}
}

func TestQueryServiceTypedHandlerResponseContractErrorIsNotMappedToStateUnavailable(t *testing.T) {
	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	service := &QueryService{
		TypedHandler: typedQueryHandlerFunc(func(_ context.Context, request QueryRequest, _ *BoundCursorClaims) (QueryResponse, error) {
			response := typedProviderResponse(request, nil)
			response.OperationKey = ""
			return response, nil
		}),
		Authorize:   func(context.Context, Authorization) error { return nil },
		CursorCodec: &CursorCodec{Key: []byte("typed-query-key")},
		Clock:       func() time.Time { return base },
	}
	_, err := service.Execute(context.Background(), typedRequestWire(t, typedProviderRequest()))
	if err == nil || !strings.Contains(err.Error(), "typed query response") {
		t.Fatalf("typed response contract error = %v", err)
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		t.Fatalf("typed response contract error was mapped to provider error: %#v", providerErr)
	}
}
