# Status projection replay

`control.ReplayRouteStatus` rebuilds the domain-level `RouteStatus` for one
route from a bounded read of the persisted status-event ledger. It is a
storage-neutral helper: the caller supplies `PersistedStatusEvent` values with
their positive database `EventID`s and must provide them in strictly ascending
ledger order. Replay intentionally does not sort by `ObservedAt`; the same
ordered sequence is what `RouteStatus.Apply` sees during live persistence.

The `Horizon` is an inclusive observation-time reconstruction cutoff. Events
observed after it are counted as skipped and do not affect the result. Events
at or before the horizon, including stale observations, are passed through
`Apply` so the normal stale-event, configuration-epoch, and sticky
credit/billing rules remain canonical. This is intentionally a historical
observation-time view, not an exact reconstruction of a live projection that
may already have accepted later events. The input digest and route ID must
match every event before that event can be applied.

`StatusReplayCoverage` reports the bounded read (`EventsSeen`, applied,
ignored, and horizon-skipped counts plus the observed-time interval). The
storage reader supplies `Complete`; `false` means retention, pagination, or
another bounded-read limit prevents claiming that all retained events were
returned. Even `Complete=true` is only the reader's coverage claim; it is not a
stable snapshot or watermark when concurrent writes or late inserts are
possible. Consumers must not present an incomplete replay as historical truth.

Replay verifies structural invariants and requires a non-zero stored digest,
but does not recompute the digest. PostgreSQL `timestamptz` values have
microsecond precision, so recomputing from a rounded read could reject an event
whose digest was created before insertion. The append path remains responsible
for authenticating the digest before it is persisted.

Replay returns only the `RouteStatus` domain projection. It does not recreate
SQL-only counters or timestamps, projection versions, last-event storage
metadata, provider evidence, or a public continuation cursor. Those remain
responsibilities of the PostgreSQL repository and query boundary.
