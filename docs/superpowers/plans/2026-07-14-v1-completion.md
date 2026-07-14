# V1 Completion Execution Plan

**Date:** 2026-07-14

**Status:** Active execution plan
**Purpose:** Finish the documented v1 product through small, independently reviewed pull requests. This plan supersedes neither the approved architecture nor the historical phase plans; it turns their completion gate into an auditable delivery sequence.

## Release target

The first release is complete only when every statement in the [v1 completion
gate](../../index.md#v1-completion-gate) has direct, repeatable evidence. The
public service-class contract remains exactly `economy`, `standard`, and
`priority`; omitted input normalizes to `standard`; no public or internal route
may treat a provider default as a requested class.

The implementation already has a strong foundation: semantic request and
response types, official SDK clients with SDK retries disabled, deterministic
class-major routing, operation/replay safety, exact money arithmetic,
memory/Redis state stores, a non-root image, and separate PR/master workflows.
Those facts are not release evidence for the requirements below. Each task
adds the missing behavior and the command that proves it.

## Baseline findings and ordering

The task order follows safety dependencies rather than package ownership:

1. Make production startup and outbound transport fail closed before allowing
   real services to exercise them.
2. Correct routing, pricing, and endpoint-profile semantics before testing
   distributed admission.
3. Replace the synthetic streaming path with one typed stream contract all
   supported adapters can fulfill.
4. Prove state, Activity, and two-replica behavior against real local
   dependencies.
5. Enforce complete adapter fixtures, security, fuzzing, and supply-chain
   evidence in CI.
6. Run the credentialed release checks only through an explicitly authorized
   protected workflow.

No task may claim a passing live gate from a parser-only, fake-controller, or
mock-only test. Mocks remain appropriate for deterministic conversion and
failure classification; Redis, Temporal, Compose, and opt-in provider gates
must identify their real runtime boundary in their logs and artifacts.

## Global implementation constraints

- Keep the semantic IR and provider-neutral types at the public boundary.
  Provider SDK request types stay inside adapter packages.
- Keep one retry authority: provider SDK retries stay disabled, Temporal owns
  durable retries, and an ambiguous dispatch is never automatically resent.
- Preserve opaque continuation bytes and endpoint pinning. Do not turn
  provider state into parsed application data merely to make it portable.
- Use integer microUSD accounting and reserve before any provider write. A
  price can be absent only when no matching monetary budget applies and the
  response explicitly reports unknown cost.
- Treat Redis outage, failed durability policy, failed function/script version
  verification, and failed required blob-state probe as admission-blocking.
- Health checks may not call provider APIs. Readiness covers a valid snapshot,
  required state dependencies, and active worker polling; liveness remains
  independent so an operator can diagnose a failed readiness transition.
- Outbound provider transport has explicit redirect, DNS, address, and host
  policy. It must not follow a redirect into a private, loopback, link-local,
  or metadata address.
- Streaming is typed end-to-end. A convenience method that calls `Generate`
  and synthesizes start/completion events is not streaming and is prohibited.
- Tests are deterministic by default, redact fixture bytes and logs, use
  barriers rather than sleeps for concurrency, and never use provider
  credentials outside the dedicated live workflow.
- Every implementation PR is branched from current `master`, has a task brief,
  implementer report, task-scoped reviewer verdict, green required GitHub
  checks, a clean mergeability check, and labels `Codex` plus its applicable
  area labels before merge.

## Shared validation environment

The ordinary local gate remains safe without external credentials:

```sh
make verify
make integration
make compose-smoke
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

New commands introduced below must be bounded, document their prerequisites,
and fail closed when their explicit opt-in environment is absent. CI runs the
offline commands on every PR. Trusted master and protected manual workflows
run the real local-dependency and provider-specific commands after the
corresponding task adds them.

## Task 1: Fail-closed runtime dependency probes and readiness

**PR title:** `feat(runtime): gate worker readiness on required dependencies`

**Files to change**

- `config/types.go`, `config/validate.go`, and
  `docs/reference/configuration.md`
- `internal/runtime/factory.go`, `internal/runtime/runtime.go`, and new focused
  dependency-probe files under `internal/runtime/`
- `internal/app/app.go`, `internal/app/worker.go`, and their tests
- `internal/httpserver/health.go` and tests
- `storage/redis/function.go`, `storage/redis/admission.go`, and tests
- `storage/s3blob/store.go` and tests when a blob probe is required
- `docs/architecture/deployment-and-operations.md`

**Implementation**

1. Define a narrow probe interface that reports dependency identity, bounded
   probe status, and a safe failure reason. Inject it into the runtime rather
   than coupling the HTTP handler to Redis, S3, or Temporal SDK types.
2. During initial build and reload, probe required Redis and blob state before
   publishing a snapshot. Redis checks reachability, server time, `noeviction`,
   the configured AOF/RDB durability mode, and the configured admission
   Function or Lua digest/version. Blob storage checks the configured bucket
   without reading or writing tenant content.
3. Use the documented versioned Redis Function as the preferred transaction
   path, retain the documented Lua compatibility fallback, and make the chosen
   mode and expected digest explicit in configuration. A production startup
   verifies rather than silently replacing a shared server-side library.
4. Add a bounded periodic monitor. A failed required dependency moves readiness
   to false, prevents new work from polling, and keeps liveness responsive.
   Recovery restores readiness only after all required probes pass.
5. Keep provider endpoints out of readiness and preserve a valid old snapshot
   after a failed reload.

**Acceptance evidence**

```sh
go test ./internal/runtime ./internal/app ./internal/httpserver ./storage/redis ./storage/s3blob
go test -race ./integration
make integration
make readiness-integration
```

Tests cover initial failure, recovery, reload failure, healthy liveness during
readiness failure, no Redis auto-mutation, and every configured persistence and
function/script policy. `make readiness-integration` uses one pinned local Redis
instance, stops and restores it while a worker is running, and proves
`/health/live` remains successful while `/health/ready` transitions from
success to failure and back without contacting a provider.

## Task 2: Safe provider egress transport and secret-safe diagnostics

**PR title:** `feat(security): enforce provider egress policy`

**Files to change**

- `config/types.go`, `config/validate.go`, and
  `docs/reference/configuration.md`
- `internal/runtime/factory.go` and focused transport-policy files under
  `internal/runtime/`
- provider client construction tests under `llm/provider/`
- `docs/architecture/security-and-privacy.md` and
  `docs/architecture/deployment-and-operations.md`

**Implementation**

1. Add an explicit configured outbound-host policy for provider endpoints.
   Normalize hostnames, reject user-info, and distinguish an allowed explicit
   hostname from an arbitrary URL supplied at request time.
2. Build an injected resolver and dial policy that rejects loopback, private,
   link-local, multicast, unspecified, carrier-grade NAT, and cloud metadata
   IPv4 and IPv6 targets. Recheck the connected address after DNS resolution
   to prevent hostname-only bypasses.
3. Disable automatic redirects for provider SDK HTTP clients. If a documented
   endpoint needs redirects, permit only a configured same-policy redirect and
   revalidate each hop.
4. Preserve SDK retry count zero, bounded connect/read timeouts, and existing
   redaction behavior. Ensure errors expose only configured endpoint IDs and
   classified safe details, never credentials, authorization headers, request
   content, continuation handles, or raw provider bodies.

**Acceptance evidence**

```sh
go test ./internal/runtime ./llm/provider/... -run 'Transport|Redirect|Egress|Redaction'
go test -race ./internal/runtime ./llm/provider/...
```

The test resolver covers IPv4 and IPv6 blocked ranges, DNS rebinding, allowed
public endpoints, redirect rejection, TLS mock behavior, timeout bounds, and
raw-byte leak scans.

## Task 3: Complete pricing and budget policy semantics

**PR title:** `fix(budget): enforce configured price and policy matching`

**Files to change**

- `config/types.go`, `config/validate.go`, and configuration fixtures
- `internal/runtime/catalog_snapshot.go` and tests
- `pricing/catalog.go`, `pricing/quote.go`, and tests
- `budget/matcher.go`, `budget/policy.go`, and tests
- `engine/generate.go`, `engine/snapshot.go`, and tests
- `docs/architecture/pricing-and-budgets.md` and
  `docs/reference/configuration.md`

**Implementation**

1. Carry `pricing.require_price_when_budgeted` and
   `budgets.require_match` from parsed configuration into the immutable engine
   snapshot and enforce them before dispatch.
2. Accept and match the documented policy fields: tenant, project, actor
   prefix, environment, logical model, endpoint, and service class. Missing
   restricted context cannot match.
3. Quote every authorized explicit fallback deterministically. A candidate
   with missing price is ineligible when any matching monetary budget applies;
   with no matching monetary budget it remains eligible only under the
   configured unpriced policy and the response reports `cost_status=unknown`.
4. Do not abort the whole plan because one candidate has no usable price.
   Record the safe reason, skip that candidate, and fail only if no authorized
   candidate remains.

**Acceptance evidence**

```sh
go test ./routing ./pricing ./budget ./engine ./internal/runtime
go test -race ./routing ./pricing ./budget ./engine
```

The cases include all matcher fields, overlapping policies, zero/one/multiple
matches, class fallbacks, stale or missing prices, a budgeted unpriced route,
an allowed unbudgeted unpriced route, and request-digest stability. Route-plan
conformance proves byte-for-byte deterministic output for a fixed snapshot,
class-major order, omission normalization to `standard` before request hashing,
rejection of `provider_default`, a fourth class, and provider tier strings, and
rejection of every unrequested service-class movement.

## Task 4: Configure Claude Platform on AWS end to end

**PR title:** `feat(adapters): configure Anthropic AWS gateway routes`

**Files to change**

- `config/types.go`, `config/validate.go`, and configuration fixtures
- `internal/runtime/factory.go` and tests
- `llm/provider/anthropicmessages/client.go` and tests as needed
- `docs/scope.md`, `docs/reference/configuration.md`, and
  `docs/reference/source-contracts.md`

**Implementation**

1. Add a closed endpoint family/profile for the Anthropic AWS gateway client,
   including region, model, and credential-reference validation.
2. Construct the official Anthropic AWS client through the runtime factory,
   using the existing workload-identity-aware credential resolver rather than
   a secret-only path. Preserve the separate Bedrock Anthropic profile.
3. Add safe route/catalog/continuation validation so AWS gateway and Bedrock
   state cannot be interchanged accidentally.
4. Add source-contract and deterministic mock coverage for accepted request,
   auth construction, response facts, classified errors, and actual class.

**Acceptance evidence**

```sh
go test ./config ./internal/runtime ./llm/provider/anthropicmessages
go test -race ./config ./internal/runtime ./llm/provider/anthropicmessages
```

## Task 5: Establish the typed streaming engine and Activity contract

**PR title:** `feat(streaming): add typed engine stream lifecycle`

**Files to change**

- `llm/engine.go`, `llm/response.go`, and focused stream types under `llm/`
- `llm/provider/adapter.go`, `llm/provider/event.go`, and tests
- `engine/stream.go`, `engine/assemble.go`, `engine/generate.go`, and tests
- `activity/activities.go`, `activity/heartbeat.go`, and tests
- `docs/architecture/unified-api.md`, `docs/architecture/temporal-worker.md`,
  and `docs/testing/strategy.md`

**Implementation**

1. Replace the synthetic `Generate` wrapper with a public typed stream API.
   Define bounded event delivery, cancellation, exactly one terminal outcome,
   final normalized response assembly, and safe event-size limits.
2. Add a provider stream port that exposes a one-way event source plus
   provider metadata and dispatch certainty. An adapter without a real stream
   implementation must reject stream use before dispatch; it may not emit a
   fabricated success stream.
3. Route stream attempts through the same normalization, plan, reservation,
   dispatch, continuation, ambiguity, and finalization rules as `Generate`.
   Preserve event order and opaque bytes while applying backpressure and
   context cancellation safely.
4. Have the Temporal Activity consume stream events only for heartbeat and
   bounded progress; it returns the final semantic value rather than placing
   raw deltas into workflow history.

**Acceptance evidence**

```sh
go test ./llm ./llm/provider ./engine ./activity -run 'Stream|Heartbeat|Replay|Ambiguous'
go test -race ./llm ./llm/provider ./engine ./activity
```

The test adapter proves delta order, terminal success/error, cancellation,
backpressure, duplicate terminal rejection, completed replay, pre-write retry,
ambiguous post-write refusal, and final stream/non-stream equivalence.

## Task 6: Wire OpenAI Responses and Azure streaming

**PR title:** `feat(openai): dispatch Responses streams`

**Files to change**

- `llm/provider/openairesponses/`, including Azure construction
- Responses/Azure stream fixtures under `llm/provider/openairesponses/testdata/`
- `docs/reference/source-contracts.md`

**Implementation**

1. Connect the official OpenAI Responses streaming API and its Azure variant to
   the Task 5 stream port without enabling SDK retries or changing the semantic
   IR.
2. Feed the fragmented-stream decoder from actual client stream bodies; retain
   request IDs, usage, class, cost, opaque state, and dispatch certainty at the
   write boundary.
3. Test direct OpenAI and Azure separately through deterministic SDK-supported
   transports, including cancellation, malformed terminal events, and strict
   rejection before a network write.

**Acceptance evidence**

```sh
go test ./llm/provider/openairesponses -run 'Stream|Fragment|Client|Strict'
go test -race ./llm/provider/openairesponses
```

## Task 7: Wire OpenAI-compatible Chat streaming

**PR title:** `feat(chat): dispatch compatible Chat streams`

**Files to change**

- `llm/provider/openaichat/`, including generic, OpenRouter, and Exa clients
- Chat stream fixtures under `llm/provider/openaichat/testdata/`
- `docs/reference/source-contracts.md`

**Implementation**

1. Connect the generic Chat streaming implementation to the Task 5 stream port
   and configure it independently for generic compatible, OpenRouter, and Exa
   endpoints.
2. Preserve tool argument fragments, usage/class/cost facts, errors, and
   dispatch certainty without inferring unsupported provider capability.
3. Use the injected transport to prove each configuration has a real stream
   request path, exact chunk equivalence, cancellation, and redaction.

**Acceptance evidence**

```sh
go test ./llm/provider/openaichat -run 'Stream|Fragment|Client|Strict'
go test -race ./llm/provider/openaichat
```

## Task 8: Wire Anthropic direct and AWS-gateway streaming

**PR title:** `feat(anthropic): dispatch direct and AWS streams`

**Files to change**

- `llm/provider/anthropicmessages/`
- Anthropic direct/AWS stream fixtures under
  `llm/provider/anthropicmessages/testdata/`
- `docs/reference/source-contracts.md`

**Implementation**

1. Connect direct Anthropic and Claude Platform on AWS streams to the Task 5
   port through their official SDK clients.
2. Preserve signed or opaque reasoning blocks, tool fragments, usage, provider
   state, and terminal error facts byte-for-byte and in source order.
3. Prove direct and AWS gateway paths independently with full, single-byte,
   split, and seeded chunk input, then verify profile-specific continuation
   pinning remains intact.

**Acceptance evidence**

```sh
go test ./llm/provider/anthropicmessages -run 'Stream|Fragment|Client|Strict'
go test -race ./llm/provider/anthropicmessages
```

## Task 9: Wire Bedrock Anthropic streaming

**PR title:** `feat(bedrock): dispatch Anthropic streams`

**Files to change**

- `llm/provider/bedrockmessages/`
- Bedrock stream fixtures under `llm/provider/bedrockmessages/testdata/`
- `docs/reference/source-contracts.md`

**Implementation**

1. Connect the current Bedrock Messages-compatible stream to the Task 5 port
   with explicit region/profile construction and zero SDK retries.
2. Map fragmented events, opaque state, usage, cost, and terminal errors while
   keeping Bedrock continuation state incompatible with direct/AWS gateway
   profiles unless the adapter has an explicit portable transcript.
3. Prove the complete client stream path with deterministic transport cases and
   strict-mode pre-dispatch failure coverage.

**Acceptance evidence**

```sh
go test ./llm/provider/bedrockmessages -run 'Stream|Fragment|Client|Strict'
go test -race ./llm/provider/bedrockmessages
```

## Task 10: Bootstrap a manifest-governed adapter contract harness

**PR title:** `test(adapters): add fixture manifest contract harness`

**Files to change**

- a shared harness and case registry under `llm/provider/contracttest/`
- every existing `llm/provider/*/testdata/contracts/*/manifest.yaml`
- fixture metadata/redaction tests under `llm/provider/`
- `Makefile`, `docs/testing/fixture-matrix.md`, and
  `docs/testing/strategy.md`

**Implementation**

1. Define the code-owned matrix by profile capability: semantic request,
   captured wire request, response, usage/cost, classified error, full and
   fragmented stream, strict loss, best-effort diagnostic, class facts,
   continuation compatibility, and security/redaction.
2. Require all manifests to have a valid schema and every listed case to have
   the documented semantic, wire, event, and metadata files. Metadata records
   upstream URL/date, SDK version, provenance, redactions, capability facts,
   and generated-field exemptions.
3. Introduce explicit `bootstrap` and `enforced` coverage status. The harness
   structurally validates every profile now; it enforces the full required
   matrix only for profiles marked `enforced`. No production profile may be
   marked `enforced` until its dedicated coverage task below supplies every
   required case.
4. Add cross-adapter helpers for reversible semantic round-trip and
   stream/non-stream assembly equivalence. They run for each enforced profile.

**Acceptance evidence**

```sh
go test ./llm/provider/contracttest ./llm/provider/...
make adapter-contracts
```

The target reports bootstrap and enforced profiles separately, fails on a
missing listed fixture, invalid metadata, unsafe bytes, or absent source date,
and never rewrites fixtures in normal test mode. It does not claim full-matrix
coverage until Tasks 11 through 14 mark the corresponding profile enforced.

## Task 11: Complete OpenAI and Azure Responses fixture coverage

**PR title:** `test(openai): enforce Responses golden coverage`

**Files to change**

- Responses/Azure fixtures and tests under `llm/provider/openairesponses/`
- required case definitions only for a real Responses capability distinction
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate every Task 10 matrix case advertised by OpenAI Responses and Azure
   Responses: tools, structured output, image parts, opaque state, usage/cost,
   service class, errors, streams, and strict/best-effort behavior.
2. Mark each Responses profile `enforced` only after all listed files and
   source-date facts pass the harness. Do not infer Azure support from a direct
   OpenAI fixture.
3. Add the complete fragmentation corpus and safe fixture redaction coverage.

**Acceptance evidence**

```sh
go test ./llm/provider/openairesponses
make adapter-contracts
```

## Task 12: Complete compatible Chat fixture coverage

**PR title:** `test(chat): enforce compatible Chat golden coverage`

**Files to change**

- generic Chat, OpenRouter, and Exa fixtures/tests under
  `llm/provider/openaichat/`
- required case definitions only for genuine Chat capability distinctions
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate all Task 10 cases advertised by generic compatible Chat,
   OpenRouter, and Exa, including strict loss/best-effort diagnostics,
   class/cost facts, errors, and stream fragmentation.
2. Mark each profile `enforced` only after it independently passes the matrix;
   wire-compatible endpoints do not inherit another endpoint's capability
   claims.
3. Update source contracts for each changed upstream behavior.

**Acceptance evidence**

```sh
go test ./llm/provider/openaichat
make adapter-contracts
```

## Task 13: Complete Anthropic direct and AWS-gateway fixture coverage

**PR title:** `test(anthropic): enforce direct and AWS golden coverage`

**Files to change**

- direct/AWS fixtures and tests under `llm/provider/anthropicmessages/`
- required case definitions only for genuine Anthropic distinctions
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate all Task 10 cases for direct Anthropic and Claude Platform on AWS,
   including signed/opaque reasoning state, tool fragments, tier facts, errors,
   streams, and continuation compatibility.
2. Mark each direct/AWS profile `enforced` independently and make every
   profile-incompatible opaque-state transition a strict pre-dispatch failure.
3. Add strict-loss and best-effort fixtures for every non-portable feature.

**Acceptance evidence**

```sh
go test ./llm/provider/anthropicmessages
make adapter-contracts
```

## Task 14: Complete Bedrock Anthropic fixture coverage

**PR title:** `test(bedrock): enforce Anthropic golden coverage`

**Files to change**

- Bedrock fixtures and tests under `llm/provider/bedrockmessages/`
- required case definitions only for genuine Bedrock distinctions
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate all Task 10 cases for each supported Bedrock Anthropic profile,
   including opaque state, tool fragments, usage, cost, errors, streams, and
   direct/AWS-gateway incompatibility.
2. Mark the profile `enforced` only after its independent contract test passes.
   Once every production profile is enforced, change the harness default to
   fail if any supported profile is merely bootstrap status.
3. Add source-date and redaction validation for all new fixtures.

**Acceptance evidence**

```sh
go test ./llm/provider/bedrockmessages
make adapter-contracts
```

## Task 15: Prove Redis admission and continuation conformance against Redis

**PR title:** `test(redis): run shared state conformance against Redis`

**Files to change**

- a shared black-box conformance suite under `storage/conformance/`
- memory and Redis test adapters under `storage/memory/` and `storage/redis/`
- Redis Function/Lua deployment fixtures under `storage/redis/` and
  `integration/redis/`
- `Makefile`, `compose.yaml`, and integration documentation
- `docs/architecture/state-and-storage.md` and `docs/testing/strategy.md`

**Implementation**

1. Run one unmodified StoreFactory suite against memory and a pinned real Redis
   container. Cover ledger transitions, digest conflict, complete replay,
   ambiguity retention, continuation immutability, MAC/tenant/digest checks,
   BlobRefs, refunds, excess cost, and clock boundaries.
2. Coordinate at least 100 concurrent admissions with barriers and assert that
   accepted microUSD never exceeds each overlapping limit.
3. Exercise Redis timeout after mutation followed by read resolution, server
   restart, configured persistence, function/script version mismatch, and
   hash-tag single-slot behavior.
4. Add a bounded `make redis-integration` target that starts or discovers only
   its named local test dependency and removes it on completion. CI invokes it
   only in trusted contexts after its logs redact data.

**Acceptance evidence**

```sh
go test ./storage/conformance ./storage/memory ./storage/redis
make redis-integration
```

## Task 16: Wire production telemetry, activity heartbeats, reload, and reconciliation

**PR title:** `feat(runtime): wire telemetry and safe lifecycle controls`

**Files to change**

- `internal/observability/`, `internal/runtime/`, and `internal/app/`
- `activity/heartbeat.go`, `activity/activities.go`, and tests
- `cmd/llm-temporal-worker/main.go`, command tests, and configuration docs
- metrics/tracing integration tests and deployment operations docs

**Implementation**

1. Construct the configured OTLP tracer/exporter and logger/metric settings at
   runtime, attach trace spans and bounded labels through normalization, state,
   planning, admission, attempts, finalization, and continuation writes, and
   flush on shutdown within the configured bound.
2. Create a fresh Temporal heartbeater per Activity invocation, inject it into
   the engine, and emit only bounded safe details at planning, admission,
   pre-write, streaming cadence, and finalization. Concurrent Activities must
   never share a start timestamp or mutable heartbeat state.
3. Implement SIGHUP and safe watched-file reload through the existing atomic
   snapshot/drain path. A bad replacement preserves the active snapshot and
   records a bounded reload failure metric.
4. Either implement the documented scoped read-only reconciliation callback or
   remove its production command surface until it has a real backend. The
   final command set must not advertise an unavailable production operation.

**Acceptance evidence**

```sh
go test ./internal/observability ./internal/runtime ./internal/app ./activity ./cmd/llm-temporal-worker
go test -race ./internal/observability ./internal/runtime ./internal/app ./activity
```

The tests inspect OTLP/log/metric outputs for required safe fields and denied
fields, verify exporter flush, per-Activity heartbeat isolation, signal reload,
drain, and rejected invalid reload behavior.

## Task 17: Make the local Compose stack runnable and prove real Temporal behavior

**PR title:** `test(integration): verify Temporal recovery with shared Redis`

**Files to change**

- `compose.yaml`, local configuration fixtures, and provider mock assets
- live harnesses under `integration/temporal/` and `integration/compose/`
- `activity/`, `engine/`, `internal/app/`, and `internal/runtime/` tests only
  where live behavior exposes an implementation defect
- `Makefile`, CI scripts, and operations runbook documentation
- `docs/architecture/temporal-worker.md` and
  `docs/architecture/deployment-and-operations.md`

**Implementation**

1. Make the opt-in local stack self-contained: pinned Temporal development
   services, Redis with the Task 1 durability requirements, a content-free
   deterministic provider mock, and a blob backend suitable for local
   continuation/result testing. The worker profile obtains a generated local
   continuation key without requiring a live provider credential.
2. Add a Temporal SDK test-environment suite using the Task 16 heartbeater for
   registration, versioned payloads, admitted/pre-write/periodic/finalizing
   heartbeat phases, cancellation before/during/after dispatch, retry rules,
   non-retryable error typing, completed replay, and shutdown drain.
3. Add a real local Temporal test that dispatches an Activity, kills the worker
   after the mock accepts the request, restarts it, and proves either completed
   replay or conservative ambiguity. Run the same test with two worker replicas
   and a shared budget; no schedule may overspend.
4. Stop and restore Redis during a running worker test. Assert live health stays
   successful, ready health becomes unavailable, polling stops, and recovery
   restores readiness only after the Task 1 probe succeeds. Verify the Compose
   worker probes hit those exact endpoints and the rendered Kubernetes probes
   reference the same paths.
5. Keep the parser-only Compose target distinct from the bounded authorized
   `make compose-live-integration` target. The latter names its Docker
   prerequisite and fails closed without explicit authorization.

**Acceptance evidence**

```sh
go test ./integration ./activity -run 'Temporal|Heartbeat|Cancellation|Replay|Readiness'
make redis-integration
make compose-live-integration
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

## Task 18: Add deterministic fuzz, property, and mutation gates

**PR title:** `test(quality): enforce semantic invariant verification`

**Files to change**

- fuzz targets and seed corpora under `llm/`, `llm/provider/`, `pricing/`,
  `budget/`, `state/`, and `storage/redis/`
- deterministic property tests for money, routing, state transitions, and event
  assembly
- mutation scripts and supporting tests under `scripts/` or `tools/`
- `Makefile`, `.github/workflows/pull-request.yml`, and
  `.github/workflows/master.yml`
- `docs/testing/strategy.md`

**Implementation**

1. Seed each fuzz target from governed golden fixtures and minimized failures.
   Assert stream decoder/event-assembler results rather than discarding them.
2. Add bounded deterministic fuzz smoke to PR CI and sharded longer fuzz runs
   to the trusted master workflow. Preserve a reproducible command for every
   saved failing corpus input.
3. Add property tests for decimal ceiling/overflow, sliding-window boundaries,
   request canonicalization, continuation MAC/tenant binding, route-plan
   determinism, omission-to-`standard`, service-class rejection, dispatch
   certainty, and state-machine transition legality.
4. Add mutation coverage for comparison boundaries, round-up direction,
   dispatch classification, service-class mapping, and state transitions. A
   mutation survivor must be recorded as a failing invariant or a deliberate
   documented boundary.

**Acceptance evidence**

```sh
make fuzz-smoke
make mutation-verify
```

## Task 19: Add security, deployment, and workflow verification gates

**PR title:** `build(ci): enforce security and workflow policy`

**Files to change**

- security, action, dependency, and deployment verification scripts under
  `scripts/` or `tools/`
- `Makefile`, `.github/workflows/pull-request.yml`, and
  `.github/workflows/master.yml`
- Kubernetes/Docker verification tests under `integration/` and `deploy/`
- `docs/testing/strategy.md`, deployment documentation, and the release runbook

**Implementation**

1. Add action syntax/policy validation, Go vulnerability checking,
   dependency/license inventory, secret/deny-field scans, and Docker/Kustomize
   security assertions. Pin CI actions by immutable commit with readable
   version comments where repository policy permits.
2. Add a workflow-contract test that parses the checked-in PR and master
   workflows. It requires PR read-only permissions and no provider secrets,
   and requires master push, manual dispatch, and the exact daily
   `0 5 * * *` schedule with `Australia/Sydney` timezone.
3. Assert rendered deployment security context, non-root UID, read-only root
   filesystem, bounded writable mount, health endpoints, and liveness/readiness
   paths. Keep manifest rendering offline and separate it from any cluster
   apply.
4. Add `make security-verify` and `make workflow-verify`; each is bounded and
   returns nonzero on a scan finding, action syntax error, schedule mismatch,
   leaked deny-field, or deployment-policy violation.

**Acceptance evidence**

```sh
make security-verify
make workflow-verify
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

## Task 20: Build release evidence, SBOM, provenance, and guarded live contracts

**PR title:** `build(release): add attestable v1 release evidence`

**Files to change**

- `Dockerfile`, build metadata sources, and image tests
- release scripts and evidence schema under `scripts/release/` and `docs/release/`
- `.github/workflows/release.yml` and trusted-workflow updates
- `docs/architecture/deployment-and-operations.md`, release runbook, and
  configuration reference
- build-tagged live provider tests under `integration/live/`

**Implementation**

1. Embed version, revision, build time, Go version, and source URL in the
   image and binary without baking configuration or credentials into layers.
2. Add `make image-verify`: inspect the final image's numeric non-root user,
   start it with a read-only root and only the documented writable mount, and
   prove its health endpoint remains reachable with no shell or root fallback.
3. Produce a machine-readable evidence record that links exact test/race/fuzz
   summaries, fixture manifest/source dates, Redis/Temporal/Compose logs,
   rendered manifests, image digest, SBOM, dependency/license output, and
   vulnerability results.
4. Generate an SPDX or CycloneDX SBOM, scan the final image, and configure
   keyless provenance/signing through GitHub Actions OIDC in a protected manual
   release workflow. The workflow verifies the published digest before marking
   a release artifact successful.
5. Add build-tagged live adapter contracts. Each profile needs an explicit
   enable flag, allow-listed model, test tenant, maximum microUSD ceiling,
   credential source, and tiny deterministic prompt. Fork PRs receive none of
   these secrets and scheduled workflows never publish or spend money.
6. Make release publication require a tagged commit, a protected environment,
   human approval, and all preceding evidence gates. Do not claim a published
   release until this protected job has passed with its configured identity.

**Acceptance evidence**

```sh
make image-verify
make release-verify
docker build --build-arg VERSION=test --build-arg REVISION=test --build-arg BUILD_TIME=2026-07-14T00:00:00Z .
```

The first two commands validate locally generated evidence without publishing.
The protected workflow later records signed image and live-contract evidence
using repository-managed credentials and identity.

## Task 21: Final v1 traceability review and release candidate

**PR title:** `docs(release): record v1 completion evidence`

**Files to change**

- a machine-readable v1 requirement map under `docs/release/`
- `docs/index.md`, `docs/scope.md`, and affected architecture/testing docs
- `Makefile` and CI only where the final aggregate gate needs wiring

**Implementation**

1. Map every v1 completion-gate statement, scope capability, and required
   security/deployment behavior to a specific implementation, deterministic
   test, real-dependency test, workflow job, and retained artifact.
2. Mark an item complete only when its cited command has a passing recorded
   run against the release candidate. Require every supported adapter profile
   to be `enforced` in the Task 10 harness; bootstrap status is a release
   failure. Distinguish implemented offline gates from protected live-provider
   and release-signing evidence.
3. Run the aggregate release verification from a clean checkout, merge only
   after PR and master checks are green and GitHub reports no merge conflict,
   then run the protected manual release workflow with authorized credentials.
4. Update the user-facing documentation from “implementation foundation” to a
   precise release status supported by the evidence map. Do not edit historical
   checkboxes merely to make the plan appear finished.

**Acceptance evidence**

```sh
make verify
make integration
make readiness-integration
make redis-integration
make compose-live-integration
make adapter-contracts
make security-verify
make workflow-verify
make fuzz-smoke
make mutation-verify
make image-verify
make release-verify
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

## External authorization boundary

Tasks 1 through 19 are implementable and testable locally or in trusted CI
without a production provider account. Task 20 prepares all release machinery,
but the first actual credentialed provider run, image publication, provenance
signature, and release tag require the repository’s protected environment,
OIDC permissions, registry destination, and deliberately scoped provider
credentials. The implementer must stop at that boundary and request explicit
authorization rather than infer permission from this plan.

## Per-task merge protocol

For each task, the coordinator records the branch base SHA, task brief,
implementation report, reviewer verdict, exact local test output, PR URL,
required-check result, mergeability result, merge commit, and post-merge
`origin/master` SHA in the ignored local execution ledger. The next task begins
only after fetching `origin/master` over HTTPS and rebasing or recreating its
branch from that current commit.
