package llm

import "context"

// Engine is the provider-neutral one-shot inference boundary. Implementations
// may be used directly by libraries or wrapped by the Temporal Activity layer.
// Temporal-specific concerns do not leak into this interface.
type Engine interface {
	Generate(context.Context, Request) (Response, error)
}

// StreamingEngine is a deprecated residual provider-neutral event-stream API.
// Deprecated: streaming is unsupported in v1. This interface remains for
// source compatibility only and MUST NOT be wired into the Temporal runtime.
type StreamingEngine interface {
	Engine
	Stream(context.Context, Request) (EventStream, error)
}
