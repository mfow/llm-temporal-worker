package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/config"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
)

func TestLoadCapabilitiesConvertsStrictEntries(t *testing.T) {
	ref := writeCatalog(t, `version: llmtw-capabilities/v1
entries:
  - id: openai-prod
    family: openai_responses
    model:
      exact: gpt-example
    verified_at: 2026-07-13T00:00:00Z
    features:
      input.text:
        level: native
      tools.auto:
        level: native
      tools.required:
        level: native
      tools.parallel:
        level: native
      output.json_schema:
        level: emulated
        transform: json-schema-tool
      service.economy:
        level: native
      service.standard:
        level: native
      service.priority:
        level: native
      continuation.response_id:
        level: unsupported
        reason: provider ids are not stable
    limits:
      context_tokens: 400000
      output_tokens: 32768
`)

	catalog, err := LoadCapabilities(ref)
	if err != nil {
		t.Fatalf("LoadCapabilities() error = %v", err)
	}
	profile, ok := catalog.Profiles["openai-prod"]
	if !ok {
		t.Fatalf("profiles = %#v", catalog.Profiles)
	}
	if profile.Family != provider.FamilyOpenAIResponses || profile.Model != "gpt-example" {
		t.Fatalf("profile identity = %#v", profile)
	}
	if profile.Set.Version != "llmtw-capabilities/v1" {
		t.Fatalf("capability version = %q", profile.Set.Version)
	}
	if got := profile.Set.Features[provider.FeatureStructuredOutput]; got.State != provider.CapabilityEmulated || got.Transform != "json-schema-tool" {
		t.Fatalf("structured output = %#v", got)
	}
	if got := profile.Set.Features[provider.FeatureContinuation]; got.State != provider.CapabilityUnsupported {
		t.Fatalf("continuation = %#v", got)
	}
	if catalog.Digest == ([32]byte{}) {
		t.Fatal("catalog digest is empty")
	}
}

func TestLoadCapabilitiesAcceptsLocalProfilesAndClosedClasses(t *testing.T) {
	ref := writeCatalog(t, `version: local-mock-v1
profiles:
  local-mock-v1:
    family: openai_chat
    model: demo-model
    input: [text, reference]
    output: [text, tool_call]
    service_classes: [economy, standard, priority]
    max_context_tokens: 32768
    max_output_tokens: 4096
`)
	catalog, err := LoadCapabilities(ref)
	if err != nil {
		t.Fatalf("LoadCapabilities() error = %v", err)
	}
	profile := catalog.Profiles["local-mock-v1"]
	if !profile.Set.Supports(provider.FeatureText, true) || !profile.Set.Supports(provider.FeatureToolCall, true) {
		t.Fatalf("compiled local features = %#v", profile.Set.Features)
	}
}

func TestLoadPricingCompilesExactDecimalEntries(t *testing.T) {
	ref := writeCatalog(t, `version: llmtw-prices/v1
id: catalog-2026-07-13
currency: USD
entries:
  - provider: openai
    endpoint_id: openai-production
    endpoint_family: openai_responses
    region: global
    model: gpt-example
    provider_tier: standard
    input_per_million: "1.250000"
    output_per_million: "10.000000"
    cache_read_per_million: "0.125000"
    source: operator-verified
`)

	catalog, err := LoadPricing(ref)
	if err != nil {
		t.Fatalf("LoadPricing() error = %v", err)
	}
	if catalog.ID != "catalog-2026-07-13" {
		t.Fatalf("catalog identity = %#v", catalog)
	}
	if len(catalog.Catalog.Entries) != 1 {
		t.Fatalf("entries = %#v", catalog.Catalog.Entries)
	}
	entry := catalog.Catalog.Entries[0]
	if entry.Provider != "openai" || entry.EndpointID != "openai-production" || entry.ProviderTier != "standard" {
		t.Fatalf("entry identity = %#v", entry)
	}
	if entry.Prices.InputPerMillion.String() != "1.250000" || entry.Prices.OutputPerMillion.String() != "10.000000" {
		t.Fatalf("entry prices = %#v", entry.Prices)
	}
}

func TestLoadPricingRejectsNonUSDSource(t *testing.T) {
	ref := writeCatalog(t, `version: llmtw-prices/v1
id: catalog-non-usd
currency: EUR
entries:
  - provider: openai
    endpoint_id: openai-production
    endpoint_family: openai_responses
    region: global
    model: gpt-example
    provider_tier: standard
    input_per_million: "1.250000"
    output_per_million: "10.000000"
`)
	if _, err := LoadPricing(ref); err == nil || !strings.Contains(err.Error(), "currency must be USD") {
		t.Fatalf("LoadPricing() error = %v, want a non-USD rejection", err)
	}
}

func TestLoadRejectsUnknownFieldsDuplicateIDsDigestAndUnsafeSize(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown capability field",
			body: `version: v1
entries:
  - id: p
    family: openai_chat
    model: {exact: m}
    features: {text: {level: native}}
    typo: true
`,
			want: "field typo not found",
		},
		{
			name: "duplicate capability id",
			body: `version: v1
entries:
  - id: p
    family: openai_chat
    model: {exact: m}
    features: {text: {level: native}}
  - id: p
    family: openai_chat
    model: {exact: m2}
    features: {text: {level: native}}
`,
			want: "duplicate capability profile ID",
		},
		{
			name: "unknown price field",
			body: `version: v1
id: prices
currency: USD
entries:
  - provider: p
    endpoint_id: e
    family: openai_chat
    region: r
    model: m
    provider_tier: standard
    input_per_million: "0"
    output_per_million: "0"
    typo: true
`,
			want: "field typo not found",
		},
		{
			name: "provider default class",
			body: `version: v1
profiles:
  p:
    family: openai_chat
    model: m
    input: [text]
    service_classes: [provider_default]
`,
			want: "economy, standard, or priority",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ref := writeCatalog(t, test.body)
			var err error
			if strings.Contains(test.name, "price") {
				_, err = LoadPricing(ref)
			} else {
				_, err = LoadCapabilities(ref)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}

	ref := writeCatalog(t, "version: v1\nprofiles: {}\n")
	ref.SHA256 = strings.Repeat("0", 64)
	if _, err := LoadCapabilities(ref); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch error = %v", err)
	}

	large := writeCatalog(t, "version: v1\nprofiles: {}\n")
	if _, err := LoadCapabilitiesWithOptions(large, Options{MaxBytes: 8}); err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("size error = %v", err)
	}
}

func TestLoadRejectsMissingEndpointReferences(t *testing.T) {
	capRef := writeCatalog(t, `version: v1
profiles:
  profile:
    family: openai_chat
    model: model
    input: [text]
`)
	priceRef := writeCatalog(t, `version: v1
id: prices
currency: USD
entries:
  - provider: mock
    endpoint_id: endpoint
    family: openai_chat
    region: local
    model: model
    provider_tier: standard
    input_per_million: "0"
    output_per_million: "0"
`)
	cfg := config.Config{
		Capabilities: config.CapabilityConfig{Catalogs: []config.CatalogRef{capRef}},
		Pricing:      config.PricingConfig{Catalogs: []config.CatalogRef{priceRef}},
		Endpoints: map[string]config.EndpointConfig{
			"endpoint": {Family: "openai_chat", CapabilityProfile: "missing", PriceCatalog: "prices"},
		},
	}
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), "missing capability profile") {
		t.Fatalf("missing capability reference error = %v", err)
	}
	cfg.Endpoints["endpoint"] = config.EndpointConfig{Family: "openai_chat", CapabilityProfile: "profile", PriceCatalog: "missing"}
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), "missing price catalog") {
		t.Fatalf("missing price reference error = %v", err)
	}
}

func TestEndpointFamilyMapsAnthropicAWSWithoutConflatingBedrock(t *testing.T) {
	if got := endpointFamily("azure_openai_chat"); got != provider.FamilyOpenAIChat {
		t.Fatalf("Azure Chat family = %q, want %q", got, provider.FamilyOpenAIChat)
	}
	if got := endpointFamily("anthropic_aws_messages"); got != provider.FamilyAnthropicMessages {
		t.Fatalf("Anthropic AWS family = %q, want %q", got, provider.FamilyAnthropicMessages)
	}
	if got := endpointFamily("bedrock_anthropic_messages"); got != provider.FamilyBedrockMessages {
		t.Fatalf("Bedrock family = %q, want %q", got, provider.FamilyBedrockMessages)
	}
}

func writeCatalog(t *testing.T, body string) config.CatalogRef {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(body))
	return config.CatalogRef{File: path, SHA256: hex.EncodeToString(digest[:])}
}
