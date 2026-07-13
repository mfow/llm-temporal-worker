package openaichat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestProfileRequiresExplicitFeaturesAndClasses(t *testing.T) {
	profile := testProfile()
	delete(profile.Capabilities.Features, provider.FeatureDocument)
	if _, err := NewProfile(profile); err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("missing feature error = %v", err)
	}
	profile = testProfile()
	delete(profile.ServiceTiers, llm.ServiceClassPriority)
	if _, err := NewProfile(profile); err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("missing tier error = %v", err)
	}
}

func TestCompileRejectsUnknownCapabilityInStrictMode(t *testing.T) {
	adapter := testAdapter(t)
	request := llm.Request{
		OperationKey: "op-unknown",
		Model:        "chat-model",
		Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{
			llm.ImagePart{URL: "https://example.test/image.png", MediaType: "image/png"},
		}}},
	}
	_, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: request,
		Query: provider.CapabilityQuery{
			EndpointID: "chat-prod",
			Family:     provider.FamilyOpenAIChat,
			Model:      "chat-model",
		},
		Capability: provider.CapabilitySet{Version: "partial", Features: map[provider.Feature]provider.Capability{
			provider.FeatureText:  {State: provider.CapabilityNative},
			provider.FeatureUsage: {State: provider.CapabilityNative},
		}},
		Strict: true,
	})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("strict unknown capability error = %v", err)
	}
}

func TestCompileRejectsContinuationBeforeInvocation(t *testing.T) {
	adapter := testAdapter(t)
	_, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{
			OperationKey: "op-continuation",
			Model:        "chat-model",
			Continuation: &llm.Continuation{Handle: "chat:completion-1"},
		},
		Query:  provider.CapabilityQuery{EndpointID: "chat-prod", Family: provider.FamilyOpenAIChat, Model: "chat-model"},
		Strict: true,
	})
	if err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("continuation error = %v", err)
	}
}

func TestInvokeSubmitsExactlyOnceAndLiftsResponse(t *testing.T) {
	responseBody := `{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"chat-model","service_tier":"default","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done","refusal":""}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	calls := 0
	client, err := NewClient(ClientConfig{
		BaseURL: "http://127.0.0.1/contract",
		APIKey:  "test-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"req-1"}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Request:    request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(client, "chat-prod", testProfile())
	if err != nil {
		t.Fatal(err)
	}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "op-invoke", Model: "chat-model"},
		Query:   provider.CapabilityQuery{EndpointID: "chat-prod", Family: provider.FamilyOpenAIChat, Model: "chat-model"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &chatObserver{}
	result, err := adapter.Invoke(context.Background(), call, observer)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || observer.before != 1 || observer.headers != 1 || observer.progress != 1 {
		t.Fatalf("transport/observer counts = %d %#v", calls, observer)
	}
	if result.Response.OperationKey != "op-invoke" || result.Response.Provider.RequestID != "req-1" || result.Response.Status != llm.ResponseStatusCompleted {
		t.Fatalf("response = %#v", result.Response)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type chatObserver struct {
	before   int
	headers  int
	progress int
}

func (observer *chatObserver) BeforePossibleWrite(context.Context) error {
	observer.before++
	return nil
}

func (observer *chatObserver) AfterResponseHeaders(context.Context, provider.ResponseMetadata) error {
	observer.headers++
	return nil
}

func (observer *chatObserver) OnProgress(context.Context, provider.Progress) {
	observer.progress++
}

func testProfile() Profile {
	features := map[provider.Feature]provider.Capability{}
	for _, feature := range allFeatures() {
		state := provider.CapabilityNative
		if feature == provider.FeatureDocument || feature == provider.FeatureContinuation || feature == provider.FeatureStreaming {
			state = provider.CapabilityUnsupported
		}
		features[feature] = provider.Capability{State: state, Reason: "contract test"}
	}
	return Profile{
		ID:                "chat-contract",
		CapabilityVersion: "chat-contract/v1",
		Capabilities:      provider.CapabilitySet{Version: "chat-contract/v1", Features: features},
		ServiceTiers: map[llm.ServiceClass]string{
			llm.ServiceClassEconomy:  "flex",
			llm.ServiceClassStandard: "default",
			llm.ServiceClassPriority: "priority",
		},
		ActualServiceClasses: map[string]llm.ServiceClass{
			"flex":     llm.ServiceClassEconomy,
			"default":  llm.ServiceClassStandard,
			"priority": llm.ServiceClassPriority,
		},
		AllowedExtensions: map[string]ExtensionSpec{
			"chat.contract": {Fields: map[string]string{"provider_hint": "user"}},
		},
	}
}

func testAdapter(t *testing.T) *Adapter {
	t.Helper()
	validated, err := NewProfile(testProfile())
	if err != nil {
		t.Fatal(err)
	}
	return &Adapter{endpointID: "chat-prod", profile: validated}
}

func marshalWire(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
