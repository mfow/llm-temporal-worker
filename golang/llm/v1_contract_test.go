package llm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	unknown = []byte(`{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p"},"kind":"provider_status","query":{}}`)
	if err := json.Unmarshal(unknown, &queryRequest); err == nil {
		t.Fatal("incomplete query context accepted")
	}
}

func TestQueryContractRejectsUnknownEnumsAndMalformedTimes(t *testing.T) {
	base := `{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p","actor":"a"},"kind":"provider_status","query":%s}`
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "availability", query: `{"availability":"unknown"}`},
		{name: "lifecycle", query: `{"lifecycle":"future"}`},
		{name: "missing spend interval", query: `{}`},
		{name: "malformed spend interval", query: `{"start_time":"not-a-time","end_time":"2026-07-19T00:00:00Z"}`},
		{name: "empty spend interval", query: `{"start_time":"2026-07-19T00:00:00Z","end_time":"2026-07-19T00:00:00Z"}`},
		{name: "inverted spend interval", query: `{"start_time":"2026-07-20T00:00:00Z","end_time":"2026-07-19T00:00:00Z"}`},
		{name: "unknown group", query: `{"start_time":"2026-07-18T00:00:00Z","end_time":"2026-07-19T00:00:00Z","group_by":["region"]}`},
		{name: "duplicate operation kind", query: `{"start_time":"2026-07-18T00:00:00Z","end_time":"2026-07-19T00:00:00Z","operation_kinds":["query","query"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := fmt.Sprintf(base, test.query)
			if test.name == "availability" || test.name == "lifecycle" {
				kind := "provider_status"
				if test.name == "lifecycle" {
					kind = "model_inventory"
				}
				payload = fmt.Sprintf(`{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p","actor":"a"},"kind":%q,"query":%s}`, kind, test.query)
			} else {
				payload = fmt.Sprintf(`{"api_version":"llm.temporal/query/v1","operation_key":"q","context":{"tenant":"t","project":"p","actor":"a"},"kind":"spend_summary","query":%s}`, test.query)
			}
			var request llm.QueryRequestV1
			if err := json.Unmarshal([]byte(payload), &request); err == nil {
				t.Fatalf("invalid query %s was accepted", test.name)
			}
		})
	}

	valid := llm.QueryResponseV1{
		OperationKey: "q", QueryExecutionID: "query-id", Kind: llm.QuerySpendSummary,
		ObservedAt: "2026-07-19T00:00:00Z", Source: "persisted", Freshness: "current", Complete: true,
		Result: llm.SpendSummary{StartTime: "2026-07-18T00:00:00Z", EndTime: "2026-07-19T00:00:00Z"},
		Cost:   llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "control_query_zero"},
	}
	for _, test := range []struct {
		name string
		edit func(*llm.QueryResponseV1)
	}{
		{name: "observed timestamp", edit: func(response *llm.QueryResponseV1) { response.ObservedAt = "tomorrow" }},
		{name: "source", edit: func(response *llm.QueryResponseV1) { response.Source = "provider" }},
		{name: "freshness", edit: func(response *llm.QueryResponseV1) { response.Freshness = "fresh" }},
		{name: "spend start timestamp", edit: func(response *llm.QueryResponseV1) {
			response.Result = llm.SpendSummary{StartTime: "bad", EndTime: "2026-07-19T00:00:00Z"}
		}},
		{name: "cost method", edit: func(response *llm.QueryResponseV1) { response.Cost.Method = "estimate" }},
		{name: "nonzero control cost", edit: func(response *llm.QueryResponseV1) { response.Cost.ActualCostUSD = stringPointer("0.1") }},
	} {
		t.Run("response_"+test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if _, err := json.Marshal(candidate); err == nil {
				t.Fatalf("invalid response %s was accepted", test.name)
			}
		})
	}
}

func TestQueryResultBoundaryRejectsOpenNestedRows(t *testing.T) {
	base := `{"api_version":"llm.temporal/query/v1","operation_key":"q","query_execution_id":"query-id","kind":"provider_status","observed_at":"2026-07-19T00:00:00Z","source":"persisted","freshness":"current","complete":true,"result":%s,"cost_status":"exact","actual_cost_usd":"0","cost_method":"control_query_zero"}`
	for _, test := range []struct {
		name   string
		result string
	}{
		{name: "unknown page field", result: `{"routes":[],"unexpected":true}`},
		{name: "null required page", result: `{"routes":null}`},
		{name: "unknown route field", result: `{"routes":[{"route_id":"r","provider":"p","endpoint":"e","availability":"available","observed_at":"2026-07-19T00:00:00Z","stale_after":"2026-07-20T00:00:00Z","unexpected":true}]}`},
		{name: "null route row", result: `{"routes":[null]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response llm.QueryResponseV1
			if err := json.Unmarshal([]byte(fmt.Sprintf(base, test.result)), &response); err == nil {
				t.Fatalf("invalid query result %s was accepted", test.name)
			}
		})
	}
	response := llm.QueryResponseV1{
		OperationKey: "q", QueryExecutionID: "query-id", Kind: llm.QueryProviderStatus,
		ObservedAt: "2026-07-19T00:00:00Z", Source: "persisted", Freshness: "current", Complete: true,
		Result: llm.ProviderStatusPage{Routes: []json.RawMessage{json.RawMessage(`{"route_id":"r","provider":"p","endpoint":"e","availability":"available","observed_at":"2026-07-19T00:00:00Z","stale_after":"2026-07-20T00:00:00Z","unexpected":true}`)}},
		Cost:   llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "control_query_zero"},
	}
	if _, err := json.Marshal(response); err == nil {
		t.Fatal("marshal accepted an unknown nested result field")
	}
	for _, test := range []struct {
		name   string
		kind   string
		result string
	}{
		{name: "budget unknown page field", kind: "budget_status", result: `{"active_at":"2026-07-19T00:00:00Z","generation_id":"g","manifest_digest":"0000000000000000000000000000000000000000000000000000000000000000","stream_high_water_mark":"h","windows":[],"unexpected":true}`},
		{name: "spend unknown page field", kind: "spend_summary", result: `{"start_time":"2026-07-18T00:00:00Z","end_time":"2026-07-19T00:00:00Z","buckets":[],"unexpected":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"api_version":"llm.temporal/query/v1","operation_key":"q","query_execution_id":"query-id","kind":%q,"observed_at":"2026-07-19T00:00:00Z","source":"persisted","freshness":"current","complete":true,"result":%s,"cost_status":"exact","actual_cost_usd":"0","cost_method":"control_query_zero"}`, test.kind, test.result)
			var response llm.QueryResponseV1
			if err := json.Unmarshal([]byte(payload), &response); err == nil {
				t.Fatalf("invalid query result %s was accepted", test.name)
			}
		})
	}
}

func TestV1SettingsPatchAndResponseMetadataUseWireDecoders(t *testing.T) {
	requestData := []byte(`{"api_version":"llm.temporal/v1","operation_key":"op","context":{"tenant":"t","project":"p","actor":"a"},"append":[],"settings_patch":{"instructions":{"set":[{"kind":"parts","content":[{"kind":"text","text":"hello"}]}]},"tools":{"set":[{"name":"lookup","description":"lookup data","input_schema":{"type":"object"}}]},"output":{"set":{"max_tokens":32,"format":{"kind":"text"}}}}}`)
	var request llm.GenerateRequestV1
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.SettingsPatch.Instructions.Set == nil || len(*request.SettingsPatch.Instructions.Set) != 1 || len((*request.SettingsPatch.Instructions.Set)[0].Content) != 1 {
		t.Fatalf("parts instruction was not decoded: %#v", request.SettingsPatch.Instructions)
	}
	if request.SettingsPatch.Tools.Set == nil || len(*request.SettingsPatch.Tools.Set) != 1 || string((*request.SettingsPatch.Tools.Set)[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("tool input schema was not decoded: %#v", request.SettingsPatch.Tools)
	}
	if request.SettingsPatch.Output.Set == nil || request.SettingsPatch.Output.Set.MaxTokens == nil || *request.SettingsPatch.Output.Set.MaxTokens != 32 {
		t.Fatalf("output max_tokens was not decoded: %#v", request.SettingsPatch.Output)
	}
	canonicalTemperature := []byte(`{"api_version":"llm.temporal/v1","operation_key":"op","context":{"tenant":"t","project":"p","actor":"a"},"append":[],"settings_patch":{"temperature":{"set":"0.7000"}}}`)
	var decimalRequest llm.GenerateRequestV1
	if err := json.Unmarshal(canonicalTemperature, &decimalRequest); err != nil {
		t.Fatalf("decimal temperature was not decoded: %v", err)
	}
	encodedDecimal, err := json.Marshal(decimalRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encodedDecimal, []byte(`"temperature":{"set":"0.7"}`)) {
		t.Fatalf("temperature was not canonically re-encoded as a string: %s", encodedDecimal)
	}
	legacyNumeric := bytes.Replace(canonicalTemperature, []byte(`"0.7000"`), []byte(`0.7`), 1)
	var legacyRequest llm.GenerateRequestV1
	if err := json.Unmarshal(legacyNumeric, &legacyRequest); err != nil {
		t.Fatalf("legacy numeric temperature was not accepted during compatibility window: %v", err)
	}
	invalidDecimal := bytes.Replace(canonicalTemperature, []byte(`"0.7000"`), []byte(`"1.0000000000000000001"`), 1)
	if err := json.Unmarshal(invalidDecimal, &legacyRequest); err == nil {
		t.Fatal("temperature with excessive precision was accepted")
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(readV1Fixture(t, "generate-response.json"), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["route"] = json.RawMessage(`{"route_id":"route-1","endpoint_id":"endpoint-1","api_family":"responses","requested_model":"gpt-test","resolved_model":"gpt-test-2026"}`)
	envelope["usage"] = json.RawMessage(`{"input_tokens":10,"output_tokens":20,"reasoning_tokens":3,"cache_read_tokens":4,"cache_write_tokens":5}`)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var response llm.GenerateResponseV1
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Route == nil || response.Route.RouteID != "route-1" || response.Route.ResolvedModel != "gpt-test-2026" {
		t.Fatalf("route metadata was not decoded: %#v", response.Route)
	}
	if response.Usage == nil || response.Usage.InputTokens != 10 || response.Usage.CacheWriteTokens != 5 {
		t.Fatalf("usage metadata was not decoded: %#v", response.Usage)
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
