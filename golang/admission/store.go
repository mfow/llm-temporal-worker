package admission

import "context"

type AdmissionStore interface {
	Begin(context.Context, BeginRequest) (BeginResult, error)
	MarkDispatching(context.Context, DispatchRequest) error
	Continue(context.Context, ContinueRequest) (ContinueResult, error)
	Complete(context.Context, CompleteRequest) error
	Fail(context.Context, FailRequest) error
	Get(context.Context, string) (Operation, error)
}

// ProviderPendingStore is an optional extension implemented by durable
// operation repositories that can persist and recover a provider-owned
// operation identifier. Engines must never require this extension for
// adapters that only support one-shot Invoke calls.
type ProviderPendingStore interface {
	AdmissionStore
	MarkProviderPending(context.Context, ProviderPendingRequest) error
	ProviderOperation(context.Context, string) (string, error)
}
