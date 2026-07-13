package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	anthropicaws "github.com/anthropics/anthropic-sdk-go/aws"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestAWSGatewayUsesOfficialMessagesClientExactlyOnce(t *testing.T) {
	responseBody := string(mustReadAWSFixture(t, "response.completed.json"))
	calls := 0
	client, err := NewAWSClient(context.Background(), AWSClientConfig{
		BaseURL: "http://127.0.0.1/aws-contract",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Path != "/aws-contract/v1/messages" {
				t.Errorf("request path = %q", request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}, "Request-Id": []string{"aws-req-1"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
		})},
		AWSConfig: anthropicaws.ClientConfig{AWSRegion: "us-east-1", SkipAuth: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	profile.ExpectedBaseURL = "http://127.0.0.1/aws-contract"
	adapter, err := New(client, "anthropic-aws", profile)
	if err != nil {
		t.Fatal(err)
	}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "aws-once", Model: "claude-contract"},
		Query:   provider.CapabilityQuery{EndpointID: "anthropic-aws", Family: provider.FamilyAnthropicMessages, Model: "claude-contract"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Invoke(context.Background(), call, nil); err != nil {
		var providerErr *provider.Error
		if errors.As(err, &providerErr) {
			t.Fatalf("invoke error: %#v cause=%v", providerErr, providerErr.Cause)
		}
		t.Fatalf("invoke error: %#v", err)
	}
	if calls != 1 {
		t.Fatalf("AWS gateway request count = %d, want 1", calls)
	}
}

func TestAWSConfigAndCallsDoNotSerializeCredentialValues(t *testing.T) {
	const secret = "aws-secret-must-not-cross-boundary"
	config := AWSClientConfig{AWSConfig: anthropicaws.ClientConfig{AWSAccessKey: "access", AWSSecretAccessKey: secret, AWSRegion: "us-east-1", SkipAuth: true}}
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedConfig), secret) {
		t.Fatal("resolved credential value appeared in AWS client configuration serialization")
	}
	adapter := &Adapter{endpointID: "anthropic-aws", profile: mustProfile(t, func() Profile {
		profile := testProfile()
		profile.ExpectedBaseURL = ""
		return profile
	}())}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "aws-credentials", Model: "claude-contract"},
		Query:   provider.CapabilityQuery{EndpointID: "anthropic-aws", Family: provider.FamilyAnthropicMessages, Model: "claude-contract"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encodedCall, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedCall), secret) {
		t.Fatal("credential value appeared in provider call serialization")
	}
}

func TestAWSClientRequiresContextAndHTTPClient(t *testing.T) {
	if _, err := NewAWSClient(nil, AWSClientConfig{HTTPClient: http.DefaultClient, AWSConfig: anthropicaws.ClientConfig{SkipAuth: true, AWSRegion: "us-east-1"}}); err == nil {
		t.Fatal("nil context unexpectedly succeeded")
	}
	if _, err := NewAWSClient(context.Background(), AWSClientConfig{AWSConfig: anthropicaws.ClientConfig{SkipAuth: true, AWSRegion: "us-east-1"}}); err == nil {
		t.Fatal("nil HTTP client unexpectedly succeeded")
	}
}

func mustReadAWSFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/contracts/anthropic-aws/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
