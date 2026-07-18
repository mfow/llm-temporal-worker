package config

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"
)

const maxConfigBytes = 4 << 20

// Load parses one strict v1 YAML document, applies only documented safe
// defaults, and validates the resulting external configuration.
func Load(data []byte) (Config, error) {
	if len(data) == 0 {
		return Config{}, fmt.Errorf("configuration is empty")
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("configuration exceeds %d bytes", maxConfigBytes)
	}
	var config Config
	if err := yaml.Load(data, &config, yaml.WithV4Defaults(), yaml.WithKnownFields(), yaml.WithUniqueKeys(), yaml.WithSingleDocument()); err != nil {
		return Config{}, fmt.Errorf("configuration YAML: %w", err)
	}
	applyDefaults(&config)
	canonicalize(&config)
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// canonicalize makes equivalent external spellings produce one effective
// configuration. SHA-256 hex values are case-insensitive in YAML input but
// are compared as canonical lowercase strings by runtime readiness checks.
func canonicalize(config *Config) {
	if config == nil {
		return
	}
	config.State.Redis.AdmissionDigest = strings.ToLower(config.State.Redis.AdmissionDigest)
	for name, endpoint := range config.Endpoints {
		for index, rawHost := range endpoint.OutboundHosts {
			if host, err := NormalizeOutboundHost(rawHost); err == nil {
				endpoint.OutboundHosts[index] = host
			}
		}
		config.Endpoints[name] = endpoint
	}
}

// Parse is an explicit alias for Load for callers that use parse/compile
// terminology.
func Parse(data []byte) (Config, error) { return Load(data) }

func applyDefaults(config *Config) {
	if config.Server.ShutdownTimeout == 0 {
		config.Server.ShutdownTimeout = Duration(45 * time.Second)
	}
	if config.Server.FinalizationTimeout == 0 {
		config.Server.FinalizationTimeout = Duration(10 * time.Second)
	}
	if config.Server.InlinePayloadBytes == 0 {
		config.Server.InlinePayloadBytes = 512 << 10
	}
	if config.Temporal.Worker.GracefulStopTimeout == 0 {
		config.Temporal.Worker.GracefulStopTimeout = Duration(30 * time.Second)
	}
	if config.Temporal.Worker.HeartbeatKeepaliveInterval == 0 {
		config.Temporal.Worker.HeartbeatKeepaliveInterval = Duration(time.Second)
	}
	if config.State.Kind == "" {
		config.State.Kind = "redis"
	}
	if config.State.OperationTerminalRetention == 0 {
		config.State.OperationTerminalRetention = Duration(45 * 24 * time.Hour)
	}
	if config.State.AmbiguousRetention == 0 {
		config.State.AmbiguousRetention = Duration(90 * 24 * time.Hour)
	}
	if config.State.ContinuationRetention == 0 {
		config.State.ContinuationRetention = Duration(30 * 24 * time.Hour)
	}
	if config.State.ReservationLease == 0 {
		config.State.ReservationLease = Duration(2 * time.Minute)
	}
	if config.BlobStore.Kind == "" {
		config.BlobStore.Kind = "s3"
	}
	if config.Limits.RequestBytes == 0 {
		config.Limits.RequestBytes = 1 << 20
	}
	if config.Limits.Items == 0 {
		config.Limits.Items = 512
	}
	if config.Limits.PartsPerItem == 0 {
		config.Limits.PartsPerItem = 64
	}
	if config.Limits.Tools == 0 {
		config.Limits.Tools = 128
	}
	if config.Limits.SchemaBytes == 0 {
		config.Limits.SchemaBytes = 256 << 10
	}
	if config.Limits.JSONDepth == 0 {
		config.Limits.JSONDepth = 64
	}
	if config.Limits.ContinuationDepth == 0 {
		config.Limits.ContinuationDepth = 256
	}
	if config.Limits.RouteAttempts == 0 {
		config.Limits.RouteAttempts = 6
	}
	if config.Limits.ProviderTimeout == 0 {
		config.Limits.ProviderTimeout = Duration(120 * time.Second)
	}
	if config.Limits.MaxOutputTokens == 0 {
		config.Limits.MaxOutputTokens = 32768
	}
	if config.Limits.MaxBudgetBucketsPerWindow == 0 {
		config.Limits.MaxBudgetBucketsPerWindow = 2048
	}
	if config.Limits.TokenEstimateSafetyRatio == "" {
		config.Limits.TokenEstimateSafetyRatio = "1.35"
	}
	if config.Capabilities.UnknownInStrictMode == "" {
		config.Capabilities.UnknownInStrictMode = "reject"
	}
	if config.Pricing.Currency == "" {
		config.Pricing.Currency = "USD"
	}
	if config.Telemetry.Logs.Format == "" {
		config.Telemetry.Logs.Format = "json"
	}
	if config.Telemetry.Logs.Level == "" {
		config.Telemetry.Logs.Level = "info"
	}
	if config.Telemetry.ContentLogging == "" {
		config.Telemetry.ContentLogging = "disabled"
	}
	for name, endpoint := range config.Endpoints {
		if endpoint.Timeout == 0 {
			endpoint.Timeout = Duration(115 * time.Second)
			config.Endpoints[name] = endpoint
		}
	}
}

// DecodeYAML is useful to callers that need to inspect the decoded value
// before validation; production loading should use Load.
func DecodeYAML(data []byte, out any) error {
	if len(data) == 0 {
		return fmt.Errorf("YAML document is empty")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("configuration YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("configuration must contain exactly one YAML document")
	} else if err != io.EOF {
		return fmt.Errorf("configuration YAML: %w", err)
	}
	return nil
}
