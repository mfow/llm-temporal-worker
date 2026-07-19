# Provider control observations

The provider-control domain (`golang/control`) is the boundary between an
adapter observation and the durable PostgreSQL projections described in the
[control-plane design](../architecture/postgresql-state-cache-and-control-plane.md#provider-status-credit-state-and-inventory).
It is intentionally independent of provider SDKs and database clients. The
PostgreSQL hand-off is implemented by `storage/postgres.ProviderStatusRepository`
and `storage/postgres.InventoryRepository`; provider adapters and query
Activities remain separate integration layers.

This package remains the normalization and projection-invariant slice of Task
13. The domain package itself does not open a database, call a provider
management API, or register a query Activity. The PostgreSQL repositories
described below are the separate durable boundary; keeping that boundary
explicit prevents an in-memory `RouteStatus` or `InventorySnapshot` from being
mistaken for persisted production state.

## Status and credit

Adapters convert inference, startup, management, and operator observations to
`control.StatusObservation` and call `NewStatusEvent`. The constructor rejects
missing identity/evidence digests, unknown enum values, invalid observation
intervals, and unbounded or unsafe provider text. Only the resulting
`StatusEvent` is suitable for an append-only event ledger; raw response bodies
and credentials are never part of the event.

Credit classification is conservative. A generic HTTP 429 is a rate/capacity
signal and does not establish exhausted credit. Exhausted/billing incidents
require a documented provider code/field or an authorized operator event.
`RouteStatus.Apply` keeps a confirmed incident sticky across ordinary
inference and startup observations. A provider management response, matching
configuration epoch, or operator event may explicitly clear it. Changing the
configuration epoch starts a fresh projection; it does not inherit the old
incident.

## Model inventory

`InventorySnapshot` validates bounded, sorted model rows, explicit lifecycle
values, safe metadata, and current/stale/unsupported provenance. Provider APIs
may paginate through `NextCursor`; an unsupported listing is represented by an
explicit `unsupported` source rather than an empty successful list.

Inventory is informational. `ConfiguredModel` is a predicate only: discovered
models never mutate the configured routing catalog or become routable without a
reviewed configuration snapshot and capability entry.

`RefreshCoordinator` collapses concurrent refreshes per endpoint. A failed
refresh preserves the last valid snapshot for stale reads while returning the
refresh error, allowing query callers to report stale provenance instead of
silently presenting a fabricated current result.

## Persistence hand-off

`ProviderStatusRepository.PersistStatusEvent` consumes a validated
`StatusEvent` in one transaction. It appends the event (with its deterministic
digest as an idempotency key), takes a transaction-scoped advisory lock for the
configuration/route identity (including when the projection row does not yet
exist), locks the route projection, applies the domain sticky-incident rules,
and commits the current projection. Stale observations
remain in the append-only ledger but do not replace the current projection.
`GetRouteStatus` reads only the projection and never returns raw provider
response data.

`InventoryRepository.PersistSnapshot` validates the deterministic inventory
digest, writes one immutable snapshot and all normalized model rows in one
transaction, and treats a repeated observation/digest as an idempotent replay.
Continuation cursors are envelope-encrypted with authenticated context bound
to the configuration, endpoint account, snapshot identity, and inventory
digest; plaintext cursors are never stored. `GetSnapshot` revalidates the
loaded rows and `OpenCursor` authenticates the cursor before returning it.

The repositories do not register provider management adapters or a query
Activity. Those runtime/query integrations remain explicit follow-up work,
and discovered models remain informational rather than routable.
