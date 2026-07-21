package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
)

type resumableEngineAdapter struct {
	mu             sync.Mutex
	submits        int
	polls          int
	terminal       bool
	submitErr      error
	pollResponses  []provider.ResumableResult
	recoveryPollID string
}

func (adapter *resumableEngineAdapter) Name() string { return "resumable-fixture" }

func (adapter *resumableEngineAdapter) Capabilities(context.Context, provider.CapabilityQuery) (provider.CapabilitySet, error) {
	return provider.CapabilitySet{Version: "fixture", Features: map[provider.Feature]provider.Capability{provider.FeatureText: {State: provider.CapabilityNative}}}, nil
}

func (adapter *resumableEngineAdapter) Compile(_ context.Context, input provider.CompileInput) (provider.Call, error) {
	return provider.Call{EndpointID: input.Query.EndpointID, Family: input.Query.Family, Model: input.Query.Model, OperationKey: input.Request.OperationKey, ServiceClass: input.Query.ServiceClass, Metadata: input.Metadata}, nil
}

func (adapter *resumableEngineAdapter) Invoke(context.Context, provider.Call, provider.Observer) (provider.Result, error) {
	return provider.Result{}, provider.NewError(provider.CodeInternal, provider.PhaseDispatch, provider.DispatchNotDispatched, provider.RetryNever, "fixture Invoke must not be called")
}

func (adapter *resumableEngineAdapter) Submit(ctx context.Context, _ provider.Call, observer provider.Observer) (provider.ResumableResult, error) {
	if err := observer.BeforePossibleWrite(ctx); err != nil {
		return provider.ResumableResult{}, err
	}
	adapter.mu.Lock()
	adapter.submits++
	adapter.mu.Unlock()
	if adapter.submitErr != nil {
		return provider.ResumableResult{}, adapter.submitErr
	}
	return provider.ResumableResult{State: provider.ResumablePending, ProviderOperationID: "fixture-provider-operation", Dispatch: provider.DispatchAccepted}, nil
}

func (adapter *resumableEngineAdapter) Poll(_ context.Context, _ provider.Call, id string, _ provider.Observer) (provider.ResumableResult, error) {
	adapter.mu.Lock()
	adapter.polls++
	poll := adapter.polls
	responses := adapter.pollResponses
	recoveryPollID := adapter.recoveryPollID
	adapter.mu.Unlock()
	if len(responses) > 0 {
		if recoveryPollID != "" && id != recoveryPollID {
			return provider.ResumableResult{}, provider.NewError(provider.CodeStateCorrupt, provider.PhasePoll, provider.DispatchAmbiguous, provider.RetryNever, "unexpected recovered provider id")
		}
		if poll > len(responses) {
			return provider.ResumableResult{}, context.Canceled
		}
		return responses[poll-1], nil
	}
	if id != "fixture-provider-operation" {
		return provider.ResumableResult{}, provider.NewError(provider.CodeStateCorrupt, provider.PhasePoll, provider.DispatchAmbiguous, provider.RetryNever, "unexpected fixture provider id")
	}
	if adapter.terminal {
		return provider.ResumableResult{State: provider.ResumableNotFound, Dispatch: provider.DispatchAmbiguous}, nil
	}
	if poll == 1 {
		// Simulate a worker/activity interruption after the durable ID was
		// written. The next Activity attempt must poll, never submit.
		return provider.ResumableResult{}, context.Canceled
	}
	response := successfulResponse()
	response.OperationKey = "resumable-retry"
	return provider.ResumableResult{State: provider.ResumableCompleted, ProviderOperationID: id, Dispatch: provider.DispatchAccepted, Result: provider.Result{Response: response}}, nil
}

type idempotencyRecoveryAdapter struct {
	*resumableEngineAdapter
	recovered  provider.ResumableResult
	recoveries int
}

func (adapter *idempotencyRecoveryAdapter) RecoverByIdempotencyKey(context.Context, provider.Call, provider.Observer) (provider.ResumableResult, error) {
	adapter.mu.Lock()
	adapter.recoveries++
	adapter.mu.Unlock()
	return adapter.recovered, nil
}

var _ provider.IdempotencyRecovery = (*idempotencyRecoveryAdapter)(nil)

var _ provider.ResumableAdapter = (*resumableEngineAdapter)(nil)

func TestGenerateResumesDurableProviderOperationWithoutSubmit(t *testing.T) {
	adapter := &resumableEngineAdapter{}
	harness := newHarness(t, adapter)
	request := baseRequest("resumable-retry")
	if _, err := harness.engine.Generate(context.Background(), request); err == nil {
		t.Fatal("first attempt unexpectedly completed")
	}
	operation, err := harness.admission.Get(context.Background(), operationIDForTest(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if string(operation.State) != "provider_pending" {
		t.Fatalf("operation state after interrupted poll = %q, want provider_pending", operation.State)
	}
	response, err := harness.engine.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if response.Status != llm.ResponseStatusCompleted {
		t.Fatalf("response status = %q, want completed", response.Status)
	}
	adapter.mu.Lock()
	submits, polls := adapter.submits, adapter.polls
	adapter.mu.Unlock()
	if submits != 1 {
		t.Fatalf("Submit calls = %d, want 1", submits)
	}
	if polls != 2 {
		t.Fatalf("Poll calls = %d, want 2", polls)
	}
}

func TestGenerateFinalizesTerminalFirstPollOutcome(t *testing.T) {
	adapter := &resumableEngineAdapter{terminal: true}
	harness := newHarness(t, adapter)
	request := baseRequest("resumable-terminal-poll")
	if _, err := harness.engine.Generate(context.Background(), request); err == nil {
		t.Fatal("terminal poll unexpectedly completed")
	}
	operation, err := harness.admission.Get(context.Background(), operationIDForTest(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != admission.StateAmbiguous {
		t.Fatalf("operation state after terminal poll = %q, want ambiguous", operation.State)
	}
}

func TestGenerateRecoversAcceptedSubmitFailureByIdempotencyKey(t *testing.T) {
	completed := successfulResponse()
	completed.OperationKey = "resumable-submit-recovery"
	adapter := &idempotencyRecoveryAdapter{resumableEngineAdapter: &resumableEngineAdapter{
		submitErr:      provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchAccepted, provider.RetryNever, "submit response was lost"),
		pollResponses:  []provider.ResumableResult{{State: provider.ResumableCompleted, ProviderOperationID: "recovered-provider-operation", Dispatch: provider.DispatchAccepted, Result: provider.Result{Response: completed}}},
		recoveryPollID: "recovered-provider-operation",
	}, recovered: provider.ResumableResult{State: provider.ResumablePending, ProviderOperationID: "recovered-provider-operation", Dispatch: provider.DispatchAccepted}}
	harness := newHarness(t, adapter)
	response, err := harness.engine.Generate(context.Background(), baseRequest("resumable-submit-recovery"))
	if err != nil {
		t.Fatalf("recovered submit failed: %v", err)
	}
	if response.Status != llm.ResponseStatusCompleted {
		t.Fatalf("response status = %q, want completed", response.Status)
	}
	adapter.mu.Lock()
	submits, recoveries, polls := adapter.submits, adapter.recoveries, adapter.polls
	adapter.mu.Unlock()
	if submits != 1 || recoveries != 1 || polls != 1 {
		t.Fatalf("submit/recovery/poll calls = %d/%d/%d, want 1/1/1", submits, recoveries, polls)
	}
}

func TestGenerateNotFoundRecoveryRemainsAmbiguous(t *testing.T) {
	adapter := &idempotencyRecoveryAdapter{resumableEngineAdapter: &resumableEngineAdapter{
		submitErr: provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchAmbiguous, provider.RetryNever, "submit response was lost"),
	}, recovered: provider.ResumableResult{State: provider.ResumableNotFound, Dispatch: provider.DispatchAmbiguous}}
	harness := newHarness(t, adapter)
	_, err := harness.engine.Generate(context.Background(), baseRequest("resumable-submit-not-found"))
	if err == nil {
		t.Fatal("not-found recovery unexpectedly completed")
	}
	operation, getErr := harness.admission.Get(context.Background(), operationIDForTest(t, baseRequest("resumable-submit-not-found")))
	if getErr != nil {
		t.Fatal(getErr)
	}
	if operation.State != admission.StateAmbiguous {
		t.Fatalf("operation state = %q, want ambiguous", operation.State)
	}
	adapter.mu.Lock()
	submits, recoveries, polls := adapter.submits, adapter.recoveries, adapter.polls
	adapter.mu.Unlock()
	if submits != 1 || recoveries != 1 || polls != 0 {
		t.Fatalf("submit/recovery/poll calls = %d/%d/%d, want 1/1/0", submits, recoveries, polls)
	}
}

func TestGenerateAcceptedSubmitFailureWithoutRecoveryRemainsAmbiguous(t *testing.T) {
	adapter := &resumableEngineAdapter{submitErr: provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchAccepted, provider.RetryNever, "submit response was lost")}
	harness := newHarness(t, adapter)
	request := baseRequest("resumable-submit-no-recovery")
	if _, err := harness.engine.Generate(context.Background(), request); err == nil {
		t.Fatal("unsupported recovery unexpectedly completed")
	}
	operation, err := harness.admission.Get(context.Background(), operationIDForTest(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != admission.StateAmbiguous {
		t.Fatalf("operation state = %q, want ambiguous", operation.State)
	}
	adapter.mu.Lock()
	submits := adapter.submits
	adapter.mu.Unlock()
	if submits != 1 {
		t.Fatalf("submit calls = %d, want one with no recovery or resubmission", submits)
	}
}

// operationIDForTest mirrors the engine's public operation identity inputs
// without exposing the internal hash helper to the fixture package.
func operationIDForTest(t *testing.T, request llm.Request) string {
	t.Helper()
	normalized, err := llm.NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := llm.RequestDigest(normalized)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := operationIdentity(normalized, digest)
	return id
}
