package activity

import (
	"context"
	"errors"

	"github.com/mfow/llm-temporal-worker/llm/provider"
	"go.temporal.io/sdk/temporal"
)

const (
	ErrorTypeInvalidArgument   = "llm_invalid_argument"
	ErrorTypeAuthentication    = "llm_authentication"
	ErrorTypeBudgetWait        = "llm_budget_wait"
	ErrorTypeProviderTransient = "llm_provider_transient"
	ErrorTypeAmbiguous         = "llm_ambiguous_dispatch"
	ErrorTypeOperationConflict = "llm_operation_conflict"
	ErrorTypeStateCorrupt      = "llm_state_corrupt"
	ErrorTypeInternal          = "llm_internal"
)

// SafeErrorDetails is the only error detail shape emitted to Temporal. It
// contains identifiers and bounded provider facts, never causes or payloads.
type SafeErrorDetails struct {
	OperationID       string `json:"operation_id,omitempty"`
	Code              string `json:"code"`
	Phase             string `json:"phase"`
	Dispatch          string `json:"dispatch"`
	RetryAfterMillis  int64  `json:"retry_after_ms,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`
}

func ToTemporalError(err error) error {
	if err == nil {
		return nil
	}
	if temporal.IsCanceledError(err) || errors.Is(err, context.Canceled) {
		return err
	}
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		return temporal.NewNonRetryableApplicationError("invalid Activity error", ErrorTypeInvalidArgument, nil, SafeErrorDetails{Code: string(provider.CodeInvalidArgument), Phase: string(provider.PhaseDecode), Dispatch: string(provider.DispatchNotDispatched)})
	}
	if providerErr.Code == provider.CodeCanceled {
		return context.Canceled
	}
	typeName, nonRetryable := classify(providerErr)
	details := SafeErrorDetails{OperationID: providerErr.OperationID, Code: string(providerErr.Code), Phase: string(providerErr.Phase), Dispatch: string(providerErr.Dispatch), RetryAfterMillis: providerErr.RetryAfter.Milliseconds(), ProviderRequestID: providerErr.Provider.RequestID}
	message := stableMessage(typeName)
	options := temporal.ApplicationErrorOptions{NonRetryable: nonRetryable, Details: []interface{}{details}}
	if !nonRetryable && providerErr.RetryAfter > 0 {
		options.NextRetryDelay = providerErr.RetryAfter
	}
	return temporal.NewApplicationErrorWithOptions(message, typeName, options)
}

func classify(err *provider.Error) (string, bool) {
	if err == nil {
		return ErrorTypeInternal, true
	}
	switch err.Code {
	case provider.CodeInvalidArgument, provider.CodeUnsupportedCapability, provider.CodeNoRoute, provider.CodeConfiguration:
		return ErrorTypeInvalidArgument, true
	case provider.CodeAuthentication, provider.CodePermissionDenied:
		return ErrorTypeAuthentication, true
	case provider.CodeBudgetDenied:
		return ErrorTypeBudgetWait, false
	case provider.CodeOperationConflict:
		return ErrorTypeOperationConflict, true
	case provider.CodeAmbiguousDispatch:
		return ErrorTypeAmbiguous, true
	case provider.CodeStateCorrupt:
		return ErrorTypeStateCorrupt, true
	case provider.CodeCanceled:
		return ErrorTypeInvalidArgument, true
	case provider.CodeProviderRateLimited, provider.CodeProviderUnavailable, provider.CodeStateUnavailable, provider.CodeDeadlineExceeded:
		return ErrorTypeProviderTransient, false
	case provider.CodeProviderInvalidResponse:
		return ErrorTypeProviderTransient, err.Dispatch == provider.DispatchAccepted || err.Dispatch == provider.DispatchAmbiguous
	default:
		return ErrorTypeInternal, err.Dispatch == provider.DispatchAccepted || err.Dispatch == provider.DispatchAmbiguous
	}
}

func stableMessage(typeName string) string {
	switch typeName {
	case ErrorTypeInvalidArgument:
		return "request or configuration is invalid"
	case ErrorTypeAuthentication:
		return "provider authentication or permission failed"
	case ErrorTypeBudgetWait:
		return "budget reservation is not currently available"
	case ErrorTypeProviderTransient:
		return "provider request failed safely and may be retried"
	case ErrorTypeAmbiguous:
		return "provider request outcome is ambiguous"
	case ErrorTypeOperationConflict:
		return "operation key is bound to a different request"
	case ErrorTypeStateCorrupt:
		return "durable inference state is corrupt"
	default:
		return "inference failed"
	}
}

// ErrorType returns the stable Temporal type for a provider-neutral error.
func ErrorType(err error) string {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		typeName, _ := classify(providerErr)
		return typeName
	}
	return ErrorTypeInternal
}
