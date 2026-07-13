package openairesponses

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestWireFixtureMatchesNormalizedSDKParams(t *testing.T) {
	params, err := lowerRequest(llm.Request{
		OperationKey: "fixture-op",
		Model:        "gpt-contract",
		ServiceClass: llm.ServiceClassEconomy,
		Input:        []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}},
	}, llm.ServiceClassEconomy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	want := readFixture(t, "request.wire.json")
	gotCanonical, err := llm.CanonicalJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, err := llm.CanonicalJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotCanonical, wantCanonical) {
		t.Fatalf("wire fixture mismatch\n got: %s\nwant: %s", gotCanonical, wantCanonical)
	}
}

func TestSemanticFixtureMatchesLiftedResponse(t *testing.T) {
	response := minimalResponse(responses.ResponseServiceTierDefault, responses.ResponseStatusCompleted)
	response.ID = "resp-fixture"
	response.Model = shared.ResponsesModel("gpt-contract")
	call := provider.Call{EndpointID: "openai-prod", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract", OperationKey: "fixture-op", ServiceClass: llm.ServiceClassStandard}
	lifted, err := liftResponse(call, &response, "req-fixture")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(lifted)
	if err != nil {
		t.Fatal(err)
	}
	want := readFixture(t, "response.semantic.json")
	gotCanonical, err := llm.CanonicalJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, err := llm.CanonicalJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotCanonical, wantCanonical) {
		t.Fatalf("semantic fixture mismatch\n got: %s\nwant: %s", gotCanonical, wantCanonical)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/contracts/openai-responses/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
