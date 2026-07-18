package pricing

import (
	"errors"
	"testing"
	"time"
)

func TestCatalogResolveExactEntryAndReload(t *testing.T) {
	entry := Entry{Provider: "openai", Family: "responses", EndpointID: "prod", Model: "gpt", ProviderTier: "default", Currency: "USD", Version: "entry-v1", EffectiveFrom: time.Unix(1, 0), Prices: UnitPrices{InputPerMillion: MustDecimalUSD("1")}}
	catalog, err := CompileCatalog("catalog-v1", "USD", []Entry{entry})
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
