package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
	"github.com/mfow/llm-temporal-worker/budget"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/routing"
	"github.com/mfow/llm-temporal-worker/state"
	memory "github.com/mfow/llm-temporal-worker/storage/memory"
)

type fakeAdapter struct {
	mu          sync.Mutex
	name        string
	rejectFirst bool
	egressFirst bool
	ambiguous   bool
	invokes     int
	calls       []provider.Call
	response    llm.Response
}

func (adapter *fakeAdapter) Name() string { return adapter.name }

func (adapter *fakeAdapter) Capabilities(context.Context, provider.CapabilityQuery) (provider.CapabilitySet, error) {
	adapter.mu.Lock()
	adapter.capabilities++
	adapter.mu.Unlock()
	return provider.CapabilitySet{Version: "provider-cap-1", Features: map[provider.Feature]provider.Capability{
		provider.FeatureText:      {State: provider.CapabilityNative},
		provider.FeatureStreaming: {State: provider.CapabilityNative},
		provider.FeatureUsage:     {State: provider.CapabilityNative},
	}}, nil
}

func (adapter *fakeAdapter) Compile(_ context.Context, input provider.CompileInput) (provider.Call, error) {
	adapter.mu.Lock()
	adapter.compiles++
	adapter.mu.Unlock()
	return provider.Call{EndpointID: input.Query.EndpointID, Family: input.Query.Family, Model: input.Query.Model, OperationKey: input.Request.OperationKey, ServiceClass: input.Query.ServiceClass, Metadata: input.Metadata}, nil
}

func (adapter *fakeAdapter) Invoke(ctx context.Context, call provider.Call, observer provider.Observer) (provider.Result, error) {
	adapter.mu.Lock()
	adapter.invokes++
	index := adapter.invokes
	adapter.calls = append(adapter.calls, call)
	response := adapter.response
	adapter.mu.Unlock()
	if err := observer.BeforePossibleWrite(ctx); err != nil {
		return provider.Result{}, err
	}
	if adapter.ambiguous {
		return provider.Result{}, provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchAmbiguous, provider.RetryNever, "provider outcome is ambiguous")
	}
	if adapter.rejectFirst && index == 1 {
		return provider.Result{}, provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchRejected, provider.RetryNextRoute, "provider rejected dispatch")
	}
	if adapter.egressFirst && index == 1 {
		return provider.Result{}, provider.NewEgressDeniedError(provider.ErrProviderEgressDenied)
	}
	observer.OnProgress(ctx, provider.Progress{Phase: string(provider.PhaseStream), OutputItems: len(response.Output)})
	return provider.Result{Response: response}, nil
}

type fakeResultStore struct {
	mu     sync.Mutex
	values map[string]llm.Response
	gets   int
	puts   int
}

func (store *fakeResultStore) Get(_ context.Context, operationID string) (llm.Response, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.gets++
	value, ok := store.values[operationID]
	if !ok {
		return llm.Response{}, ErrResultNotFound
	}
	return value, nil
}

func (store *fakeResultStore) Put(_ context.Context, operationID string, response llm.Response) (state.BlobRef, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.puts++
	if _, ok := store.values[operationID]; ok {
		return resultRef(operationID), nil
	}
	if store.values == nil {
		store.values = make(map[string]llm.Response)
	}
	store.values[operationID] = response
	return resultRef(operationID), nil
}

func resultRef(operationID string) state.BlobRef {
	return state.BlobRef{Digest: sha256.Sum256([]byte(operationID)), Size: int64(len(operationID)), Media: "application/json"}
}

type testHarness struct {
	engine    *Engine
	admission *memory.AdmissionStore
	results   *fakeResultStore
	clock     time.Time
}

func newHarness(t *testing.T, adapter provider.Adapter) testHarness {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	classes := []llm.ServiceClass{llm.ServiceClassEconomy, llm.ServiceClassStandard, llm.ServiceClassPriority}
	tiers := map[llm.ServiceClass]string{
		llm.ServiceClassEconomy:  "economy-tier",
		llm.ServiceClassStandard: "standard-tier",
		llm.ServiceClassPriority: "priority-tier",
	}
	routes, err := routing.CompileCatalog("routes-1", map[string]routing.Model{
		"logical-model": {Routes: []routing.Route{{
			ID: "route-1", EndpointID: "endpoint-1", Provider: "provider-1", Family: string(provider.FamilyOpenAIResponses), Region: "us-east-1", AccountRegion: "us-east-1", Model: "provider-model", ModelLineage: "provider-lineage", Classes: classes, ProviderTiers: tiers, PriceVersion: "prices-1", PriceAvailable: true,
			Capabilities: routing.CapabilitySet{Version: "route-cap-1", Features: map[routing.Feature]routing.Capability{routing.FeatureText: {State: routing.CapabilityNative}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]pricing.Entry, 0, len(classes))
	for _, class := range classes {
		entries = append(entries, pricing.Entry{Provider: "provider-1", Family: string(provider.FamilyOpenAIResponses), EndpointID: "endpoint-1", Region: "us-east-1", Model: "provider-model", ProviderTier: tiers[class], Currency: "USD", Version: "prices-1", Prices: pricing.UnitPrices{PerRequest: pricing.MustDecimalUSD("0.000001"), OutputPerMillion: pricing.MustDecimalUSD("1")}})
	}
	priceCatalog, err := pricing.CompileCatalog("prices-1", "USD", entries)
	if err != nil {
		t.Fatal(err)
	}
	results := &fakeResultStore{values: make(map[string]llm.Response)}
	admissionStore := memory.NewAdmissionStore(memory.AdmissionOptions{Clock: func() time.Time { return now }})
	engineValue, err := New(Dependencies{
		Snapshots:   StaticSnapshot{Value: Snapshot{Version: "snapshot-1", Routes: routes, Prices: pricing.NewResolver(priceCatalog), ReservationLease: time.Minute, OperationRetention: time.Hour}},
		Planner:     routing.DeterministicPlanner{},
		Adapters:    AdapterMap{"endpoint-1": adapter},
		Admission:   admissionStore,
		Results:     results,
		Clock:       func() time.Time { return now },
		Estimator:   budget.Estimator{MaxOutput: 1},
		MaxAttempts: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testHarness{engine: engineValue, admission: admissionStore, results: results, clock: now}
}

func baseRequest(operationKey string) llm.Request {
	return llm.Request{OperationKey: operationKey, Context: llm.RequestContext{Tenant: "tenant-1"}, Model: "logical-model", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}
}

func successfulResponse() llm.Response {
	return llm.Response{Status: llm.ResponseStatusCompleted, Output: []llm.Item{llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: "world"}}}}, Usage: llm.Usage{OutputTokens: 1}, Provider: llm.ProviderFacts{RequestID: "provider-request-1"}}
}

func TestGenerateDefaultsOmittedServiceClassToStandard(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
	harness := newHarness(t, adapter)
	response, err := harness.engine.Generate(context.Background(), baseRequest("default-class"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Service.Requested != llm.ServiceClassStandard || response.Service.Attempted != llm.ServiceClassStandard {
		t.Fatalf("service facts = %#v, want standard/standard", response.Service)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.calls) != 1 || adapter.calls[0].ServiceClass != llm.ServiceClassStandard {
		t.Fatalf("provider calls = %#v, want one standard call", adapter.calls)
	}
}

func TestGenerateFallbackDoesNotReplayRejectedDispatch(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", rejectFirst: true, response: successfulResponse()}
	harness := newHarness(t, adapter)
	request := baseRequest("fallback")
	request.ServiceClass = llm.ServiceClassPriority
	request.ServiceClassFallbacks = []llm.ServiceClass{llm.ServiceClassStandard}
	response, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Service.Requested != llm.ServiceClassPriority || response.Service.Attempted != llm.ServiceClassStandard || response.Service.FallbackIndex != 1 {
		t.Fatalf("service facts = %#v, want priority requested and standard fallback", response.Service)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.calls) != 2 || adapter.calls[0].ServiceClass != llm.ServiceClassPriority || adapter.calls[1].ServiceClass != llm.ServiceClassStandard {
		t.Fatalf("provider calls = %#v, want priority then standard", adapter.calls)
	}
	operation, err := harness.admission.Get(context.Background(), response.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != admissionStateCompleted {
		t.Fatalf("operation state = %s, want completed", operation.State)
	}
}

func TestGenerateEgressDenialAfterMarkFallsBackWithoutAmbiguity(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", egressFirst: true, response: successfulResponse()}
	harness := newHarness(t, adapter)
	request := baseRequest("egress-fallback")
	request.ServiceClass = llm.ServiceClassPriority
	request.ServiceClassFallbacks = []llm.ServiceClass{llm.ServiceClassStandard}
	response, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Service.Attempted != llm.ServiceClassStandard || response.Service.FallbackIndex != 1 {
		t.Fatalf("service facts = %#v, want standard fallback", response.Service)
	}
	adapter.mu.Lock()
	invokes := adapter.invokes
	adapter.mu.Unlock()
	if invokes != 2 {
		t.Fatalf("provider invoke count = %d, want two attempts", invokes)
	}
	operation, err := harness.admission.Get(context.Background(), response.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != admission.StateCompleted || operation.Attempt.Dispatch != admission.Accepted {
		t.Fatalf("operation after fallback = %#v, want completed accepted operation", operation)
	}
	if got, want := int64(operation.IncurredMicroUSD), response.Cost.ActualMicroUSD; got != want {
		t.Fatalf("incurred cost = %d, want fallback actual cost %d", got, want)
	}
}

func TestGenerateEgressDenialAfterMarkRecordsNoCost(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", egressFirst: true, response: successfulResponse()}
	harness := newHarness(t, adapter)
	_, err := harness.engine.Generate(context.Background(), baseRequest("egress-no-cost"))
	if err == nil {
		t.Fatal("egress-denied request unexpectedly succeeded")
	}
	var mapped *provider.Error
	if !errors.As(err, &mapped) {
		t.Fatalf("error = %T, want *provider.Error", err)
	}
	if mapped.Code != provider.CodeProviderUnavailable || mapped.Dispatch != provider.DispatchNotDispatched || mapped.Retry != provider.RetryNextRoute {
		t.Fatalf("mapped egress error = %#v", mapped)
	}
	if !errors.Is(mapped, provider.ErrProviderEgressDenied) {
		t.Fatal("mapped egress error did not retain its marker")
	}
	operation, err := harness.admission.Get(context.Background(), mapped.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != admission.StateDefiniteFailed || operation.Attempt.Dispatch != admission.NotDispatched || operation.IncurredMicroUSD != 0 || operation.FinalMicroUSD != 0 {
		t.Fatalf("operation after egress denial = %#v, want definite zero-cost no-dispatch failure", operation)
	}
}

func TestGenerateReplaysCompletedResultWithoutProviderCall(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
	harness := newHarness(t, adapter)
	request := baseRequest("replay")
	first, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID == "" || first.OperationID != second.OperationID || first.Output[0].(llm.Message).Content[0].(llm.TextPart).Text != second.Output[0].(llm.Message).Content[0].(llm.TextPart).Text {
		t.Fatalf("replayed response differs: first=%#v second=%#v", first, second)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.invokes != 1 {
		t.Fatalf("provider invoke count = %d, want one", adapter.invokes)
	}
	if harness.results.puts != 1 {
		t.Fatalf("result writes = %d, want one", harness.results.puts)
	}
}

func TestGenerateDoesNotRetryAmbiguousOperation(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", ambiguous: true, response: successfulResponse()}
	harness := newHarness(t, adapter)
	request := baseRequest("ambiguous")
	_, firstErr := harness.engine.Generate(context.Background(), request)
	if firstErr == nil {
		t.Fatal("first ambiguous request unexpectedly succeeded")
	}
	var firstProviderErr *provider.Error
	if !errors.As(firstErr, &firstProviderErr) || firstProviderErr.Code != provider.CodeAmbiguousDispatch {
		t.Fatalf("first error = %#v, want ambiguous dispatch", firstErr)
	}
	_, secondErr := harness.engine.Generate(context.Background(), request)
	if secondErr == nil {
		t.Fatal("replayed ambiguous request unexpectedly succeeded")
	}
	var secondProviderErr *provider.Error
	if !errors.As(secondErr, &secondProviderErr) || secondProviderErr.Code != provider.CodeAmbiguousDispatch {
		t.Fatalf("second error = %#v, want ambiguous dispatch", secondErr)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.invokes != 1 {
		t.Fatalf("provider invoke count = %d, want one", adapter.invokes)
	}
}

// Keep the test independent of an exported alias solely to make the expected
// admission state obvious at the assertion site.
const admissionStateCompleted = "completed"
