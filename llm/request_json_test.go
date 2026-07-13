package llm_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
)

func TestNormalizeServiceClass(t *testing.T) {
	tests := []struct{ in, want llm.ServiceClass }{
		{"", llm.ServiceClassStandard},
		{llm.ServiceClassEconomy, llm.ServiceClassEconomy},
		{llm.ServiceClassStandard, llm.ServiceClassStandard},
		{llm.ServiceClassPriority, llm.ServiceClassPriority},
	}
	for _, tt := range tests {
		got, err := llm.NormalizeServiceClass(tt.in)
		if err != nil || got != tt.want {
			t.Fatalf("NormalizeServiceClass(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestNormalizeServiceClassRejectsProviderDefault(t *testing.T) {
	if _, err := llm.NormalizeServiceClass("provider_default"); err == nil {
		t.Fatal("NormalizeServiceClass accepted provider_default")
	}
}

func TestRequestJSONFixtures(t *testing.T) {
	for _, name := range []string{"minimal.json", "full.json"} {
		data := readFixture(t, filepath.Join("request", name))
		var request llm.Request
		if err := json.Unmarshal(data, &request); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var roundTripped llm.Request
		if err := json.Unmarshal(encoded, &roundTripped); err != nil {
			t.Fatalf("round-trip %s: %v\n%s", name, err, encoded)
		}
		if request.APIVersion != llm.APIVersion || roundTripped.ServiceClass == "" {
			t.Fatalf("%s lost normalized contract: %#v", name, roundTripped)
		}
	}

	var minimal llm.Request
	if err := json.Unmarshal(readFixture(t, filepath.Join("request", "minimal.json")), &minimal); err != nil {
		t.Fatal(err)
	}
	if minimal.ServiceClass != llm.ServiceClassStandard {
		t.Fatalf("omitted service class = %q, want standard", minimal.ServiceClass)
	}
	if got := mustMarshal(t, minimal); !bytes.Contains(got, []byte(`"service_class":"standard"`)) {
		t.Fatalf("normalized request omitted explicit standard: %s", got)
	}
}

func TestRequestFullFixturePreservesTypedItems(t *testing.T) {
	var request llm.Request
	if err := json.Unmarshal(readFixture(t, filepath.Join("request", "full.json")), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 5 {
		t.Fatalf("input length = %d, want 5", len(request.Input))
	}
	wantKinds := []llm.ItemKind{llm.ItemKindMessage, llm.ItemKindToolCall, llm.ItemKindToolResult, llm.ItemKindProviderState, llm.ItemKindReference}
	for index, want := range wantKinds {
		if got := request.Input[index].ItemKind(); got != want {
			t.Fatalf("input[%d] kind = %q, want %q", index, got, want)
		}
	}
	message := request.Input[0].(llm.Message)
	image, ok := message.Content[1].(llm.ImagePart)
	if !ok || !reflect.DeepEqual(image.Bytes, []byte{1, 2}) {
		t.Fatalf("image bytes were not decoded/copied: %#v", message.Content[1])
	}
}

func TestRequestRejectsUnknownAndAmbiguousFields(t *testing.T) {
	cases := []string{
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","unknown":true}`,
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","service_class":"provider_default"}`,
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","input":[{"kind":"message","actor":"assistant","content":[]}]}`,
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","input":[{"kind":"not-an-item"}]}`,
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","input":[{"kind":"message","actor":"human","content":[{"kind":"image","url":"https://example.test/a","bytes":"AQ==","media_type":"image/png"}]}]}`,
		`{"api_version":"llm.temporal/v1","operation_key":"x","model":"m","service_class_fallbacks":["standard","standard"]}`,
	}
	for _, input := range cases {
		var request llm.Request
		if err := json.Unmarshal([]byte(input), &request); err == nil {
			t.Errorf("accepted invalid request %s", input)
		}
	}
	if _, err := llm.NormalizeServiceClass("not-a-class"); err == nil {
		t.Fatal("accepted unknown service class")
	}
}

func TestResponseJSONFixtures(t *testing.T) {
	for _, name := range []string{"tool-calls.json", "completed.json"} {
		data := readFixture(t, filepath.Join("response", name))
		var response llm.Response
		if err := json.Unmarshal(data, &response); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		encoded := mustMarshal(t, response)
		var roundTripped llm.Response
		if err := json.Unmarshal(encoded, &roundTripped); err != nil {
			t.Fatalf("round-trip %s: %v\n%s", name, err, encoded)
		}
		if response.Status != roundTripped.Status || response.OperationKey != roundTripped.OperationKey {
			t.Fatalf("round-trip changed response identity: %#v %#v", response, roundTripped)
		}
	}
}

func TestSchemasAreValidJSONAndKeepClosedServiceEnum(t *testing.T) {
	for _, name := range []string{"generate-request.schema.json", "generate-response.schema.json"} {
		data, err := os.ReadFile(filepath.Join("..", "api", "schema", "v1", name))
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) {
			t.Fatalf("schema %s is not valid JSON", name)
		}
		if name == "generate-request.schema.json" {
			var schema struct {
				AdditionalProperties bool `json:"additionalProperties"`
				Properties           map[string]struct {
					Default string   `json:"default"`
					Enum    []string `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.AdditionalProperties {
				t.Fatal("request schema must close top-level properties")
			}
			serviceClass := schema.Properties["service_class"]
			if !reflect.DeepEqual(serviceClass.Enum, []string{"economy", "standard", "priority"}) {
				t.Fatalf("service_class enum = %#v", serviceClass.Enum)
			}
			if serviceClass.Default != "standard" {
				t.Fatalf("service_class default = %q, want standard", serviceClass.Default)
			}
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
