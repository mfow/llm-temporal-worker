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

`golang/storage/postgres` now contains the first adapter for this port:
`DurableCheckpointRepository`. It performs scope-qualified reads, verifies blob
metadata and scope before publication, checks parent depth and origin
operation/cache scope, and writes the checkpoint row plus provider-state and
affinity children through one PostgreSQL unit of work. A retry of the same
immutable row is accepted; a different payload for the same checkpoint ID
returns `ErrCheckpointConflict`.

The adapter deliberately does not claim the full runtime boundary. Blob bytes
must already be durable before `PutCheckpoint`, and operation/result rows are
not published by this unit of work. It does not open blob locators, call a
provider, implement retention, or wire `V1Runtime`, `llm.generate.v1`, or
`llm.compact.v1`. Those integrations still require transaction fault-injection,
schema/ACL, and Activity-level idempotency evidence before production
composition can claim end-to-end durable checkpoint support.
