# Query execution audit ledger

`postgres.QueryExecutionRepository` is the persistence boundary for the
`llm.query.v1` audit record. It is intentionally usable before the Activity is
composed: callers pass already validated, redacted control JSON and the
repository enforces the storage invariants.

Each row stores bounded, canonicalized request and response JSON, a SHA-256
digest over the canonical response bytes,
the closed query kind and source, exact-or-unknown cost metadata, and UTC
timestamps. Prompts, model output, credentials, provider bodies, and raw tool
payloads are rejected recursively. Scope and operation values are never stored
in the row; keyed HMACs bind the row to the configured namespace key.

Rows are idempotent on `(scope_id, operation_key_hmac)`. Repeating an operation
with the same request fingerprint returns the persisted record. Reusing the
operation key with a different fingerprint returns
`ErrQueryExecutionConflict`, so a retry cannot silently overwrite audit data.

Cost metadata is explicit. Exact rows carry a validated `pricing.USD` amount
and one of `control_query_zero`, `provider_reported`, or `catalog_usage`;
`control_query_zero` can only be zero. Unknown rows carry no amount or method
and must provide a bounded lower-snake-case reason code. The repository applies
the configured retention interval when a caller omits the expiry timestamp.

This slice does not wire query Activities, provider refreshes, or query-specific
read/index APIs. Those callers remain responsible for selecting a query model
and for passing redacted control data to `Record`.

Focused checks:

```sh
cd golang
go test ./storage/postgres
```

The optional PostgreSQL integration test runs when `LLMTW_POSTGRES_ADDR` points
at a disposable database and verifies HMAC-only storage, exact-cost precision,
and idempotent replay.
