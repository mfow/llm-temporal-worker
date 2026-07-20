# Persisted credit-status query

`ProviderStatusRepository.ListCreditStatuses` is the PostgreSQL read side for
the `credit_status` control query. It reads the current
`provider_route_status` projection and the event referenced by each projection
row in one bounded SQL statement. It does not call a provider inference or
management endpoint.

The result is one deterministic row per provider/endpoint identity, ordered by
`provider` and `endpoint_id`. When configuration contains multiple route
projections for one endpoint, the newest `observed_at` row wins and `route_id`
breaks ties. The repository returns an unsigned `NextEndpointKey` position; the
query service must authenticate that position in its HMAC cursor before
exposing it to a caller.

The schema includes `provider_route_credit_query_idx`, keyed by configuration,
provider, endpoint, observation time, and route ID in the same order as this
collapse. It includes the state, confirmation timestamp, and event ID used by
the read, so the bounded query does not need to scan the append-only event
ledger to find current endpoint rows.

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
