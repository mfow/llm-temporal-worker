# Dependency Baseline

Recorded: 2026-07-13

This baseline records the toolchain and the versions selected for the planned
provider and configuration layers. Domain packages remain standard-library
only until the task that needs each dependency lands. Versions are pinned in
`go.mod` when the corresponding package is introduced; no provider SDK is
allowed to leak into the `llm` package.

## Toolchain

| Component | Selection | Source and notes |
| --- | --- | --- |
| Go module language | `go 1.26` | The module declares the Go 1.26 language/toolchain line. |
| Current patch at baseline | `go1.26.5` | [Go release history](https://go.dev/doc/devel/release), checked 2026-07-13. |
| Minimum bootstrap for Go 1.26 | `go1.24.6` | [Go 1.26 release notes](https://go.dev/doc/go1.26), checked 2026-07-13. |
| Local version hint | `.go-version` = `1.26` | CI resolves the latest available 1.26 patch through `actions/setup-go`. |

## Planned modules

| Module | Selected release | Use | License/source |
| --- | --- | --- | --- |
| `github.com/openai/openai-go/v3` | `v3.42.0` | Official OpenAI Responses and Chat Completions clients | Apache-2.0; [official repository](https://github.com/openai/openai-go) |
| `github.com/Azure/azure-sdk-for-go/sdk/azcore` | `v1.17.0` | Official Azure OpenAI endpoint/auth middleware used by the Chat profile | MIT; [official repository](https://github.com/Azure/azure-sdk-for-go) |
| `github.com/anthropics/anthropic-sdk-go` | `v1.57.0` | Official Anthropic Messages client | MIT; [official repository](https://github.com/anthropics/anthropic-sdk-go) |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | Local Draft 2020-12 schema compilation and validation | MIT; [official repository](https://github.com/santhosh-tekuri/jsonschema-go) |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Strict YAML configuration parsing | MIT; [official repository](https://github.com/yaml/go-yaml) |
| `go.temporal.io/sdk` | `v1.44.1` | Temporal Activity payload, heartbeat, error, and worker registration boundary | MIT; [official release](https://github.com/temporalio/sdk-go/releases/tag/v1.44.1) |
| `github.com/aws/aws-sdk-go-v2/service/s3` | `v1.105.1` | Official AWS S3 client for production content-addressed blobs | Apache-2.0; [official repository](https://github.com/aws/aws-sdk-go-v2) |
| `github.com/prometheus/client_golang` | `v1.23.2` | Bounded Prometheus worker/activity metrics exposition | Apache-2.0; [official repository](https://github.com/prometheus/client_golang) |
| `go.opentelemetry.io/otel` and `/sdk` | `v1.39.0` | Sanitized OpenTelemetry spans and exporter lifecycle | Apache-2.0; [official repository](https://github.com/open-telemetry/opentelemetry-go) |
| `github.com/redis/go-redis/v9` | `v9.21.0` | Official Redis client for atomic admission Functions and immutable state records | BSD-2-Clause; [official repository](https://github.com/redis/go-redis) |

The SDK versions above are checked against their official release/source pages
and the live Go module index on the recorded date. OpenAI's current module
release is `v3.42.0`; Anthropic's current repository release is `v1.57.0`.
The schema and YAML modules are introduced only in
their owning implementation tasks so that the foundation can be tested without
network access or provider transitive dependencies.

## Repository module

| Field | Value |
| --- | --- |
| Module path | `github.com/mfow/llm-temporal-worker` |
| API contract | `llm.temporal/v1` |
| Default SDK retries | Disabled at the unified adapter boundary; retry policy is owned by the routing/Temporal layer and must be recorded per attempt |
| Domain license | Apache-2.0 (repository `LICENSE`) |
