package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
)

const (
	defaultMaxProviderPolls = 64
	defaultMaxPollInterval  = 30 * time.Second
)

// ProviderPollOptions bounds a resumable operation independently of provider
// guidance. A zero MaxPolls or MaxPollInterval selects the conservative
// default. Sleep is injectable so tests can exercise cancellation and bounds
// without real delays.
type ProviderPollOptions struct {
	MaxPolls        int
	MaxPollInterval time.Duration
	Sleep           func(context.Context, time.Duration) error
}

// PollProviderOperation resumes a provider operation whose identifier was
// already durably recorded. It never calls Submit. A pending provider result
// is bounded by both the Activity context and MaxPolls; when the bound is hit
// the returned retryable error leaves the durable operation pending for the
// next Activity attempt.
func PollProviderOperation(ctx context.Context, adapter provider.ResumableAdapter, call provider.Call, providerOperationID string, observer provider.Observer, options ProviderPollOptions) (provider.Result, error) {
	if adapter == nil {
		return provider.Result{}, errors.New("resumable adapter is nil")
	}
	if providerOperationID == "" {
		return provider.Result{}, errors.New("provider operation id is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxPolls := options.MaxPolls
	if maxPolls == 0 {
		maxPolls = defaultMaxProviderPolls
	}
	if maxPolls < 1 {
		return provider.Result{}, errors.New("max provider polls must be positive")
	}
	maxInterval := options.MaxPollInterval
	if maxInterval == 0 {
		maxInterval = defaultMaxPollInterval
	}
	if maxInterval < 0 {
		return provider.Result{}, errors.New("max provider poll interval must not be negative")
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	for poll := 1; poll <= maxPolls; poll++ {
		if err := ctx.Err(); err != nil {
			return provider.Result{}, pollDeadlineError(err)
		}
		result, err := adapter.Poll(ctx, call, providerOperationID, observer)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return provider.Result{}, pollDeadlineError(err)
			}
			return provider.Result{}, err
		}
		if err := result.Validate(); err != nil {
			mapped := provider.NewError(provider.CodeProviderInvalidResponse, provider.PhasePoll, provider.DispatchAmbiguous, provider.RetryNever, "provider polling response is invalid")
			mapped.Cause = err
			return provider.Result{}, mapped
		}
		if result.State == provider.ResumableCompleted {
			return result.Result, nil
		}
		if result.State == provider.ResumableFailed {
			return provider.Result{}, result.Failure
		}
		if result.State == provider.ResumableNotFound {
			return provider.Result{}, provider.NewError(provider.CodeAmbiguousDispatch, provider.PhasePoll, provider.DispatchAmbiguous, provider.RetryNever, "provider operation could not be found")
		}
		if result.ProviderOperationID != providerOperationID {
			mapped := provider.NewError(provider.CodeStateCorrupt, provider.PhasePoll, provider.DispatchAmbiguous, provider.RetryNever, "provider operation identity changed while polling")
			mapped.Cause = fmt.Errorf("provider operation identity changed")
			return provider.Result{}, mapped
		}
		if poll == maxPolls {
			return provider.Result{}, provider.NewError(provider.CodeDeadlineExceeded, provider.PhasePoll, provider.DispatchAccepted, provider.RetrySameOperation, "provider polling limit reached")
		}
		if observer != nil {
			// Progress intentionally carries phase and counts only; provider IDs
			// are durable state, never heartbeat data.
			observer.OnProgress(ctx, provider.Progress{Phase: string(provider.PhasePoll), OutputItems: poll})
		}
		delay := result.NextPollAfter
		if delay > maxInterval {
			delay = maxInterval
		}
		if err := sleep(ctx, delay); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return provider.Result{}, pollDeadlineError(err)
			}
			return provider.Result{}, err
		}
	}
	return provider.Result{}, provider.NewError(provider.CodeInternal, provider.PhasePoll, provider.DispatchAccepted, provider.RetrySameOperation, "provider polling loop exhausted")
}

func pollDeadlineError(cause error) *provider.Error {
	mapped := provider.NewError(provider.CodeDeadlineExceeded, provider.PhasePoll, provider.DispatchAccepted, provider.RetrySameOperation, "provider polling stopped before completion")
	mapped.Cause = cause
	return mapped
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
