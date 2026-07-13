package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"

	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestMapAPIErrorProducesSafeCommonFacts(t *testing.T) {
	apiErr := &openai.Error{
		Code:       "rate_limit_exceeded",
		Message:    "secret provider body",
		StatusCode: http.StatusTooManyRequests,
		Response:   &http.Response{Header: http.Header{"X-Request-Id": []string{"req-error"}, "Retry-After": []string{"2"}}},
	}
	mapped := mapError(apiErr)
	if mapped.Code != provider.CodeProviderRateLimited || mapped.Dispatch != provider.DispatchRejected || mapped.Retry != provider.RetryAfter {
		t.Fatalf("mapped = %#v", mapped)
	}
	if mapped.Provider.RequestID != "req-error" || mapped.SafeDetails["provider_code"] != "rate_limit_exceeded" {
		t.Fatalf("provider facts = %#v", mapped)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret provider body") {
		t.Fatalf("raw provider message leaked: %s", encoded)
	}
}

func TestMapErrorClassifiesContextAndTransportFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		code provider.Code
	}{
		{context.Canceled, provider.CodeCanceled},
		{context.DeadlineExceeded, provider.CodeDeadlineExceeded},
		{errors.New("connection reset"), provider.CodeProviderUnavailable},
	} {
		mapped := mapError(test.err)
		if mapped.Code != test.code {
			t.Errorf("%v -> %s, want %s", test.err, mapped.Code, test.code)
		}
	}
}
