package openairesponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestCompileUsesAdapterCapabilitiesAndMetadata(t *testing.T) {
	adapter := newFixtureAdapter(t, []byte(`{"id":"unused"}`))
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "op-compile", Model: "gpt-contract", ServiceClass: llm.ServiceClassPriority},
		Query:   provider.CapabilityQuery{EndpointID: "openai-prod", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.OperationKey != "op-compile" || call.ServiceClass != llm.ServiceClassPriority || call.Metadata.ProviderTier != "priority" {
		t.Fatalf("call = %#v", call)
	}
	if call.Metadata.CapabilityVersion == "" || call.Metadata.EstimatedBytes <= 0 || call.Metadata.SchemaDigest == ([32]byte{}) {
		t.Fatalf("metadata = %#v", call.Metadata)
	}
	params, ok := call.SDKParams.(responses.ResponseNewParams)
	if !ok {
		t.Fatalf("SDK params type = %T", call.SDKParams)
	}
	if got := marshalParams(t, params)["service_tier"]; got != "priority" {
		t.Fatalf("compiled service tier = %#v", got)
	}
}

func TestInvokeRunsOneRequestAndLiftsResponse(t *testing.T) {
	responseBody, err := os.ReadFile("testdata/contracts/openai-responses/response.completed.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	adapter := newFixtureAdapterWithTransport(t, responseBody, func() { calls++ })
	call, err := adapter.Compile(context.Background(), provider.CompileInput{Request: llm.Request{OperationKey: "op-invoke", Model: "gpt-contract"}})
	if err != nil {
		t.Fatal(err)
	}
	observer := &recordingObserver{}
	result, err := adapter.Invoke(context.Background(), call, observer)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("HTTP calls = %d, want one", calls)
	}
	if result.Response.OperationKey != "op-invoke" || result.Response.Provider.ResponseID != "resp-1" {
		t.Fatalf("response = %#v", result.Response)
	}
	if observer.before != 1 || observer.headers != 1 || observer.progress != 1 || observer.metadata.RequestID != "req-invoke" {
		t.Fatalf("observer = %#v", observer)
	}
}

func TestStrictStoredResponseContinuationCompilesFollowUp(t *testing.T) {
	responseBody, err := os.ReadFile("testdata/contracts/openai-responses/response.completed.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := newFixtureAdapter(t, responseBody)
	first, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{
			OperationKey: "op-first",
			Model:        "gpt-contract",
			Input:        []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "first turn"}}}},
			Extensions:   map[string]json.RawMessage{"openai.responses": json.RawMessage(`{"store":true}`)},
		},
		Query:  provider.CapabilityQuery{EndpointID: "openai-prod", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract"},
		Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := adapter.Invoke(context.Background(), first, provider.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Response.Continuation == nil {
		t.Fatal("stored response did not yield a continuation")
	}
	followUp, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{
			OperationKey: "op-follow-up",
			Model:        "gpt-contract",
			Input:        []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "second turn"}}}},
			Continuation: firstResult.Response.Continuation,
		},
		Query:  provider.CapabilityQuery{EndpointID: "openai-prod", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract"},
		Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	params, ok := followUp.SDKParams.(responses.ResponseNewParams)
	if !ok {
		t.Fatalf("follow-up SDK params type = %T", followUp.SDKParams)
	}
	if got := marshalParams(t, params)["previous_response_id"]; got != "resp-1" {
		t.Fatalf("previous response ID = %#v, want resp-1", got)
	}
}

func TestInvokeOmitsContinuationForUnstoredResponse(t *testing.T) {
	responseBody, err := os.ReadFile("testdata/contracts/openai-responses/response.completed.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := newFixtureAdapter(t, responseBody)
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{
			OperationKey: "op-unstored",
			Model:        "gpt-contract",
			Extensions:   map[string]json.RawMessage{"openai.responses": json.RawMessage(`{"store":false}`)},
		},
		Query:  provider.CapabilityQuery{EndpointID: "openai-prod", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract"},
		Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Invoke(context.Background(), call, provider.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Continuation != nil {
		t.Fatalf("unstored response continuation = %#v, want nil", result.Response.Continuation)
	}
}

func TestCapabilitiesRejectMismatchedFamily(t *testing.T) {
	adapter := newFixtureAdapter(t, []byte(`{}`))
	_, err := adapter.Capabilities(context.Background(), provider.CapabilityQuery{Family: provider.FamilyOpenAIChat})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched family error = %v", err)
	}
}

type recordingObserver struct {
	before   int
	headers  int
	progress int
	metadata provider.ResponseMetadata
}

func (observer *recordingObserver) BeforePossibleWrite(context.Context) error {
	observer.before++
	return nil
}

func (observer *recordingObserver) AfterResponseHeaders(_ context.Context, metadata provider.ResponseMetadata) error {
	observer.headers++
	observer.metadata = metadata
	return nil
}

func (observer *recordingObserver) OnProgress(context.Context, provider.Progress) {
	observer.progress++
}

func newFixtureAdapter(t *testing.T, body []byte) *Adapter {
	return newFixtureAdapterWithTransport(t, body, nil)
}

func newFixtureAdapterWithTransport(t *testing.T, body []byte, onCall func()) *Adapter {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL: "http://127.0.0.1/contract",
		APIKey:  "test-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if onCall != nil {
				onCall()
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"req-invoke"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Request:    request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(client, "openai-prod", "cap-test")
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
