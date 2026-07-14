package openairesponses

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestAzureResponsesUsesV1PathAndAPIKey(t *testing.T) {
	responseBody := readFixture(t, "response.completed.json")
	var got *http.Request
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"azure-resp-1"}},
			Body:       io.NopCloser(bytes.NewReader(responseBody)),
			Request:    request,
		}, nil
	})
	client, err := NewAzureClient(AzureClientConfig{
		Endpoint:   "http://127.0.0.1",
		APIVersion: "v1",
		APIKey:     "azure-key",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAzureAdapter(client, "azure-responses", "azure-responses/v1")
	if err != nil {
		t.Fatal(err)
	}
	call, err := adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "azure-op", Model: "gpt-contract", ServiceClass: llm.ServiceClassStandard},
		Query:   provider.CapabilityQuery{EndpointID: "azure-responses", Family: provider.FamilyOpenAIResponses, Model: "gpt-contract"},
		Strict:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Invoke(context.Background(), call, provider.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Azure request was not captured")
	}
	if got.URL.Path != "/openai/v1/responses" || got.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("Azure Responses request URL = %s", got.URL)
	}
	if got.Header.Get("Api-Key") != "azure-key" || got.Header.Get("Authorization") != "" {
		t.Fatalf("Azure Responses auth headers = %#v", got.Header)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"model":"gpt-contract"`)) {
		t.Fatalf("Azure Responses request body = %s", body)
	}
	if result.Response.OperationKey != "azure-op" || result.Response.Provider.RequestID != "azure-resp-1" {
		t.Fatalf("Azure Responses result = %#v", result.Response)
	}
}

func TestAzureResponsesValidatesResolvedConfig(t *testing.T) {
	valid := AzureClientConfig{
		Endpoint:   "https://resource.openai.azure.com",
		APIVersion: "v1",
		APIKey:     "azure-key",
		HTTPClient: http.DefaultClient,
	}
	for name, config := range map[string]AzureClientConfig{
		"missing endpoint":    {APIVersion: valid.APIVersion, APIKey: valid.APIKey, HTTPClient: valid.HTTPClient},
		"missing api version": {Endpoint: valid.Endpoint, APIKey: valid.APIKey, HTTPClient: valid.HTTPClient},
		"missing api key":     {Endpoint: valid.Endpoint, APIVersion: valid.APIVersion, HTTPClient: valid.HTTPClient},
		"missing http client": {Endpoint: valid.Endpoint, APIVersion: valid.APIVersion, APIKey: valid.APIKey},
		"unsafe endpoint":     {Endpoint: "http://example.com", APIVersion: valid.APIVersion, APIKey: valid.APIKey, HTTPClient: valid.HTTPClient},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAzureClient(config); err == nil {
				t.Fatalf("NewAzureClient(%#v) unexpectedly succeeded", config)
			}
		})
	}
}

func TestAzureResponsesTokenCredentialUsesBearerAndV1Path(t *testing.T) {
	var got *http.Request
	credential := &fakeAzureTokenCredential{}
	client, err := NewAzureTokenClient(AzureTokenClientConfig{
		Endpoint:        "https://127.0.0.1",
		APIVersion:      "v1",
		TokenCredential: credential,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			got = request
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(readFixture(t, "response.completed.json"))),
				Request:    request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.sdk.Responses.New(context.Background(), responses.ResponseNewParams{Model: shared.ResponsesModel("gpt-contract")})
	if err != nil {
		t.Fatal(err)
	}
	if credential.calls == 0 {
		t.Fatal("token credential was not called")
	}
	if got == nil || got.URL.Path != "/openai/v1/responses" || got.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("Azure token Responses request URL = %v", gotURL(got))
	}
	if got.Header.Get("Authorization") != "Bearer azure-token" || got.Header.Get("Api-Key") != "" {
		t.Fatalf("Azure token Responses auth headers = %#v", got.Header)
	}
}

func TestAzureResponsesTokenClientRejectsMissingCredential(t *testing.T) {
	base := AzureTokenClientConfig{
		Endpoint:   "https://resource.openai.azure.com",
		APIVersion: "v1",
		HTTPClient: http.DefaultClient,
	}
	for name, credential := range map[string]azcore.TokenCredential{
		"nil interface": nil,
		"typed nil":     (*fakeAzureTokenCredential)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			base.TokenCredential = credential
			_, err := NewAzureTokenClient(base)
			if err == nil || !strings.Contains(err.Error(), "token credential is required") {
				t.Fatalf("missing token credential error = %v", err)
			}
		})
	}
}

func TestAzureResponsesAdapterAliases(t *testing.T) {
	client, err := NewAzureOpenAIClient(AzureClientConfig{
		Endpoint:   "https://resource.openai.azure.com",
		APIVersion: "v1",
		APIKey:     "azure-key",
		HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAzureOpenAIAdapter(client, "azure-responses", "azure-responses/v1")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != adapterName {
		t.Fatalf("adapter name = %q, want %q", adapter.Name(), adapterName)
	}
}

func TestAzureResponsesPathShimLeavesOtherRoutesUntouched(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://resource.openai.azure.com/openai/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = azureResponsesPathMiddleware(request, func(got *http.Request) (*http.Response, error) {
		called = true
		if got.URL.Path != "/openai/models" {
			t.Fatalf("non-Responses path = %q", got.URL.Path)
		}
		return nil, nil
	})
	if err != nil || !called {
		t.Fatalf("path middleware result: called=%v err=%v", called, err)
	}
}

type fakeAzureTokenCredential struct {
	calls int
}

func (credential *fakeAzureTokenCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	credential.calls++
	if len(options.Scopes) != 1 || options.Scopes[0] != "https://cognitiveservices.azure.com/.default" {
		return azcore.AccessToken{}, fmt.Errorf("unexpected scopes: %v", options.Scopes)
	}
	return azcore.AccessToken{Token: "azure-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func gotURL(request *http.Request) any {
	if request == nil {
		return nil
	}
	return request.URL
}
