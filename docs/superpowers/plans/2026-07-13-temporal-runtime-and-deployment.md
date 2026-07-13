# Temporal Runtime and Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compose the validated packages into a replay-safe inference engine,
expose `llm.generate.v1` as a Temporal Activity, and ship an observable,
gracefully terminating Docker/Kubernetes worker.

**Architecture:** `engine` owns the request lifecycle and depends only on
domain/provider/storage ports. `activity` maps Temporal context, payloads,
heartbeats, and errors. `internal/app` constructs official clients from one
immutable configuration snapshot. The process is stateless outside configured
Redis/blob stores.

**Tech Stack:** Current pinned Temporal Go SDK, standard `slog`/`net/http`,
OpenTelemetry Go, Prometheus client, AWS SDK v2 S3 client for production blobs,
Docker Compose, Kustomize/Kubernetes YAML.

**Global Constraints:**

- No LLM call occurs in a Temporal Workflow.
- Provider deadlines leave time for ledger finalization within Activity timeout.
- Heartbeats and Temporal errors contain no model content or secret.
- Cancellation after possible write is ambiguous unless provider evidence proves
  otherwise.
- Readiness fails closed on required durable state, not on one optional provider.
- Container is non-root/read-only and Kubernetes grace exceeds worker shutdown.

---

### Task 1: Compose the inference engine

**Files:**

- Create: `engine/engine.go`
- Create: `engine/dependencies.go`
- Create: `engine/generate.go`
- Create: `engine/stream.go`
- Create: `engine/attempt.go`
- Create: `engine/finalize.go`
- Test: `engine/generate_test.go`
- Test: `engine/replay_test.go`
- Test: `engine/ambiguity_test.go`
- Test: `engine/service_class_test.go`

- [ ] Build fakes for snapshot, continuation, router, price/estimator, admission,
  adapter registry, result store, and clock. Write one test per lifecycle step
  and every failure boundary.
- [ ] Assert exact call order:

```text
normalize -> load continuation -> plan -> price/estimate -> Begin
-> compile -> MarkDispatching via observer -> Invoke once
-> lift/reconcile -> Continue for a definite safe fallback, or
-> write result/child -> Complete
```

- [ ] Add replay tests: existing completed returns stored response without
  compiling/invoking; conflict stops; expired proven reserved can resume;
  dispatching/ambiguous never invokes.
- [ ] Add class tests recording requested, attempted, provider value, actual,
  fallback index, and maximum-plan reservation. Provider downgrade completes
  without a repeat.
- [ ] Run `go test ./engine`. Expected: FAIL because package is absent.
- [ ] Implement `Engine.Generate` and `Engine.Stream` around interfaces. Capture
  one snapshot per call. The stream API emits typed events but withholds terminal
  success until result, continuation, and ledger Complete succeed.
- [ ] Before any fallback after a definitely charged failure, call
  `AdmissionStore.Continue` to retain incurred cost and reserve the remaining
  plan atomically. Stop when Continue denies; never reuse a single-attempt
  reservation for two charged attempts.
- [ ] Implement a dispatch observer whose `BeforePossibleWrite` durably calls
  `MarkDispatching` before transport write; if that store call fails, abort the
  network request.
- [ ] Shield only bounded finalization/reconciliation writes from canceled
  context, retaining trace/operation IDs and a strict timeout.
- [ ] Run `go test -race -count=100 ./engine`. Expected: PASS.
- [ ] Commit: `feat(engine): compose replay-safe inference lifecycle`.

### Task 2: Define Temporal payload and Activity error contracts

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Create: `activity/types.go`
- Create: `activity/payload.go`
- Create: `activity/errors.go`
- Create: `activity/options.go`
- Create: `api/schema/v1/activity-request.schema.json`
- Create: `api/schema/v1/activity-response.schema.json`
- Test: `activity/payload_test.go`
- Test: `activity/errors_test.go`
- Test: `activity/options_test.go`

- [ ] Verify/pin the current Temporal Go SDK and record version/minimum Go in the
  dependency baseline.
- [ ] Write JSON round-trip/schema tests for inline semantic payloads and
  BlobRefs, rejecting SDK structs, oversized inline data, malformed refs,
  unknown versions, and unknown fields.
- [ ] Write table tests mapping every common error to the exact Temporal
  Application Error type/retry behavior in the error model.
- [ ] Write Activity option validation tests for deadline composition,
  heartbeat, retry horizon versus operation retention, and attempt bounds.
- [ ] Run `go test ./activity`. Expected: FAIL.
- [ ] Implement versioned payload wrappers and safe error details. Use
  Temporal's non-retryable Application Error option for invalid/auth/conflict/
  ambiguity/corrupt cases; preserve Temporal cancellation.
- [ ] Implement caller option helper without deriving Temporal priority from
  provider service class.
- [ ] Run `go test -race ./activity`. Expected: PASS.
- [ ] Commit: `feat(activity): define Temporal payload and error contracts`.

### Task 3: Implement the Activity, heartbeats, and cancellation

**Files:**

- Create: `activity/activities.go`
- Create: `activity/heartbeat.go`
- Create: `activity/context.go`
- Test: `activity/activities_test.go`
- Test: `activity/heartbeat_test.go`
- Test: `activity/cancellation_test.go`
- Test: `activity/leak_test.go`

- [ ] Use the Temporal Activity test environment to register a dependency-
  injected `Activities` method under exact name `llm.generate.v1`.
- [ ] Write tests capturing heartbeats at admitted, pre-write, periodic
  streaming/wait, and finalizing phases. Serialize details and assert prompts,
  outputs, tools, handles, raw errors, and secrets are absent.
- [ ] Write cancellation tests before write, during streaming, and after terminal
  provider output but before Complete. Assert reservation/result behavior and
  Temporal error type.
- [ ] Run `go test ./activity -run 'TestActivity|TestHeartbeat|TestCancel'`.
  Expected: FAIL.
- [ ] Implement `Activities.Generate` as a thin engine call. Inject a
  heartbeater adapter so engine progress becomes bounded/jittered Temporal
  heartbeats and context cancellation reaches SDK/storage calls.
- [ ] Add a goroutine leak test and use channels/fake clocks rather than sleeps.
- [ ] Run `go test -race -count=100 ./activity`. Expected: PASS.
- [ ] Commit: `feat(activity): run inference with heartbeat cancellation safety`.

### Task 4: Construct clients, secrets, reload, and blob stores

**Files:**

- Create: `internal/app/app.go`
- Create: `internal/app/snapshot_builder.go`
- Create: `internal/app/reload.go`
- Create: `internal/secrets/resolver.go`
- Create: `internal/secrets/env.go`
- Create: `internal/secrets/file.go`
- Create: `storage/fileblob/store.go`
- Create: `storage/s3blob/store.go`
- Test: `internal/app/snapshot_builder_test.go`
- Test: `internal/app/reload_test.go`
- Test: `internal/secrets/resolver_test.go`
- Test: `storage/fileblob/store_test.go`
- Test: `storage/s3blob/store_test.go`

- [ ] Write failing tests for environment/file/workload identity reference
  resolution, redacted effective config, client retry zero, bad endpoint/TLS,
  valid atomic reload, invalid reload retaining prior snapshot, client drain,
  blob digest/size/tenant prefix, immutable put, and canceled I/O.
- [ ] Add current pinned AWS SDK v2 S3 modules only for the production BlobStore;
  use default credential chain/workload identity, not manual signing.
- [ ] Run the package tests. Expected: FAIL.
- [ ] Implement snapshot builder order exactly as the deployment architecture:
  strict config, secret refs, clients/dependency checks, compiled snapshot,
  atomic publish, reference-counted old-client drain.
- [ ] Implement file blobs only in development mode with path containment and
  atomic rename. Implement S3 blobs with content-addressed keys, conditional
  create, checksum, size limit, TLS, and bounded context.
- [ ] Implement `SIGHUP`/file replacement reload hook as an app method; signal
  wiring lands in the CLI task.
- [ ] Run `go test -race ./internal/app ./internal/secrets ./storage/fileblob ./storage/s3blob`.
  Expected: PASS.
- [ ] Commit: `feat(runtime): build reloadable clients and blob stores`.

### Task 5: Add worker process, health, logs, metrics, and traces

**Files:**

- Create: `cmd/llm-temporal-worker/main.go`
- Create: `cmd/llm-temporal-worker/commands.go`
- Create: `internal/app/worker.go`
- Create: `internal/app/shutdown.go`
- Create: `internal/observability/log.go`
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/trace.go`
- Create: `internal/httpserver/server.go`
- Create: `internal/httpserver/health.go`
- Test: `cmd/llm-temporal-worker/commands_test.go`
- Test: `internal/app/worker_test.go`
- Test: `internal/app/shutdown_test.go`
- Test: `internal/observability/leak_test.go`
- Test: `internal/httpserver/health_test.go`

- [ ] Write command tests for `worker`, `validate-config`,
  `print-effective-config`, and scoped `reconcile`; no command may print secret
  or content.
- [ ] Write startup/shutdown tests for dependency order, exact Activity
  registration/task queue, readiness transition, stop polling, graceful
  timeout, final telemetry flush, and exit codes.
- [ ] Write metric label allow-list tests and structured log/trace leak tests
  using adversarial secrets/prompts in every error/source field.
- [ ] Run tests. Expected: FAIL.
- [ ] Implement a Temporal worker with configured graceful stop and Activity
  struct registration. Start readiness only after polling and required stores
  are healthy.
- [ ] Implement `/health/live`, `/health/ready`, and `/metrics`. Provider route
  failure alone does not fail readiness while an eligible route exists.
- [ ] Implement the bounded metrics listed in deployment docs and OpenTelemetry
  spans; hash configured tenant IDs and drop unapproved attributes.
- [ ] Run `go test -race ./cmd/... ./internal/app ./internal/httpserver ./internal/observability`.
  Expected: PASS.
- [ ] Commit: `feat(worker): add observable graceful Temporal process`.

### Task 6: Build a non-root container and local Compose stack

**Files:**

- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `compose.yaml`
- Create: `deploy/local/config.yaml`
- Create: `deploy/local/capabilities.yaml`
- Create: `deploy/local/prices.yaml`
- Create: `integration/compose/smoke_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/pull-request.yml`
- Modify: `.github/workflows/master.yml`

- [ ] Write a container smoke test that inspects numeric non-root user, read-only
  execution, CA/time-zone presence, `validate-config`, liveness/readiness, and
  no source/config/credential layers.
- [ ] Write Compose tests using Temporal development services, Redis persistence,
  a deterministic provider mock, and worker. The default profile uses no live
  credential.
- [ ] Run `go test ./integration/compose`. Expected: FAIL before artifacts exist.
- [ ] Implement a digest-pinned multi-stage Go 1.26 build and minimal runtime,
  OCI labels/build args, read-only-compatible temp mount, and no shell reliance.
- [ ] Implement Compose health/dependency ordering and one/two-worker scale
  commands. Add `make image` and `make compose-smoke`.
- [ ] Remove Docker conditional execution from Actions because `Dockerfile` now
  exists.
- [ ] Run `make image && make compose-smoke`. Expected: PASS.
- [ ] Commit: `feat(deploy): add non-root image and local Temporal stack`.

### Task 7: Add Kubernetes base and example overlays

**Files:**

- Create: `deploy/kubernetes/base/kustomization.yaml`
- Create: `deploy/kubernetes/base/deployment.yaml`
- Create: `deploy/kubernetes/base/service.yaml`
- Create: `deploy/kubernetes/base/configmap.yaml`
- Create: `deploy/kubernetes/base/serviceaccount.yaml`
- Create: `deploy/kubernetes/base/poddisruptionbudget.yaml`
- Create: `deploy/kubernetes/base/networkpolicy.yaml`
- Create: `deploy/kubernetes/examples/redis-tls/kustomization.yaml`
- Create: `deploy/kubernetes/examples/aws-workload-identity/kustomization.yaml`
- Create: `deploy/kubernetes/examples/azure-workload-identity/kustomization.yaml`
- Create: `integration/kubernetes/manifests_test.go`
- Modify: `Makefile`

- [ ] Write tests that render base and every overlay and assert non-root,
  read-only root, dropped capabilities, seccomp, resource requests/limits,
  probes, Secret references, disabled service-account token by default,
  disruption budget, NetworkPolicy, and sufficient termination grace.
- [ ] Run `go test ./integration/kubernetes`. Expected: FAIL.
- [ ] Add the Kustomize manifests with no literal credentials and safe example
  model names/prices. Workload identity overlays enable only required service
  account annotations/token behavior.
- [ ] Add `make kustomize-verify` using a pinned `kubectl kustomize` or Kustomize
  version recorded in dependency baseline.
- [ ] Run `make kustomize-verify && go test ./integration/kubernetes`.
  Expected: PASS.
- [ ] Commit: `feat(deploy): add secure Kubernetes worker base`.

### Task 8: Prove worker-loss and two-replica behavior

**Files:**

- Create: `integration/temporal/activity_test.go`
- Create: `integration/temporal/restart_test.go`
- Create: `integration/temporal/two_replica_test.go`
- Create: `integration/temporal/cancel_test.go`
- Create: `integration/temporal/testenv.go`
- Modify: `Makefile`

- [ ] Start real Temporal, Redis, provider mock, and worker processes in an
  isolated Compose project with unique task queue.
- [ ] Add controlled provider barriers for: before read, after request accepted,
  after response sent, and during stream. Kill the worker at each barrier.
- [ ] Assert pre-write retry submits once after restart; completed replay returns
  cached result; accepted-unresolved returns non-retryable ambiguity and retains
  reservation; cancellation follows the same certainty rules.
- [ ] Start two workers and race operations across them. Assert one logical
  operation submission/result and aggregate budget never exceeds the limit.
- [ ] Validate graceful `SIGTERM` and configuration reload while another
  Activity captures the old snapshot.
- [ ] Run `make temporal-integration` three times. Expected: PASS with no leaked
  Compose resources.
- [ ] Commit: `test(temporal): verify crash and multi-replica safety`.

## Phase exit

- [ ] Run `make verify`, `make redis-integration`,
  `make temporal-integration`, `make compose-smoke`, and
  `make kustomize-verify`. Expected: PASS.
- [ ] Build/run the image with read-only root and inspect UID. Expected: nonzero
  numeric UID and healthy probes.
- [ ] Search captured logs/traces/heartbeats for seeded secret/prompt markers.
  Expected: none.
- [ ] Run `git diff --check` and `git status --short`. Expected: clean after
  commits.
