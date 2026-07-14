# Dependency Baseline

Recorded: 2026-07-14

This baseline records the toolchain and the direct dependency versions checked
into `go.mod`. The implementation layers that own these dependencies have
landed; this document is now an upgrade reference rather than a plan for adding
the modules. Provider SDKs, Temporal, storage clients, and process wiring stay
behind their package boundaries, and no provider SDK is allowed to leak into
the `llm` package.

## Toolchain

| Component | Selection | Source and notes |
| --- | --- | --- |
| Go module language | `go 1.26` | The module declares the Go 1.26 language/toolchain line. |
| Current patch at baseline | `go1.26.5` | [Go release history](https://go.dev/doc/devel/release), checked 2026-07-13. |
| Minimum bootstrap for Go 1.26 | `go1.24.6` | [Go 1.26 release notes](https://go.dev/doc/go1.26), checked 2026-07-13. |
| Local version hint | `.go-version` = `1.26` | CI resolves the latest available 1.26 patch through `actions/setup-go`. |

## Direct modules

| Module | Selected release | Use | License/source |
| --- | --- | --- | --- |
| `github.com/openai/openai-go/v3` | `v3.42.0` | Official OpenAI Responses and Chat Completions clients | Apache-2.0; [official repository](https://github.com/openai/openai-go) |
| `github.com/Azure/azure-sdk-for-go/sdk/azcore` | `v1.17.0` | Official Azure OpenAI endpoint/auth middleware used by the Chat profile | MIT; [official repository](https://github.com/Azure/azure-sdk-for-go) |
| `github.com/Azure/azure-sdk-for-go/sdk/azidentity` | `v1.7.0` | Azure workload/default credential resolution | MIT; [official repository](https://github.com/Azure/azure-sdk-for-go) |
| `github.com/anthropics/anthropic-sdk-go` | `v1.57.0` | Official Anthropic Messages client | MIT; [official repository](https://github.com/anthropics/anthropic-sdk-go) |
| `github.com/aws/aws-sdk-go-v2` | `v1.42.1` | AWS SDK base types and request configuration | Apache-2.0; [official repository](https://github.com/aws/aws-sdk-go-v2) |
| `github.com/aws/aws-sdk-go-v2/config` | `v1.27.27` | AWS region and default credential-chain loading | Apache-2.0; [official repository](https://github.com/aws/aws-sdk-go-v2) |
| `github.com/aws/aws-sdk-go-v2/credentials` | `v1.17.27` | Explicit AWS credential providers used by runtime composition | Apache-2.0; [official repository](https://github.com/aws/aws-sdk-go-v2) |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | Local Draft 2020-12 schema compilation and validation | MIT; [official repository](https://github.com/santhosh-tekuri/jsonschema-go) |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Strict YAML configuration parsing | MIT; [official repository](https://github.com/yaml/go-yaml) |
| `go.temporal.io/sdk` | `v1.44.1` | Temporal Activity payload, heartbeat, error, and worker registration boundary | MIT; [official release](https://github.com/temporalio/sdk-go/releases/tag/v1.44.1) |
| `github.com/aws/aws-sdk-go-v2/service/s3` | `v1.105.1` | Official AWS S3 client for production content-addressed blobs | Apache-2.0; [official repository](https://github.com/aws/aws-sdk-go-v2) |
| `github.com/aws/smithy-go` | `v1.27.3` | AWS SDK transport and protocol support | Apache-2.0; [official repository](https://github.com/aws/smithy-go) |
| `github.com/prometheus/client_golang` | `v1.23.2` | Bounded Prometheus worker/activity metrics exposition | Apache-2.0; [official repository](https://github.com/prometheus/client_golang) |
| `github.com/prometheus/client_model` | `v0.6.2` | Prometheus metric model types used by tests and exposition boundaries | Apache-2.0; [official repository](https://github.com/prometheus/client_model) |
| `github.com/prometheus/common` | `v0.70.0` | Prometheus exposition and helper types | Apache-2.0; [official repository](https://github.com/prometheus/common) |
| `go.opentelemetry.io/otel`, `/sdk`, and `/trace` | `v1.43.0` | Sanitized OpenTelemetry spans and exporter lifecycle | Apache-2.0; [official repository](https://github.com/open-telemetry/opentelemetry-go) |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | `v1.43.0` | Official OTLP/gRPC trace exporter used by the bounded telemetry lifecycle | Apache-2.0; [official repository](https://github.com/open-telemetry/opentelemetry-go) |
| `github.com/redis/go-redis/v9` | `v9.21.0` | Official Redis client for atomic admission Functions and immutable state records | BSD-2-Clause; [official repository](https://github.com/redis/go-redis) |

The versions in this table match the direct requirements in `go.mod` on the
recorded date. The table intentionally does not enumerate indirect requirements;
`go.mod` and `go.sum` are authoritative for the complete module graph. The
schema and YAML modules are in active use by the schema and configuration
packages, while provider SDKs remain outside the provider-neutral `llm` API.

When upgrading a direct module, update this table in the same change and
re-check the affected source contract, capability/price fixtures, wire
fixtures, and retry/error/stream/usage assertions.

## Verified supply-chain gate

[`tools/supplychainverify/baseline.json`](../../tools/supplychainverify/baseline.json)
is the machine-readable counterpart to this direct-module table. It records the
approved SPDX identifiers, source references, and exact versions for every
direct requirement. `make security-verify` rejects an added, removed, or
changed direct module until that reviewed inventory is deliberately updated.
The Go vulnerability scan still analyzes reachable code across the complete
module graph, including indirect dependencies.

The target pins `govulncheck` at `v1.6.0` and runs it with `go1.26.5`, the
reviewed toolchain patch. Its JSON parser retains unique `GO-*` finding
identifiers and a fail-closed internal trace scope without retaining raw trace
data. A `vulnerability_exceptions` entry is valid only when it gives an
identified finding, owner, future RFC 3339 expiry, HTTPS remediation reference,
and a `scope` of either `module_only` or `reachable`. A `module_only` exception
accepts only the single module/version trace frame; a later reachable package or
function trace fails until the baseline is explicitly reviewed as `reachable`.
Expired, incomplete, unused, or unlisted exceptions fail the gate, so a
baseline cannot conceal a new result.

The PR workflow runs this target. On a trusted-master failure, CI uploads only
its redacted report: component pass/fail state, direct-module identifiers/count,
and finding identifiers. It deliberately excludes test output, source paths,
scanner traces, provider data, and credential-like material.

### Active vulnerability exception

| Finding | Owner | Expires | Remediation reference | Bounded rationale |
| --- | --- | --- | --- | --- |
| `GO-2026-5932` | `platform-security` | 2026-08-14T00:00:00Z | [Go vulnerability report](https://pkg.go.dev/vuln/GO-2026-5932) | `module_only`: `azidentity` currently brings `golang.org/x/crypto` for `pkcs12`; the verifier reports this unmaintained `openpgp` advisory only at module level, with no reachable-function trace. No repository package imports `openpgp`. A reachable trace fails unless this scope is explicitly re-reviewed. Re-evaluate before expiry and remove it if the module is split or upstream removes the unused package. |

## Repository module

| Field | Value |
| --- | --- |
| Module path | `github.com/mfow/llm-temporal-worker` |
| API contract | `llm.temporal/v1` |
| Default SDK retries | Disabled at the unified adapter boundary; retry policy is owned by the routing/Temporal layer and must be recorded per attempt |
| Domain license | Apache-2.0 (repository `LICENSE`) |
