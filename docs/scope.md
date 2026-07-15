# Product Scope and Success Criteria

## Problem

Callers that use Temporal should not need provider-specific request structs,
retry behavior, pricing arithmetic, or continuation state. They need a stable
Activity contract that can target multiple LLM APIs without pretending those
APIs are identical.

The worker solves that problem by acting as a small compiler and admission
controller:

1. validate a versioned semantic request;
2. resolve capabilities, service class, route, and price snapshot;
3. reserve the maximum plausible cost across every applicable budget;
4. lower the semantic request to one provider wire format;
5. perform one externally observable dispatch attempt;
6. normalize output, usage, actual service class, cost, and diagnostics;
7. persist an immutable continuation and completed operation result;
8. finalize or conservatively retain the budget reservation.

## Intended users

- Temporal workflow authors who want one inference Activity contract.
- Go services that want the same router, adapters, pricing, and budget layers
  without running a Temporal worker.
- Platform teams that need centralized endpoint credentials and cost policy.
- Test authors who need deterministic conversion fixtures without live model
  calls.

## In scope for v1

### API generations

- OpenAI Responses.
- OpenAI-compatible Chat Completions.
- Anthropic Messages.

### Endpoint profiles

- OpenAI direct.
- Azure OpenAI for Responses and Chat Completions where the deployment supports
  the selected operation.
- OpenRouter Chat Completions.
- Exa OpenAI-compatible Chat Completions.
- A configurable OpenAI-compatible endpoint profile with capabilities declared
  by configuration rather than guessed.
- Anthropic direct.
- Claude Platform on AWS through a closed `anthropic_aws_messages` endpoint
  family using the Anthropic AWS gateway client and the AWS default credential
  chain; it is distinct from Bedrock.
- Amazon Bedrock Anthropic through the current Messages-compatible Mantle path,
  with the legacy Bedrock runtime path isolated behind a separate endpoint
  profile when required by a region or model.

### Semantic features

- Developer/system instructions and ordered human/model messages.
- Text and image input by URL, inline bytes, or external blob reference.
- First-class tool definitions, tool calls, and tool results.
- JSON Schema Draft 2020-12 structured output.
- Sampling, stop sequences, output limits, and reasoning intent where supported.
- Provider-state parts that remain opaque and byte-for-byte stable.
- Strict and best-effort portability with machine-readable diagnostics.
- Typed streaming events for reusable Go-library callers, outside the
  one-shot Temporal Activity boundary.
- Exactly three request service classes: `economy`, `standard`, and `priority`.
- Explicit ordered service-class fallback, disabled by default.
- Durable continuation and endpoint pinning.
- Configurable deterministic routing, bounded failover, and circuit breaking.
- Versioned price catalogs and provider-reported cost reconciliation.
- Multiple overlapping, conservatively enforced sliding-window budgets.
- In-memory and Redis state implementations.

### Runtime and delivery

- A named Temporal Activity, `llm.generate.v1`.
- Docker and Kubernetes deployment artifacts.
- Structured logging, Prometheus metrics, and OpenTelemetry tracing.
- Separate pull-request and master GitHub Actions workflows.
- A master build scheduled daily at 05:00 `Australia/Sydney` as well as on push.

## Explicitly out of scope for v1

- Owning a Temporal Workflow definition or an agent loop.
- Executing tool calls returned by a model.
- Provider-hosted web search, file search, computer use, code interpreter, MCP,
  or other hosted tools.
- Embeddings, reranking, moderation, fine-tuning, batch APIs, file stores,
  image generation, speech, video, and realtime sessions.
- A public HTTP inference gateway.
- Automatic model substitution based on model-name similarity.
- Automatic service-class escalation or downgrade.
- Cross-region active-active Redis budget accounting.
- Exactly-once claims for external LLM APIs that do not expose a supported
  idempotency contract.
- Persisting secrets, raw credentials, or bearer tokens in Temporal payloads.

## Behavioral invariants

### No hidden semantic loss

Every field is either represented natively, deliberately emulated, rejected,
or dropped with a diagnostic in best-effort mode. An adapter may not silently
flatten tool calls, discard opaque reasoning state, or ignore an unsupported
request parameter.

### No hidden cost escalation

An omitted `service_class` becomes `standard`. A requested class is attempted
exactly. A different class may be attempted only when it appears in the
request's ordered `service_class_fallbacks`. The result reports requested,
attempted, and provider-observed classes separately.

### Admission before dispatch

When any budget applies, the worker must have a price and a bounded maximum
output. It reserves a conservative upper bound before a provider request can
leave the process. Missing or stale price data fails closed.

### One retry authority

Provider SDK automatic retries are set to zero. The operation ledger and
Temporal retry policy decide whether another dispatch is safe. A transport
failure before bytes are written can be retried; a timeout after dispatch is
ambiguous and is not retried automatically.

### Continuation integrity

Continuation records are immutable. Provider-native state is retained without
interpretation and is reused only by the adapter and endpoint family that
created it. Switching routes requires a portable canonical transcript; strict
mode rejects a switch that would lose required provider state.

### Bounded history

Activity inputs, outputs, heartbeat details, and errors stay well below
Temporal payload limits. Large binary parts and oversized normalized histories
use an external `BlobRef`; secrets never use a blob reference passed through
workflow history.

## Quality targets

These are initial service objectives for the worker itself, excluding provider
latency:

| Measure | Target |
| --- | --- |
| Admission and compilation p99 | Under 25 ms with memory state; under 75 ms with same-region Redis |
| Worker-caused successful-call error rate | Below 0.1% |
| Budget overspend from known usage | Zero under the documented clock and durability assumptions |
| Configuration reload | Atomic snapshot swap; no partially applied configuration |
| Graceful shutdown | No new Activities accepted; in-flight work given its configured stop timeout |
| Adapter conversion coverage | Every capability cell has a positive or negative fixture |

The implementation must benchmark these targets rather than treating them as
guaranteed properties of the design.
