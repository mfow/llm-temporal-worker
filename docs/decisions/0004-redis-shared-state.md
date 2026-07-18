# ADR 0004: Redis Shared State

- Status: Accepted
- Date: 2026-07-13

> This remains the current v1 decision. ADR 0007 supersedes it only when the
> post-v1 PostgreSQL cutover is implemented and its acceptance gates pass.

## Context

Horizontal workers must share operation deduplication, overlapping-window
budget reservations, and continuation records. Admission must check and update
all matching windows atomically. A database transaction is possible, but the v1
latency/access pattern is small key-value state with time-indexed buckets.

## Decision

Use Redis as the v1 production state backend and an in-memory implementation for
tests/single-process development. Implement admission as a versioned Redis
Function (Lua script fallback), use Redis server time, integer microUSD, and one
configured cluster hash tag for all admission keys.

Production fails closed on Redis errors. Require TLS/auth, `noeviction`, explicit
AOF durability choice plus RDB, persistence monitoring, backups, and restore
tests.

## Consequences

- Admission is atomic and low latency across replicas.
- Memory and Redis share one black-box conformance suite.
- The single admission hash slot is an intentional v1 scaling ceiling.
- `appendfsync everysec` has an acknowledged crash-loss window; stronger
  durability costs latency.
- Durable blobs still require a separate object-store port at larger sizes.

## Rejected alternatives

- Per-process counters allow aggregate overspend.
- Eventual counter reconciliation does not enforce a hard budget.
- Multiple Redis Cluster slots cannot provide the required v1 atomic Function.
- Using worker clocks creates cross-replica window disagreement.
