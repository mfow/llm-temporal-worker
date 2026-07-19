package provider

import (
	"errors"
	"fmt"
	"time"
)

// ResumableState is the provider's durable operation state. It is deliberately
// closed: an adapter must classify every successful response rather than
// leaving the engine to infer whether a request was accepted.
type ResumableState string

const (
	ResumablePending   ResumableState = "pending"
	ResumableCompleted ResumableState = "completed"
	ResumableFailed    ResumableState = "failed"
	ResumableNotFound  ResumableState = "not_found"
)

func (state ResumableState) Valid() bool {
	switch state {
	case ResumablePending, ResumableCompleted, ResumableFailed, ResumableNotFound:
		return true
	default:
		return false
	}
}

// ResumableResult is shared by Submit, Poll, and idempotency-key recovery.
// ProviderOperationID is safe for the operation ledger, but must not be put in
// heartbeats, logs, metrics, traces, or caller-visible errors.
type ResumableResult struct {
	State               ResumableState
	ProviderOperationID string
	NextPollAfter       time.Duration
	Result              Result
	Failure             *Error
	Metadata            ResponseMetadata
	Dispatch            DispatchCertainty
}

// Validate enforces the state-specific fields before an engine can persist or
// act on a provider result. A malformed provider response is treated as a
// protocol failure, never as a safe rejection that permits resubmission.
func (result ResumableResult) Validate() error {
	if !result.State.Valid() {
		return fmt.Errorf("resumable result has invalid state %q", result.State)
	}
	if result.NextPollAfter < 0 {
		return errors.New("resumable result next poll delay must not be negative")
	}
	if !result.Dispatch.Valid() {
		return fmt.Errorf("resumable result has invalid dispatch certainty %q", result.Dispatch)
	}
	switch result.State {
	case ResumablePending:
		if result.ProviderOperationID == "" {
			return errors.New("pending result is missing provider operation id")
		}
		if result.Failure != nil {
			return errors.New("pending result must not contain a failure")
		}
		if result.Dispatch != DispatchAccepted {
			return fmt.Errorf("pending result dispatch must be %q", DispatchAccepted)
		}
	case ResumableCompleted:
		if result.Failure != nil {
			return errors.New("completed result must not contain a failure")
		}
		if result.Dispatch != DispatchAccepted {
			return fmt.Errorf("completed result dispatch must be %q", DispatchAccepted)
		}
		if result.Result.Response.OperationKey == "" {
			return errors.New("completed result is missing operation key")
		}
		if !result.Result.Response.Status.Valid() {
			return fmt.Errorf("completed result has invalid response status %q", result.Result.Response.Status)
		}
		if result.NextPollAfter != 0 {
			return errors.New("completed result must not contain a poll delay")
		}
	case ResumableFailed:
		if result.Failure == nil {
			return errors.New("failed result is missing a provider failure")
		}
		if result.NextPollAfter != 0 {
			return errors.New("failed result must not contain a poll delay")
		}
		if result.Dispatch == DispatchNotDispatched {
			return errors.New("failed result must classify dispatch certainty")
		}
	case ResumableNotFound:
		if result.ProviderOperationID != "" {
			return errors.New("not-found result must not contain a provider operation id")
		}
		if result.Failure != nil {
			return errors.New("not-found result must not contain a failure")
		}
		if result.NextPollAfter != 0 {
			return errors.New("not-found result must not contain a poll delay")
		}
		if result.Dispatch != DispatchAmbiguous {
			return fmt.Errorf("not-found result dispatch must be %q", DispatchAmbiguous)
		}
	}
	return nil
}
