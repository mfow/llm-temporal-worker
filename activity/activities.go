package activity

import (
	"context"

	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

type Activities struct {
	Engine        llm.Engine
	Heartbeater   Heartbeater
	PayloadLimits PayloadLimits
}

func (activities *Activities) Generate(ctx context.Context, payload GenerateRequest) (GenerateResponse, error) {
	if activities == nil || activities.Engine == nil {
		return GenerateResponse{}, ToTemporalError(provider.NewError(provider.CodeInternal, provider.PhaseFinalize, provider.DispatchNotDispatched, provider.RetryNever, "Activity engine is unavailable"))
	}
	request, err := payload.Validate(activities.PayloadLimits.inlineBytes())
	if err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	if activities.Heartbeater != nil {
		if err := activities.Heartbeater.Beat(ctx, engine.Progress{Phase: "planning"}); err != nil {
			return GenerateResponse{}, ToTemporalError(err)
		}
	}
	response, err := activities.Engine.Generate(ctx, request)
	if err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	result := GenerateResponse{APIVersion: APIVersion, Response: response, Metadata: ResultMetadata{OperationID: response.OperationID}}
	if err := result.Validate(activities.PayloadLimits.inlineBytes()); err != nil {
		return GenerateResponse{}, ToTemporalError(err)
	}
	if activities.Heartbeater != nil {
		if err := activities.Heartbeater.Beat(ctx, engine.Progress{OperationID: response.OperationID, Phase: "finalizing"}); err != nil {
			return GenerateResponse{}, ToTemporalError(err)
		}
	}
	return result, nil
}

// Register installs the exact versioned Activity name rather than relying on
// a Go method name that could change during a refactor.
func (activities *Activities) Register(registry worker.ActivityRegistry) {
	registry.RegisterActivityWithOptions(activities.Generate, sdkactivity.RegisterOptions{Name: GenerateActivityName})
}
