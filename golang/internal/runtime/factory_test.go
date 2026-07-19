package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/config"
	"github.com/mfow/llm-temporal-worker/golang/engine"
	"github.com/mfow/llm-temporal-worker/golang/internal/secrets"
	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider/anthropicmessages"
	"github.com/mfow/llm-temporal-worker/golang/routing"
	redisstore "github.com/mfow/llm-temporal-worker/golang/storage/redis"
	redisclient "github.com/redis/go-redis/v9"
)

func TestRedisKeyOptionsUseConfiguredPrefix(t *testing.T) {
	value := config.Config{}
	value.State.Redis.KeyPrefix = "worker-a.v1"
	value.State.Redis.AdmissionHashTag = "admission"
	options, err := redisKeyOptions(value, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if options.Prefix != "worker-a.v1" {
		t.Fatalf("Redis key prefix = %q, want worker-a.v1", options.Prefix)
	}
	if _, err := redisstore.NewKeyOptions("bad prefix", "admission", options.KeySecret); err == nil {
		t.Fatal("invalid Redis key prefix accepted")
	}
}

func TestDefaultRedisFactoryDisablesClientRetries(t *testing.T) {
	client, err := defaultRedisFactory(context.Background(), config.RedisConfig{Addresses: []string{"127.0.0.1:6379"}}, "", "")
	if err != nil {
		t.Fatalf("defaultRedisFactory() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	standalone, ok := client.(*redisclient.Client)
	if !ok {
		t.Fatalf("defaultRedisFactory() client = %T, want *redis.Client", client)
	}
	if got := standalone.Options().MaxRetries; got != 0 {
		t.Fatalf("MaxRetries = %d, want effective zero retries", got)
	}
}

func TestProductionFactoryProviderSecretFailsClosed(t *testing.T) {
	called := false
	factory := &ProductionEngineFactory{options: ProductionFactoryOptions{
		Resolver: secrets.ResolverFunc(func(context.Context, config.SecretRef) ([]byte, error) {
			called = true
			return []byte("should-not-be-called"), nil
		}),
	}}
	_, err := factory.providerSecret(context.Background(), config.AuthConfig{Kind: "workload_identity", Audience: "provider"}, "endpoint")
	if !errors.Is(err, ErrUnsupportedProviderAuth) {
		t.Fatalf("error = %v, want ErrUnsupportedProviderAuth", err)
	}
	if called {
		t.Fatal("unsupported auth attempted secret resolution")
	}
}

func TestProductionFactoryBuildsOpenAIResponsesAdapter(t *testing.T) {
	factory, err := NewProductionEngineFactory(ProductionFactoryOptions{
		Resolver: secrets.ResolverFunc(func(_ context.Context, ref config.SecretRef) ([]byte, error) {
			if ref.Kind != config.SecretEnv || ref.Name != "OPENAI_KEY" {
				t.Fatalf("resolved unexpected secret reference: %#v", ref)
			}
			return []byte("test-key"), nil
		}),
		SnapshotLoader: SnapshotLoaderFunc(func(context.Context, *config.Snapshot) (engine.Snapshot, error) {
			return engine.Snapshot{}, nil
		}),
		HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatalf("NewProductionEngineFactory() error = %v", err)
	}
	value := config.Config{Endpoints: map[string]config.EndpointConfig{
		"openai": {Family: "openai_responses", BaseURL: "https://api.openai.com/v1", OutboundHosts: []string{"api.openai.com"}, Auth: config.AuthConfig{Kind: "bearer_env", Name: "OPENAI_KEY"}},
	}}
	snapshot := engine.Snapshot{Routes: routing.Catalog{Models: map[string]routing.Model{
		"model": {Routes: []routing.Route{{EndpointID: "openai", Capabilities: routing.CapabilitySet{Version: "cap-v1"}}}},
	}}}
	adapter, err := factory.buildAdapter(context.Background(), value, snapshot, "openai")
	if err != nil {
		t.Fatalf("buildAdapter() error = %v", err)
	}
	if adapter == nil || adapter.Name() != "openai.responses" {
		t.Fatalf("adapter = %#v, want openai.responses adapter", adapter)
	}
}

func TestProductionFactoryBuildsAnthropicAWSGatewayAdapterWithoutSecretResolution(t *testing.T) {
	resolvedSecret := false
	var constructed anthropicmessages.AWSClientConfig
	factory, err := NewProductionEngineFactory(ProductionFactoryOptions{
		Resolver: secrets.ResolverFunc(func(context.Context, config.SecretRef) ([]byte, error) {
			resolvedSecret = true
			return nil, errors.New("AWS gateway must not resolve a provider secret")
		}),
		SnapshotLoader: SnapshotLoaderFunc(func(context.Context, *config.Snapshot) (engine.Snapshot, error) {
			return engine.Snapshot{}, nil
		}),
		HTTPClient: &http.Client{},
		AnthropicAWSClientFactory: func(ctx context.Context, value anthropicmessages.AWSClientConfig) (*anthropicmessages.Client, error) {
			constructed = value
			value.AWSConfig.SkipAuth = true
			return anthropicmessages.NewAWSClient(ctx, value)
		},
	})
	if err != nil {
		t.Fatalf("NewProductionEngineFactory() error = %v", err)
	}
	value := config.Config{Endpoints: map[string]config.EndpointConfig{
		"anthropic-aws": {
			Family: "anthropic_aws_messages", BaseURL: "https://aws-external-anthropic.us-east-1.api.aws", OutboundHosts: []string{"aws-external-anthropic.us-east-1.api.aws"},
			Region: "us-east-1", AWSWorkspaceID: "ws-example-123", Auth: config.AuthConfig{Kind: "aws_default_chain"},
			ServiceClasses: map[llm.ServiceClass]config.TierConfig{llm.ServiceClassStandard: {ProviderValue: "standard_only"}},
		},
	}}
	snapshot := engine.Snapshot{Routes: routing.Catalog{Models: map[string]routing.Model{
		"model": {Routes: []routing.Route{{EndpointID: "anthropic-aws", Capabilities: routing.CapabilitySet{Version: "cap-v1"}}}},
	}}}
	adapter, err := factory.buildAdapter(context.Background(), value, snapshot, "anthropic-aws")
	if err != nil {
		t.Fatalf("buildAdapter() error = %v", err)
	}
	if adapter == nil || adapter.Name() != "anthropic.messages/anthropic-aws" {
		t.Fatalf("adapter = %#v, want Anthropic AWS messages adapter", adapter)
	}
	if resolvedSecret {
		t.Fatal("AWS gateway resolved a provider secret")
	}
	if constructed.BaseURL != value.Endpoints["anthropic-aws"].BaseURL || constructed.AWSConfig.AWSRegion != "us-east-1" || constructed.AWSConfig.WorkspaceID != "ws-example-123" {
		t.Fatalf("AWS gateway client configuration = %#v", constructed)
	}
	if constructed.AWSConfig.APIKey != "" || constructed.AWSConfig.AWSAccessKey != "" || constructed.AWSConfig.AWSSecretAccessKey != "" || constructed.AWSConfig.AWSSessionToken != "" || constructed.AWSConfig.AWSProfile != "" {
		t.Fatalf("AWS gateway client accepted static credentials: %#v", constructed)
	}
}

func TestProductionFactoryRejectsSecretAuthForAnthropicAWSGateway(t *testing.T) {
	factory := &ProductionEngineFactory{options: ProductionFactoryOptions{HTTPClient: &http.Client{}}}
	value := config.Config{Endpoints: map[string]config.EndpointConfig{
		"anthropic-aws": {Family: "anthropic_aws_messages", BaseURL: "https://aws-external-anthropic.us-east-1.api.aws", OutboundHosts: []string{"aws-external-anthropic.us-east-1.api.aws"}, Region: "us-east-1", AWSWorkspaceID: "ws-example-123", Auth: config.AuthConfig{Kind: "bearer_env", Name: "ANTHROPIC_AWS_API_KEY"}},
	}}
	snapshot := engine.Snapshot{Routes: routing.Catalog{Models: map[string]routing.Model{
		"model": {Routes: []routing.Route{{EndpointID: "anthropic-aws", Capabilities: routing.CapabilitySet{Version: "cap-v1"}}}},
	}}}
	_, err := factory.buildAdapter(context.Background(), value, snapshot, "anthropic-aws")
	if !errors.Is(err, ErrUnsupportedProviderAuth) {
		t.Fatalf("buildAdapter() error = %v, want ErrUnsupportedProviderAuth", err)
	}
}

func TestProductionFactoryRejectsUnknownFamily(t *testing.T) {
	factory := &ProductionEngineFactory{options: ProductionFactoryOptions{HTTPClient: &http.Client{}}}
	value := config.Config{Endpoints: map[string]config.EndpointConfig{
		"unknown": {Family: "provider_future", BaseURL: "https://example.test", OutboundHosts: []string{"example.test"}, Auth: config.AuthConfig{Kind: "bearer_env", Name: "KEY"}},
	}}
	snapshot := engine.Snapshot{Routes: routing.Catalog{Models: map[string]routing.Model{
		"model": {Routes: []routing.Route{{EndpointID: "unknown", Capabilities: routing.CapabilitySet{Version: "cap-v1"}}}},
	}}}
	_, err := factory.buildAdapter(context.Background(), value, snapshot, "unknown")
	if err == nil || !strings.Contains(err.Error(), "unsupported provider family") {
		t.Fatalf("error = %v, want unsupported provider family", err)
	}
}

func TestChatProfileRequiresSpecializedDialect(t *testing.T) {
	factory := &ProductionEngineFactory{}
	endpoint := config.EndpointConfig{Family: "openai_chat", BaseURL: "https://openrouter.ai/api/v1"}
	_, err := factory.chatProfile("openrouter", endpoint, provider.CapabilitySet{Version: "cap-v1"}, EndpointProfile{})
	if err == nil || !strings.Contains(err.Error(), "specialized chat dialect must be explicit") {
		t.Fatalf("error = %v, want explicit dialect failure", err)
	}
}

func TestEndpointFamilyMapsAzureAndBedrock(t *testing.T) {
	if got := endpointFamily("azure_openai_responses"); got != provider.FamilyOpenAIResponses {
		t.Fatalf("Azure family = %q, want %q", got, provider.FamilyOpenAIResponses)
	}
	if got := endpointFamily("azure_openai_chat"); got != provider.FamilyOpenAIChat {
		t.Fatalf("Azure Chat family = %q, want %q", got, provider.FamilyOpenAIChat)
	}
	if got := endpointFamily("bedrock_anthropic_messages"); got != provider.FamilyBedrockMessages {
		t.Fatalf("Bedrock family = %q, want %q", got, provider.FamilyBedrockMessages)
	}
	if got := endpointFamily("anthropic_aws_messages"); got != provider.FamilyAnthropicMessages {
		t.Fatalf("Anthropic AWS family = %q, want %q", got, provider.FamilyAnthropicMessages)
	}
	if !llm.ServiceClassPriority.Valid() {
		t.Fatal("priority service class is unexpectedly invalid")
	}
}

func TestProductionFactoryBuildsAzureOpenAIChatAdapter(t *testing.T) {
	factory, err := NewProductionEngineFactory(ProductionFactoryOptions{
		Resolver: secrets.ResolverFunc(func(_ context.Context, ref config.SecretRef) ([]byte, error) {
			if ref.Kind != config.SecretEnv || ref.Name != "AZURE_OPENAI_API_KEY" {
				t.Fatalf("resolved unexpected secret reference: %#v", ref)
			}
			return []byte("test-key"), nil
		}),
		SnapshotLoader: SnapshotLoaderFunc(func(context.Context, *config.Snapshot) (engine.Snapshot, error) { return engine.Snapshot{}, nil }),
		HTTPClient:     &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	value := azureOpenAIChatConfig(config.AuthConfig{Kind: "header_env", Name: "AZURE_OPENAI_API_KEY"})
	adapter, err := factory.buildAdapter(context.Background(), value, azureOpenAIChatSnapshot(), "azure-chat")
	if err != nil {
		t.Fatal(err)
	}
	if adapter == nil || adapter.Name() != "openai.chat/azure-chat" {
		t.Fatalf("adapter = %#v, want Azure OpenAI Chat adapter", adapter)
	}
	_, err = adapter.Compile(context.Background(), provider.CompileInput{
		Request: llm.Request{OperationKey: "azure-model-pin", Model: "other-deployment", ServiceClass: llm.ServiceClassStandard},
		Query:   provider.CapabilityQuery{EndpointID: "azure-chat", Family: provider.FamilyOpenAIChat, Model: "other-deployment"},
		Strict:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "pinned profile model") {
		t.Fatalf("model pin error = %v", err)
	}
}

func TestProductionFactoryAzureOpenAIChatFailsClosedBeforeSecretResolution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.EndpointConfig)
		want   string
	}{
		{name: "missing API version", mutate: func(endpoint *config.EndpointConfig) { endpoint.Extensions["azure"]["api_version"] = "" }, want: "Azure API version is required"},
		{name: "whitespace API version", mutate: func(endpoint *config.EndpointConfig) { endpoint.Extensions["azure"]["api_version"] = " \t " }, want: "Azure API version is required"},
		{name: "missing deployment", mutate: func(endpoint *config.EndpointConfig) { delete(endpoint.Extensions["azure"], "deployment") }, want: "Azure deployment is required"},
		{name: "non-string deployment", mutate: func(endpoint *config.EndpointConfig) { endpoint.Extensions["azure"]["deployment"] = 7 }, want: "Azure deployment is required"},
		{name: "bearer auth", mutate: func(endpoint *config.EndpointConfig) {
			endpoint.Auth = config.AuthConfig{Kind: "bearer_env", Name: "AZURE_OPENAI_API_KEY"}
		}, want: "provider authentication mode is unsupported"},
		{name: "Azure default credential", mutate: func(endpoint *config.EndpointConfig) {
			endpoint.Auth = config.AuthConfig{Kind: "azure_default_credential"}
		}, want: "provider authentication mode is unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved := false
			factory, err := NewProductionEngineFactory(ProductionFactoryOptions{
				Resolver: secrets.ResolverFunc(func(context.Context, config.SecretRef) ([]byte, error) {
					resolved = true
					return []byte("must-not-resolve"), nil
				}),
				SnapshotLoader: SnapshotLoaderFunc(func(context.Context, *config.Snapshot) (engine.Snapshot, error) { return engine.Snapshot{}, nil }),
				HTTPClient:     &http.Client{},
			})
			if err != nil {
				t.Fatal(err)
			}
			value := azureOpenAIChatConfig(config.AuthConfig{Kind: "header_env", Name: "AZURE_OPENAI_API_KEY"})
			endpoint := value.Endpoints["azure-chat"]
			test.mutate(&endpoint)
			value.Endpoints["azure-chat"] = endpoint
			_, err = factory.buildAdapter(context.Background(), value, azureOpenAIChatSnapshot(), "azure-chat")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildAdapter() error = %v, want %q", err, test.want)
			}
			if resolved {
				t.Fatal("invalid Azure Chat configuration resolved a secret")
			}
		})
	}
}

func azureOpenAIChatConfig(auth config.AuthConfig) config.Config {
	return config.Config{Endpoints: map[string]config.EndpointConfig{
		"azure-chat": {
			Family: "azure_openai_chat", BaseURL: "https://example.openai.azure.com", OutboundHosts: []string{"example.openai.azure.com"}, Auth: auth,
			ServiceClasses: map[llm.ServiceClass]config.TierConfig{llm.ServiceClassStandard: {ProviderValue: "default"}},
			Extensions:     map[string]map[string]any{"azure": {"api_version": "2025-01-01", "deployment": "chat-deployment"}},
		},
	}}
}

func azureOpenAIChatSnapshot() engine.Snapshot {
	return engine.Snapshot{Routes: routing.Catalog{Models: map[string]routing.Model{
		"model": {Routes: []routing.Route{{EndpointID: "azure-chat", Capabilities: routing.CapabilitySet{Version: "azure-chat/v1"}}}},
	}}}
}
