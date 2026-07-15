package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/budget"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/llm/provider/openairesponses"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/routing"
	"github.com/mfow/llm-temporal-worker/state"
	memory "github.com/mfow/llm-temporal-worker/storage/memory"
)

func TestEngineStreamDispatchesDirectOpenAIResponsesTypedEvents(t *testing.T) {
	for _, test := range []struct {
		name  string
		class llm.ServiceClass
		tier  string
	}{
		{name: "economy", class: llm.ServiceClassEconomy, tier: "flex"},
		{name: "standard", class: llm.ServiceClassStandard, tier: "default"},
		{name: "priority", class: llm.ServiceClassPriority, tier: "priority"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &openAIResponsesStreamTransport{body: func() io.ReadCloser {
				return io.NopCloser(strings.NewReader(openAIResponsesStreamSSE(test.tier)))
			}}
			adapter := newDirectOpenAIResponsesStreamAdapter(t, transport)
			harness := newOpenAIResponsesStreamHarness(t, "direct-responses", adapter)
			parent := installDirectResponsesContinuation(t, harness, "direct-responses")
			request := directResponsesStreamRequest("direct-stream-"+test.name, test.class)
			request.Continuation = &llm.Continuation{Handle: parent.String()}

			stream, err := harness.engine.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Engine.Stream() error = %v", err)
			}
			events := readTerminalStream(t, stream)
			if err := stream.Close(); err != nil {
				t.Fatalf("EventStream.Close() error = %v", err)
			}
			if len(events) == 0 {
				t.Fatal("stream emitted no events")
			}
			switch terminal := events[len(events)-1].(type) {
			case llm.ResponseCompleted:
			case llm.StreamErrored:
				t.Fatalf("stream terminal error before transport assertion = %v", terminal.Err)
			default:
				t.Fatalf("stream terminal = %T, want ResponseCompleted", terminal)
			}

			wire := transport.lastRequest(t)
			if got := wire["stream"]; got != true {
				t.Fatalf("stream request flag = %#v, want true", got)
			}
			if got := wire["service_tier"]; got != test.tier {
				t.Fatalf("service tier = %#v, want %q", got, test.tier)
			}
			if got := wire["previous_response_id"]; got != "resp-parent" {
				t.Fatalf("previous response ID = %#v, want resp-parent", got)
			}

			var text llm.TextDelta
			var tool llm.ToolArgumentsDelta
			var completedText llm.Item
			var completedTool llm.Item
			var terminal llm.ResponseCompleted
			for _, event := range events {
				switch value := event.(type) {
				case llm.TextDelta:
					text = value
				case llm.ToolArgumentsDelta:
					tool = value
				case llm.ContentCompleted:
					if value.OutputIndex != nil && *value.OutputIndex == 0 {
						completedText = value.Item
					}
					if value.OutputIndex != nil && *value.OutputIndex == 1 {
						completedTool = value.Item
					}
				case llm.ResponseCompleted:
					terminal = value
				}
			}
			if text.Text != "hello" || text.OutputIndex == nil || *text.OutputIndex != 0 {
				t.Fatalf("text delta = %#v, want output 0 hello", text)
			}
			textItem, ok := completedText.(llm.Message)
			if !ok || len(textItem.Content) != 1 || textItem.Content[0] != (llm.TextPart{Text: "hello"}) {
				t.Fatalf("completed text item = %#v", completedText)
			}
			if tool.CallID != "call-1" || tool.Name != "lookup" || tool.Fragment != `{"q":"x"}` || tool.OutputIndex == nil || *tool.OutputIndex != 1 {
				t.Fatalf("tool delta = %#v", tool)
			}
			toolItem, ok := completedTool.(llm.ToolCall)
			if !ok || toolItem.ID != "call-1" || toolItem.Name != "lookup" || string(toolItem.Arguments) != `{"q":"x"}` {
				t.Fatalf("completed tool item = %#v", completedTool)
			}
			if terminal.Response.Provider.ResponseID != "resp-stream" || terminal.Response.Provider.RequestID != "req-stream" {
				t.Fatalf("terminal provider facts = %#v", terminal.Response.Provider)
			}
			if terminal.Response.Service.Requested != test.class || terminal.Response.Service.Attempted != test.class || terminal.Response.Service.Actual == nil || *terminal.Response.Service.Actual != test.class || terminal.Response.Service.ProviderValue != test.tier {
				t.Fatalf("terminal service facts = %#v", terminal.Response.Service)
			}
			if terminal.Response.Continuation == nil || terminal.Response.Continuation.EndpointID != "direct-responses" || terminal.Response.Continuation.Handle == "" {
				t.Fatalf("terminal continuation = %#v", terminal.Response.Continuation)
			}
		})
	}
}

func TestEngineStreamTreatsIncompleteDirectOpenAIResponsesAsSemanticTerminals(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason string
		status llm.ResponseStatus
	}{
		{name: "length", reason: "max_output_tokens", status: llm.ResponseStatusLength},
		{name: "content filtered", reason: "content_filter", status: llm.ResponseStatusContentFiltered},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &openAIResponsesStreamTransport{body: func() io.ReadCloser {
				return io.NopCloser(strings.NewReader(openAIResponsesIncompleteStreamSSE("default", test.reason)))
			}}
			adapter := newDirectOpenAIResponsesStreamAdapter(t, transport)
			harness := newOpenAIResponsesStreamHarness(t, "direct-responses", adapter)
			parent := installDirectResponsesContinuation(t, harness, "direct-responses")
			request := directResponsesStreamRequest("direct-stream-incomplete-"+test.name, llm.ServiceClassStandard)
			request.Continuation = &llm.Continuation{Handle: parent.String()}

			stream, err := harness.engine.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Engine.Stream() error = %v", err)
			}
			events := readTerminalStream(t, stream)
			if err := stream.Close(); err != nil {
				t.Fatalf("EventStream.Close() error = %v", err)
			}
			if len(events) == 0 {
				t.Fatal("stream emitted no events")
			}
			terminal, ok := events[len(events)-1].(llm.ResponseCompleted)
			if !ok {
				if failed, failedOK := events[len(events)-1].(llm.StreamErrored); failedOK {
					t.Fatalf("incomplete response became stream error = %v", failed.Err)
				}
				t.Fatalf("stream terminal = %T, want ResponseCompleted", events[len(events)-1])
			}
			if terminal.Response.Status != test.status {
				t.Fatalf("response status = %q, want %q", terminal.Response.Status, test.status)
			}
			if terminal.Response.Provider.ResponseID != "resp-incomplete" || terminal.Response.Provider.RequestID != "req-stream" {
				t.Fatalf("terminal provider facts = %#v", terminal.Response.Provider)
			}
			if terminal.Response.Continuation == nil || terminal.Response.Continuation.Handle == "" {
				t.Fatalf("terminal continuation = %#v", terminal.Response.Continuation)
			}
			if transport.calls() != 1 {
				t.Fatalf("OpenAI stream requests = %d, want one", transport.calls())
			}
		})
	}
}

func TestEngineStreamKeepsAzureResponsesUnsupportedBeforeDispatch(t *testing.T) {
	transport := &openAIResponsesStreamTransport{body: func() io.ReadCloser {
		return io.NopCloser(strings.NewReader(openAIResponsesStreamSSE("default")))
	}}
	client, err := openairesponses.NewAzureClient(openairesponses.AzureClientConfig{
		Endpoint:   "http://127.0.0.1",
		APIVersion: "v1",
		APIKey:     "azure-test-key",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openairesponses.NewAzureAdapter(client, "azure-responses", "azure-responses/v1")
	if err != nil {
		t.Fatal(err)
	}
	harness := newOpenAIResponsesStreamHarness(t, "azure-responses", adapter)
	stream, err := harness.engine.Stream(context.Background(), directResponsesStreamRequest("azure-stays-gated", llm.ServiceClassStandard))
	if stream != nil {
		_ = stream.Close()
		t.Fatal("Azure Responses unexpectedly returned an EventStream")
	}
	if err == nil {
		t.Fatal("Azure Responses unexpectedly dispatched a stream")
	}
	mapped, ok := err.(*provider.Error)
	if !ok || mapped.Code != provider.CodeUnsupportedCapability || mapped.Dispatch != provider.DispatchNotDispatched {
		t.Fatalf("Azure stream error = %#v", err)
	}
	if transport.calls() != 0 {
		t.Fatalf("Azure transport calls = %d, want zero", transport.calls())
	}
}

func TestEngineStreamCloseAndParentCancellationReleaseDirectOpenAIResponseBody(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger func(llm.EventStream, context.CancelFunc) error
	}{
		{name: "close", trigger: func(stream llm.EventStream, _ context.CancelFunc) error { return stream.Close() }},
		{name: "parent cancellation", trigger: func(_ llm.EventStream, cancel context.CancelFunc) error { cancel(); return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := newBlockingOpenAIResponseBody()
			transport := &openAIResponsesStreamTransport{body: func() io.ReadCloser { return body }}
			adapter := newDirectOpenAIResponsesStreamAdapter(t, transport)
			harness := newOpenAIResponsesStreamHarness(t, "direct-responses", adapter)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := harness.engine.Stream(ctx, directResponsesStreamRequest("direct-stream-close-"+test.name, llm.ServiceClassStandard))
			if err != nil {
				t.Fatalf("Engine.Stream() error = %v", err)
			}
			waitChannel(t, body.entered, "OpenAI stream body read")
			if err := test.trigger(stream, cancel); err != nil {
				t.Fatalf("stream shutdown error = %v", err)
			}
			waitChannel(t, body.closed, "OpenAI stream body close")
			_ = stream.Close()
		})
	}
}

func newDirectOpenAIResponsesStreamAdapter(t *testing.T, transport http.RoundTripper) *openairesponses.Adapter {
	t.Helper()
	client, err := openairesponses.NewClient(openairesponses.ClientConfig{
		BaseURL:    "http://127.0.0.1/v1",
		APIKey:     "direct-test-key",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openairesponses.NewAdapter(client, "direct-responses", "openai-responses/v1")
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newOpenAIResponsesStreamHarness(t *testing.T, endpointID string, adapter provider.Adapter) testHarness {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	classes := []llm.ServiceClass{llm.ServiceClassEconomy, llm.ServiceClassStandard, llm.ServiceClassPriority}
	tiers := map[llm.ServiceClass]string{
		llm.ServiceClassEconomy:  "flex",
		llm.ServiceClassStandard: "default",
		llm.ServiceClassPriority: "priority",
	}
	routes, err := routing.CompileCatalog("openai-stream-routes", map[string]routing.Model{
		"gpt-contract": {Routes: []routing.Route{{
			ID: "direct-responses-route", EndpointID: endpointID, Provider: "openai", Family: string(provider.FamilyOpenAIResponses), Region: "us-east-1", AccountRegion: "us-east-1", Model: "gpt-contract", ModelLineage: "gpt-contract", Classes: classes, ProviderTiers: tiers, PriceVersion: "openai-stream-prices", PriceAvailable: true,
			Capabilities: routing.CapabilitySet{Version: "openai-responses/v1", Features: map[routing.Feature]routing.Capability{
				routing.FeatureText:         {State: routing.CapabilityNative},
				routing.FeatureContinuation: {State: routing.CapabilityNative},
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]pricing.Entry, 0, len(classes))
	for _, class := range classes {
		entries = append(entries, pricing.Entry{Provider: "openai", Family: string(provider.FamilyOpenAIResponses), EndpointID: endpointID, Region: "us-east-1", Model: "gpt-contract", ProviderTier: tiers[class], Currency: "USD", Version: "openai-stream-prices", Prices: pricing.UnitPrices{PerRequest: pricing.MustDecimalUSD("0.000001"), OutputPerMillion: pricing.MustDecimalUSD("1")}})
	}
	priceCatalog, err := pricing.CompileCatalog("openai-stream-prices", "USD", entries)
	if err != nil {
		t.Fatal(err)
	}
	results := &fakeResultStore{values: make(map[string]llm.Response)}
	admissionStore := memory.NewAdmissionStore(memory.AdmissionOptions{Clock: func() time.Time { return now }})
	engineValue, err := New(Dependencies{
		Snapshots:   StaticSnapshot{Value: Snapshot{Version: "openai-stream-snapshot", Routes: routes, Prices: pricing.NewResolver(priceCatalog), ReservationLease: time.Minute, OperationRetention: time.Hour}},
		Planner:     routing.DeterministicPlanner{},
		Adapters:    AdapterMap{endpointID: adapter},
		Admission:   admissionStore,
		Results:     results,
		Clock:       func() time.Time { return now },
		Estimator:   budget.Estimator{MaxOutput: 1},
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testHarness{engine: engineValue, admission: admissionStore, results: results, clock: now}
}

func directResponsesStreamRequest(operationKey string, class llm.ServiceClass) llm.Request {
	return llm.Request{
		OperationKey: operationKey,
		Context:      llm.RequestContext{Tenant: "tenant-1"},
		Model:        "gpt-contract",
		ServiceClass: class,
		Input: []llm.Item{llm.Message{
			Actor:   llm.ActorHuman,
			Content: []llm.Part{llm.TextPart{Text: "hello"}},
		}},
	}
}

func installDirectResponsesContinuation(t *testing.T, harness testHarness, endpointID string) state.Handle {
	t.Helper()
	keyring, err := state.NewKeyring([]state.Key{{ID: "k1", Secret: bytes.Repeat([]byte{1}, 32), Primary: true}}, bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.NewContinuationStore(memory.ContinuationOptions{Keyring: keyring, Clock: func() time.Time { return harness.clock }})
	if err != nil {
		t.Fatal(err)
	}
	harness.engine.dependencies.Continuations = store
	transcript := []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "prior request"}}}}
	_, digest, err := state.CanonicalTranscript(transcript)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.CreateRoot(context.Background(), state.Continuation{
		Tenant:             "tenant-1",
		Transcript:         transcript,
		TranscriptDigest:   digest,
		TranscriptComplete: true,
		ProviderState: []state.OpaqueStateRef{{
			Provider: "openai", EndpointID: endpointID, Family: string(provider.FamilyOpenAIResponses), ModelLineage: "gpt-contract", Media: providerHandleMedia, Data: []byte("resp-parent"), Required: true,
		}},
		Pinning:   state.Pinning{Provider: "openai", EndpointID: endpointID, Family: string(provider.FamilyOpenAIResponses), ModelLineage: "gpt-contract"},
		ExpiresAt: harness.clock.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

type openAIResponsesStreamTransport struct {
	mu       sync.Mutex
	body     func() io.ReadCloser
	requests [][]byte
}

func (transport *openAIResponsesStreamTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.body == nil {
		return nil, fmt.Errorf("OpenAI stream transport has no response body")
	}
	wire, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, append([]byte(nil), wire...))
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req-stream"}},
		Body:       transport.body(),
		Request:    request,
	}, nil
}

func (transport *openAIResponsesStreamTransport) calls() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return len(transport.requests)
}

func (transport *openAIResponsesStreamTransport) lastRequest(t *testing.T) map[string]any {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 1 {
		t.Fatalf("OpenAI stream requests = %d, want one", len(transport.requests))
	}
	var result map[string]any
	if err := json.Unmarshal(transport.requests[0], &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type blockingOpenAIResponseBody struct {
	entered   chan struct{}
	closed    chan struct{}
	readOnce  sync.Once
	closeOnce sync.Once
}

func newBlockingOpenAIResponseBody() *blockingOpenAIResponseBody {
	return &blockingOpenAIResponseBody{entered: make(chan struct{}), closed: make(chan struct{})}
}

func (body *blockingOpenAIResponseBody) Read([]byte) (int, error) {
	body.readOnce.Do(func() { close(body.entered) })
	<-body.closed
	return 0, io.EOF
}

func (body *blockingOpenAIResponseBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func openAIResponsesStreamSSE(tier string) string {
	terminal := fmt.Sprintf(`{"id":"resp-stream","model":"gpt-contract","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[],"logprobs":[]}]},{"id":"fc-1","type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}],"service_tier":%q,"status":"completed","usage":{"input_tokens":1,"input_tokens_details":{},"output_tokens":1,"output_tokens_details":{},"total_tokens":2}}`, tier)
	return strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg-1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg-1","delta":"hello","logprobs":[]}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[],"logprobs":[]}]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc-1","type":"function_call","status":"in_progress","call_id":"call-1","name":"lookup","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc-1","delta":"{\"q\":\"x\"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc-1","type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`,
		"",
		"event: response.completed",
		"data: {\"type\":\"response.completed\",\"response\":" + terminal + "}",
		"",
	}, "\n")
}

func openAIResponsesIncompleteStreamSSE(tier, reason string) string {
	terminal := fmt.Sprintf(`{"id":"resp-incomplete","model":"gpt-contract","output":[],"service_tier":%q,"status":"incomplete","incomplete_details":{"reason":%q},"usage":{"input_tokens":1,"input_tokens_details":{},"output_tokens":1,"output_tokens_details":{},"total_tokens":2}}`, tier, reason)
	return "event: response.incomplete\n" + "data: {\"type\":\"response.incomplete\",\"response\":" + terminal + "}\n\n"
}
