# Configuration Reference

## Format and loading

The worker reads one strict YAML document. Unknown fields, duplicate keys,
unknown enum values, unresolved references, and integer overflow are errors.
The command-line binary selects the document with the `--config` flag; the
default path is `/etc/llmtw/config.yaml`. It does not use a process-wide
environment-variable override for the configuration path or command mode. See
the [command-line reference](cli.md) for the validation and startup commands.

Secret values are referenced with typed `env`, `file`, or workload-identity
configuration. Those references are resolved by the production worker during
runtime construction, while `validate-config` and
`print-effective-config` only parse and canonicalize the non-secret document.
The effective non-secret configuration is canonicalized and hashed as
`config_version`.

## Complete shape

This example shows the v1 fields. Names and model identifiers are illustrative;
they are not claims that a particular deployment currently supports a feature.

```yaml
version: llm-temporal-worker/v1
environment: production

server:
  health_address: 0.0.0.0:8080
  metrics_address: 0.0.0.0:9090
  shutdown_timeout: 45s
  finalization_timeout: 10s
  readiness_probe_interval: 5s
  readiness_probe_timeout: 2s
  inline_payload_bytes: 524288

temporal:
  target: temporal.example.internal:7233
  namespace: production
  task_queue: llm-inference
  identity_prefix: llmtw
  tls:
    enabled: true
    server_name: temporal.example.internal
    ca_file: /var/run/ca/temporal.pem
  worker:
    max_concurrent_activities: 64
    max_concurrent_activity_task_polls: 8
    graceful_stop_timeout: 30s

state:
  kind: redis
  operation_terminal_retention: 45d
  ambiguous_retention: 90d
  continuation_retention: 30d
  reservation_lease: 2m
  redis:
    addresses: [redis.example.internal:6379]
    username:
      kind: env
      name: REDIS_USERNAME
    password:
      kind: file
      path: /var/run/secrets/redis-password
    tls:
      enabled: true
      server_name: redis.example.internal
      ca_file: /var/run/ca/redis.pem
    admission_hash_tag: admission
    admission_mode: function
    function_library: llmtw_admission_v1
    admission_version: admission_v1
    admission_digest: c09e24d73750bebee4aad8cd9b1f05abaa22001528cef0ff6842f2241bb8c20b
    max_connections: 96
    dial_timeout: 2s
    operation_timeout: 3s
    required_persistence: aof_and_rdb

blob_store:
  kind: s3
  inline_bytes: 262144
  s3:
    bucket: acme-llmtw-production
    region: ap-southeast-2
    prefix: v1
    auth:
      kind: aws_default_chain

limits:
  request_bytes: 1048576
  items: 512
  parts_per_item: 64
  tools: 128
  schema_bytes: 262144
  json_depth: 64
  continuation_depth: 256
  route_attempts: 6
  provider_timeout: 120s
  max_output_tokens: 32768
  max_budget_buckets_per_window: 2048
  token_estimate_safety_ratio: "1.35"

endpoints:
  openai-prod:
    family: openai_responses
    base_url: https://api.openai.com/v1
    outbound_hosts: [api.openai.com]
    auth:
      kind: bearer_env
      name: OPENAI_API_KEY
    account_region: global
    timeout: 115s
    service_classes:
      economy:
        provider_value: flex
      standard:
        provider_value: default
      priority:
        provider_value: priority
    capability_profile: openai-responses-prod-v3
    price_catalog: catalog-2026-07-13
    provider_storage:
      permitted: false

  azure-openai-au:
    family: azure_openai_responses
    base_url: https://example.openai.azure.com/openai/v1
    outbound_hosts: [example.openai.azure.com]
    auth:
      kind: azure_default_credential
    account_region: australiaeast
    timeout: 115s
    service_classes:
      standard:
        provider_value: default
      priority:
        provider_value: priority
    capability_profile: azure-responses-au-v2
    price_catalog: catalog-2026-07-13
    extensions:
      azure:
        # Azure API versions are deployment-specific and must be declared.
        api_version: 2024-10-21

  openrouter-pinned:
    family: openai_chat
    base_url: https://openrouter.ai/api/v1
    outbound_hosts: [openrouter.ai]
    auth:
      kind: bearer_env
      name: OPENROUTER_API_KEY
    account_region: global
    timeout: 115s
    service_classes:
      standard:
        provider_value: standard
    capability_profile: openrouter-chat-pinned-v2
    price_catalog: catalog-2026-07-13
    extensions:
      openrouter:
        provider_order: [ProviderA]
        allow_fallbacks: false
        require_parameters: true

  exa-answer:
    family: openai_chat
    base_url: https://api.exa.ai
    outbound_hosts: [api.exa.ai]
    auth:
      kind: bearer_env
      name: EXA_API_KEY
    account_region: global
    timeout: 115s
    service_classes:
      standard:
        provider_value: standard
    capability_profile: exa-chat-v1
    price_catalog: catalog-2026-07-13
    extensions:
      # The marker selects Exa's profile and wire contract explicitly.
      exa: {}

  anthropic-direct:
    family: anthropic_messages
    base_url: https://api.anthropic.com
    outbound_hosts: [api.anthropic.com]
    auth:
      kind: header_env
      name: ANTHROPIC_API_KEY
    account_region: global
    timeout: 115s
    service_classes:
      standard:
        provider_value: standard_only
      priority:
        provider_value: auto
        requires_capability: priority_capacity
    capability_profile: anthropic-messages-v4
    price_catalog: catalog-2026-07-13

  bedrock-us-east-1:
    family: bedrock_anthropic_messages
    outbound_hosts: [bedrock-runtime.us-east-1.amazonaws.com]
    region: us-east-1
    auth:
      kind: aws_default_chain
    timeout: 115s
    service_classes:
      economy:
        provider_value: flex
      standard:
        provider_value: default
      priority:
        provider_value: priority
    capability_profile: bedrock-anthropic-v2
    price_catalog: catalog-2026-07-13

models:
  invoice-summarizer:
    allowed_tenants: [acme]
    data_regions: [global, australiaeast]
    routes:
      - id: openai-primary
        endpoint: openai-prod
        model: gpt-example-2026-07-01
        classes: [economy, standard, priority]
      - id: azure-secondary
        endpoint: azure-openai-au
        model: gpt-example-deployment
        classes: [standard, priority]
      - id: anthropic-secondary
        endpoint: anthropic-direct
        model: claude-example-2026-06-01
        classes: [standard, priority]
      - id: bedrock-economy
        endpoint: bedrock-us-east-1
        model: anthropic.claude-example-v1
        classes: [economy, standard, priority]

capabilities:
  catalogs:
    - file: /etc/llmtw/capabilities.yaml
      sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  unknown_in_strict_mode: reject

pricing:
  catalogs:
    - file: /etc/llmtw/prices.yaml
      sha256: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
  require_price_when_budgeted: true
  currency: USD

budgets:
  require_match: true
  policies:
    - id: acme-production
      match:
        tenant: acme
        environment: production
      windows:
        - duration: 1h
          bucket: 1m
          limit_micro_usd: 25000000
        - duration: 24h
          bucket: 5m
          limit_micro_usd: 250000000
        - duration: 30d
          bucket: 1h
          limit_micro_usd: 3000000000

continuation:
  handle_keys:
    - id: key-2026-07
      primary: true
      secret:
        kind: file
        path: /var/run/secrets/continuation-hmac
  retain_canonical_transcript: true
  allow_provider_hosted_state: false

telemetry:
  logs:
    format: json
    level: info
  metrics:
    enabled: true
  tracing:
    enabled: true
    otlp_endpoint: otel-collector.observability:4317
    sample_ratio: "0.05"
  content_logging: disabled
```

## Provider egress policy

Every endpoint requires a non-empty `outbound_hosts` list. Entries are DNS
hostnames, not URLs or IP literals: they are normalized to lowercase ASCII
without a trailing dot, and duplicates are rejected. The hostname in a
non-Bedrock `base_url` must be present in that list. Bedrock endpoints with no
`base_url` must instead name the exact regional runtime hostname that the SDK
will use, such as `bedrock-runtime.us-east-1.amazonaws.com`.

At runtime the provider client permits HTTPS requests only to those configured
hostnames and the configured HTTPS port for the endpoint. A base URL hostname
is permitted only on its explicit base URL port (or 443 when no port is given);
additional outbound hostnames, if used by an SDK, are limited to 443. It
resolves the host for every new connection, rejects a DNS answer containing
loopback, private, link-local, multicast, unspecified, carrier-grade NAT,
benchmarking, or cloud metadata addresses, then dials the validated address
directly and checks the connected peer address again. A request-time URL cannot
broaden the endpoint policy merely by naming a different host.

Provider clients do not use environment proxy settings and never follow
redirects automatically. No v1 endpoint is documented as redirecting. Adding
one requires an explicit reviewed policy that validates every redirect hop.
The endpoint timeout bounds the full response read; connection and TLS
handshake phases are independently bounded to at most 10 seconds. Transport
failures are emitted as a safe endpoint-scoped classification, never as a URL,
credential, authorization header, request body, continuation value, or raw
provider response.

## Readiness and Redis admission policy

`server.readiness_probe_interval` and
`server.readiness_probe_timeout` are required positive durations; the timeout
cannot exceed the interval. The worker checks its required state dependencies
at initial construction, before a reload is published, and periodically while
running. A failed check makes `/health/ready` return `503` and stops Temporal
polling, while `/health/live` remains available for the process supervisor.
Polling resumes only after every required dependency passes again.

Readiness checks Redis with `PING`, `TIME`, the configured persistence and
`noeviction` policy, plus the configured admission code identity. It checks
the configured S3 bucket with bucket metadata only; it never reads or writes a
tenant object. Provider endpoints are intentionally excluded because one route
can be unavailable while another eligible route remains.

`state.redis.admission_mode: function` is the preferred Redis 7+ path. Before
starting a worker, deployment automation must provision the exact versioned
Function library and set `function_library`, `admission_version`, and
`admission_digest` to its immutable identity. The running worker only verifies
and calls that Function; it never loads, replaces, or rewrites shared Redis
code. `admission_mode: lua` is an explicit compatibility fallback: its
`admission_digest` must be the SHA-256 of the preloaded Lua source, and
readiness requires Redis `SCRIPT EXISTS` for that source. The worker never
falls back from a missing Lua script to `EVAL` or `SCRIPT LOAD`.

`required_persistence` selects the deployment policy: `aof_and_rdb` requires
both AOF and a non-empty RDB save policy, while `aof` and `rdb` require only
their named mechanism. Any mismatch fails readiness closed.

## Service-class rules

The request enum remains exactly `economy`, `standard`, and `priority`.
Configuration may omit unsupported entries for an endpoint; it cannot define a
fourth public class. A mapping's `provider_value` is adapter-profile validated
and cannot be supplied by a request.

A request without `service_class` becomes `standard`. There is no configurable
provider default. `service_class_fallbacks` is request data, not a worker-wide
default, because only the caller can authorize a cost/latency class change.

## Capability catalog shape

Each entry binds claims to an exact profile/model matcher:

```yaml
version: llmtw-capabilities/v1
entries:
  - id: openai-responses-prod-v3
    family: openai_responses
    model:
      exact: gpt-example-2026-07-01
    verified_at: 2026-07-13T00:00:00Z
    features:
      input.text: {level: native}
      input.image: {level: native, max_bytes: 20971520}
      tools.auto: {level: native}
      tools.required: {level: native}
      tools.parallel: {level: native}
      output.json_schema: {level: native, dialect: draft-2020-12-subset}
      continuation.response_id: {level: native, pinned: true}
      stream.typed_usage: {level: native}
      service.economy: {level: native}
      service.standard: {level: native}
      service.priority: {level: native}
    limits:
      context_tokens: 400000
      output_tokens: 32768
```

Every `emulated` capability names a transform ID that has unit/golden tests.
Catalog compilation rejects duplicate or overlapping matchers with different
claims.

## Price catalog shape

```yaml
version: llmtw-prices/v1
id: catalog-2026-07-13
currency: USD
entries:
  - endpoint_family: openai_responses
    model: gpt-example-2026-07-01
    provider_tier: default
    effective_from: 2026-07-13T00:00:00Z
    input_per_million: "1.250000"
    output_per_million: "10.000000"
    cache_read_per_million: "0.125000"
    source: operator-verified
```

Prices in examples are illustrative. Production catalogs require provenance and
review; they never refresh silently from an untrusted endpoint.

## Validation

`validate-config` performs the local checks in `config.Load` without starting a
worker:

- schema version, unknown/duplicate fields, documented defaults, duration and
  URL syntax, normalized provider host policy, and base URL membership;
- secret references are structurally valid, without reading their values;
- Temporal, state, blob, endpoint, and provider timeout bounds are valid;
- routes reference declared endpoints and only use the three public service
  classes mapped by those endpoints;
- budget windows, continuation keys, Redis numeric bounds, and retention
  inequalities are safe;
- telemetry settings obey their environment and content-logging rules.

It does not read catalog files or compare their digests, resolve environment or
file secret contents, construct provider/Temporal/Redis/S3 clients, inspect
Redis hash-slot or Function state, verify SDK retry options, or validate
Kubernetes deployment settings. The production `worker` command performs the
reference and client-construction checks during runtime composition; catalog
and deployment verification are separate gates.

Reload performs the same checks and publishes only a complete valid snapshot.
