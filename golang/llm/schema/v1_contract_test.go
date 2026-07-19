package schema_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm/schema"
)

func TestV1ContractFixturesValidate(t *testing.T) {
	cases := []struct{ schemaName, fixture string }{
		{"generate-request.schema.json", "generate-root.json"},
		{"generate-request.schema.json", "generate-delta.json"},
		{"generate-response.schema.json", "generate-response.json"},
		{"compact-request.schema.json", "compact-request.json"},
		{"compact-response.schema.json", "compact-response.json"},
		{"query-request.schema.json", "query-provider-status.json"},
		{"query-response.schema.json", "query-provider-response.json"},
	}
	for _, test := range cases {
		t.Run(test.fixture, func(t *testing.T) {
			schemaData, err := os.ReadFile(filepath.Join("..", "..", "api", "schema", "v1", test.schemaName))
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := schema.Parse(schemaData)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile(filepath.Join("..", "testdata", "v1", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled.Validate(fixture); err != nil {
				t.Fatalf("fixture rejected: %v", err)
			}
		})
	}
}

func TestV1ContractRejectsUnknownAndLegacyResponseFields(t *testing.T) {
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "api", "schema", "v1", "generate-request.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := schema.Parse(schemaData)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := os.ReadFile(filepath.Join("..", "testdata", "v1", "negative-unknown-field.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(bad); err == nil {
		t.Fatal("unknown transcript field accepted")
	}
	legacy := []byte(`{"api_version":"llm.temporal/v1","operation_key":"x","context":{"tenant":"t","project":"p","actor":"a"},"append":[],"transcript":[]}`)
	if err := compiled.Validate(legacy); err == nil {
		t.Fatal("legacy transcript field accepted")
	}
}
