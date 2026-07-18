# Pricing and Budgets

> This chapter describes current v1. The accepted PostgreSQL cutover replaces
> authoritative integer microUSD accounting with exact
> **NUMERIC(38,18)** USD, removes downstream/config currency fields, and makes
> the Go worker responsible for future FX retrieval and USD normalization.
> V2 catalog prices and actual costs are nullable when genuinely unknown; zero
> is reserved for a known zero charge. See
> [PostgreSQL state, cache, accounting, and control plane](postgresql-state-cache-and-control-plane.md).

## Goals

Budget enforcement is shared, conservative, and independent of any provider
SDK. A request is admitted only when its worst eligible spend fits every
matching budget window. Completion replaces the reservation with measured cost;
an ambiguous call keeps its full reservation.

V1 budgets govern monetary spend in integer micro-US dollars (`microUSD`).
Provider rate limits and token/request quotas are separate controls and do not
pretend to be financial budgets.

## Exact money representation

Configuration and provider decimal prices enter as decimal strings. They are
parsed exactly into integer numerator/scale form; binary floating point is
forbidden in pricing and admission.

```go
type MicroUSD int64

type UnitPrices struct {
	InputPerMillion       DecimalUSD
	OutputPerMillion      DecimalUSD
	CacheReadPerMillion   DecimalUSD
	CacheWritePerMillion  DecimalUSD
	ReasoningPerMillion   DecimalUSD
	PerRequest            DecimalUSD
}
```

Every positive component is multiplied with integer usage and rounded up to the
next microUSD at the request boundary. Components are summed with checked
arithmetic. Refunds and final actual totals retain integer units.

Redis Lua numbers are exact only within their integer-safe range. Configuration
compilation rejects any single limit, bucket total, reservation, or possible
sum at or above `2^53` microUSD. Go code also checks `int64` overflow.

This is a documented v1 limitation, not the target representation. The
PostgreSQL cutover replaces microUSD with **NUMERIC(38,18)**, providing 18
fractional digits and 20 whole-dollar digits. Its contract tests cover
sub-micro-dollar amounts, whole dollars, ten-dollar charges, large aggregates,
and overflow rejection without any binary floating-point conversion.

## Price catalog

A price entry is immutable and keyed by:

```text
provider + endpoint family + billing region/account class
+ resolved model + provider tier + effective interval
```

It contains all applicable unit prices, currency, tax policy, provenance,
effective timestamps, and a content digest/version. Logical aliases are resolved
before pricing.

Catalog precedence is explicit:

1. endpoint-specific operator override;
2. verified built-in catalog entry;
3. no price.

There is no guessed price. A candidate with any matching monetary budget is
ineligible without a current price. `pricing.require_price_when_budgeted: true`
is the only configuration that permits an unpriced candidate, and only when it
matches no monetary budget. `false` is strict: every candidate needs a current
price. `budgets.require_match: true` excludes unmatched candidates before this
decision, so that combination never dispatches an unpriced route.

An allowed unpriced result reports `cost_status=unknown`; zero reserved and
actual microUSD in that result mean “not priced”, not a zero-dollar claim.
Currency, method, and catalog version are empty, no monetary reservation is
created, and a provider-reported amount is not promoted to an auditable cost
without a current catalog quote. Metrics make the condition visible.

V2 removes that zero sentinel. Unknown catalog components and actual costs are
NULL with an explicit status/reason, while exact zero means known free. Known
spend totals exclude NULLs and separately report unknown operation counts.

## Estimation

The estimate is an upper bound for one candidate:

```text
estimated input tokens at a conservative tokenizer ratio
+ maximum configured output tokens
+ maximum billable reasoning tokens where separate
+ cache-write assumption when cache behavior is uncertain
+ fixed per-request charge
```

Endpoint/model-specific tokenizers may tighten the estimate only after
conformance tests. The fallback estimator uses UTF-8 byte length plus structural
overhead and a configurable safety factor. It must never use the model's average
completion length. The current catalog has no media-unit price, so media content
does not create a separate estimate component until that pricing contract is
added explicitly.

The request reservation is the maximum single-attempt estimate across every
candidate the router is authorized to attempt, including all explicit
service-class fallbacks. That amount is reserved against the union of policies/
windows that could match any candidate; this may over-reserve an endpoint-
specific budget but cannot undercount a later route. Only one candidate is
billable at a time.

After a definite uncharged failure the remaining-plan reservation is reused or
reduced. After a definite charged failure, `Continue` atomically converts the
attempt's matching share to incurred cost, refunds other old-window shares, and
reserves the maximum remaining candidate against the union of its windows.
Denial stops fallback after retaining the known incurred cost. No next provider
dispatch occurs before `Continue` succeeds.

## Reconciliation

Final cost uses this precedence:

1. authoritative provider-reported billed cost;
2. provider-reported usage priced by the pinned catalog entry;
3. locally reconstructed usage priced by the pinned catalog;
4. full reservation when the outcome or usage is ambiguous.

The public `llm.Cost` response includes reserved and actual/retained microUSD,
method, currency, and catalog version. The estimate is used for admission but
is not serialized as a separate response cost field. When an adapter receives
an authoritative provider cost, the raw value remains in the safe provider or
usage raw-facts maps; it is not copied into `llm.Cost`.

If measured cost exceeds the conservative reservation, completion still records
the full cost because it was already incurred. It atomically adds the excess,
marks a `reservation_underestimated` policy violation, and alerts. A budget may
then be over its limit; subsequent admissions fail. Silently clipping cost is
forbidden.

## Budget policies

```yaml
budgets:
  require_match: true
  policies:
    - id: acme-production
      match:
        tenant: acme
        project: assistant
        actor_prefix: service-
        environment: production
        logical_model: reasoning
        endpoint: openai-prod
        service_class: priority
      windows:
        - duration: 1h
          limit_micro_usd: 25000000
          bucket: 1m
        - duration: 24h
          limit_micro_usd: 250000000
          bucket: 5m
        - duration: 30d
          limit_micro_usd: 3000000000
          bucket: 1h
```

Matchers cover tenant, project, actor prefix, environment, logical model,
endpoint, and service class. A policy must declare at least one restriction;
an exact `*` is a wildcard rather than a restriction, so wildcard-only policies
are rejected. All matching policies and all windows within them apply; policies
are not first-match-wins. Missing context cannot match a restricted policy. With
`budgets.require_match: true`, every authorized
candidate must match at least one policy before it can be priced, admitted, or
dispatched. This filtering is candidate-specific, so an explicit fallback that
matches remains eligible even if the requested service class does not. If no
authorized candidate matches, the request terminates as `no_route` without an
admission operation or provider request.

Limits must be positive. Bucket size must divide into bounded operational
resolution and produce no more than the configured maximum buckets per window.
Policy IDs and window definitions are immutable across a catalog version;
changes create a new version with explicit carry-forward behavior.

## Conservative sliding windows

For time `t`, duration `W`, and bucket size `D`:

```text
first = floor((t - W) / D)
last  = floor(t / D)
active sum = every bucket index from first through last, inclusive
```

The full first bucket is counted even though only part intersects the exact
window. This can deny slightly early but cannot undercount spend. The current
reservation is added to the current bucket only after checking:

```text
active sum + requested reservation <= limit
```

The operation record stores the original bucket for each union-window
reservation. Reconciliation reduces those original buckets by unused shares and
adds actual/excess cost only to windows that matched the attempt, so a refund
cannot create a negative current bucket or move spend across time. Expired
buckets may be deleted after the longest window plus the maximum operation-
finalization delay.

Redis server time is authoritative for shared admission. The memory backend uses
an injected clock. Clock rollback in memory fails closed until time catches up;
Redis `TIME` avoids worker clock disagreement.

## Atomic admission

`AdmissionStore.Begin` performs one atomic operation:

1. locate the operation record by scoped operation key;
2. return a completed response reference, ambiguity, or conflict if it exists;
3. load the union of policy/window totals that could match the authorized plan;
4. deny with the earliest conservative retry time if any limit would be
   exceeded;
5. increment all union-window current buckets by the conservative plan amount;
6. create the `reserved` operation with request digest, amount, price version,
   and lease.

Memory uses one lock around the conformance transaction. Redis uses a
versioned server-side Function, with a Lua-script fallback for compatible Redis
versions. All ledger and budget keys use one configured hash tag in v1 so Redis
Cluster can run the transaction atomically. That intentional single-slot
constraint is documented and monitored.

Begin is idempotent: the same digest returns the existing state without charging
again; another digest returns `operation_conflict`.

`AdmissionStore.Continue` is also atomic. It verifies the dispatch token and
definite outcome, reconciles the prior attempt only in the windows that matched
that attempt, releases unused union-window shares, checks the remaining plan's
union windows, and either creates the next `reserved` attempt or terminally
records denial. Complete performs the same matching reconciliation for the
successful final attempt and releases reservations held only for unused routes.

## Denial and retry

A denial reports:

- every policy/window that denied;
- current conservative usage, requested reservation, and limit;
- the earliest time each window could admit if no new spend occurs;
- a safe aggregate `retry_after`;
- configuration and price versions.

The Activity turns denial into a retryable Temporal application error only when
the caller's schedule-to-close horizon can reach `retry_after` and retry policy
permits it. Otherwise it is terminal for that Activity. Denial never contacts a
provider.

## Durability modes

| Mode | Use | Guarantee |
| --- | --- | --- |
| Memory | unit tests and explicitly single-process development | process-local only; restart loses state |
| Redis | production and multi-replica development | shared atomic admission and durable operation records subject to Redis persistence |

Production Redis uses authentication/TLS, `noeviction`, AOF plus RDB, monitored
persistence errors, and backups. `appendfsync always` provides the strongest
reservation durability at higher latency; `everysec` may lose about a second of
writes during a crash and must be an explicit risk acceptance. Redis failure
causes admission to fail closed.

## Conformance properties

Both stores must pass identical black-box tests proving:

- no accepted schedule exceeds a limit under concurrent Begin calls;
- overlapping policies and windows are all charged atomically;
- request replay never charges twice;
- request digest conflicts never inherit another result;
- completion/refund is idempotent and cannot make a bucket negative;
- ambiguity retains the reservation;
- exact-boundary buckets overcount rather than undercount;
- time rollback, overflow, expired leases, and persistence errors fail closed;
- the earliest retry calculation is conservative.
