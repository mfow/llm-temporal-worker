package activity

import (
	"context"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

type Activities struct {
	Engine                     llm.Engine
	Heartbeater                Heartbeater
	HeartbeaterFactory         func() Heartbeater
	HeartbeatKeepaliveInterval time.Duration
	Tracer                     *observability.Tracer
	PayloadLimits              PayloadLimits

	// heartbeatTickerFactory is a test seam. Production leaves it nil and uses
	// the bounded real-time ticker in startHeartbeatKeepalive.
	heartbeatTickerFactory func(time.Duration) heartbeatTicker
}

func (activities *Activities) Generate(ctx context.Context, payload GenerateRequest) (GenerateResponse, error) {
	if activities == nil || activities.Engine == nil {
		return GenerateResponse{}, ToTemporalError(provider.NewError(provider.CodeInternal, provider.PhaseFinalize, provider.DispatchNotDispatched, provider.RetryNever, "Activity engine is unavailable"))
	}
	ctx = observability.WithTracer(ctx, activities.Tracer)
	request, err := payload.Validate(activities.PayloadLimits.inlineBytes())
	if err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	keepaliveInterval, err := activities.keepaliveInterval()
	if err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	var heartbeater Heartbeater
	generateContext := ctx
	var keepalive *heartbeatKeepalive
	if rawHeartbeater := activities.newHeartbeater(); rawHeartbeater != nil {
		serializedHeartbeater := &serializedHeartbeater{target: rawHeartbeater}
		heartbeater = &deduplicatingHeartbeater{target: serializedHeartbeater}
		ctx = engine.WithHeartbeat(ctx, heartbeater)
		if err := heartbeater.Beat(ctx, engine.Progress{Phase: "planning"}); err != nil {
			return GenerateResponse{}, ToTemporalError(err)
		}
		generateContext, keepalive = startHeartbeatKeepalive(ctx, serializedHeartbeater, keepaliveInterval, activities.heartbeatTickerFactory)
		defer func() {
			if keepalive != nil {
				_ = keepalive.stop()
			}
		}()
	}
	response, err := activities.Engine.Generate(generateContext, request)
	keepaliveErr := keepalive.stop()
	keepalive = nil
	if ctxErr := ctx.Err(); ctxErr != nil {
		return GenerateResponse{}, ToTemporalError(ctxErr)
	}
	if keepaliveErr != nil {
		return GenerateResponse{}, ToTemporalError(heartbeatKeepaliveFailure(keepaliveErr))
	}
	if err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	return activities.completeGenerate(ctx, response, heartbeater)
}

func (activities *Activities) completeGenerate(ctx context.Context, response llm.Response, heartbeater Heartbeater) (GenerateResponse, error) {
	result := GenerateResponse{APIVersion: APIVersion, Response: response, Metadata: ResultMetadata{OperationID: response.OperationID}}
	if err := result.Validate(activities.PayloadLimits.inlineBytes()); err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	if heartbeater != nil {
		if err := heartbeater.Beat(ctx, engine.Progress{OperationID: response.OperationID, Phase: "finalization"}); err != nil {
			return GenerateResponse{}, ToTemporalError(err)
		}
	}
	return result, nil
}

// generateTemporal keeps the Temporal Activity result absent when Generate
// returns an error. The SDK serializes any non-nil result before propagating
// the error, and a zero GenerateResponse is not a valid durable response.
func (activities *Activities) generateTemporal(ctx context.Context, payload GenerateRequest) (*GenerateResponse, error) {
	result, err := activities.Generate(ctx, payload)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (activities *Activities) newHeartbeater() Heartbeater {
	if activities == nil {
		return nil
	}
	if activities.HeartbeaterFactory != nil {
		return activities.HeartbeaterFactory()
	}
	return activities.Heartbeater
}

func (activities *Activities) keepaliveInterval() (time.Duration, error) {
	if activities == nil || activities.HeartbeatKeepaliveInterval == 0 {
		return DefaultHeartbeatKeepaliveInterval, nil
	}
	if activities.HeartbeatKeepaliveInterval < 0 {
		return 0, provider.NewError(provider.CodeConfiguration, provider.PhasePlan, provider.DispatchNotDispatched, provider.RetryNever, "Activity heartbeat keepalive interval must be positive")
	}
	return activities.HeartbeatKeepaliveInterval, nil
}

// serializedHeartbeater prevents the periodic provider-wait heartbeat from
// racing lifecycle heartbeats emitted by the engine on the same Activity.
type serializedHeartbeater struct {
	target Heartbeater
	mu     sync.Mutex
}

func (heartbeater *serializedHeartbeater) Beat(ctx context.Context, progress engine.Progress) error {
	if heartbeater == nil || heartbeater.target == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	heartbeater.mu.Lock()
	defer heartbeater.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return heartbeater.target.Beat(ctx, progress)
}

// deduplicatingHeartbeater suppresses duplicate lifecycle facts emitted by an
// Activity and the engine it invokes. It is allocated for one Activity call,
// so it cannot couple concurrent invocation timestamps or mutable state.
type deduplicatingHeartbeater struct {
	target Heartbeater

	mu   sync.Mutex
	last engine.Progress
	set  bool
}

func (heartbeater *deduplicatingHeartbeater) Beat(ctx context.Context, progress engine.Progress) error {
	if heartbeater == nil || heartbeater.target == nil {
		return nil
	}
	heartbeater.mu.Lock()
	duplicate := heartbeater.set && sameProgress(heartbeater.last, progress)
	if !duplicate {
		heartbeater.last = progress
		heartbeater.set = true
	}
	heartbeater.mu.Unlock()
	if duplicate {
		return ctx.Err()
	}
	return heartbeater.target.Beat(ctx, progress)
}

func sameProgress(left, right engine.Progress) bool {
	return left.OperationID == right.OperationID && left.Phase == right.Phase && left.RouteIndex == right.RouteIndex && left.ClassIndex == right.ClassIndex && left.OutputItems == right.OutputItems
}

// Register installs the exact versioned Activity name rather than relying on
// a Go method name that could change during a refactor.
func (activities *Activities) Register(registry worker.ActivityRegistry) {
	registry.RegisterActivityWithOptions(activities.generateTemporal, sdkactivity.RegisterOptions{Name: GenerateActivityName})
}
