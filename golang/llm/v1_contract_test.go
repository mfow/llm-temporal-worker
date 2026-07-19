package llm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func TestV1GenerateAndCompactFixturesRoundTrip(t *testing.T) {
	for _, name := range []string{"generate-root.json", "generate-delta.json"} {
		data := readV1Fixture(t, name)
		var request llm.GenerateRequestV1
		if err := json.Unmarshal(data, &request); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		var again llm.GenerateRequestV1
		if err := json.Unmarshal(encoded, &again); err != nil {
			t.Fatalf("%s second decode: %v", name, err)
		}
		if string(encoded) != string(mustCanonicalJSON(t, encoded)) {
			t.Fatalf("%s was not deterministic", name)
		}
	}
	for _, name := range []string{"generate-response.json", "compact-response.json"} {
		data := readV1Fixture(t, name)
		if name == "generate-response.json" {
			var value llm.GenerateResponseV1
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if _, err := json.Marshal(value); err != nil {
				t.Fatal(err)
			}
		} else {
			var value llm.CompactResponseV1
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if _, err := json.Marshal(value); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestV1RejectsUnknownTranscriptAndMismatchedQueryResult(t *testing.T) {
	var request llm.GenerateRequestV1
	if err := json.Unmarshal(readV1Fixture(t, "negative-unknown-field.json"), &request); err == nil {
		t.Fatal("unknown transcript field was accepted")
	}
	query := llm.QueryResponseV1{OperationKey: "q", QueryExecutionID: "id", Kind: llm.QueryProviderStatus, Result: llm.SpendSummary{}, Cost: llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "control_query_zero"}}
	if _, err := json.Marshal(query); err == nil {
		t.Fatal("mismatched query result was accepted")
	}
	var queryRequest llm.QueryRequestV1
	unknown := []byte(`{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p","actor":"a"},"kind":"provider_status","query":{"page_size":1001}}`)
	if err := json.Unmarshal(unknown, &queryRequest); err == nil {
		t.Fatal("oversized query page accepted")
	}
	unknown = []byte(`{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p","actor":"a"},"kind":"provider_status","query":{"unknown":true}}`)
	if err := json.Unmarshal(unknown, &queryRequest); err == nil {
		t.Fatal("unknown query field accepted")
	}
}

func TestV1VariantBoundariesAndTemperature(t *testing.T) {
	for _, variant := range []int32{0, 1, 2, 2147483647} {
		if err := llm.ValidateVariantTemperature(variant, nil); err != nil {
			t.Fatalf("variant %d: %v", variant, err)
		}
	}
	if err := llm.ValidateVariantTemperature(-1, nil); err == nil {
		t.Fatal("negative variant accepted")
	}
	zero := 0.0
	if err := llm.ValidateVariantTemperature(1, &zero); err == nil {
		t.Fatal("positive variant with zero temperature accepted")
	}
	positive := 0.2
	if err := llm.ValidateVariantTemperature(2147483647, &positive); err != nil {
		t.Fatal(err)
	}
}

func readV1Fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func stringPointer(value string) *string { return &value }
func mustCanonicalJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
