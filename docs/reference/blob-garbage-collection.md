# Blob garbage-collection contract

Blob metadata is retained in PostgreSQL while the encrypted locator is used by
the external object store. Cleanup is therefore a fenced, two-phase state
machine rather than a direct `DELETE`:

1. A bounded maintenance pass marks expired, unreferenced `retained` rows as
   `eligible` using `FOR UPDATE SKIP LOCKED`.
2. A deleter claims an eligible row (or an explicit outbox target) and changes
   it to `deleting`. The claim query repeats every reference check while the
   row is locked.
3. The deleter performs the object-store operation outside the SQL
   transaction, then calls finalize to change `deleting` to `deleted`.

The reference recheck protects request and result blobs held by non-terminal
operations, terminal operations inside their retention window, unexpired
conversation checkpoints, unexpired or non-expiring provider state, ready
response-cache entries, active cache uses through an operation, and active
cache fills. A tombstoned cache row is logically dead and does not by itself
keep the object alive. This is important because cache tombstoning and blob
deletion are intentionally separate outbox work.

`ClaimBlobDeletion` accepts an explicit blob ID for a durable delete intent,
which allows non-expiring cache metadata to be handled safely. An empty ID
list claims previously marked `eligible` rows. Both forms are bounded and
skip rows locked by another maintenance worker.

`FinalizeBlobDeletion` is idempotent: an already deleted or missing metadata
row succeeds. Consequently an object-store `not found` response can be
acknowledged without retrying forever. A row in `retained` or `eligible` cannot
be finalized without first acquiring the `deleting` fence.

The implementation lives in `golang/storage/postgres/blob_gc.go`; the
integration test is skipped unless `LLMTW_POSTGRES_ADDR` is configured and
covers active operation, checkpoint, provider-state, and ready-cache fences,
the lifecycle transition, idempotent completion, and missing-object success.
