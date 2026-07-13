# ADR 0001: Semantic IR and Official SDKs

- Status: Accepted
- Date: 2026-07-13

## Context

OpenAI Responses, OpenAI-compatible Chat Completions, Anthropic Messages, and
AWS-hosted Anthropic APIs represent messages, tool calls, structured output,
reasoning, continuation, streaming, tiers, and usage differently. Exposing a
provider union makes every caller provider-aware. Reducing everything to text
silently loses semantics.

## Decision

Expose a versioned provider-neutral semantic IR with ordered typed items and
parts. Compile that IR through explicit capabilities into a provider call, then
lift provider output back into the IR.

Use official Go SDKs for OpenAI, Anthropic/AWS support, Temporal, and AWS
integration. An OpenAI-compatible endpoint may reuse the official OpenAI SDK
transport while retaining its own profile, fixtures, and error/usage decoder.
Raw HTTP is allowed only for a verified SDK gap, behind the same adapter
interface and a follow-up ADR that names its removal condition.

Provider SDK types never enter the public API, Temporal payloads, routing,
pricing, or storage.

## Consequences

- Callers can reuse one API without pretending providers are identical.
- New features require semantic design plus per-profile capability decisions.
- Strict mode rejects loss; best-effort mode reports every transform.
- Adapters require substantially more conversion tests than thin client
  wrappers.
- SDK upgrades are isolated and fixture-reviewed.

## Rejected alternatives

- A string prompt/response facade loses tools, multimodality, reasoning, and
  continuation.
- A public union of SDK request types couples workflows to providers and stores
  unstable types in Temporal history.
- Hand-written HTTP for all providers duplicates auth, streaming, and error
  behavior without improving the semantic boundary.
