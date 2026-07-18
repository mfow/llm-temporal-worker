# ADR 0007: PostgreSQL Durable State and Exact-Response Cache

- Status: Accepted design; implementation pending
- Date: 2026-07-18
- Complements: ADR 0004; Redis remains the low-latency throttle and admission accelerator

## Context

Forkable checkpoints, exact-response caching, resumable provider job IDs,
queryable provider status, model inventories, budget/spend reporting, and
precise per-operation cost need transactional relationships and indexed
queries. Those durable relationships belong in one PostgreSQL transaction.
Redis still serves a different, latency-sensitive purpose: distributed
request/token/concurrency throttles and the complete active-window monetary
budget working set. The design must not pretend that Redis and PostgreSQL can
atomically commit one shared fact.

This repository currently uses Redis for the worker's v1 operation,
continuation, result, and budget state. The PostgreSQL service in local Compose
belongs to Temporal and is not a worker application database.

## Decision

Introduce a worker-owned PostgreSQL physical namespace. Its database, schema,
and table prefix are independently configurable. A separate database/schema is
recommended, while a shared server or database is supported when the resulting
relations and grants cannot overlap Temporal-owned objects. The namespace
becomes authoritative for durable operations and budget journal, checkpoints,
response cache, provider status, and inventory state. Redis remains a required
production dependency and is authoritative for atomic admission against the
fully materialized active budget windows, plus request/token/concurrency
throttles. Its independently configurable key prefix applies to every
worker-owned Redis key.

Normal budget admission performs no PostgreSQL budget reads. One Redis Function
validates the namespace generation and complete active-window coverage, checks
every matching window, and atomically creates an idempotent reservation. The
worker then appends/inserts the corresponding durable PostgreSQL operation and
budget-journal facts before provider dispatch. A PostgreSQL write failure means
no dispatch and causes a best-effort Redis release; an unconfirmed release
temporarily over-throttles until TTL. Completion durably updates PostgreSQL
before idempotently reconciling Redis. No provider call is allowed unless both
the Redis reservation and PostgreSQL journal write succeeded.

Every Redis mutation also appends to one namespace-scoped Redis Stream in the
same Function. Workers use that key as the monotonic budget change feed. A
generation manifest, complete-coverage sentinels, per-window bucket sentinels,
and stream high-water mark make missing or partially lost data detectable.
Missing state, failed persistence health, or an unexpected generation fails
admission closed. A worker that joins an already-live fleet validates and reads
only Redis; a stream gap invalidates its local hints and refreshes them from
Redis, not PostgreSQL. A process session ID is kept in memory and in a
persistent Redis generation roster so workers reconnecting after a persistent
Redis outage do not look like a fleet restart even when leases expired.
PostgreSQL budget reads are permitted only when the live worker lease set plus
session roster prove that the entire Go worker fleet has restarted, or when a
verified new Redis process/dataset incarnation has a missing/incomplete
generation because persistence was not retained. Same-incarnation missing or
corrupt state fails closed without a PostgreSQL read. One fenced bootstrap
coordinator then reads only the active budget
horizon and journal tail from PostgreSQL, builds and verifies a new Redis
generation, and atomically switches generations. Other workers wait for the
switch. There are no routine PostgreSQL budget reads, periodic reconciliation
reads, per-worker startup reads, or budget-query reads.

Exact **NUMERIC(38,18)** values are encoded in Redis as fixed-width 38-digit
unsigned scaled-integer strings. The Redis Function uses string comparison and
checked digit-wise addition/subtraction; it never converts a full amount to a
Lua number. This keeps Redis and PostgreSQL exact without exposing an alternate
money representation downstream.

The design capacity envelope assumes normal ongoing throughput remains vastly
below 100 new logical LLM requests per second across all workers. This is not a
runtime limit, budget, configuration field, or rejection rule. Occasional large
batches are expected to create Temporal backlog and drain under the configured
worker/provider concurrency rather than arrive as sustained admission traffic.
The budget design therefore favors one atomic Redis hash slot and one change
stream over speculative sharding. If measured sustained demand approaches or
exceeds this envelope, perform a new architecture review, including whether
Temporal remains the appropriate execution model.

Store USD amounts as **NUMERIC(38,18)**. Do not use PostgreSQL floating-point
types or Go floating-point values for money. The initial public contract names
USD properties directly and has no currency discriminator. The Go worker owns
any future FX retrieval and persists/reports only normalized USD prices and
costs. This replaces integer micro-USD and supports 18 fractional digits plus
20 whole-dollar digits, including sub-micro-dollar prices, $1, $10, and large
aggregate budgets.

Implement an exact-response cache keyed by a versioned semantic fingerprint.
A request opts in by supplying a maximum acceptable cache age; omission means
neither read nor populate the cache. The signed 32-bit **variant** defaults to
zero. A positive variant is valid only when the fully materialized temperature
is explicitly greater than zero. Variant is a cache discriminator and is never
sent to a provider as a random seed.

Cache freshness is based on the entry's completion time. Garbage-collection
eligibility is based on **last_used_at**, so an old but recently used entry is
retained. **use_count** is a signed 32-bit counter, initialized to one for the
origin operation, incremented once for each distinct logical operation served,
and saturated at the maximum value.

Catalog prices and completed-operation **actual_cost_usd NUMERIC(38,18)** are
nullable when the real amount is not known. NULL means unknown; it never means
zero or “not applicable.” A closed status and safe reason preserve that fact.
Exact costs require a method. Estimates, reservations, and conservative budget
charges stay separate. Cache hits and free control-plane queries record exact
zero.

Lookup uses a tenant-scoped HMAC-SHA-256 semantic fingerprint. Operations also
retain their bounded canonical delta request as JSONB, and cache entries retain
a canonical materialized request manifest JSONB plus digest. JSONB is for audit
and verification, has no broad index, and is not the lookup path.

Routes may share a cache entry through an explicitly certified model-equivalence
identity. Provider/endpoint identity remains provenance but is not a key input
for certified-equivalent model artifacts and lowering. Unknown quantization or
hidden provider transforms require isolated cache identities.

## Consequences

- One PostgreSQL transaction can coordinate idempotency, cache use, the durable
  budget-journal event, cost finalization, and child-checkpoint publication.
  The preceding Redis reservation remains a separate atomic operation governed
  by the conservative cross-store protocol.
- Redis retains the fast atomic budget path without becoming the durable audit
  or rebuild ledger. A PostgreSQL journal write remains mandatory before any
  paid dispatch.
- PostgreSQL constraints and explicit indexes become part of the application
  contract and require production capacity planning.
- The pre-release switch is deliberately clean because there is no released
  data. It includes no copying, backfill, dual-read/write, or legacy namespace
  fallback. A future post-release move requires a separate migration design.
- Exact decimal storage preserves sub-micro-dollar amounts and deterministic
  aggregation at the cost of more CPU/storage than fixed-width integers.
- Nullable prices preserve honest uncertainty, but spend summaries must report
  known totals and unknown counts instead of one falsely complete total.
- PostgreSQL writes and Redis are both on the provider-call critical path for
  new paid dispatches, but PostgreSQL budget reads are not. Either dependency
  being unavailable fails closed. Replays of an already completed PostgreSQL
  operation do not consume a new Redis reservation.

## Rejected alternatives

- Keeping exact monetary budgets only in Redis without a durable journal makes
  Redis loss unrecoverable. The accepted design writes a PostgreSQL journal
  before dispatch and detects/fences Redis loss, while preserving Redis as the
  only steady-state active-window read/atomic-admission path.
- Resolving any configured worker relation to a Temporal-owned relation couples
  application state to Temporal upgrades and support boundaries.
- Float/double money loses decimal precision and makes budget boundary behavior
  platform-dependent.
- A cache key based on raw request JSON makes semantically identical requests
  miss and risks including caller-only metadata.
- Updating freshness on every read makes stale entries appear newly produced;
  freshness and retention therefore use separate timestamps.
