package anthropicmessages

import (
	"fmt"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/mfow/llm-temporal-worker/llm/provider/internal/clientconfig"
)

type ClientConfig struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Client owns the official Anthropic Messages SDK client. Environment-based
// credential discovery is disabled so configuration snapshots remain the
// source of truth.
type Client struct {
	sdk     anthropic.Client
	baseURL string
}

func NewClient(config ClientConfig) (*Client, error) {
	baseURL, err := clientconfig.BaseURL(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("anthropic messages: %w", err)
	}
	if err := clientconfig.Secret("Anthropic API key", config.APIKey); err != nil {
		return nil, fmt.Errorf("anthropic messages: %w", err)
	}
	if config.HTTPClient == nil {
		return nil, fmt.Errorf("anthropic messages: HTTP client is required")
	}
	return &Client{sdk: anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(config.APIKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(config.HTTPClient),
		option.WithMaxRetries(0),
	), baseURL: baseURL}, nil
}
