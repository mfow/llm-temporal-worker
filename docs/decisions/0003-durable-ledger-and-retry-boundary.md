# ADR 0003: Durable Ledger and Retry Boundary

- Status: Accepted
- Date: 2026-07-13

## Context

Temporal can retry an Activity after worker loss, while an external LLM
provider may already have received and billed the request. Network failure does
not reliably reveal whether request bytes were accepted. SDK-internal retries
are invisible to shared budgets and route decisions.

## Decision

Disable provider SDK retries. Require a stable caller operation key and store a
request digest in an operation ledger joined atomically with budget admission.
Record `dispatching` immediately before a transport can write. Cache completed
results. Automatically replay only when a failure is proven pre-write or
definitely rejected/uncharged.

Any unresolved post-write outcome becomes terminal `ambiguous`, retains the
reservation, and returns a non-retryable reconciliation reference.

## Consequences

- A Temporal retry can safely return a prior result.
- Automatic submission is at-most-once after a possible write.
- Some calls require operator/provider reconciliation instead of automatic
  availability-oriented retry.
- Transport observation and provider status retrieval need focused testing.
- Operation records must outlive every allowed Activity retry.

## Rejected alternatives

- Relying only on Temporal retries can duplicate billable calls.
- Relying only on provider idempotency is not portable or uniformly guaranteed.
- Assuming a timeout means rejection is unsafe.
- Holding a local mutex cannot protect multiple replicas or process restarts.
