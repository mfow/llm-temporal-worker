package llm

import "context"

// Engine is the provider-neutral inference boundary. Implementations may be
// used directly by libraries or wrapped by the Temporal Activity layer.
// Temporal-specific concerns do not leak into this interface.
type Engine interface {
	Generate(context.Context, Request) (Response, error)
	Stream(context.Context, Request) (EventStream, error)
}
