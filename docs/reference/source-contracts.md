# Verified Upstream Contracts

This design was checked against primary upstream documentation and source on
2026-07-14. Implementation phases must re-check these contracts before pinning
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

### Responses streaming implementation boundary

The direct `openai-responses` profile uses the official OpenAI Go SDK stream
path through the typed `StreamingAdapter` port, so it records
`streaming: supported`. Its deterministic transport coverage verifies direct
request dispatch, typed text and tool-argument deltas, terminal lifting, and
body release on close or parent cancellation.

The `azure-responses` profile remains `streaming: unsupported`. Azure
Responses stream availability is endpoint- and model-specific, and its
offline decoder fixtures do not verify an Azure stream dispatch path. An Azure
endpoint may claim streaming only after its own profile-level capability opt-in
and transport coverage; direct OpenAI support is not evidence for it.

### Responses fixture boundary

The direct `openai-responses` and `azure-responses` profiles each have an
enforced offline fixture matrix selected by their declared capability facts.
Both exercise Responses lowering/lifting, opaque response state,
continuation-compatibility, usage, service classes, classified errors,
strict-loss handling, and redaction in their own fixture roots. The direct
profile's `streaming: supported` fact is backed by SDK dispatch coverage; the
full and fragmented decoder fixtures remain separate SSE reconstruction
evidence. Azure continuation evidence validates the adapter's endpoint-pinned
conversion path offline; it is not evidence of Azure deployment streaming
capability.

## Azure OpenAI

- [Azure priority processing](https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/priority-processing)
  documents deployment/model availability, request tier, and returned tier.
- [Azure OpenAI REST reference](https://learn.microsoft.com/en-us/azure/ai-services/openai/reference)
  is authoritative for deployment API versions and supported request fields.

Design consequence: each Azure deployment has its own capability profile.
OpenAI compatibility is not evidence that every OpenAI field/tier works.
The independently owned Azure Responses fixtures validate the configured path
and auth construction offline, but they do not establish deployment-specific
model, economy-tier, continuation, or streaming availability.

## Anthropic and AWS

- [Anthropic service tiers](https://docs.anthropic.com/en/api/service-tiers)
  distinguish standard-only and automatic priority capacity and report usage
  tier.
- [Anthropic Messages](https://docs.anthropic.com/en/api/messages) defines
  system content, message/tool blocks, thinking blocks, streaming, and usage.
- [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go) contains
  direct Messages support, configurable retry count, and AWS/Bedrock client
  support; retries are disabled here.
- [Amazon Bedrock Anthropic Claude Messages API](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html)
  defines the native Messages request/response shape and the base invocation
  and response-stream operations.
- [Anthropic Go SDK AWS gateway source](https://github.com/anthropics/anthropic-sdk-go/blob/v1.57.0/aws/aws.go)
  documents that API-key inputs and `ANTHROPIC_AWS_API_KEY` take precedence
  over SigV4, while unset credentials use the default AWS credential chain.
  It also gives configured values precedence over environment defaults for the
  region, workspace ID, and base URL.
- [Amazon Bedrock service tiers](https://docs.aws.amazon.com/bedrock/latest/userguide/service-tiers-inference.html)
  define flex, default, priority, and reserved behavior and availability.

Design consequence: direct synchronous Anthropic has no assumed economy mapping.
`anthropic_aws_messages` supplies the AWS gateway base URL, region, and
workspace ID explicitly and permits only `aws_default_chain`; its constructor
rejects API-key/static modes and `ANTHROPIC_AWS_API_KEY` so the SDK cannot
silently choose a different auth mode. AWS gateway and Bedrock have separate
endpoint families, catalog state, and pinned continuations. AWS profiles
declare exact offering/model mappings; reserved capacity is not a fourth public
request class.

### Bedrock Anthropic fixture boundary

As of 2026-07-14, the `bedrock-anthropic` profile has an enforced offline
fixture matrix for native Messages lowering/lifting, service-tier facts,
opaque thinking state, tool argument fragments, usage with unreported cost,
classified errors, and source-date/redaction checks. Opaque state remains
endpoint-pinned: direct Anthropic and AWS-gateway continuation state cannot be
replayed through the Bedrock adapter without an explicit portable transcript.
The captured SSE fixtures prove decoder and assembler semantics under
fragmentation; they are not evidence of an end-to-end client streaming-dispatch
implementation.

## OpenAI-compatible endpoints

- [OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat)
  documents service-tier request values and returns the tier actually used in
  the response.
- [OpenRouter provider routing](https://openrouter.ai/docs/guides/routing/provider-selection)
  documents provider ordering, fallback controls, and required-parameter
  routing.
- [OpenRouter models API](https://openrouter.ai/docs/api/reference/list-available-models)
  exposes model pricing metadata.
- [Exa API reference](https://docs.exa.ai/reference/exa-answer)
  documents answer responses including provider-reported dollar cost.

Design consequence: direct Chat, OpenRouter, and Exa retain separately
enforced fixtures for lowering, response lifting, service class, error,
strict-loss, redaction, and decoder behavior despite using an
OpenAI-compatible SDK transport. Decoder fixtures do not advertise streaming
dispatch: that remains unsupported until the Chat adapter implements an
`OpenStream` boundary. OpenRouter hidden provider fallback is disabled by
default, and Exa's authoritative cost is preferred when available.

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
