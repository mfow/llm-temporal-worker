package engine

import (
	"context"
	"time"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

type StreamEventKind string

const (
	StreamEventStarted   StreamEventKind = "started"
	StreamEventProgress  StreamEventKind = "progress"
	StreamEventCompleted StreamEventKind = "completed"
	StreamEventFailed    StreamEventKind = "failed"
)

// StreamEvent deliberately carries only typed progress or the final normalized
// response. Providers may expose richer events inside their adapter package;
// the engine never leaks SDK unions across this boundary.
type StreamEvent struct {
	Kind     StreamEventKind
	Phase    string
	At       time.Time
	Response *llm.Response
	Err      error
}

type StreamSink func(context.Context, StreamEvent) error

// Stream is a conservative non-streaming fallback until an adapter advertises
// a streaming port. It still guarantees that completed is emitted only after
// result persistence, continuation persistence, and admission completion.
func (engine *Engine) Stream(ctx context.Context, request llm.Request, sink StreamSink) error {
	if sink == nil {
		return engineError(provider.CodeInvalidArgument, provider.PhaseNormalize, provider.DispatchNotDispatched, provider.RetryNever, "stream sink is required", nil)
	}
	if err := sink(ctx, StreamEvent{Kind: StreamEventStarted, Phase: "planning", At: engine.dependencies.Clock()}); err != nil {
		return err
	}
	response, err := engine.Generate(ctx, request)
	if err != nil {
		_ = sink(ctx, StreamEvent{Kind: StreamEventFailed, Phase: "finalizing", At: engine.dependencies.Clock(), Err: err})
		return err
	}
	copy := response
	if err := sink(ctx, StreamEvent{Kind: StreamEventCompleted, Phase: "finalizing", At: engine.dependencies.Clock(), Response: &copy}); err != nil {
		return err
	}
	return nil
}
