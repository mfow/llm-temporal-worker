package activity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"go.temporal.io/sdk/temporal"
)

type fakeEngine struct {
	response llm.Response
	err      error
	events   []llm.Event
}

func (fakeEngine) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Activity must consume Engine.Stream rather than Engine.Generate")
}

func (engine fakeEngine) Stream(_ context.Context, _ llm.Request) (llm.EventStream, error) {
	if engine.err != nil {
		return nil, engine.err
	}
	events := engine.events
	if events == nil {
		events = []llm.Event{
			llm.ResponseStarted{EventHeader: llm.EventHeader{Sequence: 1, OperationID: engine.response.OperationID}},
			llm.ResponseCompleted{EventHeader: llm.EventHeader{Sequence: 2, OperationID: engine.response.OperationID}, Response: engine.response},
		}
	}
	return &sliceEventStream{events: events}, nil
}

type nativeFallbackEngine struct {
	response      llm.Response
	generateCalls int
	streamCalls   int
}

func (engine *nativeFallbackEngine) Generate(context.Context, llm.Request) (llm.Response, error) {
	engine.generateCalls++
	return engine.response, nil
}

func (engine *nativeFallbackEngine) Stream(context.Context, llm.Request) (llm.EventStream, error) {
	engine.streamCalls++
	return nil, provider.NewError(provider.CodeUnsupportedCapability, provider.PhaseStream, provider.DispatchNotDispatched, provider.RetryNever, "no eligible adapter implements provider streaming")
}

type terminalUnsupportedStreamEngine struct {
	response      llm.Response
	generateCalls int
}

func (engine *terminalUnsupportedStreamEngine) Generate(context.Context, llm.Request) (llm.Response, error) {
	engine.generateCalls++
	return engine.response, nil
}

func (engine *terminalUnsupportedStreamEngine) Stream(context.Context, llm.Request) (llm.EventStream, error) {
	return &sliceEventStream{events: []llm.Event{
		llm.StreamErrored{EventHeader: llm.EventHeader{Sequence: 1, OperationID: engine.response.OperationID}, Err: provider.NewError(provider.CodeUnsupportedCapability, provider.PhaseStream, provider.DispatchNotDispatched, provider.RetryNever, "stream became unavailable after admission")},
	}}, nil
}

type sliceEventStream struct {
	events []llm.Event
	next   int
}

func (stream *sliceEventStream) Next(ctx context.Context) (llm.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stream.next == len(stream.events) {
		return nil, io.EOF
	}
	event := stream.events[stream.next]
	stream.next++
	return event, nil
}

func (*sliceEventStream) Close() error { return nil }

type fakeHeartbeater struct{ progress []engine.Progress }

func (heartbeater *fakeHeartbeater) Beat(_ context.Context, progress engine.Progress) error {
	heartbeater.progress = append(heartbeater.progress, progress)
	return nil
}

func TestGenerateActivityMapsPayloadAndHeartbeats(t *testing.T) {
	heartbeater := &fakeHeartbeater{}
	activities := Activities{Engine: fakeEngine{response: llm.Response{OperationKey: "operation-1", OperationID: "operation-id", Status: llm.ResponseStatusCompleted, Service: llm.ServiceFacts{Requested: llm.ServiceClassStandard, Attempted: llm.ServiceClassStandard}}}, Heartbeater: heartbeater}
	response, err := activities.Generate(context.Background(), GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata.OperationID != "operation-id" || len(heartbeater.progress) != 3 || heartbeater.progress[0].Phase != "planning" || heartbeater.progress[1].Phase != "streaming" || heartbeater.progress[2].Phase != "finalizing" {
		t.Fatalf("response=%#v heartbeats=%#v", response, heartbeater.progress)
	}
}

func TestGenerateActivityUsesOnlyBoundedStreamProgress(t *testing.T) {
	heartbeater := &fakeHeartbeater{}
	response := llm.Response{OperationKey: "operation-1", OperationID: "operation-id", Status: llm.ResponseStatusCompleted, Service: llm.ServiceFacts{Requested: llm.ServiceClassStandard, Attempted: llm.ServiceClassStandard}}
	activities := Activities{Engine: fakeEngine{response: response, events: []llm.Event{
		llm.ResponseStarted{EventHeader: llm.EventHeader{Sequence: 1, OperationID: "operation-id"}},
		llm.TextDelta{EventHeader: llm.EventHeader{Sequence: 2, OperationID: "operation-id", OutputIndex: intPointer(0)}, Text: "unbounded raw provider delta"},
		llm.ContentCompleted{EventHeader: llm.EventHeader{Sequence: 3, OperationID: "operation-id", OutputIndex: intPointer(0)}},
		llm.ResponseCompleted{EventHeader: llm.EventHeader{Sequence: 4, OperationID: "operation-id"}, Response: response},
	}}, Heartbeater: heartbeater}
	result, err := activities.Generate(context.Background(), GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeater.progress) != 4 || heartbeater.progress[2].OutputItems != 1 {
		t.Fatalf("heartbeat progress = %#v", heartbeater.progress)
	}
	encoded, err := MarshalResponse(result, PayloadLimits{})
	if err != nil {
		t.Fatalf("marshal Activity response: %v", err)
	}
	if strings.Contains(string(encoded), "unbounded raw provider delta") {
		t.Fatalf("Activity return leaked a stream delta: %s", encoded)
	}
	if rendered := fmt.Sprintf("%#v", heartbeater.progress); strings.Contains(rendered, "unbounded raw provider delta") {
		t.Fatalf("Activity heartbeat leaked a stream delta: %s", rendered)
	}
}

func TestGenerateActivityMapsEngineError(t *testing.T) {
	err := provider.NewError(provider.CodeAmbiguousDispatch, provider.PhaseDispatch, provider.DispatchAmbiguous, provider.RetryNever, "safe")
	activities := Activities{Engine: fakeEngine{err: err}}
	_, got := activities.Generate(context.Background(), GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1"}})
	var applicationErr *temporal.ApplicationError
	if !errors.As(got, &applicationErr) {
		t.Fatalf("error = %v", got)
	}
	if applicationErr.Type() != ErrorTypeAmbiguous || !applicationErr.NonRetryable() {
		t.Fatalf("error type = %q non_retryable=%v", applicationErr.Type(), applicationErr.NonRetryable())
	}
}

func TestGenerateActivityFallsBackForPreAdmissionStreamingUnsupported(t *testing.T) {
	heartbeater := &fakeHeartbeater{}
	native := &nativeFallbackEngine{response: llm.Response{OperationKey: "operation-1", OperationID: "operation-id", Status: llm.ResponseStatusCompleted, Service: llm.ServiceFacts{Requested: llm.ServiceClassStandard, Attempted: llm.ServiceClassStandard}}}
	activities := Activities{Engine: native, Heartbeater: heartbeater}
	response, err := activities.Generate(context.Background(), GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata.OperationID != "operation-id" || native.streamCalls != 1 || native.generateCalls != 1 {
		t.Fatalf("response=%#v stream calls=%d generate calls=%d", response, native.streamCalls, native.generateCalls)
	}
	if len(heartbeater.progress) != 2 || heartbeater.progress[0].Phase != "planning" || heartbeater.progress[1].Phase != "finalizing" {
		t.Fatalf("fallback heartbeats = %#v, want planning/finalizing only", heartbeater.progress)
	}
}

func TestGenerateActivityNeverFallsBackAfterReturnedStreamTerminal(t *testing.T) {
	streaming := &terminalUnsupportedStreamEngine{response: llm.Response{OperationKey: "operation-1", OperationID: "operation-id", Status: llm.ResponseStatusCompleted, Service: llm.ServiceFacts{Requested: llm.ServiceClassStandard, Attempted: llm.ServiceClassStandard}}}
	activities := Activities{Engine: streaming}
	_, err := activities.Generate(context.Background(), GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}})
	if err == nil {
		t.Fatal("Activity accepted a terminal stream failure")
	}
	if streaming.generateCalls != 0 {
		t.Fatalf("Activity called Generate %d times after Stream returned a terminal event", streaming.generateCalls)
	}
}

func intPointer(value int) *int { return &value }
