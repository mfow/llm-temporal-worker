package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/internal/catalog"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/pricing"
)

func TestRoutePriceIdentityUsesEndpointIdentity(t *testing.T) {
	when := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	endpoint := config.EndpointConfig{
		Family:       "openai_responses",
		Region:       "australiaeast",
		PriceCatalog: "prices",
		ServiceClasses: map[llm.ServiceClass]config.TierConfig{
			llm.ServiceClassStandard: {ProviderValue: "standard"},
		},
	}
	bundle := catalog.Bundle{Pricing: map[string]catalog.PricingCatalog{
		"prices": {Version: "prices-v1", Catalog: pricing.Catalog{
			Version:  "prices-v1",
			Currency: "USD",
			Entries: []pricing.Entry{
				{Provider: "wrong-provider", Family: "openai_responses", EndpointID: "other-endpoint", Region: "australiaeast", Model: "model", ProviderTier: "standard", Version: "wrong"},
				{Provider: "right-provider", Family: "openai_responses", EndpointID: "target-endpoint", Region: "australiaeast", Model: "model", ProviderTier: "standard", Version: "right"},
			},
		}},
	}}

	providerName, region, version, err := routePriceIdentity(bundle, "target-endpoint", endpoint, "model", []llm.ServiceClass{llm.ServiceClassStandard}, when)
	if err != nil {
		t.Fatalf("routePriceIdentity() error = %v", err)
	}
	if providerName != "right-provider" || region != "australiaeast" || version != "right" {
		t.Fatalf("identity = (%q, %q, %q), want target endpoint quote", providerName, region, version)
	}
}

func TestRoutePriceIdentityRejectsMissingEndpointQuote(t *testing.T) {
	endpoint := config.EndpointConfig{
		Family:       "openai_responses",
		PriceCatalog: "prices",
		ServiceClasses: map[llm.ServiceClass]config.TierConfig{
			llm.ServiceClassPriority: {ProviderValue: "priority"},
		},
	}
	bundle := catalog.Bundle{Pricing: map[string]catalog.PricingCatalog{
		"prices": {Catalog: pricing.Catalog{Version: "prices-v1", Currency: "USD", Entries: []pricing.Entry{{
			Provider: "provider", Family: "openai_responses", EndpointID: "other", Model: "model", ProviderTier: "priority",
		}}}},
	}}
	_, _, _, err := routePriceIdentity(bundle, "target", endpoint, "model", []llm.ServiceClass{llm.ServiceClassPriority}, time.Now())
	if err == nil {
		t.Fatal("routePriceIdentity() succeeded without endpoint-specific quote")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "no active price entry") {
		t.Fatalf("error = %q, want missing quote", got)
	}
}
