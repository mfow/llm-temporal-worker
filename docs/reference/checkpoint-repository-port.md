# Durable checkpoint repository port

`golang/state` now defines the storage-neutral contract for the next durable
checkpoint slice. `DurableCheckpoint` is an immutable row-shaped DTO: it
contains scope and lineage identity, typed operation/cache/blob references,
version and compiler metadata, content digests, provider-state metadata, and
retention timestamps. Prompt, output, settings-patch, and provider-state bytes
are represented only by blob references; this contract never exposes a blob
locator or plaintext payload.

`CheckpointRepository` provides scoped reads and starts a
`CheckpointUnitOfWork`. The unit of work accepts a validated
`CheckpointWrite`, then commits or rolls back the short publication
transaction. `WithCheckpointUnitOfWork` owns that lifecycle and preserves a
callback error when rollback also fails. Repository implementations must keep
blob/provider I/O outside the transaction and must enforce scope filtering,
immutable create-if-absent semantics, parent/depth constraints, and operation
idempotency.

`CheckpointMaterializer` returns the existing `state.MaterializedState` view,
so a durable adapter and the in-memory `CheckpointGraph` share one replay
contract. The DTO's `Validate` method and `CanonicalDigest` define the common
version, digest, ordering, and timestamp checks. Expired rows remain valid
history; a materializer decides whether a caller may use one at its read time.

This is intentionally only a port and contract slice. It does not implement
PostgreSQL persistence, blob publication, retention, or `V1Runtime`, and it is
not wired into `llm.generate.v1` or `llm.compact.v1`. Those integrations must
be delivered separately with transaction fault-injection tests, schema/ACL
evidence, and Activity-level idempotency tests before production composition
can claim durable checkpoint support.
