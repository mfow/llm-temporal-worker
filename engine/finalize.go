package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/routing"
	"github.com/mfow/llm-temporal-worker/state"
)

func (engine *Engine) finalizeSuccess(ctx context.Context, request llm.Request, snapshot Snapshot, quoted quotedPlan, index int, operation admission.Operation, parent *state.Continuation, call provider.Call, response llm.Response) (result llm.Response, resultErr error) {
	if index < 0 || index >= len(quoted.candidates) {
		return llm.Response{}, engineError(provider.CodeInternal, provider.PhaseFinalize, provider.DispatchAccepted, provider.RetryNever, "finalization candidate index is invalid", nil)
	}
	candidate := quoted.candidates[index]
	ctx, finalizeSpan := engine.startTrace(ctx, "llmtw.finalization", operationTraceAttrs(operation.ID, candidate.candidate)...)
	defer func() {
		if resultErr != nil {
			engine.recordTraceError(ctx, finalizeSpan, resultErr)
		}
		finalizeSpan.End()
	}()
	candidate.candidate.PriceVersion = candidate.priceVersion()
	response.APIVersion = llm.APIVersion
	response.OperationKey = request.OperationKey
	response.OperationID = operation.ID
	response.Route = llm.RouteFacts{RouteID: candidate.candidate.RouteID, EndpointID: candidate.candidate.EndpointID, APIFamily: candidate.candidate.Family, RequestedModel: request.Model, ResolvedModel: call.Model}
	response.Service.Requested = candidate.candidate.RequestedClass
	response.Service.Attempted = candidate.candidate.AttemptedClass
	response.Service.FallbackIndex = candidate.candidate.FallbackIndex
	if response.Service.ProviderValue == "" {
		response.Service.ProviderValue = call.Metadata.ProviderTier
	}
	actual := pricing.Cost{}
	if candidate.priceKnown() {
		var err error
		actual, err = actualCost(*candidate.entry, response)
		if err != nil {
			return llm.Response{}, engine.finishFailed(ctx, operation, candidate.candidate, engineError(provider.CodeProviderInvalidResponse, provider.PhaseLift, provider.DispatchAccepted, provider.RetryNever, "response usage could not be priced", err), candidate.estimate.MicroUSD)
		}
		response.Cost = llm.Cost{Status: llm.CostStatusKnown, Currency: actual.Currency, ReservedMicroUSD: int64(operation.ReservedMicroUSD), ActualMicroUSD: int64(actual.MicroUSD), Method: string(actual.Method), CatalogVersion: actual.CatalogVersion}
	} else {
		response.Cost = llm.Cost{Status: llm.CostStatusUnknown, ReservedMicroUSD: int64(operation.ReservedMicroUSD)}
	}
	if response.Status == "" {
		response.Status = llm.ResponseStatusCompleted
	}
	if response.Output == nil {
		response.Output = []llm.Item{}
	}
	if response.Diagnostics == nil {
		response.Diagnostics = []llm.Diagnostic{}
	}
	if err := engine.beat(ctx, Progress{OperationID: operation.ID, Phase: "finalization", RouteIndex: candidate.candidate.RouteIndex, ClassIndex: candidate.candidate.FallbackIndex, At: engine.dependencies.Clock()}); err != nil {
		return llm.Response{}, err
	}
	if response.Continuation != nil {
		if err := engine.beat(ctx, Progress{OperationID: operation.ID, Phase: "continuation_write", RouteIndex: candidate.candidate.RouteIndex, ClassIndex: candidate.candidate.FallbackIndex, At: engine.dependencies.Clock()}); err != nil {
			return llm.Response{}, err
		}
		secure, continuationErr := engine.persistContinuation(ctx, request, response, candidate.candidate, operation.ID, parent, snapshot)
		if continuationErr != nil {
			return llm.Response{}, engine.finishFailed(ctx, operation, candidate.candidate, continuationErr, actual.MicroUSD)
		}
		response.Continuation = secure
	}
	finalCtx, cancel := engine.finalizationContext(ctx)
	defer cancel()
	resultRef, err := engine.dependencies.Results.Put(finalCtx, operation.ID, response)
	if err != nil {
		return llm.Response{}, engine.finishFailed(finalCtx, operation, candidate.candidate, engineError(provider.CodeStateUnavailable, provider.PhaseFinalize, provider.DispatchAccepted, provider.RetrySameOperation, "result store write failed", err), actual.MicroUSD)
	}
	attempt := admission.AttemptFacts{RouteID: candidate.candidate.RouteID, EndpointID: candidate.candidate.EndpointID, Provider: candidate.candidate.Provider, ProviderRequestID: response.Provider.RequestID, ServiceClass: string(candidate.candidate.AttemptedClass), AttemptNumber: index + 1, Dispatch: admission.Accepted}
	var ref *state.BlobRef
	if resultRef.Valid() {
		copyRef := resultRef
		ref = &copyRef
	}
	if err := engine.dependencies.Admission.Complete(finalCtx, admission.CompleteRequest{OperationID: operation.ID, DispatchToken: operation.DispatchToken, Actual: actual.MicroUSD, ResultRef: ref, Attempt: attempt}); err != nil {
		return llm.Response{}, engineError(provider.CodeStateUnavailable, provider.PhaseFinalize, provider.DispatchAccepted, provider.RetrySameOperation, "operation completion failed", err)
	}
	recordCompletion(ctx, response)
	response.Cost.ReservedMicroUSD = int64(operation.ReservedMicroUSD)
	return response, nil
}

func actualCost(entry pricing.Entry, response llm.Response) (pricing.Cost, error) {
	if response.Cost.Method != "" {
		actual := pricing.MicroUSD(response.Cost.ActualMicroUSD)
		if !actual.Valid() {
			return pricing.Cost{}, fmt.Errorf("provider-reported cost is outside safe range")
		}
		currency := response.Cost.Currency
		if currency == "" {
			currency = entry.Currency
		}
		return pricing.Cost{MicroUSD: actual, Currency: currency, Method: pricing.CostProviderReported, CatalogVersion: entry.Version}, nil
	}
	return pricing.CostFromUsage(entry, pricing.Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, ReasoningTokens: response.Usage.ReasoningTokens, CacheReadTokens: response.Usage.CacheReadTokens, CacheWriteTokens: response.Usage.CacheWriteTokens})
}

func (engine *Engine) persistContinuation(ctx context.Context, request llm.Request, response llm.Response, candidate routing.Candidate, operationID string, parent *state.Continuation, snapshot Snapshot) (continuation *llm.Continuation, resultErr *provider.Error) {
	ctx, continuationSpan := engine.startTrace(ctx, "llmtw.continuation_write", operationTraceAttrs(operationID, candidate)...)
	defer func() {
		if resultErr != nil {
			engine.recordTraceError(ctx, continuationSpan, resultErr)
		}
		continuationSpan.End()
	}()
	if engine.dependencies.Continuations == nil {
		return nil, engineError(provider.CodeStateUnavailable, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetrySameOperation, "continuation store is unavailable", nil)
	}
	providerValue := response.Continuation
	if providerValue == nil || providerValue.Handle == "" {
		return nil, engineError(provider.CodeProviderInvalidResponse, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetryNever, "provider continuation is incomplete", nil)
	}
	now := engine.dependencies.Clock()
	ttl := snapshot.ContinuationRetention
	if ttl <= 0 {
		ttl = defaultContinuationTTL
	}
	transcript := append([]llm.Item(nil), request.Input...)
	transcript = append(transcript, response.Output...)
	_, digest, err := state.CanonicalTranscript(transcript)
	if err != nil {
		return nil, engineError(provider.CodeStateCorrupt, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetryNever, "continuation transcript is invalid", err)
	}
	providerStates := []state.OpaqueStateRef{{Provider: candidate.Provider, EndpointID: candidate.EndpointID, Family: candidate.Family, Media: providerHandleMedia, Data: []byte(providerValue.Handle), Required: true}}
	for _, value := range providerValue.ProviderStates {
		if value.Provider == "" || value.EndpointFamily == "" || value.MediaType == "" || len(value.Opaque) == 0 {
			return nil, engineError(provider.CodeProviderInvalidResponse, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetryNever, "provider continuation state is incomplete", nil)
		}
		providerStates = append(providerStates, state.OpaqueStateRef{Provider: value.Provider, EndpointID: candidate.EndpointID, Family: value.EndpointFamily, Media: value.MediaType, Data: append([]byte(nil), value.Opaque...), Required: true})
	}
	child := state.Continuation{Tenant: request.Context.Tenant, ParentID: "", Transcript: transcript, TranscriptDigest: digest, TranscriptComplete: true, ProviderState: providerStates, Pinning: candidate.Pinning, LastOperationID: operationID, CapabilityVersion: candidate.CapabilityVersion, PriceVersion: candidate.PriceVersion, CreatedAt: now, ExpiresAt: now.Add(ttl), Depth: 0}
	var handle state.Handle
	if parent != nil {
		child.ParentID = parent.ID
		child.Depth = parent.Depth + 1
		var putErr error
		handle, putErr = engine.dependencies.Continuations.PutChild(ctx, state.PutChildRequest{Parent: state.Handle(request.Continuation.Handle), Child: child, OperationKey: request.OperationKey})
		if putErr != nil {
			return nil, engineError(provider.CodeStateUnavailable, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetrySameOperation, "continuation child write failed", putErr)
		}
	} else {
		root, ok := engine.dependencies.Continuations.(RootContinuationStore)
		if !ok {
			return nil, engineError(provider.CodeStateUnavailable, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetrySameOperation, "continuation store cannot create a root", nil)
		}
		var rootErr error
		handle, rootErr = root.CreateRoot(ctx, child)
		if rootErr != nil {
			return nil, engineError(provider.CodeStateUnavailable, provider.PhaseContinuationWrite, provider.DispatchAccepted, provider.RetrySameOperation, "continuation root write failed", rootErr)
		}
	}
	if metrics := observability.MetricsFromContext(ctx); metrics != nil {
		metrics.RecordContinuation("created")
	}
	return &llm.Continuation{Handle: handle.String(), EndpointID: candidate.EndpointID, Model: candidate.Model, ExpiresAt: timePtr(child.ExpiresAt), Pinned: true}, nil
}

func timePtr(value time.Time) *time.Time { return &value }

func (engine *Engine) finalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(parent)
	return context.WithTimeout(base, engine.dependencies.FinalizationTimeout)
}

func (engine *Engine) beat(ctx context.Context, progress Progress) error {
	heartbeat := heartbeatFromContext(ctx)
	if heartbeat == nil {
		heartbeat = engine.dependencies.Heartbeat
	}
	if heartbeat == nil {
		return nil
	}
	if err := heartbeat.Beat(ctx, progress); err != nil {
		return engineError(provider.CodeStateUnavailable, provider.PhaseFinalize, provider.DispatchNotDispatched, provider.RetrySameOperation, "progress heartbeat failed", err)
	}
	return nil
}
