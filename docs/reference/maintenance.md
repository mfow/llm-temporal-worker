# Maintenance contract

Maintenance is a bounded, separately operated concern. It is not a Temporal
Activity and it must not run with the worker's `llmtw_runtime` role. Operators
run the maintenance adapter with `llmtw_maintenance`, which is granted the
destructive privileges needed for cleanup while runtime remains append-only
for checkpoints, provider state, and control history.

## Retention passes

`golang/maintenance` exposes a storage-neutral `RetentionPolicy` and
`RetentionStore`. Every pass has an explicit UTC `Now` and a batch `Limit`
(maximum 10,000). A cache row is eligible only when its `last_used_at` is
older than the cache cutoff and it is ready, inactive, has no retained
descendant, and has no active fill. Other resource kinds use their own expiry
horizon. The in-memory adapter rechecks these facts while holding its lock;
PostgreSQL adapters must repeat them in the locked SQL statement.

Cache rows are tombstoned rather than immediately deleted. This preserves the
dedupe boundary and lets the transaction enqueue an external blob deletion.
Rows that are active, recently used, referenced by a retained descendant, or
under an active fill are skipped. A batch never loads the active budget working
set into a worker process.

The policy contract includes status, inventory, operation, budget, and
checkpoint horizons so adapters can add table-specific retention safely. The
current PostgreSQL implementation intentionally exposes only the cache
tombstone path and outbox lifecycle: operation, checkpoint, and budget rows
have restrictive foreign keys and audit/rebuild obligations that must be
handled in their own transaction before a physical delete is enabled.

## Outbox lifecycle

Cleanup publishes a safe event in the same PostgreSQL transaction that marks a
cache entry tombstoned. `Event` payloads are valid JSON containing identifiers
or encrypted locators only; prompt/response bytes and credentials are never
accepted by the contract. `(event_kind, dedupe_key)` is idempotent.

Workers claim at most the requested batch with `FOR UPDATE SKIP LOCKED` in a
short transaction. Pending/failed rows whose `available_at` has arrived and
processing rows whose lease expired are claimable. A live lease is not claimed
twice. Completion and retry update only rows in `processing`; a lost lease or
already-completed row returns an ownership error.

External deletion happens after the transaction commits. A missing object is
success, so retries cannot turn an already-cleaned object into a permanent
failure. Other handler failures move the event to `failed` with a bounded
retry time. `Dispatcher.RunOnce` processes one batch and reports claimed,
completed, missing-object, and retried counts for metrics.

The SQL adapter is deliberately separate from runtime repositories. It uses
the namespace renderer for every relation and rejects unbounded limits,
invalid leases, invalid identifiers, and malformed payloads before issuing
SQL.
