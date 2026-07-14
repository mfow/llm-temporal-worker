package activity

import (
	"context"
	"errors"
	"io"

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
	stream, err := activities.Engine.Stream(ctx, request)
	if err != nil {
		if preAdmissionStreamingUnavailable(err) {
			response, generateErr := activities.Engine.Generate(ctx, request)
			if generateErr != nil {
				return GenerateResponse{}, ToTemporalError(generateErr)
			}
			return activities.completeGenerate(ctx, response)
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
		if activities.Heartbeater != nil {
			if progress, ok := StreamProgress(event, outputItems); ok {
				if err := activities.Heartbeater.Beat(ctx, progress); err != nil {
					return GenerateResponse{}, ToTemporalError(err)
				}
			}
		}
		switch terminal := event.(type) {
		case llm.ResponseCompleted:
			return activities.completeGenerate(ctx, terminal.Response)
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

func (activities *Activities) completeGenerate(ctx context.Context, response llm.Response) (GenerateResponse, error) {
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
