package anthropicmessages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestInvokeMakesExactlyOneSDKCallAndLiftsResponse(t *testing.T) {
	responseBody := `{"id":"msg_invoke","type":"message","role":"assistant","model":"claude-contract","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1,"service_tier":"standard"}}`
	calls := 0
	client, err := NewClient(ClientConfig{
		BaseURL: "http://127.0.0.1/contract",
		APIKey:  "test-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Path != "/contract/v1/messages" {
				t.Errorf("request path = %q", request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "Request-Id": []string{"req-invoke"}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Request:    request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	profile.ExpectedBaseURL = "http://127.0.0.1/contract"
	adapter, err := New(client, "anthropic-prod", profile)
	if err != nil {
		t.Fatal(err)
	}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "invoke", Model: "claude-contract"},
		Query:   provider.CapabilityQuery{EndpointID: "anthropic-prod", Family: provider.FamilyAnthropicMessages, Model: "claude-contract"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &anthropicObserver{}
	result, err := adapter.Invoke(context.Background(), call, observer)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || observer.before != 1 || observer.headers != 1 || observer.progress != 1 {
		t.Fatalf("transport/observer counts = %d %#v", calls, observer)
	}
	if result.Response.OperationKey != "invoke" || result.Response.Provider.RequestID != "req-invoke" || result.Response.Status != llm.ResponseStatusCompleted || result.Response.Service.Actual == nil || *result.Response.Service.Actual != llm.ServiceClassStandard {
		t.Fatalf("response = %#v", result.Response)
	}
}

func TestInvokeMapsRateLimitAndAuthenticationErrorsWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		fixture   string
		wantCode  provider.Code
		requestID string
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, fixture: "error.rate-limit.json", wantCode: provider.CodeProviderRateLimited, requestID: "req-rate"},
		{name: "auth", status: http.StatusUnauthorized, fixture: "error.authentication.json", wantCode: provider.CodeAuthentication, requestID: "req-auth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			body := string(mustReadFixture(t, test.fixture))
			client, err := NewClient(ClientConfig{
				BaseURL: "http://127.0.0.1/contract",
				APIKey:  "test-key",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": []string{"application/json"}, "Request-Id": []string{test.requestID}, "Retry-After": []string{"2"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			profile := testProfile()
			profile.ExpectedBaseURL = "http://127.0.0.1/contract"
			adapter, err := New(client, "anthropic-prod", profile)
			if err != nil {
				t.Fatal(err)
			}
			call, err := adapter.Compile(context.Background(), provider.CompileInput{Request: llm.Request{OperationKey: "error-" + test.name, Model: "claude-contract"}, Query: provider.CapabilityQuery{EndpointID: "anthropic-prod", Family: provider.FamilyAnthropicMessages, Model: "claude-contract"}, Strict: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Invoke(context.Background(), call, nil)
			var providerErr *provider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != test.wantCode || providerErr.Provider.RequestID != test.requestID {
				t.Fatalf("mapped error = %#v", err)
			}
			if calls != 1 {
				t.Fatalf("SDK call count = %d, want 1", calls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type anthropicObserver struct {
	before   int
	headers  int
	progress int
}

func (observer *anthropicObserver) BeforePossibleWrite(context.Context) error {
	observer.before++
	return nil
}

func (observer *anthropicObserver) AfterResponseHeaders(context.Context, provider.ResponseMetadata) error {
	observer.headers++
	return nil
}

func (observer *anthropicObserver) OnProgress(context.Context, provider.Progress) {
	observer.progress++
}
