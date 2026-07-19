# PostgreSQL repository foundation

The first PostgreSQL repository slice is implemented in
[`golang/storage/postgres`](../../golang/storage/postgres). It is intentionally
limited to the connection, namespace, scope, encrypted-locator, and exact USD
codec boundaries. Operation replay, attempts, checkpoints, budgets, and cache
repositories are separate delivery slices and are not represented as complete
by this package.

## Connection and transaction boundary

`postgres.BuildPoolConfig` validates one immutable `Namespace`, configures a
bounded `pgxpool`, and supports multiple hosts on one port. When TLS is
enabled, a configured CA and server name are required and certificate
verification remains enabled. New connections set UTC, statement/lock/idle
transaction timeouts, and `synchronous_commit=on`. `postgres.Health` is a
read-only readiness check: it verifies `current_database()` and the UTC
session; it never creates schema objects.

Use `postgres.WithTransaction` for durable writes. The helper owns begin,
commit, and rollback, sets UTC and synchronous commit locally, and passes a
`pgx.Tx` to the callback. Blob/provider I/O must happen outside that callback;
the transaction helper does not expose a pool through the transaction port.

## Scope and encrypted locators

`ScopeRepository` derives tenant and project HMAC-SHA-256 lookup values from a
versioned keyring and upserts only those digests. Raw tenant/project values are
never SQL parameters or persisted columns. `BlobRepository` stores bounded
object metadata and an authenticated envelope-encrypted locator. The content
digest and byte length describe the object bytes, not the locator string. The
AEAD context binds scope, stable blob identity, payload kind, and content
digest, so a copied or tampered ciphertext fails authentication even when two
operations reuse one content-addressed row. Key rotation retains old read
keys while new values use the active key.

## Exact USD values

`EncodeUSD` and `DecodeUSD` use the existing exact `pricing.USD` type and bind
canonical decimal text to PostgreSQL `NUMERIC(38,18)`. Exponents, signs,
rounding, floating-point values, and overflow are rejected. Nullable helpers
preserve unknown cost as SQL `NULL`; unknown is never coerced to numeric zero.

All identifiers are generated through the validated namespace and all values
are positional SQL parameters. This package does not use `search_path`, broad
JSONB lookup, raw provider IDs, or streaming APIs.
