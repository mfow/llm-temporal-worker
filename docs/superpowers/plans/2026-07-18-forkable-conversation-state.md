# Forkable Conversation State and Control Plane Implementation Plan

> **For the implementing agent:** Execute tasks in order on a clean HTTPS-backed
> branch. This document is the production contract. Do not redesign the schema,
> cache key, currency boundary, or OCaml query typing while implementing.

**Goal:** Replace repeated full-history inference calls with immutable forkable
checkpoints, exact-response caching, provider-cache affinity, isolated
compaction, resumable provider polling, and typed status/model/credit/budget/
spend queries in Go and the existing OCaml package.

**Architecture:** Redis remains the atomic, low-latency authority for the
complete active budget working set and operational throttles. PostgreSQL stores
the durable operation/budget journal, exact-decimal USD cost facts, checkpoints,
cache entries, provider state, health/inventory, and maintenance state. Normal
budget decisions perform no PostgreSQL reads; Redis can be rebuilt from the
journal only on a full-service cold start or detected non-persistent Redis loss.
Both Redis and the worker-owned PostgreSQL namespace are configurable. The
unreleased **llm.generate.v1** contract is replaced
in place and appends a delta to an
immutable parent. **llm.compact.v1** creates a lossily summarized child with
application tools and structured output disabled. **llm.query.v1** is a closed
tagged union mirrored by an OCaml GADT.

**Primary references:**

- [Conversation checkpoints and compaction](../../architecture/conversation-checkpoints-and-compaction.md)
- [PostgreSQL schema and query protocols](../../architecture/postgresql-state-cache-and-control-plane.md)
- [OCaml conversation and query client](../../architecture/ocaml-conversation-and-query-client.md)
- [ADR 0006](../../decisions/0006-forkable-conversation-checkpoints.md)
- [ADR 0007](../../decisions/0007-postgresql-authoritative-state-and-response-cache.md)
- [ADR 0008](../../decisions/0008-resumable-provider-operations-and-typed-queries.md)

## Global constraints

- Do not modify or overlap any Temporal-owned PostgreSQL relation. Support a
  configurable worker database, schema, and relation-name prefix. Recommend a
  dedicated database and schema, but test shared-server, shared-database, and
  explicitly prefixed shared-schema layouts. Keep worker roles, backup policy,
  and readiness checks explicit.
- PostgreSQL and public v1/OCaml money are exact USD decimals with 18
  fractional digits. No float, generic currency field, or externally injected
  FX value is accepted.
- Redis and PostgreSQL remain required and own different budget roles. Redis
  atomically decides against active windows; PostgreSQL receives idempotent
  journal/projection writes before dispatch and is the exceptional rebuild
  source. Never claim a cross-store transaction.
- A joining/single-restarted worker, budget status query, Stream gap, and normal
  admission/finalization must perform zero PostgreSQL budget reads. Such reads
  are allowed only after Redis proves zero live worker leases (full-service cold
  bootstrap) or a verified new Redis process/dataset incarnation lacks a
  complete persisted generation. Same-incarnation partial loss fails closed
  without reading PostgreSQL.
- Normal ongoing throughput is assumed to remain vastly below 100 new logical
  LLM requests/second. This is a design/load-test envelope, not a runtime cap or
  configuration field. Large occasional batches queue in Temporal and drain
  under configured concurrency; do not shard Redis for hypothetical scale.
- Operation replay, worker exact-response cache, and provider prompt cache
  remain three separate mechanisms.
- Cache opt-in requires **max_age_seconds**. Omission means neither read nor
  populate.
- Variant is non-negative int32. A positive variant requires materialized
  temperature explicitly greater than zero and is never sent as provider seed.
- Provider identity is excluded from the exact-cache key only through a
  certified model-equivalence class. Unknown quantization is isolated.
- The same checkpoint may have arbitrarily many immutable children.
- Provider poll IDs are persisted before polling. Restart resumes the ID and
  never submits again.
- A compaction request has no application tools, tool choice is none, and no
  application structured-output configuration. The subsequent Generate
  restores those settings.
- Every completed Generate, Compact, Query, and cache hit has an explicit
  exact-or-unknown cost state. Exact uses **actual_cost_usd NUMERIC(38,18)**;
  unknown uses NULL plus a safe reason. Only confirmed free work is exact zero.
- Provider SDK automatic retries remain zero. Never convert the external
  acceptance/persistence gap into an exactly-once claim.
- Prompt, output, tool data, provider IDs, raw errors, cache fingerprints, and
  tenant identifiers stay out of logs/metrics/traces.
- Begin each behavior task with a failing test and finish with a focused commit.
- This is a pre-release contract replacement. Do not add a `v2`, compatibility
  adapter, data import, backfill, table rename, dual-read, dual-write, legacy
  fallback, or migration plan for this change. Initialize clean PostgreSQL and
  Redis namespaces and replace the unreleased v1 fixtures/contracts atomically.
  After the first release, any incompatible change must receive a separate,
  explicit migration and compatibility design.

## Branch and dependency preparation

- [ ] Run **git status --short**, **git remote get-url origin**, and
  **git branch --show-current**. Stop for unrelated changes.
- [ ] Fetch/rebase from
  **https://github.com/mfow/llm-temporal-worker.git master**.
- [ ] Confirm merged PR 109's OCaml continuation/expiry validation remains in
  the implementation base, and inspect any later OCaml validation PRs. Do not
  overwrite stronger codecs or nominal-ID invariants.
- [ ] Inspect repository-layout PR 110 or its successor. If its Go-worker move
  has landed, mechanically prefix the Go file paths in Tasks 1-16 and 19-21
  with **golang/** and run Go/Make commands from that module as documented by
  the landed layout. This path adjustment does not reopen any design decision.
- [ ] Record current Go, OCaml, PostgreSQL, Temporal, Redis, and provider SDK
  baselines in the implementation PR.
- [ ] Add only reviewed current security-patched PostgreSQL client and
  exact-decimal dependencies. Record checksums and licenses.
- [ ] Run **make verify** and the existing OCaml Dune test from a clean base.
  Record unrelated baseline failures before changing code.

---

### Task 0: Make the existing Redis key namespace configurable

**Files:**

- Modify: **config/types.go**, **config/validate.go**, **config/load.go**
- Modify: **api/schema/v1/config.schema.json**
- Modify: **internal/runtime/factory.go**, **storage/redis/options.go**
- Modify: **deploy/docker-compose.yml**, **deploy/kubernetes/**
- Test: **config/redis_prefix_test.go**
- Test: **storage/redis/prefix_isolation_test.go**
- Test: **internal/runtime/factory_redis_prefix_test.go**

- [ ] Add **state.redis.key_prefix**, default **llmtw**, and the direct
  environment override **LLMTW_REDIS_KEY_PREFIX**. Apply direct non-secret
  environment overrides before validation and effective-config rendering.
- [ ] Accept only **[A-Za-z0-9][A-Za-z0-9._-]{0,63}**. Reject empty, colon,
  braces, whitespace, control characters, overlength values, and any value
  that would make a Redis Cluster hash tag or ACL pattern ambiguous.
- [ ] Construct one validated **KeyOptions** in the runtime factory and pass it
  to admission, continuation, throttling, replay, and every other worker-owned
  Redis store. Remove literal `llmtw` fallbacks from individual factories.
- [ ] Preserve the existing Redis Cluster hash-tag placement inside the
  prefix. Do not treat the prefix as a tenant boundary, authentication control,
  or replacement for tenant/project hashing.
- [ ] Render the effective prefix in **print-effective-config**, readiness
  diagnostics, Compose, and Kubernetes examples. Document the required ACL as
  `~<key-prefix>:*` while keeping Redis function-library naming and
  lifecycle a separately reviewed global concern.
- [ ] Run the complete Redis conformance suite twice against one Redis server,
  using two prefixes, and prove replay, budgets/throttling, continuation,
  cleanup, and expiry cannot observe or delete the other namespace.
- [ ] Treat the prefix as immutable for the worker process lifetime. A changed
  prefix selects a clean namespace; do not copy, rename, scan, dual-read,
  dual-write, or fall back to the old prefix.
- [ ] Commit: **feat(config): make redis key namespace explicit**.

### Task 1: Freeze the initial v1 JSON contracts and canonical fixtures

**Files:**

- Modify: **api/schema/v1/generate-request.schema.json**
- Modify: **api/schema/v1/generate-response.schema.json**
- Create: **api/schema/v1/compact-request.schema.json**
- Create: **api/schema/v1/compact-response.schema.json**
- Create: **api/schema/v1/query-request.schema.json**
- Create: **api/schema/v1/query-response.schema.json**
- Modify: **llm/testdata/v1/**
- Modify: **llm/schema/**, **activity/types.go**, **activity/payload.go**
- Test: **llm/schema/v1_contract_test.go**
- Test: **activity/v1_payload_test.go**

- [ ] Write fixtures for root/delta/fork, every Set/Clear/omitted patch,
  cache omitted/present, int32 boundaries, positive variant with zero/positive/
  unknown temperature, each cache disposition, Compact cache omitted/present,
  Compact positive-variant rejection, and every Query tag.
- [ ] Add negative fixtures for unknown fields/tags, mismatched query result,
  currency fields, numeric rather than string USD, tool/structured-output fields
  on Compact, oversized pages/cursors, and transcript fields in Generate responses.
- [ ] Define canonical decimal strings with 0 to 18 fractional digits on input
  and one normalized representation on output.
- [ ] Define **llm.generate.v1**, **llm.compact.v1**, and **llm.query.v1**
  constants. Keep request and result schemas independently versioned.
- [ ] Replace the existing unreleased Generate v1 schema and fixtures in place.
  Do not create an `api/schema/v2` tree, a v2 Activity name, or compatibility
  codecs for the superseded pre-release shape.
- [ ] Run focused schema/payload tests. Expected: fail before types/codecs exist.
- [ ] Implement only schema parsing and closed wire records; do not dispatch.
- [ ] Run **make schema-verify** and canonical JSON tests. Expected: pass.
- [ ] Commit: **feat(contract): define checkpoint cache compact and query APIs**.

### Task 2: Replace generic currency/microUSD authority with exact USD decimal

**Files:**

- Modify: **pricing/decimal.go**, **pricing/money.go**, **pricing/cost.go**
- Modify: **pricing/entry.go**, **pricing/catalog.go**
- Modify: **budget/**, **admission/**, **llm/response.go**
- Modify: **api/schema/v1/generate-response.schema.json**
- Test: **pricing/usd_decimal_test.go**
- Test: **pricing/usd_decimal_property_test.go**

- [ ] Define a non-negative fixed-scale USD decimal backed by checked big
  integer/rational arithmetic and capped to **NUMERIC(38,18)**.
- [ ] Write parse/add/subtract/multiply/divide/round-trip/overflow tests,
  including values smaller than one microUSD, 18 fractional digits, $1, $10,
  the maximum 20-whole-digit value, and one-digit overflow.
- [ ] Prove no float conversion in price, estimate, reconciliation, JSON, SQL,
  metrics input, or query aggregation. Add a source-policy test for forbidden
  money float/currency fields.
- [ ] Rename public/database concepts to **reserved_cost_usd**,
  **incurred_cost_usd**, **actual_cost_usd**, and **limit_usd**.
- [ ] Define exact/unknown cost variants. NULL is unknown; zero is known free.
  Keep estimates, reservations, retained budget bounds, and actual cost as
  distinct types/columns. Never coalesce unknown actual cost to zero.
- [ ] Make catalog component prices nullable with exact/partial/unknown status,
  closed unknown-component codes, and a safe reason. Only an exact catalog can
  drive catalog costing or monetary admission.
- [ ] Remove generic **currency** from price compilation and downstream Cost.
  Remove **pricing.currency** configuration and v1/OCaml response currency in
  the same contract replacement.
- [ ] Rewrite the existing pre-release fixtures at the same time and delete the
  integer-microUSD and generic-currency codecs. Do not retain a conversion
  adapter or legacy read path.
- [ ] Run **go test -race ./pricing ./budget ./admission ./llm/...**.
- [ ] Commit: **refactor(money): make exact usd decimal authoritative**.

### Task 3: Add the initial PostgreSQL schema and physical namespace contract

**Files:**

- Create: **storage/postgres/schema/000001_worker_state.sql**
- Create: **storage/postgres/install_schema.go**
- Create: **storage/postgres/namespace.go**
- Create: **storage/postgres/schema_contract_test.go**
- Create: **storage/postgres/index_contract_test.go**
- Create: **storage/postgres/testdb_test.go**
- Modify: **go.mod**, **go.sum**, **Makefile**, CI service configuration

- [ ] Add configuration and direct environment overrides for
  **state.postgres.database/LLMTW_POSTGRES_DATABASE**,
  **state.postgres.schema/LLMTW_POSTGRES_SCHEMA**, and
  **state.postgres.table_prefix/LLMTW_POSTGRES_TABLE_PREFIX**. Apply them before
  validation and print the effective non-secret selection.
- [ ] Validate database/schema as **[a-z][a-z0-9_]{0,62}** and the table prefix
  as empty or **[a-z][a-z0-9_]{0,22}_**, with a 24-byte maximum. Reject quoted,
  mixed-case, overlength, control-character, and unqualified values.
- [ ] Build every identifier through one namespace object and safe driver
  identifier quoting. Never use `search_path`, string concatenation, or an
  Activity-supplied identifier. Check **current_database()** against the
  configured database before schema installation and at readiness.
- [ ] Transcribe the final DDL from the PostgreSQL architecture document,
  including every check, unique key, FK, partial/covering/BRIN index, fillfactor,
  and deferred cycle FK. Apply the configured prefix to every worker-owned
  table, index, sequence, constraint, view, function, and schema-version object.
- [ ] Make schema-owner, runtime, and maintenance role grants explicit. Runtime cannot
  update/delete checkpoint tables; maintenance is separate.
- [ ] Add a prefixed schema-contract/version marker for clean installation and
  readiness verification. Startup verifies the exact expected contract but
  never mutates schema.
- [ ] Test a clean install, idempotent contract check, least-privilege runtime
  operations, and destructive test-only teardown in an ephemeral database.
- [ ] Query **pg_catalog** to assert exact numeric precision/scale, FK support
  indexes, partial predicates, uniqueness, fillfactor, and no money/float type.
- [ ] Assert the budget schema exactly includes the journal identity/unique
  event keys, window/bucket/journal rebuild covering index, operation/journal
  index, journal-time BRIN, one-active-generation partial unique index,
  generation-history covering index, bucket projection primary/retention
  indexes, reservation window covering index, and open-reservation partial
  index, plus the generation and last-journal foreign-key support indexes,
  specified in the architecture DDL. Do not substitute a broad JSONB or
  standalone low-cardinality index.
- [ ] Assert operations have bounded canonical request JSONB and cache entries
  have canonical manifest JSONB, with no broad JSONB index; fixed-size
  HMAC-SHA-256 columns own lookup.
- [ ] Test dedicated database/schema, a shared server, a shared database with a
  dedicated schema, and an explicitly prefixed shared schema. Reject any
  computed worker object name that collides with a configured Temporal-owned
  relation; do not require the database itself to differ.
- [ ] Assert all generated identifiers fit PostgreSQL's 63-byte limit without
  truncation and are collision-free for the complete schema object catalog.
- [ ] Do not implement a data importer, backfill, Redis scan, relation rename,
  dual storage, or upgrade runner. This task installs the first schema into a
  clean selected namespace. A future post-release schema change will need a
  separately designed migration mechanism.
- [ ] Add **make postgres-integration** and a CI PostgreSQL service.
- [ ] Run schema/index tests against the pinned PostgreSQL image.
- [ ] Commit: **feat(postgres): add authoritative worker schema**.

### Task 4: Implement PostgreSQL repository foundation

**Files:**

- Create: **storage/postgres/pool.go**
- Create: **storage/postgres/transaction.go**
- Create: **storage/postgres/scope.go**
- Create: **storage/postgres/configuration.go**
- Create: **storage/postgres/blob.go**
- Create: **storage/postgres/codec.go**
- Create: **storage/postgres/crypto.go**
- Test: **storage/postgres/foundation_integration_test.go**
- Test: **storage/postgres/crypto_test.go**

- [ ] Add bounded pgx pool configuration, TLS verification, UTC session setup,
  statement/lock/idle transaction timeouts, health checks, and redacted errors.
- [ ] Implement UUIDv7 and keyed tenant/project HMAC derivation with rotation
  version in application configuration.
- [ ] Envelope-encrypt locators/provider references with authenticated context;
  test wrong scope/key/tamper and rotation.
- [ ] Add exact NUMERIC encode/decode that rejects rounding, exponent ambiguity,
  negative values, NaN, infinity, excess scale, and overflow.
- [ ] Implement scope/config/blob repositories using statements compiled from
  the validated namespace, fully qualified identifiers, positional values, and
  context deadlines. Test that values can never alter identifiers.
- [ ] Add fault injection at Begin/Commit/network loss and ensure no transaction
  remains open across blob/provider I/O.
- [ ] Run **go test -race ./storage/postgres -run Foundation**.
- [ ] Commit: **feat(postgres): add scoped repository foundation**.

### Task 5: Move operation replay, attempts, results, and cost to PostgreSQL

**Files:**

- Create: **storage/postgres/operation.go**
- Create: **storage/postgres/attempt.go**
- Create: **storage/postgres/result.go**
- Modify: **admission/store.go**, **admission/types.go**
- Modify: **storage/conformance/conformance.go**
- Test: **storage/postgres/operation_integration_test.go**
- Test: **storage/postgres/operation_concurrency_test.go**

- [ ] Extend conformance for operation kinds, exact decimal costs, provider
  pending, multiple attempts, scoped conflict, result digest, and expiry.
- [ ] Implement unique-key Begin with digest conflict and completed replay.
- [ ] Store the complete normalized Generate v1 Activity request JSONB and verify it with
  the request-fingerprint HMAC on operation-key conflict.
- [ ] Implement compare-and-set state/lease transitions and documented lock
  order. Only provably pre-write expired leases can be reclaimed.
- [ ] Persist each route attempt rather than overwriting only final route facts.
- [ ] Enforce completed implies result and a valid exact-or-unknown USD cost
  state. Exact requires amount/method; unknown requires NULL amount/method plus
  a safe reason. Cache/query zero is explicit exact zero.
- [ ] Test 100 concurrent same-key Begins, different-digest conflict, crash
  before/after commit, terminal expiry lookup, and no sensitive SQL/log output.
- [ ] Replace the old interchangeable-store conformance assumption: operation
  replay/result/cost conformance targets memory and PostgreSQL; Redis receives
  the separate active-budget/throttle conformance in Task 6.
- [ ] Commit: **feat(postgres): persist operations attempts results and cost**.

### Task 6: Split atomic Redis budgets from the PostgreSQL durable journal

**Files:**

- Create: **storage/postgres/budget_journal.go**
- Create: **storage/redis/budget_manifest.go**
- Create: **storage/redis/budget_stream.go**
- Create: **storage/redis/budget_bootstrap.go**
- Modify: **storage/redis/function/**
- Modify: **budget/policy.go**, **budget/window.go**
- Modify: **admission/transition.go**
- Test: **storage/redis/budget_exact_decimal_test.go**
- Test: **storage/redis/budget_generation_integration_test.go**
- Test: **storage/postgres/budget_journal_integration_test.go**
- Test: **integration/budget_cross_store_recovery_test.go**
- Test: **integration/budget_postgres_read_policy_test.go**

- [ ] Persist compiled policies/windows from the immutable configuration
  snapshot with exact **limit_usd** in PostgreSQL and materialize the complete
  active horizon in Redis, including explicit zero bucket fields.
- [ ] Encode every Redis amount as a zero-padded 38-digit unsigned integer
  scaled by `10^18`. Implement and property-test lexicographic compare and
  checked digit-wise add/subtract inside the Redis Function. Forbid converting
  a full money value through Lua/Go floating point or `tonumber`.
- [ ] Add the active-generation pointer, manifest, policy/window hashes,
  operation reservation index, **budget:events** Stream, and **budget:workers**
  leases under the configured prefix/hash tag. Validate generation, manifest,
  complete coverage, every expected member, config/price version, and operation
  identity inside each atomic Function.
- [ ] Make one Function atomically check all matching monetary/request/token/
  concurrency windows, acquire the idempotent reservation, and XADD the change
  event. Reconcile/release through equally idempotent Functions. Use Redis
  `TIME`; do not hold a PostgreSQL transaction during a Redis call.
- [ ] Have every worker tail the Stream independently with `XREAD`, storing its
  cursor in its lease. Do not use one consumer group. A gap discards local hints
  and reloads Redis only; local state may reject early but never authorize.
  Trim behind the minimum non-expired worker cursor plus a safety margin.
- [ ] Give each Go process a random in-memory session ID and persist a bounded
  roster entry in the Redis generation. A still-running process reconnecting
  after a persistent Redis outage presents the same session and never triggers
  a PostgreSQL budget read even if its lease expired. New processes get new
  sessions; do not treat a pod/host name as stable identity.
- [ ] After Redis acceptance, use one PostgreSQL write transaction to insert the
  idempotent journal event and conditionally update bucket/reservation
  projections via `INSERT ... ON CONFLICT DO NOTHING RETURNING`. Dispatch only
  after commit. A write failure triggers best-effort Redis release and never a
  provider call. Finalization commits journal/cost first, then reconciles Redis.
- [ ] Instrument SQL at the repository boundary and fail tests if normal
  admission, completion, budget status, a joining worker, a single-worker
  restart, or a Stream gap issues a SELECT against any PostgreSQL budget table.
- [ ] Permit PostgreSQL budget reads only when worker leases plus the persistent
  session roster prove zero live/reconnecting Go workers, or a verified new
  Redis process/dataset incarnation lacks a complete persisted generation.
  Track the last verified server run ID/incarnation in each live process and in
  the generation manifest. Same-incarnation partial loss fails closed and
  requires operator restore/restart; it does not read PostgreSQL. Fence one
  bootstrap coordinator; load the active horizon/open
  reservations/journal tail into new generation keys; verify counts/digests;
  take the journal advisory fence for final catch-up; atomically flip the Redis
  generation; let waiters resume from the Stream. Never fall back on one missing
  Redis member during normal service.
- [ ] Test crash at Redis acquire, PostgreSQL journal commit, dispatch,
  PostgreSQL completion, and Redis reconcile; duplicate events; partial Redis
  key loss; FLUSHDB/non-persistent restart; persistent Redis restart; full fleet
  restart; rolling restart; worker lease expiry; Stream trimming/gaps; and
  racing completion during rebuild. Failures may over-reserve, never overspend.
- [ ] Assert accepted accounted charges plus active reservations never exceed
  any window under 100-way concurrency. Run one bounded test at up to 100 new
  logical requests/second plus a separate much larger queued Temporal batch.
  Treat 100/second as deliberately above expected normal throughput; do not
  require headroom beyond it or implement/test a runtime 100-RPS ceiling.
- [ ] Capture indexed **EXPLAIN (ANALYZE, BUFFERS)** only for the exceptional
  active-horizon/journal rebuild queries; no SQL active-window-sum query exists.
- [ ] Commit: **feat(budget): keep atomic redis budgets with durable postgres journal**.

### Task 7: Implement immutable checkpoint graph and materializer

**Files:**

- Create: **state/checkpoint.go**
- Create: **state/settings_patch.go**
- Create: **state/materialize.go**
- Create: **state/model_state.go**
- Create: **storage/postgres/checkpoint.go**
- Modify: **state/handle.go**, **state/continuation.go**
- Modify: **engine/generate.go**, **engine/finalize.go**
- Test: **state/materialize_property_test.go**
- Test: **storage/postgres/checkpoint_integration_test.go**
- Test: **engine/fork_test.go**

- [ ] Model omitted/Set/Clear without pointers that collapse states. Collection
  Set replaces; Clear resets/removes.
- [ ] Write graph tests for root, linear history, N siblings, repeated identical
  deltas with distinct operation keys, depth/size limits, and cross-tenant
  handles.
- [ ] Fix the current continuation gap: materialization includes every ancestor
  delta/response or verified snapshot, not only current request plus output.
- [ ] Persist blobs first, then atomically publish child/result/operation.
  Existing checkpoints never update.
- [ ] Validate tool-call/result frontier across delta boundaries and never allow
  a child to begin inside an unmatched exchange.
- [ ] Add periodic self-contained snapshots and property-test that snapshot
  materialization equals full replay byte-for-byte.
- [ ] Return only new output plus child handle through Activity payloads.
- [ ] Run **go test -race ./state ./storage/postgres ./engine -run
  'Checkpoint|Materialize|Fork'**.
- [ ] Commit: **feat(state): add immutable forkable checkpoint graph**.

### Task 8: Implement certified model equivalence and cache fingerprints

**Files:**

- Create: **internal/catalog/model_equivalence.go**
- Create: **cache/fingerprint.go**
- Create: **cache/canonical.go**
- Create: **storage/postgres/model_equivalence.go**
- Modify: **config/types.go**, **config/validate.go**, route catalog loaders
- Test: **cache/fingerprint_test.go**
- Test: **cache/fingerprint_property_test.go**
- Test: **internal/catalog/model_equivalence_test.go**

- [ ] Add explicit equivalence class/member catalog schema with artifact,
  weights, quantization, tokenizer, chat-template, safety, semantic compiler,
  evidence, and status fields.
- [ ] Require unique isolated identity when quantization or hidden transforms
  are unknown. Never infer equivalence from a model name.
- [ ] Add positive fixtures sharing one model across OpenAI, Azure OpenAI, and a
  verified OpenRouter pass-through. Add negative fixtures for quantization,
  prompt injection, different revision, compiler loss, and provider extensions.
- [ ] Define the fingerprint include/exclude matrix exactly as the architecture
  document. Provider/route/account/region/service class are provenance, not key
  data for a certified class.
- [ ] Domain-separate Generate and Compact fingerprints. Compact uses the same
  opt-in maximum-age contract but accepts only variant zero.
- [ ] HMAC canonical semantic bytes; never store/log raw prompt hashes. Version
  canonicalizer, semantic profile, compiler, compaction prompt, and cache epoch.
- [ ] Persist a bounded canonical cache-request manifest JSONB plus digest for
  audit/match verification. Represent ancestor/large content with immutable
  digests/BlobRefs; never query JSONB on the cache hot path.
- [ ] Property-test map order, JSON spelling, operation key, cache age, route,
  and actor tags do not change the key; every output-affecting setting does.
- [ ] Commit: **feat(cache): certify model equivalence and semantic keys**.

### Task 9: Implement exact-response cache and concurrent fill collapse

**Files:**

- Create: **cache/policy.go**
- Create: **cache/service.go**
- Create: **storage/postgres/cache.go**
- Modify: **engine/generate.go**, **engine/finalize.go**
- Test: **storage/postgres/cache_integration_test.go**
- Test: **engine/cache_test.go**
- Test: **integration/cache_concurrency_test.go**

- [ ] Validate cache omission/presence, bounded positive age, int32 variant, and
  effective temperature rules after inheritance.
- [ ] Keep the cache service operation-kind neutral and require a domain-
  separated result template; Task 11 wires Compact through the same service.
- [ ] Lookup certified model-equivalence keys using completion time for
  freshness. PostgreSQL failure on opt-in fails before a paid call.
- [ ] Implement fill owner/lease without a long transaction. Resolve expired
  owners through operation state before takeover; never assume dispatching safe.
- [ ] On origin completion insert entry/use with count one. On a hit atomically
  insert distinct use, saturate int32 count, update last-used, create a
  cache-replay child/result, complete operation with exact zero.
- [ ] Rehydrate current operation/checkpoint/provenance; never return origin
  operation identity. Drop only provider state certified fork-safe.
- [ ] Test old-but-recently-used retention, stale completion despite fresh last
  use, Activity retry count idempotency, saturation, concurrent miss collapse,
  owner crash at every boundary, variant samples, and cross-provider sharing.
- [ ] Capture cache lookup/fill/GC query plans at representative volume.
- [ ] Commit: **feat(cache): add durable exact response reuse**.

### Task 10: Add provider prompt-cache affinity without weakening routing

**Files:**

- Modify: **state/checkpoint.go**, **routing/types.go**, **routing/planner.go**
- Modify: provider usage lifting in **llm/provider/** adapters
- Create: **routing/affinity.go**
- Test: **routing/affinity_property_test.go**
- Test: provider affinity fixtures under **llm/provider/testdata/**

- [ ] Persist hard pin and ordered soft affinity with provider/endpoint/account/
  region/family/model lineage, HMAC cache key, epoch, usage observations, and
  expiry.
- [ ] Filter authorization/residency/class/capability/health/price/budget before
  moving exact soft-affinity route to the front of its eligible class.
- [ ] Derive stable provider prompt-cache keys from tenant scope, parent
  canonical digest, provider epoch, and model lineage; never raw content IDs.
- [ ] Add route properties proving affinity cannot authorize a route/class,
  bypass an open credit incident, or drop required opaque state.
- [ ] Prove three forks reuse the parent prefix/cache identity.
- [ ] Record cache-read/write tokens separately from worker cache disposition.
- [ ] Commit: **feat(routing): prefer safe provider cache affinity**.

### Task 11: Implement isolated explicit and automatic compaction

**Files:**

- Create: **compaction/policy.go**
- Create: **compaction/generic.go**
- Create: **compaction/trigger.go**
- Create: **compaction/prompt/v1.txt**
- Create: **engine/compact.go**
- Modify: **engine/generate.go**, provider capability catalogs
- Test: **compaction/generic_test.go**
- Test: **compaction/trigger_property_test.go**
- Test: **engine/compaction_recovery_test.go**
- Test: provider-native fixtures

- [ ] Add Compact Activity/engine path that creates an immutable child and
  never returns a normal answer.
- [ ] Strip application tools/tool policy and output format from the internal
  compaction call; force tool choice none and accept bounded plain text only.
- [ ] Preserve original settings unchanged on the checkpoint and restore them
  for the subsequent Generate. Test exact before/after equality.
- [ ] Version generic prompt, policy, summarizer route, retained recent turns,
  output reserve, hysteresis, and materialization thresholds.
- [ ] Never cut unmatched tool call/result, instructions, tools/schemas, open
  tasks, durable facts, citations, or recent-turn window.
- [ ] Run generic compaction as a deterministic durable child operation.
  Crash between compact/generate reuses the completed child/cost.
- [ ] Apply the same opt-in exact cache to explicit Compact. For automatic
  compaction, propagate the parent Generate maximum age but force compaction
  variant zero; omission propagates as omission. A hit creates a new
  compaction child at exact zero cost.
- [ ] Implement provider-native compaction only for a capability fixture proving
  tool/output isolation, artifact reuse/fork/expiry, and complete usage
  aggregation. Otherwise select generic.
- [ ] Pin beta headers and transport restrictions in capability data, not
  adapter conditionals. Initial fixtures must cover Anthropic's
  `compact-2026-01-12` header and Bedrock's `InvokeModel`-only boundary; a
  Bedrock `Converse` route must fail capability selection rather than silently
  omitting compaction.
- [ ] Exact cache lookup precedes automatic compaction; include policy/prompt/
  artifact versions in fingerprint.
- [ ] Commit: **feat(compaction): add isolated durable context reduction**.

### Task 12: Add resumable provider submit/poll adapters

**Files:**

- Modify: **llm/provider/adapter.go**, **llm/provider/error.go**
- Create: **llm/provider/resumable.go**
- Create: **engine/provider_pending.go**
- Modify: **engine/attempt.go**, **activity/heartbeat.go**
- Modify: supported provider adapters and capability profiles
- Test: **llm/provider/contracttest/resumable.go**
- Test: **engine/provider_pending_test.go**
- Test: **integration/temporal/provider_poll_recovery_test.go**

- [ ] Define optional Submit/Poll/RecoverByIdempotencyKey capability with typed
  pending/completed/failed status and safe next-poll guidance.
- [ ] Persist deterministic idempotency key and dispatching boundary before
  submit. Persist encrypted poll ID and **provider_pending** before first poll.
- [ ] Start every retry by reading operation state. Pending means Poll, never
  Submit.
- [ ] Handle crash after acceptance/before ID: recover only through documented
  idempotency lookup; otherwise ambiguous/non-retryable.
- [ ] Bound polling by Activity deadline, provider guidance, poll count, and
  cancellation. Heartbeats contain no poll ID.
- [ ] Crash the worker before submit, after submit before ID persistence, after
  ID commit, during poll, and after provider completion before result commit.
  Assert submission count at most one except the explicitly unrecoverable case,
  which remains ambiguous and still does not resubmit.
- [ ] Commit: **feat(provider): resume durable asynchronous operations**.

### Task 13: Persist provider health, credit incidents, and model inventory

**Files:**

- Create: **control/status.go**
- Create: **control/credit.go**
- Create: **control/inventory.go**
- Create: **storage/postgres/provider_status.go**
- Create: **storage/postgres/inventory.go**
- Modify: **routing/health.go**, provider error classifiers
- Test: **control/status_test.go**
- Test: **storage/postgres/provider_control_integration_test.go**
- Test: provider management API fixtures

- [ ] Convert inference/startup/operator/management observations to a closed
  safe event. Update append-only event and current projection transactionally.
- [ ] Require documented provider code/field or operator evidence for exhausted
  credit/billing issue. Generic 429 remains rate/capacity.
- [ ] Define sticky-clear rules for credit/billing incidents and config epoch.
- [ ] Implement supported provider model listing with pagination; store bounded
  normalized snapshots and safe metadata. Unsupported is explicit.
- [ ] Keep configured logical routes authoritative; discovered models never
  become routable automatically.
- [ ] Add refresh collapse per endpoint and stale/current/complete provenance.
- [ ] Commit: **feat(control): persist provider status credit and inventory**.

### Task 14: Implement typed Query service and Temporal Activity

**Files:**

- Create: **control/query.go**
- Create: **control/provider_status_query.go**
- Create: **control/model_inventory_query.go**
- Create: **control/credit_status_query.go**
- Create: **control/budget_status_query.go**
- Create: **control/spend_summary_query.go**
- Create: **activity/query.go**
- Modify: **activity/activities.go**, **activity/errors.go**
- Test: **control/query_test.go**
- Test: **activity/query_test.go**
- Test: **storage/postgres/query_plan_integration_test.go**

- [ ] Implement the five closed request/result pairs, common provenance, stable
  ordering, bounded page size, and HMAC cursor bound to scope/tag/filter/horizon.
- [ ] Default provider/model/credit/spend queries to persisted reads. Optional
  freshness may call only supported provider management/list APIs, never
  inference.
- [ ] Budget status reads exact current reservations/accounted charges/available
  USD from Redis only and returns generation/manifest/Stream provenance. Reject
  an instant outside Redis coverage; never fall back to PostgreSQL budget
  tables. Spend summary uses completed operation/cost rows, a half-open UTC
  interval, allow-listed dimensions, known exact sum, unknown count, and
  partial/complete status; SQL never coalesces NULL cost to zero.
- [ ] Record every query as an operation/result with exact-or-unknown actual USD
  cost. Use exact zero only for confirmed free stored/control calls.
- [ ] Add authorization tests, tag/result mismatch, cursor tamper/cross-kind/
  expiry, unknown enum, pagination stability, timeout, and refresh collapse.
- [ ] Seed representative data and require intended indexes for the four
  PostgreSQL query shapes; prove budget status uses only Redis. `spend_summary`
  must not read PostgreSQL budget policy/window/bucket/reservation/journal rows.
- [ ] Commit: **feat(activity): add typed control plane queries**.

### Task 15: Wire Generate v1 and Compact Activities end to end

**Files:**

- Modify: **activity/activities.go**, **activity/types.go**
- Modify: **engine/dependencies.go**, **engine/generate.go**
- Modify: **internal/runtime/factory.go**, **internal/runtime/temporal.go**
- Test: **activity/v1_integration_test.go**
- Test: **integration/temporal/conversation_lifecycle_test.go**

- [ ] Register exact three Activity names on the existing task queue.
- [ ] Keep heartbeats/errors/payloads small and redacted. Set limits well below
  current Temporal limits and reverify official limits at implementation time.
- [ ] Order Generate as replay, materialize/validate, authorized equivalence
  cache lookup, compaction decision, route/affinity, atomic Redis budget
  reservation, PostgreSQL journal write, provider state machine, PostgreSQL
  checkpoint/cache/cost finalization, then Redis budget reconciliation.
- [ ] Make cache and query database failures fail closed before inference.
- [ ] Prove Activity input/output size is independent of ancestor transcript
  length for 1, 100, and 10,000-turn synthetic lineages.
- [ ] Test tool-call response followed by tool-result delta and structured-output
  restoration after compaction.
- [ ] Commit: **feat(activity): serve delta conversations and compaction**.

### Task 16: Add worker-owned FX retrieval and normalized USD catalogs

**Files:**

- Create: **pricing/fx.go**
- Create: **pricing/fx_refresh.go**
- Create: **storage/postgres/pricing.go**
- Create: **storage/postgres/fx.go**
- Modify: **internal/runtime/catalog_snapshot.go**
- Modify: **config/types.go**, **config/validate.go**, config schema/examples
- Test: **pricing/fx_test.go**
- Test: **internal/runtime/fx_reload_test.go**

- [ ] Remove externally supplied currency/rate values. Configuration selects an
  allow-listed FX adapter/source, maximum age, refresh policy, and credential
  reference only.
- [ ] Implement exact conversion to USD, observation/source digest, validity,
  staleness, retry, and fail-closed behavior.
- [ ] Persist internal rate snapshots and only normalized USD price entries.
  Never store/report a provider operation price in foreign currency.
- [ ] Preserve unresolved catalog components as NULL with partial/unknown
  status. Do not make a route look free when FX or source pricing is uncertain.
- [ ] Make price/config reload atomic; a failed FX refresh keeps the last still-
  valid snapshot and rejects after expiry.
- [ ] Add fixtures for current USD (no FX), non-USD future input, stale/malformed
  rate, excessive precision, source outage, rotation, and audit linkage.
- [ ] Commit: **feat(pricing): own fx normalization inside worker**.

### Task 17: Extend the OCaml protocol layer

**Files:**

- Modify: **ocaml/llm_temporal_worker/lib/llm_temporal_identifier.ml/.mli**
- Modify: **ocaml/llm_temporal_worker/lib/llm_temporal_models.ml/.mli**
- Modify: **ocaml/llm_temporal_worker/lib/llm_temporal_codec.ml**
- Modify: **ocaml/llm_temporal_worker/lib/llm_temporal_invocation.ml/.mli**
- Modify: **ocaml/llm_temporal_worker/lib/dune**
- Test: **ocaml/llm_temporal_worker/test/test_wrapper.ml**

- [ ] Retain landed PR 109 validation and its nominal ID/error conventions.
- [ ] Implement exact **Usd_decimal.t**, exact/unknown settled-cost variants,
  checkpoint/cursor/equivalence IDs,
  patch/cache/Generate/Compact records, and every Query wire variant/result.
- [ ] Remove public currency/microUSD fields. Prohibit float in money API.
- [ ] Encode Keep by omission, Set/Clear distinctly, decimal as string, variant
  as int32, and all query tags closed.
- [ ] Add three exact Activity descriptors and low-level invoke functions.
- [ ] Consume Go golden fixtures for every positive/negative shape and assert
  canonical round trips.
- [ ] Run Dune build/test with the pinned Temporal SDK.
- [ ] Commit: **feat(ocaml): add conversation compact query and usd protocols**.

### Task 18: Add natural OCaml Conversation and Query GADT APIs

**Files:**

- Create: **ocaml/llm_temporal_worker/lib/llm_temporal_conversation.ml/.mli**
- Create: **ocaml/llm_temporal_worker/lib/llm_temporal_query.ml/.mli**
- Modify: **ocaml/llm_temporal_worker/lib/llm_temporal.ml/.mli**
- Modify: **ocaml/llm_temporal_worker/README.md**
- Create: **ocaml/llm_temporal_worker/test/test_conversation.ml**
- Create: **ocaml/llm_temporal_worker/test/test_query.ml**
- Add downstream compile fixture

- [ ] Implement immutable root/checkpoint/fork/respond/compact values. A success
  returns response plus child conversation; no mutable implicit head.
- [ ] Add natural persistent builders for Settings.Patch and Cache_policy.
- [ ] Implement the five-constructor GADT and safe tag/result matcher without
  **Obj.magic** or unchecked JSON.
- [ ] Keep cursor/result type associated across pagination.
- [ ] Rebuild existing one-shot helpers on a Generate v1 root in the same
  package/facade, replacing the current unreleased wire shape in place.
- [ ] Test three siblings from one parent, no inherited fields on wire, decimal
  exactness, compaction tool/output isolation, query type inference/mismatch,
  and unchanged Temporal errors.
- [ ] Compile the exact end-to-end sample in the OCaml architecture document as
  an external package that imports only **Llm_temporal**. The fixture must run
  all five typed Query constructors, a cache-enabled root call, three sibling
  forks from one immutable parent using variants 0/1/2, explicit compaction,
  and a post-compaction Generate that restores tools and structured output.
- [ ] Type-check both the natural facade and the low-level Activity descriptor
  examples. Assert the facade returns Temporal futures that can be composed
  with **Temporal.Future.all** without a hidden mutable conversation head.
- [ ] Commit: **feat(ocaml): expose immutable conversations and typed queries**.

### Task 19: Compose Redis budgets with PostgreSQL durable state

**Files:**

- Modify: **internal/runtime/factory.go**, dependency probes/reload
- Modify: **config/** and **api/schema/v1/config.schema.json**
- Modify: **compose.yaml**, **deploy/local/**, **deploy/kubernetes/**
- Modify: **Dockerfile**, CI workflows, **config.example.yaml**
- Update: all affected docs/reference/deployment files
- Test: runtime, Compose, Kubernetes, configuration policy tests

- [ ] Add worker PostgreSQL addresses/TLS/pool/database/schema/table-prefix/key
  references and the exact direct environment overrides defined in Task 3.
  Reject only namespace selections whose computed relations overlap
  Temporal-owned relations; do not reject a safely isolated shared server or
  shared database.
- [ ] Start only after PostgreSQL transaction/schema/index/blob/key checks and
  Redis PING/TIME/noeviction/persistence/Function/prefix/manifest checks pass.
  Pause new-work polling on loss of either dependency; completed operation
  replay may use a separately bounded path only if its dependencies are healthy.
- [ ] Keep provider availability out of process readiness; it remains routing/
  query state.
- [ ] Make Compose demonstrate the recommended separate Temporal and worker
  databases/roles. Add integration profiles for a shared database with a
  dedicated schema and an explicitly prefixed shared schema. Two worker
  replicas share the selected worker namespace.
- [ ] Keep Redis as a required production service in Compose/Kubernetes with
  AOF+RDB, `noeviction`, TLS/auth, monitored persistence, the configured key
  prefix/ACL, preprovisioned Function digest, and persistent storage. Two worker
  replicas share the same Redis budget generation and Stream.
- [ ] Update Kubernetes secrets/config, network policy, probes, resource
  examples, backup/restore runbook, and graceful shutdown.
- [ ] Switch composition in one commit so Redis owns only active budgets and
  operational throttles while PostgreSQL owns durable operation/journal/
  conversation/cache/control state. Remove only superseded Redis durable
  continuation/result/operation-replay representations; retain Redis Functions,
  configuration, deployment, client, readiness, and budget tests.
- [ ] Do not import Redis data or implement a migration, backfill, relation
  rename, dual-read/write interval, or fallback. This is a clean pre-release
  responsibility split. Redis/PostgreSQL writes are not compatibility
  dual-writes: they record different hot and durable facts.
- [ ] Exercise rolling worker restart (no PostgreSQL budget reads), full-fleet
  cold restart (one fenced PostgreSQL budget rebuild), persistent Redis restart
  (no lost-generation rebuild), non-persistent Redis restart (fenced rebuild),
  and independent PostgreSQL outage.
- [ ] Commit: **refactor(runtime): compose redis budgets with postgres state**.

### Task 20: Add retention, GC, outbox, and database operations

**Files:**

- Create: **maintenance/cache_gc.go**
- Create: **maintenance/retention.go**
- Create: **maintenance/outbox.go**
- Create: **storage/postgres/maintenance.go**
- Modify: metrics/telemetry and deployment operations docs
- Test: **maintenance/retention_integration_test.go**
- Test: **maintenance/outbox_integration_test.go**

- [ ] Implement bounded **SKIP LOCKED** batches and separate maintenance role.
- [ ] Cache eligibility uses last use; old-but-used-yesterday is retained.
- [ ] Recheck child/fill/use/blob references inside deletion transaction.
- [ ] Publish external deletion in outbox and make missing object success.
- [ ] Add status/inventory/budget/operation retention without deleting active
  retry/poll/audit state.
- [ ] Budget retention uses bounded indexed DELETE/UPDATE statements but never
  loads the active budget working set into a running worker. It must preserve
  every row required by the maximum window, open reservation, and allowed cold
  rebuild.
- [ ] When authoritative billing later resolves an unknown operation cost,
  append **resolve_unknown_exact** rather than editing history. In one
  PostgreSQL transaction update operation cost, reservation, bucket projection,
  and journal revision; then idempotently reconcile Redis. Retain the prior
  conservative bound on any failure.
- [ ] Add metrics for eligible/deleted/skipped/failure, dead tuples, pool/lock/
  query latency, cache hit/use/fill, pending polls, exact/unknown cost, and FX age.
- [ ] Load test autovacuum/fillfactor and record table-specific production
  settings instead of guessing.
- [ ] Commit: **feat(maintenance): add safe state and cache retention**.

### Task 21: Prove crash, fork, cache, query, and restore behavior

**Files:**

- Create: **integration/temporal/v1_recovery_test.go**
- Create: **integration/postgres/concurrency_test.go**
- Create: **integration/postgres/query_plan_test.go**
- Create: **integration/postgres/restore_test.go**
- Create: **integration/redis/budget_recovery_test.go**
- Create: **integration/redis/budget_stream_test.go**
- Modify: **docs/testing/strategy.md**, CI workflows, Makefile

- [ ] Run a 10,000-turn synthetic lineage with bounded Activity payloads,
  periodic snapshots, compaction, and three-way forks.
- [ ] Kill workers/database connections at every operation/cache/compaction/
  submit/poll/finalization commit boundary. Count provider submissions.
- [ ] Run 100-way identical cache miss, same-parent fork, same-key replay, and
  overlapping-budget admission under race/deadlock detection.
- [ ] Prove steady-state budget admission/finalization, a joining worker,
  rolling restart, Stream gap, and budget-status queries execute zero
  PostgreSQL budget SELECTs. Use a SQL statement classifier/allowlist in the
  integration fixture, not timing or log inference.
- [ ] Prove a zero-live-worker cold start elects one bootstrap reader and a
  Redis non-persistent loss performs one fenced rebuild; all other workers wait.
  Persistent Redis restart preserves generation, partial loss fails closed, and
  a racing journal writer is applied exactly once across the generation flip.
- [ ] Verify cross-provider certified cache reuse and separate unknown-quantized
  caches.
- [ ] Verify cache zero cost, count once, freshness versus last use, and 180-day
  unused cleanup.
- [ ] Verify all five Go/OCaml queries, pagination, authorization, fresh/stale/
  unsupported states, exact spend decimals, NULL unknown prices/costs, and
  partial spend summaries.
- [ ] Back up PostgreSQL plus blobs and Redis persistence, restore to an
  isolated environment, and replay a completion/resume a pending poll without
  resubmission. Separately prove PostgreSQL can rebuild a deliberately empty
  Redis budget generation during an allowed cold bootstrap.
- [ ] Run security tests for cross-tenant handles/HMACs, SQL injection, cursor
  tamper, encrypted provider IDs, log redaction, KMS rotation, and database role
  denial.
- [ ] Run query-plan tests at representative cardinality and remove only
  empirically unused indexes after updating this design through a superseding
  ADR.
- [ ] Commit: **test(v1): prove durable conversation and control plane recovery**.

## Phase exit

- [ ] **make verify**
- [ ] **make postgres-integration**
- [ ] **make redis-integration**
- [ ] **make integration**
- [ ] **make compose-smoke**
- [ ] **make compose-live-integration** with content-free provider fixtures
- [ ] **make kustomize-verify**
- [ ] OCaml Dune build/test plus external package compile
- [ ] Race tests for state/cache/budget/control/provider packages
- [ ] Focused fuzz/property tests for canonical JSON, patches, decimals,
  checkpoints, cache fingerprints, cursors, and provider state
- [ ] Database schema/index contract and representative EXPLAIN gates
- [ ] Physical-namespace matrix: dedicated database/schema, shared database,
  and prefixed shared schema, with exact object names and no truncation/collision
- [ ] Redis prefix isolation, exact-decimal Function, broadcast Stream,
  generation-loss detection, and cold-rebuild gates
- [ ] SQL instrumentation proves zero PostgreSQL budget reads outside the two
  explicitly allowed cold-recovery conditions
- [ ] Bounded up-to-100-new-logical-request/second sizing run plus a separate
  large queued batch drain; verify the figure is not enforced as a runtime
  limit or treated as expected ongoing throughput
- [ ] Backup/restore proof
- [ ] **git diff --exit-code** after generated fixtures/checks

## Final acceptance traceability

| Requirement | Owning tasks | Required proof |
| --- | --- | --- |
| Delta-only Temporal history | 1, 7, 15 | payload size independent of ancestry |
| Immutable forks | 7, 18, 21 | three concurrent children from one parent |
| Sparse settings | 1, 7, 17 | omitted/Set/Clear cross-language fixtures |
| Exact response cache | 8, 9 | freshness, variants, one fill, count/last use |
| Cross-provider equivalence | 8, 9 | certified shared hit; quantization isolation |
| Provider cache affinity | 10 | preference only after eligibility |
| Explicit/automatic compaction | 11, 15 | durable reuse and isolation/restoration |
| Restart-safe provider polling | 5, 12, 21 | one submit and persisted ID polling |
| Provider/model/credit queries | 13, 14, 18 | closed Go/OCaml tag/result pairs |
| Budget/spend queries | 6, 14, 18 | Redis current budget plus PostgreSQL operation-cost summary |
| All completed costs | 2, 5, 14 | valid exact-or-unknown state; zero only if known |
| Price precision and uncertainty | 2, 3, 14, 17 | sub-micro through $10+, NULL preserved end to end |
| Worker-owned FX to USD | 2, 16 | no downstream currency; versioned rate proof |
| Redis active budget engine | 0, 6, 14, 19, 21 | exact atomic windows, Stream, loss detection, rebuild fencing |
| Near-zero PostgreSQL budget reads | 6, 14, 19, 21 | SQL classifier; cold fleet or verified non-persistent new Redis incarnation are the only exceptions |
| Production PostgreSQL | 3-6, 19-21 | durable journal/state, constraints/indexes, roles, restore |
| Unified OCaml package | 17, 18 | same facade/package and downstream compile |
| Configurable storage namespaces | 0, 3, 4, 19 | Redis prefix isolation and PostgreSQL namespace matrix |
| Pre-release in-place contract | 1, 2, 15, 17-19 | Generate v1 only; no compatibility or data movement |
