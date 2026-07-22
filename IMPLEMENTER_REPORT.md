# Task 1 implementer report

## Scope

This change completes the v1 contract matrix at the JSON-schema, Go codec, and
Temporal Activity payload boundaries. It closes the tagged Query request/result
union, restricts Generate response checkpoints to generation/cache-replay,
validates the Compact control policy and parent checkpoint, and enforces the
documented cache-variant/temperature invariant.

The branch was based on `1a81a5ab931394f2bd7bb3d8c42f6de1e38f6173` (current
`origin/master` when work began). Task 0's namespace implementation was
already an ancestor and was not changed.

## Requirements addressed

- Added valid fixtures for fork patch Set/Clear leaves, cache variants,
  cache-free compaction, and all five Query request/result kinds.
- Added negative fixtures for legacy Generate fields, currency/numeric cost
  encodings, unsupported Compact controls, mismatched Query results, and
  malformed Query pagination.
- Added schema, codec, Activity-payload, and race-test coverage for the matrix.
- Documented the closed tagged-record boundary in `docs/reference/activity-runtime.md`.

## Validation

- `go test ./llm ./llm/schema ./activity`
- `go test -race ./llm ./llm/schema ./activity`
- `make schema-verify`
- `git diff --check`

## Self-review and concerns

The existing numeric-temperature compatibility window remains intact; the
helper rejects a materialized zero temperature with a positive cache variant,
while omitted temperature is validated after inherited settings are
materialized. The generic `checkpoint` schema
definition remains for other envelopes, while Generate references its narrower
checkpoint definition. Compact policy remains intentionally control-plane
only; application tools and structured output are restored by a later Generate.
