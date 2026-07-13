# Routing and Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement deterministic capability-aware route planning, explicit
service-class fallback, safe failure movement, opaque continuation handles, and
portable/pinned immutable continuation state.

**Architecture:** The router is a pure function over a normalized request,
compiled snapshot, continuation constraints, and a health view. State packages
own secure handles and immutable records; storage implementations satisfy ports
without influencing route semantics.

**Tech Stack:** Go 1.26 standard library, SHA-256/HMAC and `crypto/rand`,
provider-neutral foundation types, injected clocks/ID sources, Go property/fuzz
tests.

**Global Constraints:**

- Route configuration can change endpoint/model; only request fallback data can
  change service class.
- Candidate ordering is stable for one snapshot; no map/random/latency ordering.
- Opaque provider state is never interpreted or sent across its pin.
- Handles contain no prompt/provider token and are tenant-bound.
- Memory storage implements production semantics but is rejected for
  multi-replica production.

---

### Task 1: Define route inputs, candidates, and rejection diagnostics

**Files:**

- Create: `routing/types.go`
- Create: `routing/rejection.go`
- Create: `routing/planner.go`
- Test: `routing/planner_test.go`
- Test: `routing/testdata/plan/basic.json`
- Test: `routing/testdata/plan/rejections.json`

- [ ] Write tests that construct a normalized logical-model request and snapshot
  with three ordered routes. Assert exact candidate identity/order and
  structured rejections for tenant, region, model, health, capability, price,
  extension, context limit, and continuation mismatch.
- [ ] Use this interface in the test:

```go
type Planner interface {
	Plan(context.Context, routing.Input) (routing.Plan, error)
}

type Input struct {
	Request      llm.NormalizedRequest
	Catalog      Catalog
	Continuation state.Constraints
	Health       HealthView
}
```

- [ ] Run `go test ./routing -run TestPlanner`. Expected: FAIL because package is
  absent.
- [ ] Implement candidate filtering in one documented order. Preserve every
  rejection as code, route ID, field path, and safe detail; cap diagnostics but
  retain counts.
- [ ] Define `Candidate.ID` as the digest of route/endpoint/family/model/class,
  capability/price versions, extension digest, and pin facts.
- [ ] Serialize golden plans with sorted deterministic diagnostics.
- [ ] Run `go test -race ./routing`. Expected: PASS.
- [ ] Commit: `feat(routing): plan deterministic eligible candidates`.

### Task 2: Enforce the two fallback axes

**Files:**

- Create: `routing/fallback.go`
- Modify: `routing/planner.go`
- Test: `routing/fallback_test.go`
- Test: `routing/fallback_property_test.go`

- [ ] Write table/property tests for no fallback, each single fallback, ordered
  two-class fallback, duplicate/invalid lists already rejected by normalization,
  endpoint exhaustion, and class-major ordering.
- [ ] Include this invariant for generated valid inputs:

```go
for _, candidate := range plan.Candidates {
	if candidate.Class != req.ServiceClass &&
		!slices.Contains(req.ServiceClassFallbacks, candidate.Class) {
		t.Fatalf("planner selected unauthorized class %q", candidate.Class)
	}
}
```

- [ ] Run `go test ./routing -run 'TestFallback|TestPlannerNeverAddsClass'`.
  Expected: FAIL until fallback expansion is implemented.
- [ ] Expand candidate groups as requested class followed by fallback list,
  visiting routes in configured order inside each group. Never infer a class
  from endpoint mapping or health.
- [ ] Add requested/attempted class and fallback index to candidate identity.
  Provider actual class is response data and must not alter the plan retroactively.
- [ ] Run `go test -race ./routing` 100 times. Expected: stable PASS.
- [ ] Commit: `feat(routing): require explicit service class fallback`.

### Task 3: Implement safe failure movement and passive health

**Files:**

- Create: `routing/executor_policy.go`
- Create: `routing/health.go`
- Create: `routing/attempt.go`
- Test: `routing/executor_policy_test.go`
- Test: `routing/health_test.go`

- [ ] Write every row of the failure movement table as a test: compile error,
  definite pre-write, verified uncharged capacity/rate, accepted output,
  cancellation before/after write, ambiguous timeout, retrievable provider job,
  auth/config error, and exhausted deadline/attempt count.
- [ ] Run `go test ./routing -run 'TestMovement|TestHealth'`. Expected: FAIL.
- [ ] Implement a pure `NextDecision(plan, attempts, err, now, deadline)` that
  returns stop, next route, next explicit class, wait, or retrieve-existing.
  Ambiguous always stops automatic submission.
- [ ] Implement passive health with injected clock and bounded consecutive
  definite transient failures. Auth/config opens until snapshot change;
  ambiguous outcomes do not count as safe transient failures.
- [ ] Ensure a health-open endpoint creates a route rejection but cannot bypass
  class, pinning, region, or capability restrictions.
- [ ] Run `go test -race ./routing`. Expected: PASS without sleeps.
- [ ] Commit: `feat(routing): classify safe failover and route health`.

### Task 4: Implement tenant-bound continuation handles

**Files:**

- Create: `state/handle.go`
- Create: `state/keyring.go`
- Create: `state/error.go`
- Test: `state/handle_test.go`
- Test: `state/handle_fuzz_test.go`

- [ ] Write failing tests for valid round trip, different tenant, tampered
  version/key/ID/MAC, truncated/oversized input, unknown/retired key, key
  rotation, constant-time MAC behavior at the API level, and no embedded
  transcript/provider ID.
- [ ] Define the handle grammar exactly:

```text
ctn_v1.<key-id>.<base64url-128-bit-random-id>.<base64url-HMAC-SHA256>
```

The MAC input is length-prefixed version, key ID, random ID bytes, and canonical
tenant scope.
- [ ] Run `go test ./state -run TestHandle`. Expected: FAIL.
- [ ] Implement generation with `crypto/rand.Reader` and parse/verify with
  bounded split/base64 decode and `hmac.Equal`. Inject randomness only for tests.
- [ ] Expose safe errors that do not reveal whether a continuation exists for a
  different tenant.
- [ ] Add fuzz seeds and run a 60-second fuzz test. Expected: no panic or
  unbounded allocation.
- [ ] Commit: `feat(state): add tenant-bound continuation handles`.

### Task 5: Define immutable continuation and pinning semantics

**Files:**

- Create: `state/continuation.go`
- Create: `state/provider_state.go`
- Create: `state/store.go`
- Create: `state/pinning.go`
- Create: `state/canonical.go`
- Test: `state/continuation_test.go`
- Test: `state/pinning_test.go`
- Test: `state/canonical_test.go`

- [ ] Write tests for immutable child creation, idempotent child by operation,
  parent branching, canonical transcript digest, complete/incomplete portable
  transcript, provider response/conversation IDs, encrypted/thinking bytes,
  same/wrong provider/account/family/model lineage, expiry, and depth limit.
- [ ] Run `go test ./state -run 'TestContinuation|TestPinning'`. Expected: FAIL.
- [ ] Implement `ContinuationStore`, `BlobStore`, `Continuation`, `Pinning`,
  and opaque refs from the state architecture. Constructors deep-copy bytes and
  validate digest/media/provenance.
- [ ] Implement `ConstraintsFor(candidate)` returning compatible, portable
  replay, optional-drop-with-diagnostic, or pinned rejection. Strict mode never
  drops required state.
- [ ] Canonically serialize transcript items with a domain/version prefix.
  Provider opaque bytes remain separate and byte-exact.
- [ ] Run `go test -race ./state`. Expected: PASS.
- [ ] Commit: `feat(state): model immutable portable and pinned continuation`.

### Task 6: Implement memory continuation and blob stores

**Files:**

- Create: `storage/memory/options.go`
- Create: `storage/memory/continuation.go`
- Create: `storage/memory/blob.go`
- Create: `storage/memory/expiry.go`
- Test: `storage/memory/continuation_test.go`
- Test: `storage/memory/blob_test.go`
- Create: `state/contract/store_suite.go`
- Test: `state/contract/store_suite_test.go`

- [ ] Define a reusable state conformance suite covering create-if-absent,
  immutable branches, same-operation idempotency, collision, tenant isolation,
  MAC/digest/schema corruption, BlobRef size/digest, expiry, GC eligibility, and
  concurrent readers/writers.
- [ ] Run the suite against an absent memory factory. Expected: FAIL.
- [ ] Implement bounded maps under a mutex, injected monotonic wall clock,
  immutable copies, deterministic expiry, and content-addressed blobs. Avoid
  background goroutines in unit tests; expose `Sweep(now)`.
- [ ] Add production-mode validation rejecting memory state for replica count
  greater than one or durable continuation requirements.
- [ ] Run `go test -race ./state/... ./storage/memory/...`. Expected: PASS.
- [ ] Commit: `feat(storage): add conformant memory continuation store`.

### Task 7: Add route/continuation attack and property tests

**Files:**

- Create: `routing/planner_fuzz_test.go`
- Create: `routing/continuation_property_test.go`
- Create: `state/canonical_fuzz_test.go`
- Create: `testdata/contracts/security/continuation/*`
- Modify: `Makefile`

- [ ] Generate route lists, class lists, capabilities, and continuation pins.
  Assert output is deterministic, every candidate is configured/authorized,
  every requested feature is native/emulated as allowed, and no opaque state
  crosses its pin.
- [ ] Add malicious handle/record fixtures: cross-tenant IDs, key enumeration,
  malformed lengths, swapped blob, unknown media, extreme nesting, and
  incompatible endpoint lineage.
- [ ] Run tests before adding any missing checks. Expected: at least the new
  attack/property cases fail against the current implementation.
- [ ] Fix only the owning validation layer; do not add adapter/provider checks
  into the router.
- [ ] Add `make routing-contracts` and 60-second fuzz commands.
- [ ] Run `make routing-contracts && go test -race ./routing ./state ./storage/memory`.
  Expected: PASS.
- [ ] Commit: `test(routing): harden route and continuation invariants`.

## Phase exit

- [ ] Run `go test -race -count=100 ./routing ./state ./storage/memory`.
  Expected: PASS without flakiness.
- [ ] Run continuation and planner fuzz targets for 60 seconds each.
- [ ] Inspect all exported routing/state types for SDK, Redis, Temporal, or
  secret dependencies. Expected: none.
- [ ] Confirm route golden files are byte-identical across two runs.
- [ ] Run `make verify`. Expected: PASS.
