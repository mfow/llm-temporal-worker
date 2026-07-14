package activity

import (
	"context"
	"errors"
	"testing"

	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"go.temporal.io/sdk/temporal"
)

type fakeEngine struct {
	response llm.Response
	err      error
}

func (engine fakeEngine) Generate(context.Context, llm.Request) (llm.Response, error) {
	return engine.response, engine.err
}

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
	if response.Metadata.OperationID != "operation-id" || len(heartbeater.progress) != 2 || heartbeater.progress[0].Phase != "planning" || heartbeater.progress[1].Phase != "finalizing" {
		t.Fatalf("response=%#v heartbeats=%#v", response, heartbeater.progress)
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
