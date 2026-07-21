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
tenant/project/actor scope and admits all five closed query kinds: provider
status, model inventory, credit status, budget status, and spend summary. It
verifies HMAC cursors for the three keyset-paginated kinds, binding each token
to the query kind, scope/tags, canonical filter, and snapshot horizon. Budget
status and spend summary intentionally have no public cursor because each is a
single bounded snapshot. Typed handlers receive authenticated cursor claims,
including the opaque storage position and horizon, so a repeatable-read
adapter can enforce the same snapshot before reading its next page. Cursors
must be issued with the worker's typed `CursorCodec` key; the raw `Handler`
seam remains available for adapters migrating from the legacy cursor envelope.
Query-specific persisted reads, provider refreshes, and Activity
composition remain follow-up work tracked in
[Task 14, typed Query service and Temporal Activity, of the forkable
conversation-state plan](../superpowers/plans/2026-07-18-forkable-conversation-state.md#task-14-implement-typed-query-service-and-temporal-activity).
`QueryService.Audit` is the storage-neutral seam for the audit requirement: it
receives canonical redacted request/response envelopes, SHA-256 request and
response digests, and exact-or-unknown cost metadata after all response and
cursor checks. A configured sink must persist the record before `Execute`
returns; a sink error becomes retryable state-unavailable/finalize failure.
The production Activity factory still has to connect this hook to the
repository-only [query execution audit ledger](query-audit-ledger.md).

`V1Runtime` is the seam for the durable checkpoint, cache, provider, and
control-plane implementation. Production composition currently installs an
explicit fail-closed runtime until that implementation is wired. A missing or
unconfigured runtime therefore returns a non-retryable configuration error
before provider dispatch; it does not silently fall back to the pre-release
Activity envelope.

The boundary is one-shot by design. It does not register or dispatch
`llm.StreamingEngine`, token events, or provider stream decoders. Provider
fragment decoders remain parser-regression code only.
