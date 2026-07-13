package openairesponses

import (
	"context"
	"net/http"
	"testing"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/mfow/llm-temporal-worker/llm/provider/internal/contract"
)

func TestSDKRetriesDisabled(t *testing.T) {
	server := contract.NewRetryServer(t)
	client, err := NewClient(ClientConfig{BaseURL: server.URL, APIKey: "test-key", HTTPClient: server.HTTPClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.sdk.Responses.New(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-contract"),
		Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("hello")},
	})
	if err == nil {
		t.Fatal("expected retryable response error")
	}
	if got := server.Count(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestClientValidatesResolvedConfig(t *testing.T) {
	for _, config := range []ClientConfig{
		{BaseURL: "https://api.openai.com/v1", APIKey: "", HTTPClient: http.DefaultClient},
		{BaseURL: "https://api.openai.com/v1", APIKey: "key", HTTPClient: nil},
		{BaseURL: "http://untrusted.example", APIKey: "key", HTTPClient: http.DefaultClient},
	} {
		if _, err := NewClient(config); err == nil {
			t.Errorf("NewClient(%#v) unexpectedly succeeded", config)
		}
	}
}
