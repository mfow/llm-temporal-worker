# v1 Activity runtime boundary

The worker registers three exact names on its configured Temporal task queue:

| Name | Input | Output |
| --- | --- | --- |
| `llm.generate.v1` | `llm.GenerateRequestV1` | `llm.GenerateResponseV1` |
| `llm.compact.v1` | `llm.CompactRequestV1` | `llm.CompactResponseV1` |
| `llm.query.v1` | `llm.QueryRequestV1` | `llm.QueryResponseV1` |

The Activity adapter validates the closed JSON record and the configured
application payload limit before calling the injected `activity.V1Runtime`.
Responses are validated against the same limit before Temporal serialization;
errors are converted to bounded `SafeErrorDetails` and never include prompts,
outputs, provider bodies, or identifiers from a runtime error message.

`Activities.QueryService` is an independent seam for `llm.query.v1`. It may be
provided before the Generate/Compact runtime is composed; the Activity still
fails closed when neither seam is configured. The boundary authorizes the
tenant/project/actor scope, accepts only the provider-status, model-inventory,
and credit-status query kinds in this slice, and verifies HMAC cursors bound to
the query kind, scope, and filter. Cursors must be issued with the worker's
query cursor key. Budget-status and spend-summary handlers, query-specific
persisted reads, and Activity composition remain follow-up work tracked in
[Task 14, typed Query service and Temporal Activity, of the forkable
conversation-state plan](../superpowers/plans/2026-07-18-forkable-conversation-state.md#task-14-implement-typed-query-service-and-temporal-activity).
The repository-only query execution audit boundary now validates and persists
redacted records; its current scope is documented in the [query execution
audit ledger](query-audit-ledger.md), and it is not wired into an Activity in
this slice.

`V1Runtime` is the seam for the durable checkpoint, cache, provider, and
control-plane implementation. Production composition currently installs an
explicit fail-closed runtime until that implementation is wired. A missing or
unconfigured runtime therefore returns a non-retryable configuration error
before provider dispatch; it does not silently fall back to the pre-release
Activity envelope.

The boundary is one-shot by design. It does not register or dispatch
`llm.StreamingEngine`, token events, or provider stream decoders. Provider
fragment decoders remain parser-regression code only.
