# ADR 0007: PostgreSQL Authoritative State and Exact-Response Cache

- Status: Accepted design; implementation pending
- Date: 2026-07-18
- Supersedes: ADR 0004 after the v2 storage cutover

## Context

Forkable checkpoints, exact-response caching, resumable provider job IDs,
queryable provider status, model inventories, budget/spend reporting, and
precise per-operation cost need transactional relationships and indexed
queries. Splitting those invariants between Redis and PostgreSQL would require
an unrecoverable dual-write protocol.

This repository currently uses Redis for the worker's v1 operation,
continuation, result, and budget state. The PostgreSQL service in local Compose
belongs to Temporal and is not a worker application database.

## Decision

Introduce a separate worker-owned PostgreSQL database and schema. At cutover,
it becomes authoritative for all worker operation, budget, checkpoint, cache,
provider-status, and inventory state, including concepts that already exist in
Redis. Temporal's own database remains vendor-managed and untouched. Redis may
later be used as a disposable read-through accelerator, never as an
authoritative or dual-written store.

Store USD amounts as **NUMERIC(38,18)**. Do not use PostgreSQL floating-point
types or Go floating-point values for money. The post-v1 public contract names
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

- One transaction can coordinate idempotency, cache use, budget reservations,
  cost finalization, and child-checkpoint publication.
- PostgreSQL constraints and explicit indexes become part of the application
  contract and require production capacity planning.
- The cutover can be deliberately breaking because the service has no
  production data, but its execution plan still includes validation and
  rollback gates.
- Exact decimal storage preserves sub-micro-dollar amounts and deterministic
  aggregation at the cost of more CPU/storage than fixed-width integers.
- Nullable prices preserve honest uncertainty, but spend summaries must report
  known totals and unknown counts instead of one falsely complete total.
- PostgreSQL is on the provider-call critical path; cache-enabled and
  budget-governed operations fail closed when authoritative state is
  unavailable.

## Rejected alternatives

- Keeping budgets in Redis and costs/cache in PostgreSQL creates cross-store
  atomicity gaps.
- Reusing Temporal's internal PostgreSQL schema couples application migrations
  to Temporal upgrades and support boundaries.
- Float/double money loses decimal precision and makes budget boundary behavior
  platform-dependent.
- A cache key based on raw request JSON makes semantically identical requests
  miss and risks including caller-only metadata.
- Updating freshness on every read makes stale entries appear newly produced;
  freshness and retention therefore use separate timestamps.
