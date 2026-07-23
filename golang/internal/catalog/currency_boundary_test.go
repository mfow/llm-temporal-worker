package catalog

import (
	"fmt"
	"strings"
	"testing"
)

// The v1 catalog contract is implicitly USD: a generic currency field is not
// part of the schema and must not be accepted even when its value is USD.
// This keeps callers from assuming that a future FX/rate field is supported.
func TestLoadPricingRejectsCurrencyFieldAtUSDOnlyBoundary(t *testing.T) {
	for _, currency := range []string{"USD", "EUR"} {
		t.Run(currency, func(t *testing.T) {
			ref := writeCatalog(t, fmt.Sprintf(`version: llmtw-prices/v1
id: catalog-currency-%s
currency: %s
entries:
  - provider: openai
    endpoint_id: openai-production
    endpoint_family: openai_responses
    region: global
    model: gpt-example
    provider_tier: standard
    input_per_million: "1.250000"
    output_per_million: "10.000000"
`, strings.ToLower(currency), currency))
			if _, err := LoadPricing(ref); err == nil || !strings.Contains(strings.ToLower(err.Error()), "currency") {
				t.Fatalf("LoadPricing() error = %v, want strict rejection of currency field", err)
			}
		})
	}
}
