# Provider control observations

The provider-control domain (`golang/control`) is the boundary between an
adapter observation and the durable PostgreSQL projections described in the
[control-plane design](../architecture/postgresql-state-cache-and-control-plane.md#provider-status-credit-state-and-inventory).
It is intentionally independent of provider SDKs and database clients.

This package is the normalization and projection-invariant slice of Task 13.
It does not open a database, append an event, update a PostgreSQL projection,
call a provider management API, or register a query Activity. Those durable
repositories and adapters are a follow-up Task 13/14 change. Keeping that
boundary explicit prevents an in-memory `RouteStatus` or `InventorySnapshot`
from being mistaken for persisted production state.

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

The future PostgreSQL repository should consume `StatusEvent` values in one
transaction: append the event and update the route current projection, then
persist `InventorySnapshot` rows and their bounded model rows. The domain
package supplies validation, safe fields, deterministic digests, and sticky
incident rules; it does not claim that this hand-off is implemented yet.
