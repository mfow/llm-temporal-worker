package provider

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mfow/llm-temporal-worker/llm"
)

type Code string

const (
	CodeInvalidArgument         Code = "invalid_argument"
	CodeUnsupportedCapability   Code = "unsupported_capability"
	CodeNoRoute                 Code = "no_route"
	CodeAuthentication          Code = "authentication"
	CodePermissionDenied        Code = "permission_denied"
	CodeBudgetDenied            Code = "budget_denied"
	CodeOperationConflict       Code = "operation_conflict"
	CodeAmbiguousDispatch       Code = "ambiguous_dispatch"
	CodeProviderRateLimited     Code = "provider_rate_limited"
	CodeProviderUnavailable     Code = "provider_unavailable"
	CodeProviderInvalidResponse Code = "provider_invalid_response"
	CodeDeadlineExceeded        Code = "deadline_exceeded"
	CodeCanceled                Code = "canceled"
	CodeStateUnavailable        Code = "state_unavailable"
	CodeStateCorrupt            Code = "state_corrupt"
	CodeConfiguration           Code = "configuration"
	CodeInternal                Code = "internal"
)

func (code Code) Valid() bool {
	switch code {
	case CodeInvalidArgument, CodeUnsupportedCapability, CodeNoRoute,
		CodeAuthentication, CodePermissionDenied, CodeBudgetDenied,
		CodeOperationConflict, CodeAmbiguousDispatch, CodeProviderRateLimited,
		CodeProviderUnavailable, CodeProviderInvalidResponse, CodeDeadlineExceeded,
		CodeCanceled, CodeStateUnavailable, CodeStateCorrupt, CodeConfiguration,
		CodeInternal:
		return true
	default:
		return false
	}
}

type Phase string

const (
	PhaseDecode            Phase = "decode"
	PhaseNormalize         Phase = "normalize"
	PhaseStateLoad         Phase = "state_load"
	PhasePlan              Phase = "plan"
	PhasePrice             Phase = "price"
	PhaseAdmission         Phase = "admission"
	PhaseCompile           Phase = "compile"
	PhaseDispatch          Phase = "dispatch"
	PhaseStream            Phase = "stream"
	PhaseLift              Phase = "lift"
	PhaseFinalize          Phase = "finalize"
	PhaseContinuationWrite Phase = "continuation_write"
)

func (phase Phase) Valid() bool {
	switch phase {
	case PhaseDecode, PhaseNormalize, PhaseStateLoad, PhasePlan, PhasePrice,
		PhaseAdmission, PhaseCompile, PhaseDispatch, PhaseStream, PhaseLift,
		PhaseFinalize, PhaseContinuationWrite:
		return true
	default:
		return false
	}
}

type DispatchCertainty string

const (
	DispatchNotDispatched DispatchCertainty = "not_dispatched"
	DispatchRejected      DispatchCertainty = "rejected"
	DispatchAccepted      DispatchCertainty = "accepted"
	DispatchAmbiguous     DispatchCertainty = "ambiguous"
)

func (certainty DispatchCertainty) Valid() bool {
	switch certainty {
	case DispatchNotDispatched, DispatchRejected, DispatchAccepted, DispatchAmbiguous:
		return true
	default:
		return false
	}
}

type RetryDisposition string

const (
	RetryNever         RetryDisposition = "never"
	RetrySameOperation RetryDisposition = "same_operation"
	RetryNextRoute     RetryDisposition = "next_route"
	RetryAfter         RetryDisposition = "after"
)

func (retry RetryDisposition) Valid() bool {
	switch retry {
	case RetryNever, RetrySameOperation, RetryNextRoute, RetryAfter:
		return true
	default:
		return false
	}
}

type Error struct {
	Code        Code
	Phase       Phase
	Dispatch    DispatchCertainty
	Retry       RetryDisposition
	RetryAfter  time.Duration
	OperationID string
	Provider    llm.ProviderFacts
	SafeMessage string
	SafeDetails map[string]string
	Cause       error
}

func NewError(code Code, phase Phase, dispatch DispatchCertainty, retry RetryDisposition, message string) *Error {
	return &Error{Code: code, Phase: phase, Dispatch: dispatch, Retry: retry, SafeMessage: message}
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	return err.SafeMessage
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *Error) MarshalJSON() ([]byte, error) {
	if err == nil {
		return []byte("null"), nil
	}
	if err.SafeMessage == "" {
		return nil, fmt.Errorf("safe error message must not be empty")
	}
	if !err.Code.Valid() {
		return nil, fmt.Errorf("error code %q is invalid", err.Code)
	}
	if !err.Phase.Valid() {
		return nil, fmt.Errorf("error phase %q is invalid", err.Phase)
	}
	if !err.Dispatch.Valid() {
		return nil, fmt.Errorf("dispatch certainty %q is invalid", err.Dispatch)
	}
	if !err.Retry.Valid() {
		return nil, fmt.Errorf("retry disposition %q is invalid", err.Retry)
	}
	fields := map[string]any{
		"code":     err.Code,
		"phase":    err.Phase,
		"dispatch": err.Dispatch,
		"retry":    err.Retry,
		"message":  err.SafeMessage,
	}
	if err.RetryAfter > 0 {
		fields["retry_after_ms"] = err.RetryAfter.Milliseconds()
	}
	if err.OperationID != "" {
		fields["operation_id"] = err.OperationID
	}
	provider := map[string]string{}
	if err.Provider.ResponseID != "" {
		provider["response_id"] = err.Provider.ResponseID
	}
	if err.Provider.RequestID != "" {
		provider["request_id"] = err.Provider.RequestID
	}
	if err.Provider.GenerationID != "" {
		provider["generation_id"] = err.Provider.GenerationID
	}
	if err.Provider.FinishReason != "" {
		provider["finish_reason"] = err.Provider.FinishReason
	}
	if len(provider) > 0 {
		fields["provider"] = provider
	}
	if len(err.SafeDetails) > 0 {
		fields["details"] = err.SafeDetails
	}
	return json.Marshal(fields)
}
