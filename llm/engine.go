package llm

import "context"

// Engine is the provider-neutral one-shot inference boundary. Implementations
// may be used directly by libraries or wrapped by the Temporal Activity layer.
// Temporal-specific concerns do not leak into this interface.
type Engine interface {
	Generate(context.Context, Request) (Response, error)
}

// StreamingEngine is the optional provider-neutral event-stream extension for
// library callers. The Temporal runtime intentionally accepts Engine so each
// Activity has one bounded request and one completed response.
type StreamingEngine interface {
	Engine
	Stream(context.Context, Request) (EventStream, error)
}
