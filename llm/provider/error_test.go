package provider_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestCommonErrorKeepsCauseLocalAndSerializesSafeFacts(t *testing.T) {
	cause := errors.New("provider secret response body")
	err := &provider.Error{
		Code:        provider.CodeAuthentication,
		Phase:       provider.PhaseDispatch,
		Dispatch:    provider.DispatchRejected,
		Retry:       provider.RetryNever,
		OperationID: "op-1",
		Provider: llm.ProviderFacts{
			RequestID: "req-1",
			Raw:       map[string]json.RawMessage{"body": json.RawMessage(`"secret"`)},
		},
		SafeMessage: "provider authentication failed",
		SafeDetails: map[string]string{"status": "401"},
		Cause:       cause,
	}
	if err.Error() != err.SafeMessage {
		t.Fatalf("Error() = %q, want safe message", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap did not retain local cause")
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	serialized := string(encoded)
	if strings.Contains(serialized, "secret") || strings.Contains(serialized, "body") || strings.Contains(serialized, "Cause") {
		t.Fatalf("safe error leaked cause/provider raw data: %s", serialized)
	}
	if !strings.Contains(serialized, "provider authentication failed") || !strings.Contains(serialized, "req-1") {
		t.Fatalf("safe facts missing: %s", serialized)
	}
}

func TestDispatchCertaintiesAreClosed(t *testing.T) {
	for _, value := range []provider.DispatchCertainty{
		provider.DispatchNotDispatched,
		provider.DispatchRejected,
		provider.DispatchAccepted,
		provider.DispatchAmbiguous,
	} {
		if !value.Valid() {
			t.Errorf("dispatch certainty %q is invalid", value)
		}
	}
	if provider.DispatchCertainty("other").Valid() {
		t.Fatal("unknown dispatch certainty was accepted")
	}
}

func TestErrorEnumsAreClosed(t *testing.T) {
	if provider.Code("other").Valid() || provider.Phase("other").Valid() || provider.RetryDisposition("other").Valid() {
		t.Fatal("unknown common error enum was accepted")
	}
	if _, err := json.Marshal(&provider.Error{
		Code:        provider.Code("other"),
		Phase:       provider.PhaseDispatch,
		Dispatch:    provider.DispatchRejected,
		Retry:       provider.RetryNever,
		SafeMessage: "safe",
	}); err == nil {
		t.Fatal("marshaled an error with an invalid code")
	}
}
