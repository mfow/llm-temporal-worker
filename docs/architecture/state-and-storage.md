# State and Storage

## Storage responsibilities

V1 persists three kinds of state behind separate domain ports:

| State | Mutable | Contains model content | Atomicity requirement |
| --- | --- | --- | --- |
| Operation ledger and budget buckets | state-machine updates | no; completed result is a reference | one transaction across operation and all matching windows |
| Continuation records | immutable after creation | canonical transcript/provider state or BlobRefs | create-if-absent by child ID |
| Result/blob objects | immutable | possibly | digest-verified put/get |

Redis implements operation, budget, and continuation state. A blob-store port
handles payloads that exceed safe Redis or Temporal inline limits; filesystem is
development-only and object storage is the production example.

The `storage/redis` implementation uses the official go-redis v9 client and
one embedded, versioned Lua Function for each admission mutation. Every key
touched by a mutation is supplied explicitly and carries the configured
`{admission}` hash tag. A transport error is never retried blindly; the caller
reads the operation or continuation index to resolve whether the write
committed. Offline command/function harnesses exercise the same store ports
without requiring a Redis daemon. The live gate remains a pinned Redis
integration run with Functions enabled and persistence settings matching the
deployment profile.

Budget hashes receive a Redis TTL only when the operation has an explicit
expiry; the TTL is the operation retention plus the longest matching window.
This keeps every bucket needed by an in-flight operation visible while
allowing expired windows to be collected. Operations without an expiry are
retained until the configured Redis retention/GC policy removes them.

## Operation record

```go
type Operation struct {
	ID                    string
	ScopeKey              string
	ExternalKeyHash       [32]byte
	RequestDigest         [32]byte
	State                 OperationState
	ReservedMicroUSD      pricing.MicroUSD
	IncurredMicroUSD      pricing.MicroUSD
	FinalMicroUSD         pricing.MicroUSD
	Reservations          []WindowReservation
	MatchedWindowsDigest  [32]byte
	ConfigVersion         string
	PriceVersion          string
	Attempt               AttemptFacts
	ResultRef             *state.BlobRef
	LeaseUntil            time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ExpiresAt             time.Time
}
```

`AttemptFacts` holds route IDs, provider request IDs, tier values, and dispatch
observation; it contains no prompt or output text. Legal state transitions are
validated in both Go and the Redis Function. A terminal state cannot return to a
nonterminal state.

External operation keys are HMACed for Redis key construction. The original key
is not logged or stored unless an operator explicitly chooses a safe identifier.

## Continuation record

```go
type Continuation struct {
	ID                 string
	TenantHash         [32]byte
	ParentID           string
	TranscriptRef      BlobRef
	TranscriptDigest   [32]byte
	ProviderState      []OpaqueStateRef
	Pinning            Pinning
	LastOperationID    string
	CapabilityVersion  string
	CreatedAt          time.Time
	ExpiresAt          time.Time
}
```

The transcript serialization is canonical, versioned, and content-addressed.
Provider-state entries preserve provider, endpoint family/account, media type,
digest, and exact bytes. A store read verifies schema version, tenant binding,
MAC, digest, size, and expiry before decoding any semantic item.

Deletion is retention-driven. Deleting a parent does not mutate children; a
reference counter or mark/sweep job retains blobs until no live continuation or
operation references them.

## Redis key layout

```text
llmtw:{admission}:op:<scope-hmac>:<operation-hmac>
llmtw:{admission}:budget:<policy-version>:<window>:<bucket>
llmtw:{admission}:function-version

llmtw:continuation:<tenant-hmac>:<continuation-id>
llmtw:result:<tenant-hmac>:<digest>
```

All keys touched by admission share the literal configured `{admission}` hash
tag so a Redis Cluster executes one atomic Function. This creates an intentional
single-slot throughput ceiling in v1. Continuation/result keys need not share
that slot because they are referenced only after their immutable write
completes.

Redis values use explicit schema versions and integer strings. Function
arguments are validated for count, length, numeric range, and allowed transition.
The Function uses Redis `TIME`. Dynamic key names, source code, or caller
expressions are never interpolated into Lua.

## Redis operation protocol

`Begin`, `MarkDispatching`, `Continue`, `Complete`, and `Fail` are
versioned Function entry points. Deployment loads code by digest and verifies
the server's digest before readiness.

- `Begin` performs idempotency lookup, every budget check/increment, and
  operation creation atomically.
- `MarkDispatching` compares state, operation token, and lease before recording
  the selected attempt.
- `Continue` records a definite attempt outcome, reconciles the old reservation
  vector, and atomically admits/reserves the remaining plan or ends denied.
- `Complete` stores a prewritten immutable result reference, reconciles the
  original buckets, and transitions terminally.
- `Fail` records definite/ambiguous/canceled classification and applies the
  matching reservation rule.

The result/blob is written before `Complete`. An orphan is safe and garbage
collectable; a completed ledger entry never points to an unwritten object.

Network loss after a Function call is resolved by reading the operation record
with the same operation token. The client never assumes the mutation failed.

## Memory store

The memory implementation uses the exact public store ports, one mutex for
atomic admission, an injected clock, immutable copied byte slices, and bounded
maps with expiry. It is not a simplified fake: the common conformance suite
runs unchanged against memory and Redis.

Memory mode is rejected when:

- more than one worker replica is configured;
- production mode is enabled;
- durable continuation is required across restart;
- configuration reload would orphan live operations.

## Leases and recovery

A reservation has a short pre-dispatch lease. Its owner renews while compiling
or waiting for a connection. If it expires while still `reserved` and there is
proof that `BeforePossibleWrite` never ran, another Activity attempt may claim
it without another charge.

Once `dispatching` is recorded, lease expiry never authorizes resubmission. A
reconciler may:

1. query a provider by a stored idempotency/job/reference ID when supported;
2. complete from a recovered result;
3. otherwise transition to `ambiguous` and retain the reservation.

The reconciler is a separate process mode or scheduled workflow and never needs
prompt content.

## Retention

Configuration compilation enforces:

```text
operation terminal retention
  >= maximum Activity retry horizon
   + maximum queue delay
   + clock/skew safety margin

continuation retention
  >= advertised continuation lifetime

blob retention
  >= maximum live referring record lifetime + GC safety margin
```

Ambiguous records use the longest operation retention. Tombstones retain the
operation key/request digest after result deletion so late retries cannot submit
again.

## Redis production profile

- TLS and ACL credentials supplied by secret reference;
- `noeviction` and memory alerts before exhaustion;
- AOF and RDB enabled, with persistence status in readiness/alerts;
- backups plus restore drills;
- replica/failover mode tested with Function availability;
- command allow-list limited to required operations;
- bounded client pools, timeouts, circuit metrics, and no client retries around
  ambiguous writes without read-after-error;
- keyspace notifications not required for correctness.

If Redis is unavailable or persistence is unhealthy under configured policy,
admission and continuation fail closed. Existing provider calls still attempt a
bounded ambiguity/finalization write before returning.
