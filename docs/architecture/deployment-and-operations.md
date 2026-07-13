# Deployment and Operations

## Process modes

The same Go binary supports:

```text
llm-temporal-worker worker
llm-temporal-worker validate-config
llm-temporal-worker print-effective-config
llm-temporal-worker reconcile --operation-id <safe-id>
```

`print-effective-config` redacts secrets and emits stable digests. Reconciliation
is read-only unless an explicit operation is selected and the provider supports
status recovery; production operators normally run it through a controlled
Temporal Workflow.

## Container

The multi-stage Docker build:

1. uses a pinned Go 1.26 patch image by digest;
2. downloads modules from checked-in `go.mod`/`go.sum`;
3. runs tests in CI, not as a hidden image-build side effect;
4. builds a static Linux binary with version/commit/build-time metadata;
5. copies it into a pinned minimal runtime image;
6. runs as a numeric non-root user with a read-only root filesystem;
7. includes CA certificates and time-zone data;
8. exposes no shell requirement and writes only to a mounted temp directory.

The image has OCI source/revision/license labels, an SBOM, provenance attestation,
and vulnerability scan in release CI. No configuration, credentials, test
fixtures, or provider payloads are baked into it.

## Kubernetes base

`deploy/kubernetes/base` contains:

- Deployment with one container, security context, resource requests/limits,
  probes, and graceful termination;
- ConfigMap for non-secret configuration;
- Secret references as examples, never literal credentials;
- Service for health/metrics endpoints;
- ServiceAccount with automount disabled unless workload identity is configured;
- PodDisruptionBudget;
- NetworkPolicy examples;
- Kustomization.

Example overlays demonstrate Redis/TLS, AWS workload identity, Azure workload
identity, and static secret references without committing values.

## Probes and shutdown

| Endpoint | Meaning | Failure effect |
| --- | --- | --- |
| `/health/live` | event loop and internal deadlock sentinel responsive | restart pod |
| `/health/ready` | valid snapshot, worker polling, required state stores healthy | remove from service/polling |
| `/metrics` | Prometheus exposition | none |

Provider endpoints are excluded from readiness because one route can be down
while another is valid. A configuration with zero eligible routes is not ready.

On termination, readiness fails immediately, polling stops, in-flight Activities
receive the Temporal worker grace period, and telemetry flushes. Deployment
`terminationGracePeriodSeconds` is validated to exceed:

```text
worker_stop_timeout + finalization_timeout + telemetry_flush_timeout + margin
```

## Configuration delivery

The process accepts a YAML file path plus environment variables for a small set
of bootstrap fields. Provider credentials use environment/file/workload-identity
references and are resolved after parsing.

Reload is triggered by `SIGHUP` or watched file replacement:

1. read a complete new file;
2. parse with unknown fields rejected;
3. resolve all references and compile catalogs;
4. validate routes, cycles, capability mappings, prices, budgets, retention, and
   Redis numeric bounds;
5. build clients and perform bounded dependency checks;
6. atomically publish the snapshot;
7. drain old clients after their captured Activities finish.

A bad reload leaves the old snapshot serving, sets a metric/condition, and logs
safe diagnostics. Environment variables are not hot-reloaded.

## Required metrics

Metric labels use bounded configured IDs, never tenant-provided free text:

- `llmtw_activity_total{status,error_class}`;
- `llmtw_activity_duration_seconds{phase}`;
- `llmtw_provider_attempt_total{endpoint,model,class,outcome}`;
- `llmtw_provider_duration_seconds{endpoint,model,class}`;
- `llmtw_service_class_actual_total{requested,actual,endpoint}`;
- `llmtw_budget_admission_total{policy,outcome}`;
- `llmtw_budget_reserved_micro_usd{policy,window}`;
- `llmtw_cost_micro_usd_total{endpoint,model,class,method}`;
- `llmtw_operation_state_total{state}`;
- `llmtw_ambiguous_total{endpoint}`;
- `llmtw_continuation_total{decision}`;
- `llmtw_config_reload_total{outcome}`;
- Redis/SDK pool, heartbeat age, and worker polling gauges.

Money counters use microUSD integer semantics; exporters may expose them as
floating-point observations only after the accounting decision.

## Logs and traces

Structured logs include timestamp, level, trace/span, Temporal workflow/run and
Activity IDs, operation ID, route IDs, configured tenant hash, status, and safe
error code. Prompt/output/tool content, continuation handles, authorization,
provider-state bytes, and raw provider bodies are denied fields.

Tracing spans cover normalization, state load, planning, admission, each
provider attempt, finalization, and continuation write. Trace propagation to
providers is opt-in because custom headers may be unsupported or disclose
internal identifiers.

## Alerts

Release dashboards and runbooks cover:

- ambiguous dispatch above zero;
- priority requests downgraded or actual tier unknown;
- reservation underestimation;
- budget denials and sustained conservative over-reservation;
- Redis persistence/eviction/memory/Function digest failures;
- no eligible routes or failed config reload;
- provider auth failures and route circuit opening;
- Activity heartbeat/stuck age and task queue backlog;
- continuation corruption or digest mismatch;
- cost catalog expiry.

## Local stack

`compose.yaml` starts Temporal development services, Redis with persistence, and
the worker. Profiles add provider mocks and an optional live worker. Local smoke
tests:

1. wait for Temporal and Redis health;
2. register the worker;
3. execute a fixture Activity against a mock provider;
4. kill the worker after dispatch;
5. restart and prove completed replay or ambiguity handling;
6. start two replicas and prove shared budget admission.

Live provider tests are opt-in, require explicit environment flags and tiny cost
ceilings, and never run on pull requests from untrusted forks.

## GitHub Actions split

`pull-request.yml` validates documentation and, once Go exists, formatting,
vet, race tests, unit/integration tests, build, and Docker build for pull
requests. It has read-only permissions and no provider credentials.

`master.yml` runs the same gates on master pushes, manual dispatch, and every
day at 05:00 using the `Australia/Sydney` schedule timezone. Release publishing
is a later opt-in job; a scheduled validation never deploys automatically.

Both workflows use concurrency cancellation, pinned major official actions,
dependency caching through `setup-go`, explicit timeouts, and conditional Go/
Docker steps so the documentation-only repository remains green before
implementation begins.
