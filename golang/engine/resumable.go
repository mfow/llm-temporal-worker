package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
	"github.com/mfow/llm-temporal-worker/golang/routing"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

// invokeAttempt dispatches a provider call through the optional resumable
// port. The third return value means that the operation is already durable in
// provider_pending and the caller must return the polling error without
// transitioning the operation to a terminal state.
func (engine *Engine) invokeAttempt(ctx context.Context, operation admission.Operation, candidate routing.Candidate, adapter provider.Adapter, call provider.Call, observer *dispatchObserver) (provider.Result, error, bool) {
	resumable, ok := adapter.(provider.ResumableAdapter)
	if !ok {
		result, err := adapter.Invoke(ctx, call, observer)
		return result, err, false
	}
	outcome, err := resumable.Submit(ctx, call, observer)
	if err != nil {
		return provider.Result{}, err, false
	}
	if err := outcome.Validate(); err != nil {
		mapped := provider.NewError(provider.CodeProviderInvalidResponse, provider.PhaseDispatch, provider.DispatchAmbiguous, provider.RetryNever, "resumable provider response is invalid")
		mapped.Cause = err
		return provider.Result{}, mapped, false
	}
	switch outcome.State {
	case provider.ResumableCompleted:
		return outcome.Result, nil, false
	case provider.ResumableFailed:
		return provider.Result{}, outcome.Failure, false
	case provider.ResumableNotFound:
		return provider.Result{}, provider.NewError(provider.CodeAmbiguousDispatch, provider.PhaseDispatch, provider.DispatchAmbiguous, provider.RetryNever, "provider operation could not be found"), false
	case provider.ResumablePending:
		// The adapter contract requires a possible-write boundary before an
		// accepted result. Without it there is no safe way to persist the id.
		if observer == nil || !observer.marked {
			return provider.Result{}, engineError(provider.CodeAmbiguousDispatch, provider.PhaseDispatch, provider.DispatchAccepted, provider.RetryNever, "resumable provider accepted work before dispatch was recorded", nil), false
		}
		store, ok := engine.dependencies.Admission.(admission.ProviderPendingStore)
		if !ok {
			return provider.Result{}, engineError(provider.CodeStateUnavailable, provider.PhaseFinalize, provider.DispatchAccepted, provider.RetryNever, "provider operation repository is unavailable", nil), false
		}
		if err := store.MarkProviderPending(ctx, admission.ProviderPendingRequest{
			OperationID: operation.ID, DispatchToken: operation.DispatchToken,
			ProviderOperationID: outcome.ProviderOperationID,
			EndpointID:          candidate.EndpointID, Provider: candidate.Provider,
		}); err != nil {
			return provider.Result{}, engineError(provider.CodeStateUnavailable, provider.PhaseFinalize, provider.DispatchAccepted, provider.RetryNever, "provider operation persistence failed", err), false
		}
		result, pollErr := PollProviderOperation(ctx, resumable, call, outcome.ProviderOperationID, observer, ProviderPollOptions{})
		if pollErr != nil {
			return provider.Result{}, pollErr, true
		}
		return result, nil, false
	default:
		return provider.Result{}, fmt.Errorf("resumable provider state %q was not handled", outcome.State), false
	}
}

func (engine *Engine) resumeProviderPending(ctx context.Context, request, providerRequest llm.Request, snapshot Snapshot, quoted quotedPlan, operation admission.Operation, parent *state.Continuation) (llm.Response, error) {
	store, ok := engine.dependencies.Admission.(admission.ProviderPendingStore)
	if !ok {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseStateLoad, provider.DispatchAccepted, provider.RetrySameOperation, "provider operation repository is unavailable", nil)
	}
	providerOperationID, err := store.ProviderOperation(ctx, operation.ID)
	if err != nil {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseStateLoad, provider.DispatchAccepted, provider.RetrySameOperation, "provider operation could not be loaded", err)
	}
	index := -1
	for i, candidate := range quoted.candidates {
		if candidate.candidate.EndpointID == operation.Attempt.EndpointID {
			index = i
			break
		}
	}
	if index < 0 {
		return llm.Response{}, engineError(provider.CodeStateCorrupt, provider.PhasePlan, provider.DispatchAccepted, provider.RetryNever, "provider pending endpoint is not in the current route plan", nil)
	}
	candidate := quoted.candidates[index]
	adapter, err := engine.dependencies.Adapters.Adapter(ctx, candidate.candidate)
	if err != nil {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseCompile, provider.DispatchAccepted, provider.RetrySameOperation, "provider pending adapter is unavailable", err)
	}
	resumable, ok := adapter.(provider.ResumableAdapter)
	if !ok {
		return llm.Response{}, engineError(provider.CodeStateCorrupt, provider.PhasePoll, provider.DispatchAccepted, provider.RetryNever, "provider pending adapter is not resumable", nil)
	}
	query := provider.CapabilityQuery{EndpointID: candidate.candidate.EndpointID, Family: provider.Family(candidate.candidate.Family), Model: candidate.candidate.Model, ServiceClass: candidate.candidate.AttemptedClass}
	capability, err := adapter.Capabilities(ctx, query)
	if err != nil {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseCompile, provider.DispatchAccepted, provider.RetrySameOperation, "provider pending capabilities are unavailable", err)
	}
	call, err := adapter.Compile(ctx, provider.CompileInput{
		Request: providerRequest, Query: query, Capability: capability,
		Strict:   request.Portability != llm.PortabilityBestEffort,
		Metadata: provider.CallMetadata{SchemaDigest: mustRequestDigest(request), CapabilityVersion: candidate.candidate.CapabilityVersion, ProviderTier: candidate.candidate.ProviderTier, OpaqueStateRequired: request.Continuation != nil},
	})
	if err != nil {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseCompile, provider.DispatchAccepted, provider.RetrySameOperation, "provider pending call could not be compiled", err)
	}
	lease := snapshot.ReservationLease
	if lease <= 0 {
		lease = defaultLease
	}
	attempt := operation.Attempt.AttemptNumber
	if attempt <= 0 {
		attempt = index + 1
	}
	observer := &dispatchObserver{engine: engine, operation: operation, candidate: candidate.candidate, attempt: attempt, leaseUntil: engine.dependencies.Clock().Add(lease), marked: true}
	if err := engine.beat(ctx, Progress{OperationID: operation.ID, Phase: "provider_wait", RouteIndex: candidate.candidate.RouteIndex, ClassIndex: candidate.candidate.FallbackIndex, At: engine.dependencies.Clock()}); err != nil {
		return llm.Response{}, err
	}
	result, pollErr := PollProviderOperation(ctx, resumable, call, providerOperationID, observer, ProviderPollOptions{})
	if pollErr != nil {
		mapped := pendingPollError(pollErr)
		if mapped.Retry == provider.RetrySameOperation || mapped.Code == provider.CodeCanceled {
			mapped.OperationID = operation.ID
			return llm.Response{}, mapped
		}
		return llm.Response{}, engine.finishFailed(ctx, operation, candidate.candidate, mapped, 0)
	}
	return engine.finalizeSuccess(ctx, request, snapshot, quoted, index, operation, parent, call, result.Response)
}

// pendingPollError preserves retry guidance while an operation is already
// durable. classifyProviderError intentionally turns accepted dispatch errors
// into terminal ambiguity for new submissions; doing that for a poll would
// discard safe retry of the same provider-owned operation.
func pendingPollError(err error) *provider.Error {
	var mapped *provider.Error
	if errors.As(err, &mapped) {
		copy := *mapped
		if copy.Retry == provider.RetrySameOperation || copy.Code == provider.CodeCanceled {
			return &copy
		}
		return &copy
	}
	return engineError(provider.CodeStateUnavailable, provider.PhasePoll, provider.DispatchAccepted, provider.RetrySameOperation, "provider poll failed", err)
}
