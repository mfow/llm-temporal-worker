package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
	"github.com/mfow/llm-temporal-worker/budget"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/pricing"
)

func TestGenerateSkipsBudgetedUnpricedCandidateAndUsesPricedFallback(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
	harness := newHarness(t, adapter)
	snapshot := harness.engine.dependencies.Snapshots.(StaticSnapshot).Value
	snapshot.Prices = pricing.NewResolver(testPriceCatalog(t, priceEntryForTier("standard-tier")))
	snapshot.BudgetPolicies = []budget.Policy{{
		ID:      "priority-only",
		Match:   budget.Matcher{Tenant: "tenant-1", ServiceClass: llm.ServiceClassPriority},
		Windows: []budget.Window{{ID: "priority-only/hour", Duration: time.Hour, Bucket: time.Minute, Limit: 1_000}},
	}}
	harness.engine.dependencies.Snapshots = StaticSnapshot{Value: snapshot}

	request := baseRequest("skip-budgeted-unpriced")
	request.ServiceClass = llm.ServiceClassPriority
	request.ServiceClassFallbacks = []llm.ServiceClass{llm.ServiceClassStandard}
	response, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Service.Attempted != llm.ServiceClassStandard || response.Cost.Status != llm.CostStatusKnown {
		t.Fatalf("response = %#v, want priced standard fallback", response)
	}
	adapter.mu.Lock()
	calls := append([]provider.Call(nil), adapter.calls...)
	adapter.mu.Unlock()
	if len(calls) != 1 || calls[0].ServiceClass != llm.ServiceClassStandard {
		t.Fatalf("provider calls = %#v, want only priced standard fallback", calls)
	}
}

func TestGenerateAllowsUnbudgetedUnpricedCandidateWithUnknownCost(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
	harness := newHarness(t, adapter)
	snapshot := harness.engine.dependencies.Snapshots.(StaticSnapshot).Value
	snapshot.Prices = pricing.NewResolver(testPriceCatalog(t, priceEntryForTier("standard-tier")))
	snapshot.RequirePriceWhenBudgeted = true
	harness.engine.dependencies.Snapshots = StaticSnapshot{Value: snapshot}

	request := baseRequest("allow-unbudgeted-unpriced")
	request.ServiceClass = llm.ServiceClassPriority
	response, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Cost.Status != llm.CostStatusUnknown || response.Cost.ReservedMicroUSD != 0 || response.Cost.ActualMicroUSD != 0 || response.Cost.Method != "" || response.Cost.CatalogVersion != "" {
		t.Fatalf("unpriced response cost = %#v, want unknown zero-value monetary facts", response.Cost)
	}
	operation, err := harness.admission.Get(context.Background(), response.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ReservedMicroUSD != 0 || operation.IncurredMicroUSD != 0 || operation.PriceVersion != "" {
		t.Fatalf("unpriced operation = %#v, want zero unknown-price accounting", operation)
	}
}

func TestGenerateRejectsUnavailablePriceWhenPolicyRequiresIt(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []pricing.Entry
	}{
		{name: "missing", entries: []pricing.Entry{priceEntryForTier("standard-tier")}},
		{name: "stale", entries: []pricing.Entry{priceEntryForTier("standard-tier"), func() pricing.Entry {
			entry := priceEntryForTier("priority-tier")
			entry.EffectiveUntil = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			return entry
		}()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
			harness := newHarness(t, adapter)
			snapshot := harness.engine.dependencies.Snapshots.(StaticSnapshot).Value
			snapshot.Prices = pricing.NewResolver(testPriceCatalog(t, test.entries...))
			snapshot.RequirePriceWhenBudgeted = true
			snapshot.BudgetPolicies = []budget.Policy{{
				ID:      "priority-only",
				Match:   budget.Matcher{Tenant: "tenant-1", ServiceClass: llm.ServiceClassPriority},
				Windows: []budget.Window{{ID: "priority-only/hour", Duration: time.Hour, Bucket: time.Minute, Limit: 1_000}},
			}}
			harness.engine.dependencies.Snapshots = StaticSnapshot{Value: snapshot}

			request := baseRequest("reject-budgeted-unpriced-" + test.name)
			request.ServiceClass = llm.ServiceClassPriority
			_, err := harness.engine.Generate(context.Background(), request)
			if err == nil {
				t.Fatal("budgeted unpriced request unexpectedly dispatched")
			}
			var mapped *provider.Error
			if !errors.As(err, &mapped) || mapped.Code != provider.CodeNoRoute || mapped.Phase != provider.PhasePrice || mapped.Dispatch != provider.DispatchNotDispatched || mapped.Retry != provider.RetryNever {
				t.Fatalf("error = %#v, want no-route price failure", err)
			}
			if mapped.SafeDetails["reason"] != "no_eligible_price" {
				t.Fatalf("safe details = %#v, want no_eligible_price", mapped.SafeDetails)
			}
			requireNoOperation(t, harness, request)
			adapter.mu.Lock()
			invokes := adapter.invokes
			adapter.mu.Unlock()
			if invokes != 0 {
				t.Fatalf("provider invoke count = %d, want zero", invokes)
			}
		})
	}
}

func TestGenerateRejectsUnbudgetedUnpricedCandidateWhenPolicyIsStrict(t *testing.T) {
	adapter := &fakeAdapter{name: "fake", response: successfulResponse()}
	harness := newHarness(t, adapter)
	snapshot := harness.engine.dependencies.Snapshots.(StaticSnapshot).Value
	snapshot.Prices = pricing.NewResolver(testPriceCatalog(t, priceEntryForTier("standard-tier")))
	snapshot.RequirePriceWhenBudgeted = false
	harness.engine.dependencies.Snapshots = StaticSnapshot{Value: snapshot}

	request := baseRequest("reject-strict-unpriced")
	request.ServiceClass = llm.ServiceClassPriority
	_, err := harness.engine.Generate(context.Background(), request)
	if err == nil {
		t.Fatal("strict unpriced request unexpectedly dispatched")
	}
	var mapped *provider.Error
	if !errors.As(err, &mapped) || mapped.Code != provider.CodeNoRoute || mapped.Phase != provider.PhasePrice {
		t.Fatalf("error = %#v, want no-route price failure", err)
	}
	if mapped.SafeDetails["reason"] != "no_eligible_price" {
		t.Fatalf("safe details = %#v, want no_eligible_price", mapped.SafeDetails)
	}
	requireNoOperation(t, harness, request)
}

func testPriceCatalog(t *testing.T, entries ...pricing.Entry) pricing.Catalog {
	t.Helper()
	catalog, err := pricing.CompileCatalog("prices-1", "USD", entries)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func priceEntryForTier(tier string) pricing.Entry {
	return pricing.Entry{
		Provider: "provider-1", Family: string(provider.FamilyOpenAIResponses), EndpointID: "endpoint-1", Region: "us-east-1", Model: "provider-model", ProviderTier: tier,
		Currency: "USD", Version: "prices-1", Prices: pricing.UnitPrices{PerRequest: pricing.MustDecimalUSD("0.000001"), OutputPerMillion: pricing.MustDecimalUSD("1")},
	}
}

func requireNoOperation(t *testing.T, harness testHarness, request llm.Request) {
	t.Helper()
	normalized, err := llm.NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := llm.RequestDigest(normalized)
	if err != nil {
		t.Fatal(err)
	}
	operationID, _ := operationIdentity(normalized, digest)
	if _, err := harness.admission.Get(context.Background(), operationID); !errors.Is(err, admission.ErrOperationNotFound) {
		t.Fatalf("admission Get(%q) error = %v, want ErrOperationNotFound", operationID, err)
	}
}
