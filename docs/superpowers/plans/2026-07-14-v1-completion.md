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
```

Tests cover initial failure, recovery, reload failure, healthy liveness during
readiness failure, no Redis auto-mutation, and every configured persistence and
function/script policy.

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
go test ./pricing ./budget ./engine ./internal/runtime
go test -race ./pricing ./budget ./engine
```

The cases include all matcher fields, overlapping policies, zero/one/multiple
matches, class fallbacks, stale or missing prices, a budgeted unpriced route,
an allowed unbudgeted unpriced route, and request-digest stability.

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

## Task 6: Wire real streaming for every supported adapter profile

**PR title:** `feat(adapters): dispatch typed provider streams`

**Files to change**

- `llm/provider/openairesponses/`, including Azure construction
- `llm/provider/openaichat/`, including OpenRouter and Exa construction
- `llm/provider/anthropicmessages/`, including direct and AWS construction
- `llm/provider/bedrockmessages/`
- shared stream tests and fixtures under `llm/provider/*/testdata/contracts/`
- `docs/reference/source-contracts.md`

**Implementation**

1. Connect each official SDK streaming API to the Task 5 stream port without
   changing the semantic IR or enabling SDK retries.
2. Feed the existing fragmented-stream decoders from actual client stream
   bodies, map provider IDs/usage/class/cost/continuation facts, and classify
   dispatch certainty at the write boundary.
3. Preserve provider-specific opaque reasoning, signatures, tool-argument
   fragments, status/refusal events, and terminal error facts. Reject features
   that cannot be represented in strict mode before any network write.
4. Test OpenAI Responses, Azure Responses, generic OpenAI-compatible Chat,
   OpenRouter, Exa, Anthropic direct, Anthropic AWS, and Bedrock Anthropic
   through deterministic SDK-supported transports.

**Acceptance evidence**

```sh
go test ./llm/provider/... -run 'Stream|Fragment|Client|Strict'
go test -race ./llm/provider/...
```

Every supported profile has a real client-stream path, full/single-byte/split/
seeded chunk equivalence, cancellation, malformed terminal handling, and no
credential or content leakage.

## Task 7: Add a manifest-governed cross-adapter contract harness

**PR title:** `test(adapters): enforce complete contract fixture manifests`

**Files to change**

- a shared fixture harness under `llm/provider/contracttest/`
- required case definitions under `llm/provider/contracttest/`
- every `llm/provider/*/testdata/contracts/*/manifest.yaml`
- fixture metadata and redaction tests under `llm/provider/`
- `docs/testing/fixture-matrix.md` and `docs/testing/strategy.md`

**Implementation**

1. Define the code-owned required matrix by profile capabilities: semantic
   request, captured wire request, response, usage/cost, classified error,
   full and fragmented stream, strict loss, best-effort diagnostic, class
   facts, continuation compatibility, and security/redaction.
2. Require each fixture directory to contain the documented semantic, wire,
   event, and metadata files. Metadata records upstream URL/date, SDK version,
   provenance, redactions, capability facts, and generated-field exemptions.
3. Fail a package test when a required case has no manifest entry, a manifest
   points at a missing file, a fixture contains unsafe bytes, or the source
   contract date is absent.
4. Add cross-adapter assertions for semantic round-trip where supported and
   stream assembly equivalence to non-stream output.

**Acceptance evidence**

```sh
go test ./llm/provider/contracttest ./llm/provider/...
make adapter-contracts
```

The target lists every profile and required case in its output, fails on a
deliberately removed manifest entry, and never rewrites fixtures in normal
test mode.

## Task 8: Complete Responses and Chat fixture coverage

**PR title:** `test(adapters): complete Responses and Chat golden coverage`

**Files to change**

- OpenAI and Azure Responses fixtures/tests under
  `llm/provider/openairesponses/`
- generic Chat, OpenRouter, and Exa fixtures/tests under
  `llm/provider/openaichat/`
- shared required cases only when a real capability distinction requires it
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate every Task 7 matrix case each supported Responses and Chat profile
   advertises, including tools, structured output, image parts, opaque state,
   usage/cost, service class, errors, and streams.
2. Add explicit strict rejections and best-effort diagnostics for unsupported
   semantics. Never mark a provider capability supported merely because a
   generic wire field happens to serialize.
3. Add complete fragmentation corpus coverage and source-date updates for all
   changed upstream facts.

**Acceptance evidence**

```sh
go test ./llm/provider/openairesponses ./llm/provider/openaichat
make adapter-contracts
```

## Task 9: Complete Anthropic, Anthropic AWS, and Bedrock fixture coverage

**PR title:** `test(adapters): complete Anthropic golden coverage`

**Files to change**

- Anthropic direct/AWS fixtures/tests under `llm/provider/anthropicmessages/`
- Bedrock fixtures/tests under `llm/provider/bedrockmessages/`
- shared required cases only for real capability distinctions
- `docs/reference/source-contracts.md`

**Implementation**

1. Populate the Task 7 matrix for direct Anthropic, Anthropic AWS gateway, and
   Bedrock Anthropic profiles, including signed/opaque reasoning state,
   tool-call argument fragments, usage, tier facts, errors, and streams.
2. Make direct, AWS gateway, and Bedrock continuation compatibility explicit
   in fixtures and tests. No adapter may restore opaque state across an
   incompatible profile.
3. Add strict-loss and best-effort fixtures for every profile-specific feature
   that is not portable.

**Acceptance evidence**

```sh
go test ./llm/provider/anthropicmessages ./llm/provider/bedrockmessages
make adapter-contracts
```

## Task 10: Prove Redis admission and continuation conformance against Redis

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

## Task 11: Make the local Compose stack runnable and prove real Temporal behavior

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
2. Add a Temporal SDK test-environment suite for registration, versioned
   payloads, heartbeats at admitted/pre-write/periodic/finalizing phases,
   cancellation before/during/after dispatch, retry rules, non-retryable
   error typing, completed replay, and shutdown drain.
3. Add a real local Temporal test that dispatches an Activity, kills the worker
   after the mock accepts the request, restarts it, and proves either completed
   replay or conservative ambiguity. Run the same test with two worker replicas
   and a shared budget; no schedule may overspend.
4. Keep the parser-only Compose target distinct from the bounded authorized
   `make compose-live-integration` target. The latter names its Docker
   prerequisite and fails closed without explicit authorization.

**Acceptance evidence**

```sh
go test ./integration ./activity -run 'Temporal|Heartbeat|Cancellation|Replay'
make redis-integration
make compose-live-integration
```

## Task 12: Wire production telemetry, activity heartbeats, reload, and reconciliation

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

## Task 13: Add deterministic fuzz, property, mutation, and security gates

**PR title:** `test(security): enforce invariant and fuzz verification`

**Files to change**

- fuzz targets and seed corpora under `llm/`, `llm/provider/`, `pricing/`,
  `budget/`, `state/`, and `storage/redis/`
- deterministic property tests for money, state transitions, and event assembly
- test and scanner scripts under `scripts/` or `tools/`
- `Makefile`, `.github/workflows/pull-request.yml`, and
  `.github/workflows/master.yml`
- `docs/testing/strategy.md` and security documentation

**Implementation**

1. Seed each fuzz target from the governed golden fixtures and minimized
   failures. Assert stream decoder/event-assembler invariants rather than
   discarding decoded output.
2. Add bounded deterministic fuzz smoke to PR CI and sharded longer fuzz runs
   to the daily trusted master workflow. Preserve a reproducible command for
   every saved failing corpus input.
3. Add property tests for decimal ceiling/overflow, sliding-window boundaries,
   request canonicalization, continuation MAC/tenant binding, dispatch
   certainty, and state-machine transition legality.
4. Add mutation coverage for comparison boundaries, round-up direction,
   dispatch classification, service-class mapping, and state transitions. A
   mutation survivor must be recorded as a failing invariant or a deliberate
   documented boundary.
5. Add static security verification for Go vulnerabilities, action workflow
   syntax, dependency/license inventory, secret/leak scans, and Docker/Kustomize
   security invariants. Pin CI actions by immutable commit with readable
   version comments where repository policy permits.

**Acceptance evidence**

```sh
make security-verify
make fuzz-smoke
make mutation-verify
```

All commands are bounded, offline where possible, and return nonzero on a
scanner finding, an action syntax failure, a leaked deny-field, or an invariant
violation.

## Task 14: Build release evidence, SBOM, provenance, and guarded live contracts

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
2. Produce a machine-readable evidence record that links exact test/race/fuzz
   summaries, fixture manifest/source dates, Redis/Temporal/Compose logs,
   rendered manifests, image digest, SBOM, dependency/license output, and
   vulnerability results.
3. Generate an SPDX or CycloneDX SBOM, scan the final image, and configure
   keyless provenance/signing through GitHub Actions OIDC in a protected manual
   release workflow. The workflow verifies the published digest before marking
   a release artifact successful.
4. Add build-tagged live adapter contracts. Each profile needs an explicit
   enable flag, allow-listed model, test tenant, maximum microUSD ceiling,
   credential source, and tiny deterministic prompt. Fork PRs receive none of
   these secrets and scheduled workflows never publish or spend money.
5. Make release publication require a tagged commit, a protected environment,
   human approval, and all preceding evidence gates. Do not claim a published
   release until this protected job has passed with its configured identity.

**Acceptance evidence**

```sh
make release-verify
docker build --build-arg VERSION=test --build-arg REVISION=test --build-arg BUILD_TIME=2026-07-14T00:00:00Z .
```

The first command validates the schema and locally generated evidence without
publishing. The protected workflow later records signed image and live-contract
evidence using repository-managed credentials and identity.

## Task 15: Final v1 traceability review and release candidate

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
   run against the release candidate. Distinguish implemented offline gates
   from protected live-provider and release-signing evidence.
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
make redis-integration
make compose-live-integration
make adapter-contracts
make security-verify
make fuzz-smoke
make mutation-verify
make release-verify
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

## External authorization boundary

Tasks 1 through 13 are implementable and testable locally or in trusted CI
without a production provider account. Task 14 prepares all release machinery,
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
