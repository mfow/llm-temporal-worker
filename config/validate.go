package config

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strings"

	"github.com/mfow/llm-temporal-worker/llm"
)

var supportedFamilies = map[string]struct{}{
	"openai_responses":           {},
	"azure_openai_responses":     {},
	"openai_chat":                {},
	"anthropic_messages":         {},
	"anthropic_aws_messages":     {},
	"bedrock_anthropic_messages": {},
}

// Validate checks references, closed enums, safety bounds, and retention
// inequalities. It never resolves secret values or performs network I/O.
func (config Config) Validate() error {
	if config.Version != APIVersion {
		return fmt.Errorf("version must be %q", APIVersion)
	}
	if err := validateIdentifier(config.Environment, "environment"); err != nil {
		return err
	}
	if err := config.Server.validate(); err != nil {
		return err
	}
	if err := config.Temporal.validate(); err != nil {
		return err
	}
	if err := config.State.validate(); err != nil {
		return err
	}
	if err := config.BlobStore.validate(); err != nil {
		return err
	}
	if err := config.Limits.validate(); err != nil {
		return err
	}
	if len(config.Endpoints) == 0 {
		return fmt.Errorf("endpoints must not be empty")
	}
	for _, name := range sortedKeys(config.Endpoints) {
		if err := config.Endpoints[name].validate("endpoints."+name, config.Limits.ProviderTimeout); err != nil {
			return err
		}
	}
	if len(config.Models) == 0 {
		return fmt.Errorf("models must not be empty")
	}
	for _, name := range sortedKeys(config.Models) {
		if err := config.Models[name].validate("models."+name, config.Endpoints); err != nil {
			return err
		}
	}
	if err := config.Capabilities.validate(); err != nil {
		return err
	}
	if err := config.Pricing.validate(); err != nil {
		return err
	}
	if err := config.Budgets.validate(); err != nil {
		return err
	}
	if err := config.Continuation.validate(); err != nil {
		return err
	}
	if err := config.Telemetry.validate(config.Environment); err != nil {
		return err
	}
	return nil
}

func (server ServerConfig) validate() error {
	if err := validateAddress(server.HealthAddress, "server.health_address"); err != nil {
		return err
	}
	if err := validateAddress(server.MetricsAddress, "server.metrics_address"); err != nil {
		return err
	}
	if err := validatePositiveDuration(server.ShutdownTimeout, "server.shutdown_timeout"); err != nil {
		return err
	}
	if err := validatePositiveDuration(server.FinalizationTimeout, "server.finalization_timeout"); err != nil {
		return err
	}
	if err := validatePositiveDuration(server.ReadinessProbeInterval, "server.readiness_probe_interval"); err != nil {
		return err
	}
	if err := validatePositiveDuration(server.ReadinessProbeTimeout, "server.readiness_probe_timeout"); err != nil {
		return err
	}
	if server.ReadinessProbeTimeout > server.ReadinessProbeInterval {
		return fmt.Errorf("server.readiness_probe_timeout must not exceed readiness_probe_interval")
	}
	if server.InlinePayloadBytes <= 0 || server.InlinePayloadBytes > 16<<20 {
		return fmt.Errorf("server.inline_payload_bytes must be between 1 and 16777216")
	}
	return nil
}

func (temporal TemporalConfig) validate() error {
	if strings.TrimSpace(temporal.Target) == "" || strings.ContainsAny(temporal.Target, "\r\n") {
		return fmt.Errorf("temporal.target must be non-empty")
	}
	for name, value := range map[string]string{
		"temporal.namespace":       temporal.Namespace,
		"temporal.task_queue":      temporal.TaskQueue,
		"temporal.identity_prefix": temporal.IdentityPrefix,
	} {
		if err := validateIdentifier(value, name); err != nil {
			return err
		}
	}
	if temporal.TLS.Enabled && temporal.TLS.CAFile == "" {
		return fmt.Errorf("temporal.tls.ca_file is required when TLS is enabled")
	}
	if temporal.TLS.Enabled && temporal.TLS.ServerName == "" {
		return fmt.Errorf("temporal.tls.server_name is required when TLS is enabled")
	}
	if temporal.Worker.MaxConcurrentActivities <= 0 || temporal.Worker.MaxConcurrentActivityTaskPolls <= 0 {
		return fmt.Errorf("temporal.worker concurrency values must be positive")
	}
	return validatePositiveDuration(temporal.Worker.GracefulStopTimeout, "temporal.worker.graceful_stop_timeout")
}

func (state StateConfig) validate() error {
	if state.Kind != "redis" {
		return fmt.Errorf("state.kind %q is unsupported", state.Kind)
	}
	for name, value := range map[string]Duration{
		"state.operation_terminal_retention": state.OperationTerminalRetention,
		"state.ambiguous_retention":          state.AmbiguousRetention,
		"state.continuation_retention":       state.ContinuationRetention,
		"state.reservation_lease":            state.ReservationLease,
	} {
		if err := validatePositiveDuration(value, name); err != nil {
			return err
		}
	}
	if state.OperationTerminalRetention < state.ReservationLease {
		return fmt.Errorf("state.operation_terminal_retention must cover reservation_lease")
	}
	if state.AmbiguousRetention < state.OperationTerminalRetention {
		return fmt.Errorf("state.ambiguous_retention must cover operation_terminal_retention")
	}
	if err := state.Redis.validate(); err != nil {
		return err
	}
	return nil
}

func (redis RedisConfig) validate() error {
	if len(redis.Addresses) == 0 {
		return fmt.Errorf("state.redis.addresses must not be empty")
	}
	for index, address := range redis.Addresses {
		if err := validateAddress(address, fmt.Sprintf("state.redis.addresses[%d]", index)); err != nil {
			return err
		}
	}
	if err := redis.Username.Validate("state.redis.username"); err != nil {
		return err
	}
	if err := redis.Password.Validate("state.redis.password"); err != nil {
		return err
	}
	if redis.AdmissionHashTag == "" || redis.FunctionLibrary == "" || redis.AdmissionVersion == "" {
		return fmt.Errorf("state.redis.admission_hash_tag, function_library, and admission_version are required")
	}
	switch redis.AdmissionMode {
	case "function", "lua":
	default:
		return fmt.Errorf("state.redis.admission_mode %q is unsupported", redis.AdmissionMode)
	}
	if len(redis.AdmissionDigest) != 64 {
		return fmt.Errorf("state.redis.admission_digest must be 64 hex characters")
	}
	if _, err := hex.DecodeString(redis.AdmissionDigest); err != nil {
		return fmt.Errorf("state.redis.admission_digest must be hex")
	}
	if redis.MaxConnections <= 0 || redis.MaxConnections > 100000 {
		return fmt.Errorf("state.redis.max_connections is outside safe bounds")
	}
	if err := validatePositiveDuration(redis.DialTimeout, "state.redis.dial_timeout"); err != nil {
		return err
	}
	if err := validatePositiveDuration(redis.OperationTimeout, "state.redis.operation_timeout"); err != nil {
		return err
	}
	switch redis.RequiredPersistence {
	case "aof_and_rdb", "aof", "rdb":
		return nil
	default:
		return fmt.Errorf("state.redis.required_persistence %q is unsupported", redis.RequiredPersistence)
	}
}

func (blob BlobStoreConfig) validate() error {
	if blob.Kind != "s3" {
		return fmt.Errorf("blob_store.kind %q is unsupported", blob.Kind)
	}
	if blob.InlineBytes <= 0 || blob.InlineBytes > 16<<20 {
		return fmt.Errorf("blob_store.inline_bytes is outside safe bounds")
	}
	if blob.S3.Bucket == "" || blob.S3.Region == "" || blob.S3.Prefix == "" {
		return fmt.Errorf("blob_store.s3 bucket, region, and prefix are required")
	}
	return blob.S3.Auth.Validate("blob_store.s3.auth")
}

func (limits LimitsConfig) validate() error {
	positive := map[string]int{
		"limits.request_bytes":                 limits.RequestBytes,
		"limits.items":                         limits.Items,
		"limits.parts_per_item":                limits.PartsPerItem,
		"limits.tools":                         limits.Tools,
		"limits.schema_bytes":                  limits.SchemaBytes,
		"limits.json_depth":                    limits.JSONDepth,
		"limits.continuation_depth":            limits.ContinuationDepth,
		"limits.route_attempts":                limits.RouteAttempts,
		"limits.max_output_tokens":             limits.MaxOutputTokens,
		"limits.max_budget_buckets_per_window": limits.MaxBudgetBucketsPerWindow,
	}
	for name, value := range positive {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if limits.RequestBytes > 64<<20 || limits.SchemaBytes > limits.RequestBytes {
		return fmt.Errorf("limits request/schema byte bounds are unsafe")
	}
	if err := validatePositiveDuration(limits.ProviderTimeout, "limits.provider_timeout"); err != nil {
		return err
	}
	ratio, ok := new(big.Rat).SetString(limits.TokenEstimateSafetyRatio)
	if !ok || ratio.Sign() <= 0 || ratio.Cmp(big.NewRat(100, 1)) > 0 {
		return fmt.Errorf("limits.token_estimate_safety_ratio must be a finite positive decimal <= 100")
	}
	return nil
}

func (endpoint EndpointConfig) validate(path string, providerTimeout Duration) error {
	if _, ok := supportedFamilies[endpoint.Family]; !ok {
		return fmt.Errorf("%s.family %q is unsupported", path, endpoint.Family)
	}
	baseHost := ""
	var err error
	if endpoint.Family == "bedrock_anthropic_messages" {
		if endpoint.Region == "" {
			return fmt.Errorf("%s.region is required for Bedrock", path)
		}
		baseHost, err = normalizedHTTPSURLHost(endpoint.BaseURL, path+".base_url", true)
		if err != nil {
			return err
		}
	} else if endpoint.Family == "anthropic_aws_messages" {
		if endpoint.Region == "" {
			return fmt.Errorf("%s.region is required for Anthropic AWS gateway", path)
		}
		if err := validateIdentifier(endpoint.Region, path+".region"); err != nil {
			return err
		}
		if endpoint.AWSWorkspaceID == "" {
			return fmt.Errorf("%s.aws_workspace_id is required for Anthropic AWS gateway", path)
		}
		if err := validateIdentifier(endpoint.AWSWorkspaceID, path+".aws_workspace_id"); err != nil {
			return err
		}
		if endpoint.Auth.Kind != "aws_default_chain" {
			return fmt.Errorf("%s.auth.kind must be aws_default_chain for Anthropic AWS gateway", path)
		}
		baseHost, err = normalizedHTTPSURLHost(endpoint.BaseURL, path+".base_url", false)
		if err != nil {
			return err
		}
	} else if baseHost, err = normalizedHTTPSURLHost(endpoint.BaseURL, path+".base_url", false); err != nil {
		return err
	}
	if endpoint.Family != "anthropic_aws_messages" && endpoint.AWSWorkspaceID != "" {
		return fmt.Errorf("%s.aws_workspace_id is only valid for Anthropic AWS gateway endpoints", path)
	}
	if err := endpoint.validateOutboundHosts(path, baseHost); err != nil {
		return err
	}
	if err := endpoint.Auth.Validate(path + ".auth"); err != nil {
		return err
	}
	if endpoint.AccountRegion == "" && endpoint.Region == "" {
		return fmt.Errorf("%s.account_region or region is required", path)
	}
	if err := validatePositiveDuration(endpoint.Timeout, path+".timeout"); err != nil {
		return err
	}
	if endpoint.Timeout > providerTimeout {
		return fmt.Errorf("%s.timeout must not exceed limits.provider_timeout", path)
	}
	if endpoint.CapabilityProfile == "" || endpoint.PriceCatalog == "" {
		return fmt.Errorf("%s capability_profile and price_catalog are required", path)
	}
	if len(endpoint.ServiceClasses) == 0 {
		return fmt.Errorf("%s.service_classes must not be empty", path)
	}
	for class, tier := range endpoint.ServiceClasses {
		if !class.Valid() {
			return fmt.Errorf("%s.service_classes contains unknown public class %q; want economy, standard, or priority", path, class)
		}
		if tier.ProviderValue == "" {
			return fmt.Errorf("%s.service_classes.%s.provider_value is required", path, class)
		}
	}
	return nil
}

func (endpoint EndpointConfig) validateOutboundHosts(path, baseHost string) error {
	if len(endpoint.OutboundHosts) == 0 {
		return fmt.Errorf("%s.outbound_hosts must not be empty", path)
	}
	seen := make(map[string]struct{}, len(endpoint.OutboundHosts))
	baseAllowed := baseHost == ""
	for index, rawHost := range endpoint.OutboundHosts {
		host, err := NormalizeOutboundHost(rawHost)
		if err != nil {
			return fmt.Errorf("%s.outbound_hosts[%d] must be a normalized DNS hostname", path, index)
		}
		if _, duplicate := seen[host]; duplicate {
			return fmt.Errorf("%s.outbound_hosts contains duplicate hostname", path)
		}
		seen[host] = struct{}{}
		if host == baseHost {
			baseAllowed = true
		}
	}
	if !baseAllowed {
		return fmt.Errorf("%s.outbound_hosts must include the base_url hostname", path)
	}
	return nil
}

func (model ModelConfig) validate(path string, endpoints map[string]EndpointConfig) error {
	if len(model.Routes) == 0 {
		return fmt.Errorf("%s.routes must not be empty", path)
	}
	seen := make(map[string]struct{}, len(model.Routes))
	for index, route := range model.Routes {
		routePath := fmt.Sprintf("%s.routes[%d]", path, index)
		if err := validateIdentifier(route.ID, routePath+".id"); err != nil {
			return err
		}
		if _, exists := seen[route.ID]; exists {
			return fmt.Errorf("%s duplicate route ID %q", path, route.ID)
		}
		seen[route.ID] = struct{}{}
		endpoint, exists := endpoints[route.Endpoint]
		if !exists {
			return fmt.Errorf("%s.endpoint %q is not configured", routePath, route.Endpoint)
		}
		if route.Model == "" {
			return fmt.Errorf("%s.model is required", routePath)
		}
		if len(route.Classes) == 0 {
			return fmt.Errorf("%s.classes must not be empty", routePath)
		}
		classSeen := make(map[llm.ServiceClass]struct{}, len(route.Classes))
		for _, class := range route.Classes {
			if !class.Valid() {
				return fmt.Errorf("%s.classes contains unknown public class %q", routePath, class)
			}
			if _, duplicate := classSeen[class]; duplicate {
				return fmt.Errorf("%s.classes repeats %q", routePath, class)
			}
			classSeen[class] = struct{}{}
			if _, supported := endpoint.ServiceClasses[class]; !supported {
				return fmt.Errorf("%s class %q is not mapped by endpoint %q", routePath, class, route.Endpoint)
			}
		}
	}
	return nil
}

func (catalogs CapabilityConfig) validate() error {
	if catalogs.UnknownInStrictMode != "reject" && catalogs.UnknownInStrictMode != "allow" {
		return fmt.Errorf("capabilities.unknown_in_strict_mode must be reject or allow")
	}
	return validateCatalogs(catalogs.Catalogs, "capabilities.catalogs")
}

func (pricing PricingConfig) validate() error {
	if pricing.Currency != "USD" {
		return fmt.Errorf("pricing.currency %q is unsupported", pricing.Currency)
	}
	return validateCatalogs(pricing.Catalogs, "pricing.catalogs")
}

func validateCatalogs(catalogs []CatalogRef, path string) error {
	if len(catalogs) == 0 {
		return fmt.Errorf("%s must not be empty", path)
	}
	for index, catalog := range catalogs {
		if catalog.File == "" || !strings.HasPrefix(catalog.File, "/") {
			return fmt.Errorf("%s[%d].file must be an absolute path", path, index)
		}
		if len(catalog.SHA256) != 64 {
			return fmt.Errorf("%s[%d].sha256 must be 64 hex characters", path, index)
		}
		if _, err := hex.DecodeString(catalog.SHA256); err != nil {
			return fmt.Errorf("%s[%d].sha256 must be hex", path, index)
		}
	}
	return nil
}

func (budgets BudgetsConfig) validate() error {
	seen := make(map[string]struct{}, len(budgets.Policies))
	for index, policy := range budgets.Policies {
		path := fmt.Sprintf("budgets.policies[%d]", index)
		if err := validateIdentifier(policy.ID, path+".id"); err != nil {
			return err
		}
		if _, exists := seen[policy.ID]; exists {
			return fmt.Errorf("%s duplicate policy ID %q", path, policy.ID)
		}
		seen[policy.ID] = struct{}{}
		if policy.Match.Tenant == "" || policy.Match.Environment == "" {
			return fmt.Errorf("%s.match tenant and environment are required", path)
		}
		if len(policy.Windows) == 0 {
			return fmt.Errorf("%s.windows must not be empty", path)
		}
		for windowIndex, window := range policy.Windows {
			windowPath := fmt.Sprintf("%s.windows[%d]", path, windowIndex)
			if err := validatePositiveDuration(window.Duration, windowPath+".duration"); err != nil {
				return err
			}
			if err := validatePositiveDuration(window.Bucket, windowPath+".bucket"); err != nil {
				return err
			}
			if window.Bucket > window.Duration {
				return fmt.Errorf("%s.bucket must not exceed duration", windowPath)
			}
			if window.LimitMicroUSD <= 0 {
				return fmt.Errorf("%s.limit_micro_usd must be positive", windowPath)
			}
		}
	}
	if budgets.RequireMatch && len(budgets.Policies) == 0 {
		return fmt.Errorf("budgets.policies is required when require_match is true")
	}
	return nil
}

func (continuation ContinuationConfig) validate() error {
	if len(continuation.HandleKeys) == 0 {
		return fmt.Errorf("continuation.handle_keys must not be empty")
	}
	primary := 0
	seen := make(map[string]struct{}, len(continuation.HandleKeys))
	for index, key := range continuation.HandleKeys {
		path := fmt.Sprintf("continuation.handle_keys[%d]", index)
		if err := validateIdentifier(key.ID, path+".id"); err != nil {
			return err
		}
		if _, exists := seen[key.ID]; exists {
			return fmt.Errorf("%s duplicate key ID %q", path, key.ID)
		}
		seen[key.ID] = struct{}{}
		if key.Primary {
			primary++
		}
		if err := key.Secret.Validate(path + ".secret"); err != nil {
			return err
		}
	}
	if primary != 1 {
		return fmt.Errorf("continuation.handle_keys must contain exactly one primary key")
	}
	return nil
}

func (telemetry TelemetryConfig) validate(environment string) error {
	if telemetry.Logs.Format != "json" && telemetry.Logs.Format != "text" {
		return fmt.Errorf("telemetry.logs.format must be json or text")
	}
	switch telemetry.Logs.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("telemetry.logs.level is unsupported")
	}
	switch telemetry.ContentLogging {
	case "disabled", "redacted":
	default:
		return fmt.Errorf("telemetry.content_logging must be disabled or redacted")
	}
	if environment == "production" && telemetry.ContentLogging != "disabled" {
		return fmt.Errorf("telemetry.content_logging must be disabled in production")
	}
	if telemetry.Tracing.Enabled {
		if telemetry.Tracing.OTLPEndpoint == "" {
			return fmt.Errorf("telemetry.tracing.otlp_endpoint is required when tracing is enabled")
		}
		ratio, ok := new(big.Rat).SetString(telemetry.Tracing.SampleRatio)
		if !ok || ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) > 0 {
			return fmt.Errorf("telemetry.tracing.sample_ratio must be between 0 and 1")
		}
	}
	return nil
}

func validateAddress(value, path string) error {
	if value == "" {
		return fmt.Errorf("%s must be non-empty host:port", path)
	}
	if _, _, err := net.SplitHostPort(value); err != nil {
		return fmt.Errorf("%s must be host:port: %w", path, err)
	}
	return nil
}
