# ADR 0004: Redis Shared State

- Status: Accepted
- Date: 2026-07-13

> This remains part of the initial-release design. ADR 0007 narrows Redis to
> the complete active budget/throttle working set and adds PostgreSQL durable
> state/journaling; it does not retire Redis.

## Context

Horizontal workers must share overlapping-window budget reservations and
operational throttles. Admission must check and update all matching windows
atomically. Durable operation deduplication and continuation records live in
PostgreSQL under ADR 0007; the live admission access pattern remains small
key-value state with time-indexed buckets.

## Decision

Use Redis as the production atomic budget/throttle backend and an in-memory
reference model only for deterministic unit/conformance tests. The in-memory
model is not a configurable production or local-stack replacement. Implement admission as a
versioned Redis Function (Lua script fallback), use Redis server time, exact
fixed-width 38-digit scaled-integer strings for **NUMERIC(38,18)** USD, and one
configured cluster hash tag for all budget keys. The active generation includes
complete bucket coverage, reservation indexes, a manifest, worker leases, and
one broadcast Redis Stream for budget changes. Every worker-owned key also
uses one validated configurable prefix, defaulting to **llmtw** and overridable
by **LLMTW_REDIS_KEY_PREFIX** at process start.

PostgreSQL stores the durable operation/budget journal before provider dispatch,
but normal budget decisions and status queries read Redis only. PostgreSQL
budget reads are reserved for a full-fleet cold bootstrap or rebuilding a
missing/incomplete generation after a verified new Redis incarnation lost
persistence, as specified in ADR 0007. Same-incarnation corruption fails closed
without a PostgreSQL read.

Production fails closed on Redis errors. Require TLS/auth, `noeviction`, explicit
AOF durability choice plus RDB, persistence monitoring, backups, and restore
tests.

## Consequences

- Admission is atomic and low latency across replicas.
- The in-memory reference model and Redis Function share one atomic-budget
  state-transition conformance suite; production authorization always uses Redis.
- The single budget hash slot is intentional. The sizing envelope assumes
  normal ongoing traffic vastly below 100 new logical requests/second, with
  occasional large batches queued in Temporal. The 100/second figure is only
  the upper design/test envelope and is not enforced.
- `appendfsync everysec` has an acknowledged crash-loss window; stronger
  durability costs latency.
- Durable blobs still require a separate object-store port at larger sizes.

## Rejected alternatives

- Per-process counters allow aggregate overspend.
- Eventual counter reconciliation does not enforce a hard budget.
- Multiple Redis Cluster slots cannot provide the required v1 atomic Function.
- Using worker clocks creates cross-replica window disagreement.
