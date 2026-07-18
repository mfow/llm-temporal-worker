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
