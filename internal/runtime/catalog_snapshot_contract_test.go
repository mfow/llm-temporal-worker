package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/internal/catalog"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/routing"
)

func testPriceEntry(endpointID, model, tier string) pricing.Entry {
	return pricing.Entry{
		Provider: "test-provider", Family: "openai_responses", EndpointID: endpointID, Region: "australiaeast", Model: model, ProviderTier: tier,
		Prices: pricing.UnitPrices{InputPerMillion: pricing.MustDecimalUSD("1"), OutputPerMillion: pricing.MustDecimalUSD("2")},
	}
}

func compiledPriceCatalog(t *testing.T, id, version, currency string, entries []pricing.Entry) catalog.PricingCatalog {
	t.Helper()
	compiled, err := pricing.CompileCatalog(version, currency, entries)
	if err != nil {
		t.Fatal(err)
	}
	return catalog.PricingCatalog{ID: id, Version: version, Catalog: compiled}
}

func TestCatalogSnapshotLoaderRejectsMissingSnapshotAndCancellation(t *testing.T) {
	loader := CatalogSnapshotLoader{}
	if _, err := loader.Load(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "configuration snapshot is required") {
		t.Fatalf("nil snapshot error = %v", err)
	}
	_, err := loader.Load(context.Background(), &config.Snapshot{})
	if err == nil {
		t.Fatal("empty snapshot unexpectedly loaded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = loader.Load(ctx, &config.Snapshot{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load error = %v, want context.Canceled", err)
	}
}

func TestSnapshotLoaderFuncDelegatesToFunction(t *testing.T) {
	called := false
	loader := SnapshotLoaderFunc(func(ctx context.Context, snapshot *config.Snapshot) (engine.Snapshot, error) {
		called = ctx != nil && snapshot != nil
		return engine.Snapshot{Version: "snapshot-v1"}, nil
	})
	got, err := loader.Load(context.Background(), &config.Snapshot{})
	if err != nil || got.Version != "snapshot-v1" || !called {
		t.Fatalf("Load() = %#v, %v, called=%v", got, err, called)
	}
}

func TestCatalogSnapshotLoaderPublishesLocalCatalogSmoke(t *testing.T) {
	configData, err := os.ReadFile("../../deploy/local/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := os.ReadFile("../../deploy/local/capabilities.yaml")
	if err != nil {
		t.Fatal(err)
	}
	prices, err := os.ReadFile("../../deploy/local/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	capabilitiesPath := filepath.Join(directory, "capabilities.yaml")
	pricesPath := filepath.Join(directory, "prices.yaml")
	if err := os.WriteFile(capabilitiesPath, capabilities, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pricesPath, prices, 0o600); err != nil {
		t.Fatal(err)
	}
	data := strings.ReplaceAll(string(configData), "/etc/llmtw/capabilities.yaml", capabilitiesPath)
	data = strings.ReplaceAll(data, "/etc/llmtw/prices.yaml", pricesPath)
	compiled, err := config.Compile(context.Background(), []byte(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	loaded, err := (CatalogSnapshotLoader{Clock: func() time.Time { return now }}).Load(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := loaded.Routes.Models["demo-model"]
	if !ok || len(model.Routes) != 1 || model.Routes[0].Provider != "provider-mock" || model.Routes[0].PriceVersion != "local-v1" {
		t.Fatalf("loaded route snapshot = %#v", loaded.Routes)
	}
	if loaded.Prices == nil || len(loaded.BudgetPolicies) != 1 || !loaded.RequireBudgetMatch || loaded.Environment != "development" {
		t.Fatalf("loaded runtime snapshot = %#v", loaded)
	}
	if _, err := loaded.Prices.Resolve(pricing.Query{Provider: "provider-mock", Family: "openai_chat", EndpointID: "provider-mock", Region: "local", Model: "demo-model", ProviderTier: "standard", At: now}); err != nil {
		t.Fatalf("loaded price resolver cannot resolve route quote: %v", err)
	}
}

func TestMergePricingCatalogsIsDeterministicAndRejectsInvalidBundles(t *testing.T) {
	bundle := catalog.Bundle{Pricing: map[string]catalog.PricingCatalog{
		"z": compiledPriceCatalog(t, "z", "z-v1", "USD", []pricing.Entry{testPriceEntry("endpoint-z", "model-z", "standard")}),
		"a": compiledPriceCatalog(t, "a", "a-v1", "USD", []pricing.Entry{testPriceEntry("endpoint-a", "model-a", "standard")}),
	}}
	merged, err := mergePricingCatalogs(bundle, "config-v1")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Version != "runtime-prices/config-v1" || merged.Currency != "USD" || len(merged.Entries) != 2 {
		t.Fatalf("merged catalog = %#v", merged)
	}
	if merged.Entries[0].EndpointID != "endpoint-a" || merged.Entries[1].EndpointID != "endpoint-z" {
		t.Fatalf("merged entries are not deterministic: %#v", merged.Entries)
	}
	if got := merged.DigestHex(); got == "" {
		t.Fatal("merged catalog has no digest")
	}

	if got, err := mergePricingCatalogs(bundle, " "); err != nil || got.Version != "runtime-prices" {
		t.Fatalf("blank config version = %#v, %v", got, err)
	}
	for name, invalid := range map[string]catalog.Bundle{
		"empty": {Pricing: map[string]catalog.PricingCatalog{}},
		"mixed currencies": {Pricing: map[string]catalog.PricingCatalog{
			"usd": compiledPriceCatalog(t, "usd", "usd-v1", "USD", []pricing.Entry{testPriceEntry("usd", "model", "standard")}),
			"eur": compiledPriceCatalog(t, "eur", "eur-v1", "EUR", []pricing.Entry{testPriceEntry("eur", "model", "standard")}),
		}},
		"invalid entry": {Pricing: map[string]catalog.PricingCatalog{
			"broken": {Catalog: pricing.Catalog{Currency: "USD", Entries: []pricing.Entry{{Provider: "missing-rest-of-identity"}}}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mergePricingCatalogs(invalid, "config-v1"); err == nil {
				t.Fatal("mergePricingCatalogs() unexpectedly succeeded")
			}
		})
	}
}

func testRouteInputs(t *testing.T) (config.Config, catalog.Bundle) {
	t.Helper()
	value := config.Config{
		Version: "config-v1",
		Limits:  config.LimitsConfig{MaxBudgetBucketsPerWindow: 64},
		Endpoints: map[string]config.EndpointConfig{
			"endpoint-a": {
				Family: "azure_openai_responses", Region: "australiaeast", AccountRegion: "au", CapabilityProfile: "profile-a", PriceCatalog: "prices",
				ServiceClasses: map[llm.ServiceClass]config.TierConfig{llm.ServiceClassStandard: {ProviderValue: "standard"}},
				Extensions:     map[string]map[string]any{"zeta": {}, "alpha": {}},
			},
		},
		Models: map[string]config.ModelConfig{
			"logical-model": {AllowedTenants: []string{"tenant-a"}, DataRegions: []string{"au"}, Routes: []config.RouteConfig{{ID: "route-a", Endpoint: "endpoint-a", Model: "gpt-test", Classes: []llm.ServiceClass{llm.ServiceClassStandard}}}},
		},
	}
	profile := catalog.CapabilityProfile{
		ID: "profile-a", Family: provider.FamilyOpenAIResponses, Model: "gpt-test",
		Set: provider.CapabilitySet{Version: "cap-v1", Features: map[provider.Feature]provider.Capability{
			provider.FeatureText:     {State: provider.CapabilityNative},
			provider.FeatureToolCall: {State: provider.CapabilityEmulated, Transform: "json-tool"},
		}},
	}
	return value, catalog.Bundle{
		Capabilities: map[string]catalog.CapabilityProfile{"profile-a": profile},
		Pricing:      map[string]catalog.PricingCatalog{"prices": compiledPriceCatalog(t, "prices", "prices-v1", "USD", []pricing.Entry{testPriceEntry("endpoint-a", "gpt-test", "standard")})},
	}
}

func TestCompileRoutesPublishesProviderAndRoutingContracts(t *testing.T) {
	value, bundle := testRouteInputs(t)
	routes, err := compileRoutes(value, bundle, time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	model, ok := routes.Models["logical-model"]
	if !ok || len(model.Routes) != 1 {
		t.Fatalf("compiled routes = %#v", routes)
	}
	route := model.Routes[0]
	if route.ID != "route-a" || route.Provider != "test-provider" || route.Family != string(provider.FamilyOpenAIResponses) || route.Region != "australiaeast" || route.AccountRegion != "au" || route.ModelLineage != "gpt-test" || route.PriceVersion != "prices-v1" || !route.PriceAvailable {
		t.Fatalf("compiled route identity = %#v", route)
	}
	if route.ProviderTiers[llm.ServiceClassStandard] != "standard" || len(route.ExtensionNames) != 2 || route.ExtensionNames[0] != "alpha" || route.ExtensionNames[1] != "zeta" {
		t.Fatalf("compiled route classes/extensions = %#v", route)
	}
	if got := route.Capabilities.Features[routing.FeatureToolCall]; got.State != routing.CapabilityEmulated || got.Transform != "json-tool" {
		t.Fatalf("compiled routing capabilities = %#v", route.Capabilities)
	}
	if routes.Version != value.Version || len(route.AllowedTenants) != 1 || route.AllowedTenants[0] != "tenant-a" || route.AllowedRegions[0] != "au" {
		t.Fatalf("compiled route constraints = %#v", route)
	}
}

func TestCompileRoutesRejectsBrokenReferencesAndIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(config.Config, catalog.Bundle)
		want   string
	}{
		{name: "missing endpoint", mutate: func(value config.Config, _ catalog.Bundle) {
			value.Models["logical-model"].Routes[0].Endpoint = "missing"
		}, want: "references missing endpoint"},
		{name: "missing capability profile", mutate: func(value config.Config, _ catalog.Bundle) {
			value.Endpoints["endpoint-a"] = value.Endpoints["endpoint-a"]
			endpoint := value.Endpoints["endpoint-a"]
			endpoint.CapabilityProfile = "missing"
			value.Endpoints["endpoint-a"] = endpoint
		}, want: "capability profile \"missing\" is unavailable"},
		{name: "family mismatch", mutate: func(_ config.Config, bundle catalog.Bundle) {
			profile := bundle.Capabilities["profile-a"]
			profile.Family = provider.FamilyAnthropicMessages
			bundle.Capabilities["profile-a"] = profile
		}, want: "capability family"},
		{name: "model mismatch", mutate: func(value config.Config, _ catalog.Bundle) {
			routes := value.Models["logical-model"].Routes
			routes[0].Model = "other-model"
			value.Models["logical-model"] = config.ModelConfig{Routes: routes}
		}, want: "does not match capability profile"},
		{name: "missing price", mutate: func(_ config.Config, bundle catalog.Bundle) {
			bundle.Pricing["prices"] = catalog.PricingCatalog{Catalog: pricing.Catalog{Currency: "USD"}}
		}, want: "no active price entry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, bundle := testRouteInputs(t)
			test.mutate(value, bundle)
			_, err := compileRoutes(value, bundle, time.Now())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("compileRoutes() = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCompileBudgetPoliciesMapsAndValidatesWindows(t *testing.T) {
	value := config.Config{Limits: config.LimitsConfig{MaxBudgetBucketsPerWindow: 64}, Budgets: config.BudgetsConfig{Policies: []config.BudgetPolicy{{
		ID: "tenant-policy", Match: config.BudgetMatch{Tenant: "tenant-a", Project: "project-a", ActorPrefix: "svc-", Environment: "production", LogicalModel: "logical-model", EndpointID: "endpoint-a", ServiceClass: llm.ServiceClassPriority}, Windows: []config.BudgetWindow{{Duration: config.Duration(time.Hour), Bucket: config.Duration(time.Minute), LimitMicroUSD: 12345}},
	}}}}
	policies, err := compileBudgetPolicies(value)
	if err != nil || len(policies) != 1 {
		t.Fatalf("compileBudgetPolicies() = %#v, %v", policies, err)
	}
	if policies[0].ID != "tenant-policy" || policies[0].Match.Tenant != "tenant-a" || policies[0].Match.Project != "project-a" || policies[0].Match.ActorPrefix != "svc-" || policies[0].Match.Environment != "production" || policies[0].Match.LogicalModel != "logical-model" || policies[0].Match.EndpointID != "endpoint-a" || policies[0].Match.ServiceClass != llm.ServiceClassPriority || policies[0].Windows[0].ID != "tenant-policy/0" || policies[0].Windows[0].Limit != 12345 {
		t.Fatalf("compiled budget policy = %#v", policies[0])
	}

	value.Limits.MaxBudgetBucketsPerWindow = 1
	if _, err := compileBudgetPolicies(value); err == nil || !strings.Contains(err.Error(), "budget policy") {
		t.Fatalf("unsafe budget policy error = %v", err)
	}
}

func TestRoutingCapabilitiesOnlyProjectsSupportedProviderFeatures(t *testing.T) {
	set := provider.CapabilitySet{Version: "cap-v1", Features: map[provider.Feature]provider.Capability{
		provider.FeatureText:      {State: provider.CapabilityNative, Reason: "verified"},
		provider.FeatureImage:     {State: provider.CapabilityNative},
		provider.FeatureReasoning: {State: provider.CapabilityUnsupported, Reason: "not available"},
	}}
	routed := routingCapabilities(set)
	if routed.Version != "cap-v1" || routed.Features[routing.FeatureText].Reason != "verified" || routed.Features[routing.FeatureReasoning].State != routing.CapabilityUnsupported {
		t.Fatalf("routing capabilities = %#v", routed)
	}
	if _, ok := routed.Features[routing.Feature("image")]; ok {
		t.Fatal("provider-only image capability leaked into routing")
	}
}
