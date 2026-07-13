# Verified Upstream Contracts

This design was checked against primary upstream documentation and source on
2026-07-13. Implementation phases must re-check these contracts before pinning
dependencies because SDKs, model capabilities, pricing, and service tiers
change independently.

## OpenAI

- [Responses API](https://platform.openai.com/docs/api-reference/responses)
  provides typed input/output, tools, streaming, usage, and response state.
- [Conversation state](https://platform.openai.com/docs/guides/conversation-state)
  documents response/conversation continuation and storage behavior.
- [Priority processing](https://platform.openai.com/docs/guides/priority-processing)
  uses a priority service tier and reports the actual tier, which can differ.
- [Flex processing](https://platform.openai.com/docs/guides/flex-processing)
  is lower-cost/slower and can return resource-unavailable failures.
- [OpenAI Go SDK](https://github.com/openai/openai-go) supports Responses and
  configurable retry count; this design sets retries to zero.

Design consequence: economy/standard/priority are semantic values mapped per
capability profile to flex/default/priority; actual response tier is retained.

## Azure OpenAI

- [Azure priority processing](https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/priority-processing)
  documents deployment/model availability, request tier, and returned tier.
- [Azure OpenAI REST reference](https://learn.microsoft.com/en-us/azure/ai-services/openai/reference)
  is authoritative for deployment API versions and supported request fields.

Design consequence: each Azure deployment has its own capability profile.
OpenAI compatibility is not evidence that every OpenAI field/tier works.

## Anthropic and AWS

- [Anthropic service tiers](https://docs.anthropic.com/en/api/service-tiers)
  distinguish standard-only and automatic priority capacity and report usage
  tier.
- [Anthropic Messages](https://docs.anthropic.com/en/api/messages) defines
  system content, message/tool blocks, thinking blocks, streaming, and usage.
- [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go) contains
  direct Messages support, configurable retry count, and AWS/Bedrock client
  support; retries are disabled here.
- [Amazon Bedrock service tiers](https://docs.aws.amazon.com/bedrock/latest/userguide/service-tiers-inference.html)
  define flex, default, priority, and reserved behavior and availability.

Design consequence: direct synchronous Anthropic has no assumed economy mapping.
AWS profiles declare exact offering/model mappings; reserved capacity is not a
fourth public request class.

## OpenAI-compatible endpoints

- [OpenRouter provider routing](https://openrouter.ai/docs/features/provider-routing)
  documents provider ordering, fallback controls, and required-parameter
  routing.
- [OpenRouter models API](https://openrouter.ai/docs/api/reference/list-available-models)
  exposes model pricing metadata.
- [Exa API reference](https://docs.exa.ai/reference/exa-answer)
  documents answer responses including provider-reported dollar cost.

Design consequence: OpenRouter hidden provider fallback is disabled by default,
and Exa's authoritative cost is preferred when available. Both retain separate
profiles and fixtures despite using an OpenAI-compatible SDK transport.

## Temporal

- [Go Activities](https://docs.temporal.io/develop/go/core-application#develop-activity)
  support struct methods and dependency injection.
- [Activity heartbeats](https://docs.temporal.io/develop/go/failure-detection#activity-heartbeats)
  deliver cancellation and progress details for long-running work.
- [Go SDK test suite](https://pkg.go.dev/go.temporal.io/sdk/testsuite) provides
  Activity/Workflow test environments.
- [Temporal payload limits](https://docs.temporal.io/cloud/limits#payload-size-limits)
  motivate application-level inline limits and BlobRefs.

Design consequence: LLM I/O occurs in an Activity, heartbeats remain small and
redacted, and large content stays outside Workflow history.

## Redis

- [Redis Functions](https://redis.io/docs/latest/develop/programmability/functions-intro/)
  execute server-side atomic logic.
- [Redis TIME](https://redis.io/docs/latest/commands/time/) supplies shared
  server time.
- [Cluster hash tags](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/#hash-tags)
  colocate Function keys in one slot.
- [Redis persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)
  explains RDB/AOF tradeoffs, including the every-second durability window.
- [Eviction policies](https://redis.io/docs/latest/develop/reference/eviction/)
  document `noeviction` behavior.

Design consequence: one atomic admission hash slot, integer arithmetic,
server-time windows, fail-closed errors, and an explicit persistence profile.

## GitHub Actions and Go

- [Workflow schedule syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#onschedule)
  supports a schedule timezone, used for 05:00 `Australia/Sydney`.
- [checkout](https://github.com/actions/checkout) and
  [setup-go](https://github.com/actions/setup-go) are the official actions used
  in CI.
- [Go releases](https://go.dev/doc/devel/release) identify the current Go 1.26
  patch line; CI uses `go-version: 1.26.x` while the module records Go 1.26.

## Upgrade rule

An SDK or API upgrade PR must update:

1. dependency/source date and pinned version;
2. affected capability and price catalog provenance;
3. redacted golden wire fixtures;
4. service-tier, retry, error, stream, usage, and continuation assertions;
5. this source-contract record when behavior changed.
