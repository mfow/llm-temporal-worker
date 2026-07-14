# Adapter Fixture Matrix

## Profiles

The initial fixture suite defines one exact test profile for each path:

| ID | Family | Transport/client | Purpose |
| --- | --- | --- | --- |
| `openai-responses` | OpenAI Responses | official OpenAI Go SDK | native typed Responses contract |
| `azure-responses` | Azure OpenAI Responses | official OpenAI Go SDK with Azure options | deployment-specific compatibility/tier |
| `openrouter-chat` | OpenAI-compatible Chat | official OpenAI Go SDK with pinned OpenRouter endpoint | provider routing controls and compatible differences |
| `exa-chat` | OpenAI-compatible Chat | official OpenAI Go SDK with Exa endpoint | answer/search response and reported cost |
| `anthropic-direct` | Anthropic Messages | official Anthropic Go SDK | system/tool/thinking/tier behavior |
| `anthropic-aws` | Claude Platform on AWS Messages | official Anthropic Go SDK AWS support | AWS auth/gateway differences |
| `bedrock-anthropic` | Amazon Bedrock Messages | official Anthropic Bedrock/Mantle support | Bedrock model/tier/error behavior |

An optional compatible endpoint copies `openrouter-chat`'s common suite but must
add its own profile directory and difference fixtures before registration.

## Common semantic cases

Every profile has either a success fixture or an explicit compile-rejection
fixture for each row:

| Area | Cases |
| --- | --- |
| Instructions | empty, one text block, ordered multiple blocks, unsupported non-text |
| Messages | one human, alternating human/model, empty retained content, Unicode, refusal |
| Text | ASCII, multi-byte UTF-8, newline, maximum accepted, over limit |
| Image | URL, bytes, BlobRef, media type, detail, unsupported source, over limit |
| Document | URL/bytes/BlobRef, title, media type, unsupported |
| JSON part | scalar, array, object, duplicate input key rejection, depth limit |
| Tools | none, auto, required, named, parallel true/false, multiple calls, tool error result |
| Tool schema | primitives, nested object/array, enum, refs if allowed, unsupported keyword |
| Structured output | text, JSON object, strict Draft 2020-12 subset, invalid model JSON |
| Sampling | absent, temperature, top-p, stop, conflicting/unsupported control |
| Output limit | absent/default normalization, exact max, endpoint max exceeded |
| Reasoning | absent, effort, summary, opaque/encrypted state, unsupported |
| Continuation | none, compatible ID, canonical replay, expired, wrong endpoint/model |
| Extensions | absent, allowed typed extension, unknown namespace, forbidden override |
| Portability | strict native, strict rejection, best-effort transform diagnostic |
| Usage | input/output, cache read/write, reasoning, absent optional, malformed/overflow |
| Completion | complete, length, tool calls, refusal, content filter, provider error |
| Identity | request/response/generation IDs and missing optional ID |

## Service-class matrix

Each cell has request-lowering, response-lifting, and unsupported fixtures:

| Profile | economy | standard | priority |
| --- | --- | --- | --- |
| OpenAI Responses | `flex` | `default` | `priority` |
| Azure Responses | only when deployment profile declares it | `default` | `priority` when declared |
| OpenRouter Chat | only when pinned provider profile declares it | profile value | only when profile declares it |
| Exa Chat | unsupported until verified | profile value | unsupported until verified |
| Anthropic direct | unsupported synchronously | `standard_only` | `auto` only with priority-capacity claim |
| Anthropic AWS | exact offering profile | exact offering profile | exact offering profile |
| Bedrock Anthropic | `flex` when supported | `default` | `priority` when supported |

For every supported priority path, include requested priority/actual standard
downgrade and requested priority/actual priority. For every unknown provider
tier, assert `actual` is unset with a diagnostic, never guessed as standard.
For every unsupported economy path, assert compile rejection occurs before
admission/dispatch unless another configured route supports economy.

## Error and transport matrix

Every profile covers:

| Stage | Cases | Required dispatch fact |
| --- | --- | --- |
| Client compile | invalid model/control/schema/extension | not dispatched |
| DNS/connect/TLS before write | refusal, cert, deadline | not dispatched |
| Write | zero bytes, partial/unknown bytes | not dispatched or ambiguous from observer evidence |
| HTTP/provider rejection | auth, permission, invalid, rate, capacity | profile-specific rejected/cost fact |
| Headers then disconnect | with/without request ID | accepted or ambiguous |
| Body | malformed JSON, unknown block, usage overflow | accepted; cost reconciled conservatively |
| Stream | malformed event, truncated terminal, error after delta | accepted/ambiguous as profile specifies |
| Cancellation | before write, during body, after terminal before finalize | canceled, ambiguous, or accepted |

Assertions include common code, phase, retry disposition, reservation treatment,
request ID, safe Temporal details, and no raw body leakage.

## Stream event matrix

Each supported event type has a standalone fixture and appears in one compound
stream:

- response/message start;
- content/output item start and finish;
- text delta including split UTF-8;
- tool call start, argument fragments, and completed canonical JSON;
- reasoning/thinking, signature, redacted, and encrypted opaque blocks;
- refusal/status delta;
- usage update and cache/reasoning details;
- provider tier and response metadata;
- terminal success, terminal provider error, and truncated stream.

The checked-in provider tests currently execute representative fixtures as full
streams, single-byte chunks, and deterministic seeded random chunks. The v1
acceptance matrix still requires explicit every-split-point, empty-read, and
CR/LF-boundary cases; those are not yet covered by the current provider test
suite.

## Reverse-conversion assertions

For semantics supported natively:

```text
semantic -> provider parameters -> captured wire
captured provider response -> semantic
semantic -> provider -> semantic equivalence
stream events -> assembled response == non-stream response
```

Equivalence ignores only fields explicitly marked generated (provider IDs and
timestamps). Opaque provider state compares exact bytes and order. Best-effort
transforms compare the documented transformed form plus diagnostics.

## Fixture paths

Fixtures are owned by the adapter package that knows how to interpret them:

```text
llm/provider/openaichat/testdata/contracts/common/chat/
llm/provider/openaichat/testdata/contracts/azure-responses/
llm/provider/openaichat/testdata/contracts/openrouter-chat/
llm/provider/openaichat/testdata/contracts/exa-chat/
llm/provider/openairesponses/testdata/contracts/openai-responses/
llm/provider/anthropicmessages/testdata/contracts/anthropic-direct/
llm/provider/anthropicmessages/testdata/contracts/anthropic-aws/
llm/provider/bedrockmessages/testdata/contracts/bedrock-anthropic/
```

Each listed profile has a `manifest.yaml` listing its required matrix cases.
Error fixtures live beside the provider profile that emits them, and security
cases are package tests rather than a root-level fixture directory. A test
compares manifests to a code-owned required-case list, so adding a new semantic
field or enum fails the owning adapter until support/rejection fixtures are
added.
