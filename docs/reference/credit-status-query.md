# Persisted credit-status query

`ProviderStatusRepository.ListCreditStatuses` is the PostgreSQL read side for
the `credit_status` control query. It reads the current
`provider_route_status` projection and, for an incident, the newest matching
authoritative event for that route in one bounded SQL statement. It does not
call a provider inference or management endpoint.

The result is one deterministic row per provider/endpoint identity, ordered by
`provider` and `endpoint_id`. When configuration contains multiple route
projections for one endpoint, the newest `observed_at` row wins and `route_id`
breaks ties. The repository returns an unsigned `NextEndpointKey` position for
maintenance/internal callers; it must not be exposed publicly until the
historical replay described below is implemented.

The schema includes `provider_route_credit_query_idx`, keyed by configuration,
provider, endpoint, observation time, and route ID in the same order as this
collapse. It includes the state and confirmation timestamp needed for the
current projection. Incident evidence is selected
independently of `last_event_id`, because a later inference or startup-probe
event may leave a sticky incident in the projection while not being
authoritative evidence for it.

By default only endpoints with a non-OK credit or billing state are returned.
`IncludeOK` includes healthy endpoints as well. Provider and endpoint filters
are bounded identifiers and page size is capped at 1,000.

The mapper exposes only normalized state, confirmed time, and bounded evidence:

- provider `provider_code` or `safe_error_code` becomes a `provider_api`
  evidence source;
- an operator event becomes `operator`; and
- an observation with no evidence code is reported as `unknown`.

The domain stores billing incidents as `issue`; the query wire mapper emits the
closed contract value `blocked` and never leaks the storage enum.

Raw provider responses, credentials, and event digests are never returned by
this read side. Provider refresh and public cursor signing remain explicit
query-service responsibilities, as required by the control-plane plan.

## Public pagination blocker

Public `credit_status` pagination is intentionally not wired yet. The
repository returns the current mutable `provider_route_status` projection and
an unsigned keyset position. Signing that position with a timestamp would not
create a historical snapshot: a route refreshed after the timestamp would be
omitted, while its earlier state is no longer present in the projection.
Incident evidence also needs an as-of reconstruction from the append-only
`provider_status_events` ledger. A future adapter must define that replay
(including route collapse, stale/epoch handling, and confirmation timestamps)
before exposing a cursor to callers.

The direct repository API remains useful for unpinned maintenance reads and
retains an optional `SnapshotHorizon` bound for callers that explicitly accept
current-projection semantics. Runtime composition is also intentionally
explicit: `ProductionFactory` currently exposes PostgreSQL as a readiness
probe and closer, while `newRuntimeActivities` wires the fail-closed
`UnconfiguredV1Runtime`. No production worker dispatch is claimed here.
