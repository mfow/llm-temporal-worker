package engine

import "context"

type heartbeatContextKey struct{}

// WithHeartbeat binds a per-invocation heartbeat sink to a request context.
// The engine prefers this value over its static dependency so concurrent
// Temporal Activities never share mutable heartbeat state.
func WithHeartbeat(ctx context.Context, heartbeat Heartbeat) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if heartbeat == nil {
		return ctx
	}
	return context.WithValue(ctx, heartbeatContextKey{}, heartbeat)
}

func heartbeatFromContext(ctx context.Context) Heartbeat {
	if ctx == nil {
		return nil
	}
	heartbeat, _ := ctx.Value(heartbeatContextKey{}).(Heartbeat)
	return heartbeat
}
