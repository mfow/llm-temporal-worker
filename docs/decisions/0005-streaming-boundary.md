# ADR 0005: Streaming Boundary

- Status: Superseded on 2026-07-15 by the Generate-only Temporal Activity boundary
- Date: 2026-07-13

## Context

Provider streaming formats differ and may fragment arbitrary bytes. Temporal
Activity return values are durable payloads, not live network streams. Exposing
provider stream objects would couple callers to SDKs and make recovery unclear.

## Current decision (superseding this ADR)

The reusable Go library retains typed stream events through optional
`llm.StreamingEngine`. The Temporal `llm.generate.v1` Activity instead depends
only on `llm.Engine`, invokes `Generate` once, and returns one completed
response. It neither consumes nor heartbeats provider token events.

## Historical decision (superseded)

The following was the original decision. Its Temporal stream-consumption
language is no longer current.

Expose typed provider-neutral stream events in the reusable Go engine. Each
adapter decodes its provider stream into those events and assembles one final
normalized response.

The Temporal `llm.generate.v1` Activity consumes the stream internally,
heartbeats redacted progress, stores/finalizes the result, and returns only the
final response or tool-call turn.

## Consequences

- Library callers can build interactive transports without provider-specific
  parsing.
- Temporal callers get durable final results and cancellation/heartbeat support.
- Partial tokens are not written to Workflow history.
- Adapter tests must cover arbitrary fragmentation, event ordering, and terminal
  usage.
- A future durable live-stream gateway is a separate service/protocol, not an
  Activity return-type change.

## Rejected alternatives

- Returning an SDK stream through Temporal is not serializable or durable.
- Writing every token to Workflow signals/history is costly and risks payload
  limits.
- Supporting only non-streaming misses cancellation/progress and reusable
  library needs.
