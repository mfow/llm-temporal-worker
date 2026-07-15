package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestResponsesEventSourceClassifiesRateLimitedTerminalEventsWithoutLeakingMessage(t *testing.T) {
	const secret = "provider-secret-message-must-not-leak"
	for _, test := range []struct {
		name           string
		event          responses.ResponseStreamEventUnion
		wantResponseID string
	}{
		{
			name: "response failed",
			event: responses.ResponseStreamEventUnion{
				Type: "response.failed",
				Response: responses.Response{
					ID: "resp-rate-limited",
					Error: responses.ResponseError{
						Code:    responses.ResponseErrorCodeRateLimitExceeded,
						Message: secret,
					},
				},
			},
			wantResponseID: "resp-rate-limited",
		},
		{
			name: "error event",
			event: responses.ResponseStreamEventUnion{
				Type:    "error",
				Code:    "rate_limit_exceeded",
				Message: secret,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &responsesEventSource{
				call: provider.Call{
					EndpointID:   "openai-stream-test",
					Family:       provider.FamilyOpenAIResponses,
					OperationKey: "stream-rate-limit",
				},
				requestID: "req-rate-limited",
			}
			event, terminal, err := source.decode(test.event)
			if err != nil {
				t.Fatal(err)
			}
			if !terminal {
				t.Fatal("terminal provider error was not terminal")
			}
			failed, ok := event.(provider.StreamErrored)
			if !ok {
				t.Fatalf("event = %T, want provider.StreamErrored", event)
			}
			mapped, ok := failed.Err.(*provider.Error)
			if !ok {
				t.Fatalf("terminal error = %T, want *provider.Error", failed.Err)
			}
			if mapped.Code != provider.CodeProviderRateLimited || mapped.Phase != provider.PhaseStream || mapped.Dispatch != provider.DispatchAccepted || mapped.Retry != provider.RetryAfter {
				t.Fatalf("mapped terminal = %#v", mapped)
			}
			if mapped.Provider.RequestID != "req-rate-limited" || mapped.Provider.ResponseID != test.wantResponseID {
				t.Fatalf("mapped provider facts = %#v", mapped.Provider)
			}
			if mapped.SafeDetails["provider"] != adapterName || mapped.SafeDetails["provider_code"] != "rate_limit_exceeded" || mapped.SafeDetails["endpoint"] != "openai-stream-test" {
				t.Fatalf("mapped safe details = %#v", mapped.SafeDetails)
			}
			if _, hasStatus := mapped.SafeDetails["status"]; hasStatus {
				t.Fatalf("stream terminal inferred an HTTP status: %#v", mapped.SafeDetails)
			}
			encoded, err := json.Marshal(mapped)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || strings.Contains(mapped.Error(), secret) {
				t.Fatalf("provider terminal message leaked: %s", encoded)
			}
		})
	}
}
