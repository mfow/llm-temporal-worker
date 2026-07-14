package activity

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

type Activities struct {
	Engine             llm.Engine
	Heartbeater        Heartbeater
	HeartbeaterFactory func() Heartbeater
	Tracer             *observability.Tracer
	PayloadLimits      PayloadLimits
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
	heartbeater := activities.newHeartbeater()
	if heartbeater != nil {
		heartbeater = &deduplicatingHeartbeater{target: heartbeater}
		ctx = engine.WithHeartbeat(ctx, heartbeater)
		if err := heartbeater.Beat(ctx, engine.Progress{Phase: "planning"}); err != nil {
			return GenerateResponse{}, ToTemporalError(err)
		}
	}
	stream, err := activities.Engine.Stream(ctx, request)
	if err != nil {
		if preAdmissionStreamingUnavailable(err) {
			response, generateErr := activities.Engine.Generate(ctx, request)
			if generateErr != nil {
				return GenerateResponse{}, ToTemporalError(generateErr)
			}
			return activities.completeGenerate(ctx, response, heartbeater)
		}
		return GenerateResponse{}, ToTemporalError(err)
	}
	defer stream.Close()
	outputItems := 0
	for {
		event, nextErr := stream.Next(ctx)
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				return GenerateResponse{}, ToTemporalError(provider.NewError(provider.CodeProviderInvalidResponse, provider.PhaseStream, provider.DispatchAccepted, provider.RetryNever, "stream ended before a terminal outcome"))
			}
			return GenerateResponse{}, ToTemporalError(nextErr)
		}
		if _, ok := event.(llm.ContentCompleted); ok {
			outputItems++
		}
		if heartbeater != nil {
			if progress, ok := StreamProgress(event, outputItems); ok {
				if err := heartbeater.Beat(ctx, progress); err != nil {
					return GenerateResponse{}, ToTemporalError(err)
				}
			}
		}
		switch terminal := event.(type) {
		case llm.ResponseCompleted:
			return activities.completeGenerate(ctx, terminal.Response, heartbeater)
		case llm.StreamErrored:
			return GenerateResponse{}, ToTemporalError(terminal.Err)
		}
	}
}

// preAdmissionStreamingUnavailable is deliberately narrow: Activity may use
// the native Generate lifecycle only when Stream returned before it created an
// EventStream or an admitted operation. A terminal StreamErrored event is
// never eligible for this fallback because its operation may already be
// finalized or ambiguous.
func preAdmissionStreamingUnavailable(err error) bool {
	var providerErr *provider.Error
	return errors.As(err, &providerErr) && providerErr.Code == provider.CodeUnsupportedCapability && providerErr.Phase == provider.PhaseStream && providerErr.Dispatch == provider.DispatchNotDispatched && providerErr.OperationID == ""
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
