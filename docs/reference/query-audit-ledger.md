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

`QueryExecutionRepository.RecordAudit` adapts the storage-neutral
`control.QueryService.Audit` callback to this ledger. It canonicalizes and
fingerprint-checks the request, converts exact USD text without floating-point
rounding, and delegates to `Record` for redaction, retention, and idempotency:

```go
queryService.Audit = repository.RecordAudit
```

`Record` also verifies that every request fingerprint matches the canonical
request JSON before it writes the row, so direct repository callers cannot
persist an audit identity that is detached from its request payload. On an
idempotent replay it performs the same binding against the persisted request
JSON and keyed fingerprint, so a direct database mutation cannot silently
change the audit payload returned by a retry.

The production factory still owns construction of the repository, query
handlers, and authorization policy; this adapter does not select provider
refreshes or implement query-specific read/index plans.

Focused checks:

```sh
cd golang
go test ./storage/postgres
```

The optional PostgreSQL integration test runs when `LLMTW_POSTGRES_ADDR` points
at a disposable database and verifies HMAC-only storage, exact-cost precision,
and idempotent replay.
