package bedrockmessages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestCompileMapsOnlyTheThreePublicBedrockServiceClasses(t *testing.T) {
	adapter := &Adapter{endpointID: "bedrock-prod", profile: mustBedrockProfile(t, "")}
	for _, test := range []struct {
		class llm.ServiceClass
		tier  string
	}{
		{class: llm.ServiceClassEconomy, tier: "flex"},
		{class: llm.ServiceClassStandard, tier: "default"},
		{class: llm.ServiceClassPriority, tier: "priority"},
	} {
		call, err := adapter.Compile(context.Background(), provider.CompileInput{
			Request: llm.Request{OperationKey: "tier-" + string(test.class), Model: "claude-contract", ServiceClass: test.class},
			Query:   provider.CapabilityQuery{EndpointID: "bedrock-prod", Family: provider.FamilyBedrockMessages, Model: "claude-contract"}, Strict: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if call.Metadata.ProviderTier != test.tier {
			t.Fatalf("class %q provider tier = %q, want %q", test.class, call.Metadata.ProviderTier, test.tier)
		}
		wire := marshalBedrockWire(t, call.SDKParams)
		if wire["service_tier"] != test.tier {
			t.Fatalf("class %q wire tier = %#v, want %q", test.class, wire["service_tier"], test.tier)
		}
	}
}

func TestProfileRejectsReservedCapacityAsPublicTier(t *testing.T) {
	profile := DefaultProfile("bedrock-contract")
	profile.ServiceTiers[llm.ServiceClassEconomy] = "reserved"
	if _, err := NewProfile(profile); err == nil || !strings.Contains(err.Error(), "invalid provider tier") {
		t.Fatalf("reserved tier validation error = %v", err)
	}
}

func TestInvokeUsesBedrockMiddlewareOnceAndLiftsAWSRequestID(t *testing.T) {
	responseBody := string(mustReadBedrockFixture(t, "response.completed.json"))
	calls := 0
	var requestBody []byte
	client, err := NewClient(context.Background(), ClientConfig{
		BaseURL: "http://127.0.0.1",
		HTTPClient: &http.Client{Transport: bedrockRoundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			requestBody, _ = io.ReadAll(request.Body)
			if !strings.HasSuffix(request.URL.Path, "/model/claude-contract/invoke") {
				t.Errorf("Bedrock request path = %q", request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Amzn-Requestid": []string{"bedrock-req-1"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
		})},
		AWSConfig: aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("contract-access", "contract-secret", "")},
	})
	if err != nil {
		t.Fatalf("invoke error: %#v", err)
	}
	profile := mustBedrockProfile(t, "http://127.0.0.1")
	adapter, err := New(client, "bedrock-prod", profile)
	if err != nil {
		t.Fatal(err)
	}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "bedrock-once", Model: "claude-contract"},
		Query:   provider.CapabilityQuery{EndpointID: "bedrock-prod", Family: provider.FamilyBedrockMessages, Model: "claude-contract"}, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Invoke(context.Background(), call, nil)
	if err != nil {
		var providerErr *provider.Error
		if errors.As(err, &providerErr) {
			t.Fatalf("invoke error: %#v cause=%v", providerErr, providerErr.Cause)
		}
		t.Fatalf("invoke error: %#v", err)
	}
	if calls != 1 || result.Response.Provider.RequestID != "bedrock-req-1" || result.Response.Service.Actual == nil || *result.Response.Service.Actual != llm.ServiceClassStandard {
		t.Fatalf("calls/response = %d %#v", calls, result.Response)
	}
	if !strings.Contains(string(requestBody), `"service_tier":"default"`) {
		t.Fatalf("Bedrock request body omitted default tier: %s", requestBody)
	}
	if strings.Contains(string(requestBody), "contract-secret") {
		t.Fatal("AWS secret appeared in Bedrock request body")
	}
}

func TestBedrockCallSerializationDoesNotContainResolvedCredentials(t *testing.T) {
	adapter := &Adapter{endpointID: "bedrock-prod", profile: mustBedrockProfile(t, "")}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "bedrock-credentials", Model: "claude-contract"},
		Query:   provider.CapabilityQuery{EndpointID: "bedrock-prod", Family: provider.FamilyBedrockMessages, Model: "claude-contract"}, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "contract-secret") || strings.Contains(string(encoded), "contract-access") {
		t.Fatalf("credential value appeared in call serialization: %s", encoded)
	}
}

type bedrockRoundTrip func(*http.Request) (*http.Response, error)

func (function bedrockRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mustBedrockProfile(t *testing.T, baseURL string) Profile {
	t.Helper()
	profile := DefaultProfile("bedrock-contract")
	profile.CapabilityVersion = "bedrock-contract/v1"
	profile.Capabilities.Version = profile.CapabilityVersion
	profile.ExpectedBaseURL = baseURL
	profile.ExpectedModel = "claude-contract"
	validated, err := NewProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

func marshalBedrockWire(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func mustReadBedrockFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/contracts/bedrock-anthropic/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
