package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/redis/go-redis/v9"

	"github.com/mfow/llm-temporal-worker/budget"
	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/internal/app"
	"github.com/mfow/llm-temporal-worker/internal/secrets"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/llm/provider/anthropicmessages"
	"github.com/mfow/llm-temporal-worker/llm/provider/bedrockmessages"
	"github.com/mfow/llm-temporal-worker/llm/provider/openaichat"
	"github.com/mfow/llm-temporal-worker/llm/provider/openairesponses"
	"github.com/mfow/llm-temporal-worker/routing"
	"github.com/mfow/llm-temporal-worker/state"
	"github.com/mfow/llm-temporal-worker/storage/blob"
	redisstore "github.com/mfow/llm-temporal-worker/storage/redis"
	"github.com/mfow/llm-temporal-worker/storage/s3blob"
)

var (
	ErrProductionFactoryInvalid = errors.New("production engine factory is invalid")
	ErrUnsupportedProviderAuth  = errors.New("provider authentication mode is unsupported")
	ErrProviderProfileMissing   = errors.New("provider profile is missing")
	ErrDependencyUnavailable    = errors.New("runtime dependency is unavailable")
)

// ChatDialect identifies a protocol-compatible OpenAI Chat endpoint. The
// factory never infers provider-specific wire defaults from a hostname; a
// specialized dialect must be selected explicitly by the profile builder.
type ChatDialect string

const (
	ChatDialectGeneric    ChatDialect = "generic"
	ChatDialectOpenRouter ChatDialect = "openrouter"
	ChatDialectExa        ChatDialect = "exa"
)

// EndpointProfile is the verified capability/service-tier contract attached
// to one endpoint adapter. The pointers are optional because the factory can
// derive safe generic profiles from the compiled route snapshot. Specialized
// OpenRouter/Exa profiles should be supplied explicitly.
type EndpointProfile struct {
	ChatDialect ChatDialect
	Chat        *openaichat.Profile
	Anthropic   *anthropicmessages.Profile
	Bedrock     *bedrockmessages.Profile
}

type RedisFactory func(context.Context, config.RedisConfig, string, string) (redis.UniversalClient, error)
type BlobFactory func(context.Context, config.Config) (blob.Store, io.Closer, error)
type AWSConfigFactory func(context.Context, string) (aws.Config, error)
type AzureCredentialFactory func(context.Context, config.EndpointConfig) (azcore.TokenCredential, error)

// ProductionFactoryOptions provides explicit seams for tests and deployment
// composition. If a client/factory is omitted, the official SDK defaults are
// used only for the corresponding documented authentication mode; unsupported
// modes fail closed with ErrUnsupportedProviderAuth.
type ProductionFactoryOptions struct {
	Resolver       secrets.Resolver
	SnapshotLoader SnapshotLoader
	Profiles       map[string]EndpointProfile
	HTTPClient     *http.Client
	Clock          func() time.Time
	Planner        routing.Planner

	RedisKeySecret  []byte
	RedisClient     redis.UniversalClient
	RedisFactory    RedisFactory
	BlobStore       blob.Store
	BlobFactory     BlobFactory
	BlobRefResolver BlobRefResolver

	AWSConfigFactory       AWSConfigFactory
	AzureCredentialFactory AzureCredentialFactory
	AzureAPIVersions       map[string]string
}

// ProductionEngineFactory composes the full provider-neutral engine from one
// immutable config snapshot. It owns the SDK/state clients returned in the
// ClientSet and never places resolved secrets into the engine snapshot.
type ProductionEngineFactory struct {
	options ProductionFactoryOptions
}

func NewProductionEngineFactory(options ProductionFactoryOptions) (*ProductionEngineFactory, error) {
	if options.SnapshotLoader == nil {
		return nil, fmt.Errorf("%w: snapshot loader is required", ErrProductionFactoryInvalid)
	}
	if options.Resolver == nil {
		return nil, fmt.Errorf("%w: secret resolver is required", ErrProductionFactoryInvalid)
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{}
	}
	if options.RedisClient == nil && options.RedisFactory == nil {
		options.RedisFactory = defaultRedisFactory
	}
	if options.BlobStore == nil && options.BlobFactory == nil {
		options.BlobFactory = defaultBlobFactory
	}
	if options.AWSConfigFactory == nil {
		options.AWSConfigFactory = defaultAWSConfigFactory
	}
	if options.AzureCredentialFactory == nil {
		options.AzureCredentialFactory = defaultAzureCredentialFactory
	}
	return &ProductionEngineFactory{options: options}, nil
}

var _ EngineFactory = (*ProductionEngineFactory)(nil)

func (factory *ProductionEngineFactory) Build(ctx context.Context, snapshot *config.Snapshot) (llm.Engine, app.ClientSet, error) {
	if factory == nil {
		return nil, nil, fmt.Errorf("%w: factory is nil", ErrProductionFactoryInvalid)
	}
	if snapshot == nil {
		return nil, nil, fmt.Errorf("%w: configuration snapshot is required", ErrProductionFactoryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	value := snapshot.Config()
	engineSnapshot, err := factory.options.SnapshotLoader.Load(ctx, snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("load engine snapshot: %w", err)
	}
	adapters, err := factory.buildAdapters(ctx, value, engineSnapshot)
	if err != nil {
		return nil, nil, err
	}
	redisClient, redisOwned, err := factory.buildRedis(ctx, value)
	if err != nil {
		return nil, nil, err
	}
	closeOwned := func() {
		if redisOwned && redisClient != nil {
			_ = redisClient.Close()
		}
	}
	keySecret, err := factory.redisKeySecret(ctx, value)
	if err != nil {
		closeOwned()
		return nil, nil, err
	}
	clock := factory.options.Clock
	admissionStore, err := redisstore.NewAdmissionStore(redisstore.AdmissionOptions{Client: redisClient, Keys: redisstore.KeyOptions{Prefix: "llmtw", HashTag: value.State.Redis.AdmissionHashTag, KeySecret: keySecret}, Clock: clock, MaxRecordBytes: value.Limits.RequestBytes})
	if err != nil {
		closeOwned()
		return nil, nil, fmt.Errorf("construct Redis admission store: %w", err)
	}
	keyring, err := factory.continuationKeyring(ctx, value)
	if err != nil {
		closeOwned()
		return nil, nil, err
	}
	continuationStore, err := redisstore.NewContinuationStore(redisstore.ContinuationOptions{Client: redisClient, Keys: redisstore.KeyOptions{Prefix: "llmtw", HashTag: value.State.Redis.AdmissionHashTag, KeySecret: keySecret}, Keyring: keyring, Clock: clock, MaxBytes: value.Limits.RequestBytes, MaxDepth: value.Limits.ContinuationDepth})
	if err != nil {
		closeOwned()
		return nil, nil, fmt.Errorf("construct Redis continuation store: %w", err)
	}
	blobStore, blobCloser, err := factory.buildBlob(ctx, value)
	if err != nil {
		closeOwned()
		return nil, nil, err
	}
	closeAll := func() {
		if blobCloser != nil {
			_ = blobCloser.Close()
		}
		closeOwned()
	}
	refResolver := factory.options.BlobRefResolver
	if refResolver == nil && value.BlobStore.Kind == "s3" {
		refResolver, err = NewContentAddressedBlobRefResolver("s3", value.BlobStore.S3.Prefix)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("construct blob reference resolver: %w", err)
		}
	}
	results, err := NewBlobResultStore(blobStore, admissionStore, refResolver, clock)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	estimator, err := buildEstimator(value)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	planner := factory.options.Planner
	if planner == nil {
		planner = routing.DeterministicPlanner{MaxRejections: value.Limits.RouteAttempts * 64}
	}
	engineValue, err := engine.New(engine.Dependencies{
		Snapshots: engine.StaticSnapshot{Value: engineSnapshot}, Planner: planner, Adapters: engine.AdapterMap(adapters), Admission: admissionStore, Continuations: continuationStore, Results: results,
		Clock: clock, Estimator: estimator, MaxAttempts: value.Limits.RouteAttempts, FinalizationTimeout: time.Duration(value.Server.FinalizationTimeout),
	})
	if err != nil {
		closeAll()
		return nil, nil, fmt.Errorf("construct engine: %w", err)
	}
	return engineValue, app.ClientSetFunc(func(closeContext context.Context) error {
		if closeContext == nil {
			closeContext = context.Background()
		}
		closeAll()
		return nil
	}), nil
}

func buildEstimator(value config.Config) (budget.Estimator, error) {
	ratio, ok := new(big.Rat).SetString(value.Limits.TokenEstimateSafetyRatio)
	if !ok || ratio.Sign() <= 0 {
		return budget.Estimator{}, fmt.Errorf("invalid token estimate safety ratio")
	}
	return budget.Estimator{SafetyRatio: ratio, MaxOutput: int64(value.Limits.MaxOutputTokens)}, nil
}

func (factory *ProductionEngineFactory) buildRedis(ctx context.Context, value config.Config) (redis.UniversalClient, bool, error) {
	if factory.options.RedisClient != nil {
		return factory.options.RedisClient, false, nil
	}
	if factory.options.RedisFactory == nil {
		return nil, false, fmt.Errorf("%w: Redis factory is unavailable", ErrDependencyUnavailable)
	}
	username, err := factory.resolveAuthSecret(ctx, config.AuthConfig{Kind: "bearer_env", Name: ""}, value.State.Redis.Username)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Redis username: %w", err)
	}
	password, err := factory.resolveAuthSecret(ctx, config.AuthConfig{Kind: "bearer_env", Name: ""}, value.State.Redis.Password)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Redis password: %w", err)
	}
	client, err := factory.options.RedisFactory(ctx, value.State.Redis, string(username), string(password))
	if err != nil {
		return nil, false, fmt.Errorf("construct Redis client: %w", err)
	}
	if client == nil {
		return nil, false, fmt.Errorf("%w: Redis factory returned nil client", ErrDependencyUnavailable)
	}
	return client, true, nil
}

func (factory *ProductionEngineFactory) redisKeySecret(ctx context.Context, value config.Config) ([]byte, error) {
	if len(factory.options.RedisKeySecret) >= 32 {
		return append([]byte(nil), factory.options.RedisKeySecret...), nil
	}
	password, err := factory.options.Resolver.Resolve(ctx, value.State.Redis.Password)
	if err != nil {
		return nil, fmt.Errorf("resolve Redis key secret: %w", err)
	}
	if len(password) == 0 {
		return nil, fmt.Errorf("%w: Redis key secret is empty", ErrDependencyUnavailable)
	}
	digest := sha256.Sum256(append([]byte("llmtw:redis-key-v1:"), password...))
	return digest[:], nil
}

func (factory *ProductionEngineFactory) continuationKeyring(ctx context.Context, value config.Config) (*state.Keyring, error) {
	keys := make([]state.Key, 0, len(value.Continuation.HandleKeys))
	for _, key := range value.Continuation.HandleKeys {
		secret, err := factory.options.Resolver.Resolve(ctx, key.Secret)
		if err != nil {
			return nil, fmt.Errorf("resolve continuation key %q: %w", key.ID, err)
		}
		keys = append(keys, state.Key{ID: key.ID, Secret: secret, Primary: key.Primary})
	}
	keyring, err := state.NewKeyring(keys, nil)
	if err != nil {
		return nil, fmt.Errorf("construct continuation keyring: %w", err)
	}
	return keyring, nil
}

func (factory *ProductionEngineFactory) buildBlob(ctx context.Context, value config.Config) (blob.Store, io.Closer, error) {
	if factory.options.BlobStore != nil {
		return factory.options.BlobStore, nil, nil
	}
	if factory.options.BlobFactory == nil {
		return nil, nil, fmt.Errorf("%w: blob factory is unavailable", ErrDependencyUnavailable)
	}
	store, closer, err := factory.options.BlobFactory(ctx, value)
	if err != nil {
		return nil, nil, fmt.Errorf("construct blob store: %w", err)
	}
	if store == nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, nil, fmt.Errorf("%w: blob factory returned nil store", ErrDependencyUnavailable)
	}
	return store, closer, nil
}

func (factory *ProductionEngineFactory) buildAdapters(ctx context.Context, value config.Config, snapshot engine.Snapshot) (map[string]provider.Adapter, error) {
	ids := make([]string, 0, len(value.Endpoints))
	for id := range value.Endpoints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	adapters := make(map[string]provider.Adapter, len(ids))
	for _, endpointID := range ids {
		adapter, err := factory.buildAdapter(ctx, value, snapshot, endpointID)
		if err != nil {
			return nil, err
		}
		adapters[endpointID] = adapter
	}
	return adapters, nil
}

func (factory *ProductionEngineFactory) buildAdapter(ctx context.Context, value config.Config, snapshot engine.Snapshot, endpointID string) (provider.Adapter, error) {
	endpoint := value.Endpoints[endpointID]
	capabilities, err := endpointCapabilities(snapshot, endpointID)
	if err != nil {
		return nil, err
	}
	client := endpointHTTPClient(factory.options.HTTPClient, time.Duration(endpoint.Timeout))
	profile := factory.options.Profiles[endpointID]
	switch endpoint.Family {
	case "openai_responses":
		key, err := factory.providerSecret(ctx, endpoint.Auth, endpointID)
		if err != nil {
			return nil, err
		}
		openaiClient, err := openairesponses.NewClient(openairesponses.ClientConfig{BaseURL: endpoint.BaseURL, APIKey: string(key), HTTPClient: client})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return openairesponses.NewAdapter(openaiClient, endpointID, capabilities.Version)
	case "azure_openai_responses":
		apiVersion := factory.azureAPIVersion(endpointID, endpoint)
		if apiVersion == "" {
			return nil, fmt.Errorf("endpoint %q: Azure API version is required", endpointID)
		}
		switch endpoint.Auth.Kind {
		case "azure_default_credential":
			credential, err := factory.options.AzureCredentialFactory(ctx, endpoint)
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: create Azure credential: %w", endpointID, err)
			}
			azureClient, err := openairesponses.NewAzureTokenClient(openairesponses.AzureTokenClientConfig{Endpoint: endpoint.BaseURL, APIVersion: apiVersion, TokenCredential: credential, HTTPClient: client})
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
			}
			return openairesponses.NewAdapter(azureClient, endpointID, capabilities.Version)
		case "bearer_env", "header_env":
			key, err := factory.providerSecret(ctx, endpoint.Auth, endpointID)
			if err != nil {
				return nil, err
			}
			azureClient, err := openairesponses.NewAzureClient(openairesponses.AzureClientConfig{Endpoint: endpoint.BaseURL, APIVersion: apiVersion, APIKey: string(key), HTTPClient: client})
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
			}
			return openairesponses.NewAdapter(azureClient, endpointID, capabilities.Version)
		default:
			return nil, factory.unsupportedAuth(endpointID, endpoint.Auth.Kind)
		}
	case "openai_chat":
		chatProfile, err := factory.chatProfile(endpointID, endpoint, capabilities, profile)
		if err != nil {
			return nil, err
		}
		key, err := factory.providerSecret(ctx, endpoint.Auth, endpointID)
		if err != nil {
			return nil, err
		}
		switch profile.ChatDialect {
		case ChatDialectOpenRouter:
			openrouterClient, err := openaichat.NewOpenRouterClient(openaichat.OpenRouterClientConfig{BaseURL: endpoint.BaseURL, APIKey: string(key), HTTPClient: client})
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
			}
			return openaichat.NewAdapter(openrouterClient, endpointID, *chatProfile)
		case ChatDialectExa:
			exaClient, err := openaichat.NewExaClient(openaichat.ExaClientConfig{BaseURL: endpoint.BaseURL, APIKey: string(key), HTTPClient: client})
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
			}
			return openaichat.NewAdapter(exaClient, endpointID, *chatProfile)
		default:
			chatClient, err := openaichat.NewClient(openaichat.ClientConfig{BaseURL: endpoint.BaseURL, APIKey: string(key), HTTPClient: client})
			if err != nil {
				return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
			}
			return openaichat.NewAdapter(chatClient, endpointID, *chatProfile)
		}
	case "anthropic_messages":
		anthropicProfile, err := factory.anthropicProfile(endpointID, endpoint, capabilities, profile)
		if err != nil {
			return nil, err
		}
		key, err := factory.providerSecret(ctx, endpoint.Auth, endpointID)
		if err != nil {
			return nil, err
		}
		anthropicClient, err := anthropicmessages.NewClient(anthropicmessages.ClientConfig{BaseURL: endpoint.BaseURL, APIKey: string(key), HTTPClient: client})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return anthropicmessages.NewAdapter(anthropicClient, endpointID, *anthropicProfile)
	case "bedrock_anthropic_messages":
		if endpoint.Auth.Kind != "aws_default_chain" {
			return nil, factory.unsupportedAuth(endpointID, endpoint.Auth.Kind)
		}
		bedrockProfile, err := factory.bedrockProfile(endpointID, endpoint, capabilities, profile)
		if err != nil {
			return nil, err
		}
		awsValue, err := factory.options.AWSConfigFactory(ctx, endpoint.Region)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: create AWS config: %w", endpointID, err)
		}
		bedrockClient, err := bedrockmessages.NewClient(ctx, bedrockmessages.ClientConfig{BaseURL: endpoint.BaseURL, HTTPClient: client, AWSConfig: awsValue})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return bedrockmessages.NewAdapter(bedrockClient, endpointID, *bedrockProfile)
	default:
		return nil, fmt.Errorf("endpoint %q: unsupported provider family %q", endpointID, endpoint.Family)
	}
}

func endpointHTTPClient(base *http.Client, timeout time.Duration) *http.Client {
	copy := *base
	if timeout > 0 {
		copy.Timeout = timeout
	}
	return &copy
}

func endpointCapabilities(snapshot engine.Snapshot, endpointID string) (provider.CapabilitySet, error) {
	found := provider.CapabilitySet{Features: make(map[provider.Feature]provider.Capability)}
	for _, model := range snapshot.Routes.Models {
		for _, route := range model.Routes {
			if route.EndpointID != endpointID {
				continue
			}
			capabilities := providerCapabilities(route.Capabilities)
			if found.Version == "" {
				found.Version = capabilities.Version
				found.Features = capabilities.Features
				continue
			}
			if found.Version != capabilities.Version {
				return provider.CapabilitySet{}, fmt.Errorf("endpoint %q has conflicting capability versions", endpointID)
			}
		}
	}
	if found.Version == "" {
		return provider.CapabilitySet{}, fmt.Errorf("endpoint %q has no route capability profile", endpointID)
	}
	return completeCapabilities(found), nil
}

func providerCapabilities(value routing.CapabilitySet) provider.CapabilitySet {
	result := provider.CapabilitySet{Version: value.Version, Features: make(map[provider.Feature]provider.Capability)}
	for source, target := range map[routing.Feature]provider.Feature{
		routing.FeatureText:             provider.FeatureText,
		routing.FeatureToolCall:         provider.FeatureToolCall,
		routing.FeatureStructuredOutput: provider.FeatureStructuredOutput,
		routing.FeatureReasoning:        provider.FeatureReasoning,
		routing.FeatureContinuation:     provider.FeatureContinuation,
	} {
		if capability, ok := value.Features[source]; ok {
			result.Features[target] = provider.Capability{State: provider.CapabilityState(capability.State), Transform: capability.Transform, Reason: capability.Reason}
		}
	}
	return result
}

func completeCapabilities(value provider.CapabilitySet) provider.CapabilitySet {
	copy := provider.CapabilitySet{Version: value.Version, Features: make(map[provider.Feature]provider.Capability, len(allProviderFeatures))}
	for _, feature := range allProviderFeatures {
		copy.Features[feature] = provider.Capability{State: provider.CapabilityUnknown, Reason: "catalog did not declare this capability"}
	}
	for feature, capability := range value.Features {
		copy.Features[feature] = capability
	}
	return copy
}

var allProviderFeatures = []provider.Feature{provider.FeatureText, provider.FeatureImage, provider.FeatureDocument, provider.FeatureToolCall, provider.FeatureStructuredOutput, provider.FeatureReasoning, provider.FeatureContinuation, provider.FeatureStreaming, provider.FeatureUsage}

func (factory *ProductionEngineFactory) chatProfile(endpointID string, endpoint config.EndpointConfig, capabilities provider.CapabilitySet, supplied EndpointProfile) (*openaichat.Profile, error) {
	if supplied.Chat != nil {
		copy := *supplied.Chat
		return &copy, nil
	}
	tiers, actual := endpointTiers(endpoint)
	allowed := extensionSpecs(endpoint)
	base := strings.TrimRight(endpoint.BaseURL, "/")
	dialect := supplied.ChatDialect
	if dialect == "" {
		_, openRouterExtension := endpoint.Extensions["openrouter"]
		_, exaExtension := endpoint.Extensions["exa"]
		switch {
		case openRouterExtension && exaExtension:
			return nil, fmt.Errorf("endpoint %q: chat dialect markers are ambiguous", endpointID)
		case openRouterExtension:
			dialect = ChatDialectOpenRouter
		case exaExtension:
			dialect = ChatDialectExa
		case base == "https://openrouter.ai/api/v1" || base == "https://api.exa.ai":
			return nil, fmt.Errorf("endpoint %q: specialized chat dialect must be explicit", endpointID)
		default:
			dialect = ChatDialectGeneric
		}
	}
	switch dialect {
	case ChatDialectOpenRouter:
		order, err := stringSlice(extensionValue(endpoint, "openrouter", "provider_order"))
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		value, err := openaichat.NewOpenRouterProfile(openaichat.OpenRouterProfileConfig{ID: endpointID, CapabilityVersion: capabilities.Version, BaseURL: endpoint.BaseURL, Capabilities: capabilities, ServiceTiers: tiers, ActualServiceClasses: actual, ProviderOrder: order, AllowFallbacks: false, RequireParameters: true})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return &value, nil
	case ChatDialectExa:
		value, err := openaichat.NewExaProfile(openaichat.ExaProfileConfig{ID: endpointID, CapabilityVersion: capabilities.Version, BaseURL: endpoint.BaseURL, Capabilities: capabilities, ServiceTiers: tiers, ActualServiceClasses: actual})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return &value, nil
	case ChatDialectGeneric:
		value, err := openaichat.NewProfile(openaichat.Profile{ID: endpointID, CapabilityVersion: capabilities.Version, Capabilities: capabilities, ServiceTiers: tiers, ActualServiceClasses: actual, AllowedExtensions: allowed, ExpectedBaseURL: endpoint.BaseURL})
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
		}
		return &value, nil
	default:
		return nil, fmt.Errorf("endpoint %q: unsupported chat dialect %q", endpointID, dialect)
	}
}

func (factory *ProductionEngineFactory) anthropicProfile(endpointID string, endpoint config.EndpointConfig, capabilities provider.CapabilitySet, supplied EndpointProfile) (*anthropicmessages.Profile, error) {
	if supplied.Anthropic != nil {
		copy := *supplied.Anthropic
		return &copy, nil
	}
	tiers, actual := endpointTiers(endpoint)
	priority := endpoint.ServiceClasses[llm.ServiceClassPriority]
	value, err := anthropicmessages.NewProfile(anthropicmessages.Profile{ID: endpointID, CapabilityVersion: capabilities.Version, Capabilities: capabilities, ServiceTiers: tiers, ActualServiceClasses: actual, AllowedExtensions: anthropicExtensionSpecs(endpoint), ExpectedBaseURL: endpoint.BaseURL, PriorityCapacity: priority.ProviderValue == "auto" || priority.RequiresCapability != ""})
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
	}
	return &value, nil
}

func (factory *ProductionEngineFactory) bedrockProfile(endpointID string, endpoint config.EndpointConfig, capabilities provider.CapabilitySet, supplied EndpointProfile) (*bedrockmessages.Profile, error) {
	if supplied.Bedrock != nil {
		copy := *supplied.Bedrock
		return &copy, nil
	}
	tiers, actual := endpointTiers(endpoint)
	value, err := bedrockmessages.NewProfile(bedrockmessages.Profile{ID: endpointID, CapabilityVersion: capabilities.Version, Capabilities: capabilities, ServiceTiers: tiers, ActualServiceClasses: actual, ExpectedBaseURL: endpoint.BaseURL})
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", endpointID, err)
	}
	return &value, nil
}

func endpointTiers(endpoint config.EndpointConfig) (map[llm.ServiceClass]string, map[string]llm.ServiceClass) {
	classes := map[llm.ServiceClass]string{llm.ServiceClassEconomy: "", llm.ServiceClassStandard: "", llm.ServiceClassPriority: ""}
	actual := make(map[string]llm.ServiceClass)
	for class := range classes {
		if value, ok := endpoint.ServiceClasses[class]; ok {
			classes[class] = value.ProviderValue
			if value.ProviderValue != "" {
				actual[value.ProviderValue] = class
			}
		}
	}
	return classes, actual
}

func extensionSpecs(endpoint config.EndpointConfig) map[string]openaichat.ExtensionSpec {
	result := make(map[string]openaichat.ExtensionSpec, len(endpoint.Extensions))
	for namespace, fields := range endpoint.Extensions {
		mapped := make(map[string]string, len(fields))
		for field := range fields {
			mapped[field] = field
		}
		result[namespace] = openaichat.ExtensionSpec{Fields: mapped}
	}
	return result
}

func anthropicExtensionSpecs(endpoint config.EndpointConfig) map[string]anthropicmessages.ExtensionSpec {
	result := make(map[string]anthropicmessages.ExtensionSpec, len(endpoint.Extensions))
	for namespace, fields := range endpoint.Extensions {
		mapped := make(map[string]string, len(fields))
		for field := range fields {
			mapped[field] = field
		}
		result[namespace] = anthropicmessages.ExtensionSpec{Fields: mapped}
	}
	return result
}

func extensionValue(endpoint config.EndpointConfig, namespace, field string) any {
	if values, ok := endpoint.Extensions[namespace]; ok {
		return values[field]
	}
	return nil
}

func stringSlice(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		if stringsValue, ok := value.([]string); ok {
			return append([]string(nil), stringsValue...), nil
		}
		return nil, fmt.Errorf("extension provider_order must be an array of strings")
	}
	result := make([]string, len(values))
	for index, item := range values {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("extension provider_order entry %d must be a non-empty string", index)
		}
		result[index] = text
	}
	return result, nil
}

func (factory *ProductionEngineFactory) providerSecret(ctx context.Context, auth config.AuthConfig, endpointID string) ([]byte, error) {
	switch auth.Kind {
	case "bearer_env", "header_env":
		return factory.options.Resolver.Resolve(ctx, config.SecretRef{Kind: config.SecretEnv, Name: auth.Name})
	default:
		return nil, factory.unsupportedAuth(endpointID, auth.Kind)
	}
}

func (factory *ProductionEngineFactory) resolveAuthSecret(ctx context.Context, _ config.AuthConfig, ref config.SecretRef) ([]byte, error) {
	return factory.options.Resolver.Resolve(ctx, ref)
}

func (factory *ProductionEngineFactory) unsupportedAuth(endpointID, kind string) error {
	return fmt.Errorf("endpoint %q: %w: %q", endpointID, ErrUnsupportedProviderAuth, kind)
}

func (factory *ProductionEngineFactory) azureAPIVersion(endpointID string, endpoint config.EndpointConfig) string {
	if value := factory.options.AzureAPIVersions[endpointID]; strings.TrimSpace(value) != "" {
		return value
	}
	if value, ok := endpoint.Extensions["azure"]["api_version"].(string); ok {
		return value
	}
	return ""
}

func defaultRedisFactory(_ context.Context, value config.RedisConfig, username, password string) (redis.UniversalClient, error) {
	tlsConfig, err := loadRedisTLS(value.TLS)
	if err != nil {
		return nil, err
	}
	return redis.NewUniversalClient(&redis.UniversalOptions{Addrs: append([]string(nil), value.Addresses...), Username: username, Password: password, DialTimeout: time.Duration(value.DialTimeout), ReadTimeout: time.Duration(value.OperationTimeout), WriteTimeout: time.Duration(value.OperationTimeout), PoolSize: value.MaxConnections, MaxRetries: 0, TLSConfig: tlsConfig}), nil
}

func defaultBlobFactory(ctx context.Context, value config.Config) (blob.Store, io.Closer, error) {
	if value.BlobStore.S3.Auth.Kind != "aws_default_chain" {
		return nil, nil, fmt.Errorf("%w: S3 auth %q", ErrUnsupportedProviderAuth, value.BlobStore.S3.Auth.Kind)
	}
	awsValue, err := defaultAWSConfigFactory(ctx, value.BlobStore.S3.Region)
	if err != nil {
		return nil, nil, err
	}
	client := s3.NewFromConfig(awsValue)
	store, err := s3blob.New(s3blob.Options{Client: client, Bucket: value.BlobStore.S3.Bucket, Prefix: value.BlobStore.S3.Prefix, MaxBytes: int64(value.Limits.RequestBytes)})
	if err != nil {
		return nil, nil, err
	}
	return store, nil, nil
}

func defaultAWSConfigFactory(ctx context.Context, region string) (aws.Config, error) {
	value, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return aws.Config{}, err
	}
	return value, nil
}

func defaultAzureCredentialFactory(ctx context.Context, _ config.EndpointConfig) (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
}

func loadRedisTLS(value config.TLSConfig) (*tls.Config, error) {
	if !value.Enabled {
		return nil, nil
	}
	if value.CAFile == "" {
		return nil, fmt.Errorf("Redis TLS CA file is required")
	}
	data, err := os.ReadFile(value.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read Redis TLS CA certificate")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("Redis TLS CA certificate is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: value.ServerName, RootCAs: pool}, nil
}
