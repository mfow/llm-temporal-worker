# ADR 0002: Public Service Classes

- Status: Accepted
- Date: 2026-07-13

## Context

Providers use incompatible names for latency/capacity pricing: examples include
flex, default, priority, auto, standard-only, and reserved capacity. Exposing
those names would make logical requests provider-specific. A provider-selected
default would also make omitted behavior configuration-dependent and difficult
to budget.

## Decision

The public request enum is exactly:

```text
economy | standard | priority
```

Omission normalizes to `standard` before request hashing. There is no public
provider-default value.

Endpoint capability profiles explicitly map supported public classes to
provider values. Unsupported mappings make a candidate ineligible. The request
may include an ordered `service_class_fallbacks` list containing only the same
three values. Without that list the class cannot change.

Responses report requested, attempted, provider actual, mapped actual, and
fallback index. An upstream downgrade is observed and reconciled; it is not
misreported as the requested class.

Temporal task priority is separate and is never inferred from this field.

## Consequences

- Omitted behavior is stable across providers and deployments.
- Operators can add endpoint mappings without changing callers.
- Direct synchronous Anthropic endpoints may have no economy candidate.
- A provider can accept priority and deliver standard; clients see both facts.
- Budget admission reserves the maximum eligible explicit class path.

## Rejected alternatives

- `provider_default` makes semantics and cost depend on the selected route.
- Passing provider strings through the API destroys portability.
- Silent class fallback can increase cost or latency without caller consent.
- Mapping provider tier to Temporal task priority couples unrelated queues.
