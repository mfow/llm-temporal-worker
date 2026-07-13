# LLM Temporal Worker Master Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement and release the documented reusable Go inference engine and
Temporal Activity Worker without weakening provider semantics, retry safety, or
shared budget enforcement.

**Architecture:** A provider-neutral semantic IR is compiled through immutable
capability, route, price, and budget snapshots. Official provider SDK adapters
perform one observed dispatch. A durable operation ledger and continuation store
make Temporal retries safe across replicas.

**Tech Stack:** Go 1.26, official OpenAI/Anthropic/Temporal Go SDKs, official AWS
support used by the Anthropic SDK, Redis Functions through go-redis, strict YAML
and JSON Schema tooling, Docker, Kubernetes/Kustomize, GitHub Actions.

**Global Constraints:**

- Public service class is exactly `economy | standard | priority`; omission is
  `standard`.
- Provider SDK retry counts are zero; no automatic post-write replay.
- All provider capability and price claims are versioned data.
- Prompt/output/tool/provider-state content stays out of logs, metrics, traces,
  heartbeat details, and the operation ledger.
- Every behavioral task starts with a failing test and ends with a focused
  commit.
- The implementation does not add v1 exclusions listed in
  [scope](../../scope.md).

---

## How to execute

Use one clean HTTPS-backed branch and preserve the order below. At the start of
each phase:

```sh
git status --short
git remote get-url origin
git fetch https://github.com/mfow/llm-temporal-worker.git master
git rebase FETCH_HEAD
```

Expected: no unrelated working-tree changes; the remote URL/fetch uses HTTPS.
Resolve phase-plan checkboxes in the phase file as work lands. Do not begin a
dependent phase until its exit gate is committed.

The architecture documents are the contract. If implementation evidence forces
a decision change, first add/supersede an ADR, update all affected docs and
tests, and commit that decision separately.

## Phase sequence

### Phase 1: Foundation and contracts

Plan: [Foundation and contracts](2026-07-13-foundation-and-contracts.md)

Delivers the module, canonical semantic types, exact service-class behavior,
schema validation, provider ports/events, strict configuration decoding, and
dependency-boundary tests.

- [ ] Execute every task in the phase plan.
- [ ] Run `go test -race ./...`; expect PASS.
- [ ] Run `go vet ./...`; expect no findings.
- [ ] Run `go build ./...`; expect success.
- [ ] Confirm the pull-request workflow runs Go gates because `go.mod` exists.
- [ ] Tag the merge commit `foundation-contract-v1` only after review.

No adapter phase may change exported v1 semantics without updating foundation
contract fixtures first.

### Phase 2: Provider adapters

Plan: [Provider adapters](2026-07-13-provider-adapters.md)

Delivers OpenAI Responses, Azure/OpenRouter/Exa compatible Chat, Anthropic
Messages, AWS variants, streaming decoders, exact tier/usage/cost lifting, and
redacted exhaustive fixtures.

- [ ] Re-verify official SDK/API contracts from
  [source contracts](../../reference/source-contracts.md).
- [ ] Execute every adapter plan task in listed order.
- [ ] Run `go test -race ./llm/provider/...`; expect PASS.
- [ ] Run `go test ./llm/provider/... -run TestFixtureManifestComplete`; expect
  every profile/case present.
- [ ] Run each adapter stream fuzz target for 60 seconds locally; expect no
  crash, hang, event divergence, or unbounded allocation.
- [ ] Commit the generated fixture manifest and verified dependency versions.

Live credentials are not required for the phase exit gate.

### Phase 3: Routing and continuation

Plan: [Routing and continuation](2026-07-13-routing-and-continuation.md)

Delivers deterministic candidate planning, two independent fallback axes,
continuation handle security, portable/pinned state decisions, health policy,
and memory continuation storage.

- [ ] Execute every routing/state plan task.
- [ ] Run `go test -race ./routing ./state ./storage/memory`; expect PASS.
- [ ] Prove property tests never select an omitted service class.
- [ ] Prove opaque state never crosses provider/endpoint pinning.
- [ ] Commit route golden plans and continuation attack-case fixtures.

### Phase 4: Pricing, budgets, and durable admission

Plan: [Pricing and budgets](2026-07-13-pricing-and-budgets.md)

Delivers exact microUSD arithmetic, catalog resolution, conservative estimates,
overlapping sliding windows, operation ledger, memory/Redis conformance, and
Redis continuation/result storage.

- [ ] Execute every pricing/admission/storage task.
- [ ] Run `go test -race ./pricing ./budget ./admission ./storage/...`; expect
  PASS.
- [ ] Run the Redis conformance suite against a real pinned Redis image.
- [ ] Run concurrent exact-boundary admission at least 100 times; accepted spend
  must never exceed any limit.
- [ ] Kill Redis around each Function response boundary and prove read-after-error
  resolution.
- [ ] Record persistence choice and restore-test evidence.

Do not merge a backend whose conformance behavior differs from memory.

### Phase 5: Engine, Temporal runtime, and deployment

Plan: [Temporal runtime and deployment](2026-07-13-temporal-runtime-and-deployment.md)

Delivers orchestration, Activity mapping, worker process, configuration reload,
telemetry, Docker/Compose, and Kubernetes base/examples.

- [ ] Execute every runtime/deployment task.
- [ ] Run `go test -race ./...`, `go vet ./...`, and
  `go build ./cmd/llm-temporal-worker`; expect success.
- [ ] Run Temporal integration tests with worker loss before/after dispatch.
- [ ] Run two worker replicas with Redis and prove completed replay/shared
  budget enforcement.
- [ ] Build and run the container as non-root with a read-only root filesystem.
- [ ] Render every Kustomize overlay and pass policy assertions.

### Phase 6: Verification and release

Plan: [Verification and release](2026-07-13-verification-and-release.md)

Hardens CI, executes the full fixture/security/fuzz/live matrix, creates
reproducible artifacts, and records release evidence.

- [ ] Execute every verification plan task.
- [ ] Confirm pull-request Actions run without secrets.
- [ ] Confirm master Actions run on push/manual and at 05:00
  `Australia/Sydney`.
- [ ] Run the complete release gate below on a clean checkout.
- [ ] Review all open architecture deviations; no unrecorded deviation may ship.
- [ ] Create the v1 tag only from a green master commit.

## Clean-checkout release gate

```sh
make verify
make integration
make compose-smoke
make kustomize-verify
docker build --tag llm-temporal-worker:release-candidate .
git diff --exit-code
git status --short
```

Expected:

- all commands exit zero;
- `git diff --exit-code` prints nothing;
- `git status --short` prints nothing;
- no test needs a live provider credential unless the explicit live gate is
  selected.

Then run opt-in live verification with hard spend caps:

```sh
LLMTW_LIVE_MAX_MICRO_USD=500000 go test -tags=live ./integration/live/...
```

Expected: enabled endpoint profiles report request ID, actual tier, usage/cost,
and continuation; disabled profiles skip with a named reason.

## Merge discipline

- Each task uses the exact commit subject in its phase plan.
- A phase PR contains one phase unless a required contract change spans two.
- PR description links the architecture documents and lists commands/evidence.
- Dependency and generated-fixture diffs are reviewed separately from semantic
  code.
- Failed live tests do not trigger automatic fixture/catalog rewrites.
- The master branch stays releasable; scheduled CI validates but never deploys.

## Final acceptance traceability

| Requirement | Primary implementation phase | Primary proof |
| --- | --- | --- |
| Unified typed API | Foundation | schema and normalization fixtures |
| Exact three service classes | Foundation + adapters | enum, mapping, downgrade fixtures |
| OpenAI Responses | Adapters | wire/stream/error goldens |
| Azure/OpenRouter/Exa Chat | Adapters | profile-specific compatible fixtures |
| Anthropic direct/AWS/Bedrock | Adapters | message/thinking/tier fixtures |
| Configurable routing/fallback | Routing | deterministic plan properties |
| Durable continuation | Routing + storage | pinning, MAC, branch, restart tests |
| Pricing and overlapping budgets | Pricing/budgets | arithmetic and concurrent store conformance |
| Temporal-safe retries | Runtime | crash/heartbeat/cancel/ambiguity tests |
| Docker/Kubernetes | Runtime | container and rendered-manifest smoke |
| Exhaustive conversion testing | Adapters + verification | manifest completeness and fuzz |
| PR/master/daily CI | Verification | Actions runs and schedule inspection |
