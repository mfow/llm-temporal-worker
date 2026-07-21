package activity

import (
	"context"
	"errors"

	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

// CheckpointHandleMaterializer resolves an opaque caller-facing checkpoint
// handle in its already-authorized scope. The handle-capable form is kept
// separate from state.CheckpointMaterializer because repositories use opaque
// handles while the storage-neutral graph uses internal checkpoint IDs.
type CheckpointHandleMaterializer interface {
	MaterializeHandle(context.Context, string, string, state.MaterializeLimits) (state.MaterializedState, error)
}

// MaterializedGenerateRuntime receives the validated replay state before it
// is allowed to dispatch Generate. Keeping this as an optional extension of
// V1Runtime preserves the existing Activity contract for runtimes that do not
// opt into checkpoint-aware requests.
type MaterializedGenerateRuntime interface {
	GenerateV1Materialized(context.Context, llm.GenerateRequestV1, state.MaterializedState) (llm.GenerateResponseV1, error)
}

// MaterializedCompactRuntime receives the validated parent state before it is
// allowed to dispatch Compact.
type MaterializedCompactRuntime interface {
	CompactV1Materialized(context.Context, llm.CompactRequestV1, state.MaterializedState) (llm.CompactResponseV1, error)
}

// ScopeResolver maps the authenticated request context to the opaque durable
// state scope. There is deliberately no default concatenation: PostgreSQL
// scopes are stable identifiers, not raw tenant/project strings.
type ScopeResolver func(llm.RequestContext) (string, error)

// MaterializingV1Runtime is a narrow composition seam for the one-shot v1
// Activity boundary. A request carrying a parent checkpoint is materialized
// and validated before the runtime receives it. If the runtime does not
// implement the corresponding state-aware extension, dispatch fails closed.
// Root Generate requests (which have no parent) use the existing V1Runtime
// method because there is no checkpoint to replay.
//
// This seam intentionally does not perform provider, cache, budget, or
// checkpoint publication work. Those operations remain owned by the durable
// runtime implementation behind MaterializedGenerateRuntime and
// MaterializedCompactRuntime.
type MaterializingV1Runtime struct {
	Runtime      V1Runtime
	Materializer CheckpointHandleMaterializer
	Scope        ScopeResolver
	Limits       state.MaterializeLimits
}

var _ V1Runtime = (*MaterializingV1Runtime)(nil)

func (runtime *MaterializingV1Runtime) GenerateV1(ctx context.Context, request llm.GenerateRequestV1) (llm.GenerateResponseV1, error) {
	if runtime == nil || runtime.Runtime == nil {
		return llm.GenerateResponseV1{}, runtimeConfigurationError("runtime is not configured")
	}
	if request.Parent == nil {
		return runtime.Runtime.GenerateV1(ctx, request)
	}
	materialized, err := runtime.materialize(ctx, request.Context, string(*request.Parent))
	if err != nil {
		return llm.GenerateResponseV1{}, err
	}
	aware, ok := runtime.Runtime.(MaterializedGenerateRuntime)
	if !ok {
		return llm.GenerateResponseV1{}, runtimeConfigurationError("Generate runtime does not accept materialized checkpoints")
	}
	return aware.GenerateV1Materialized(ctx, request, materialized)
}

func (runtime *MaterializingV1Runtime) CompactV1(ctx context.Context, request llm.CompactRequestV1) (llm.CompactResponseV1, error) {
	if runtime == nil || runtime.Runtime == nil {
		return llm.CompactResponseV1{}, runtimeConfigurationError("runtime is not configured")
	}
	materialized, err := runtime.materialize(ctx, request.Context, string(request.Parent))
	if err != nil {
		return llm.CompactResponseV1{}, err
	}
	aware, ok := runtime.Runtime.(MaterializedCompactRuntime)
	if !ok {
		return llm.CompactResponseV1{}, runtimeConfigurationError("Compact runtime does not accept materialized checkpoints")
	}
	return aware.CompactV1Materialized(ctx, request, materialized)
}

func (runtime *MaterializingV1Runtime) QueryV1(ctx context.Context, request llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	if runtime == nil || runtime.Runtime == nil {
		return llm.QueryResponseV1{}, runtimeConfigurationError("runtime is not configured")
	}
	return runtime.Runtime.QueryV1(ctx, request)
}

func (runtime *MaterializingV1Runtime) materialize(ctx context.Context, requestContext llm.RequestContext, handle string) (state.MaterializedState, error) {
	if ctx == nil {
		return state.MaterializedState{}, runtimeConfigurationError("Activity context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return state.MaterializedState{}, err
	}
	if runtime.Materializer == nil || runtime.Scope == nil {
		return state.MaterializedState{}, runtimeConfigurationError("checkpoint materialization is not configured")
	}
	scopeID, err := runtime.Scope(requestContext)
	if err != nil {
		return state.MaterializedState{}, provider.NewError(provider.CodeInvalidArgument, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetryNever, "checkpoint scope is invalid")
	}
	if scopeID == "" {
		return state.MaterializedState{}, provider.NewError(provider.CodeInvalidArgument, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetryNever, "checkpoint scope is invalid")
	}
	materialized, err := runtime.Materializer.MaterializeHandle(ctx, scopeID, handle, runtime.Limits)
	if err == nil {
		return materialized, nil
	}
	return state.MaterializedState{}, mapMaterializationError(err)
}

func runtimeConfigurationError(message string) error {
	return provider.NewError(provider.CodeConfiguration, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetryNever, message)
}

func mapMaterializationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, state.ErrNotFound) || errors.Is(err, state.ErrTenantMismatch) || errors.Is(err, state.ErrExpired) {
		return provider.NewError(provider.CodeInvalidArgument, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetryNever, "checkpoint could not be materialized")
	}
	// Corrupt lineage and blob failures are worker/state failures. Preserve the
	// retryable distinction only at this sanitized provider boundary.
	return provider.NewError(provider.CodeStateUnavailable, provider.PhaseStateLoad, provider.DispatchNotDispatched, provider.RetrySameOperation, "checkpoint state is unavailable")
}
