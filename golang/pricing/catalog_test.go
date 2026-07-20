package pricing

import (
	"errors"
	"testing"
	"time"
)

func TestCatalogResolveExactEntryAndReload(t *testing.T) {
	entry := Entry{Provider: "openai", Family: "responses", EndpointID: "prod", Model: "gpt", ProviderTier: "default", Version: "entry-v1", EffectiveFrom: time.Unix(1, 0), Prices: UnitPrices{InputPerMillion: MustDecimalUSD("1")}}
	catalog, err := CompileUSD("catalog-v1", []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(catalog)
	quote, err := resolver.Resolve(Query{Provider: "openai", Family: "responses", EndpointID: "prod", Model: "gpt", ProviderTier: "default", At: time.Unix(2, 0)})
	if err != nil || quote.Entry.Version != "entry-v1" {
		t.Fatalf("quote = %#v %v", quote, err)
	}
	if _, err := resolver.Resolve(Query{Provider: "openai", Family: "responses", EndpointID: "prod", Model: "other", ProviderTier: "default", At: time.Unix(2, 0)}); !errors.Is(err, ErrNoActivePrice) {
		t.Fatalf("unknown price error = %v, want ErrNoActivePrice", err)
	}
	price, err := CostFromUsage(entry, Usage{InputTokens: 1_000_000})
	if err != nil || price.MicroUSD != 1_000_000 {
		t.Fatalf("cost = %#v %v", price, err)
	}
}

func TestCostFromUsageRejectsUnknownCatalogComponent(t *testing.T) {
	entry := Entry{
		Prices:            UnitPrices{InputPerMillion: MustDecimalUSD("1")},
		UnknownComponents: []PriceComponent{PriceComponentInput},
	}
	if _, err := CostFromUsage(entry, Usage{InputTokens: 1}); err == nil {
		t.Fatal("CostFromUsage accepted an omitted input price as known zero")
	}
	if _, err := CostFromUsage(entry, Usage{}); err != nil {
		t.Fatalf("zero usage should not require an unknown input price: %v", err)
	}
}

func TestCompileUSDRejectsInvalidUnknownComponent(t *testing.T) {
	entry := Entry{Provider: "openai", Family: "responses", EndpointID: "prod", Model: "gpt", ProviderTier: "standard", UnknownComponents: []PriceComponent{"future"}}
	if _, err := CompileUSD("catalog-v1", []Entry{entry}); err == nil {
		t.Fatal("CompileUSD accepted an unknown price component")
	}
}
