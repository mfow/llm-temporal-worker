# Provider Adapters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement official-SDK adapters and exhaustive conversion fixtures for
OpenAI Responses, Azure/OpenRouter/Exa compatible Chat, Anthropic Messages, and
AWS-hosted Anthropic paths.

**Architecture:** Each endpoint family owns lowering, one-shot SDK invocation,
stream decoding, response lifting, and provider error facts. Shared semantic
ports remain SDK-free. Endpoint profiles state exact capabilities and service
mapping; compatible endpoints are separate profiles, not assumed clones.

**Tech Stack:** Current pinned releases of `github.com/openai/openai-go/v3` and
`github.com/anthropics/anthropic-sdk-go` (including its supported AWS/Bedrock
paths), `net/http/httptest`, Go fuzzing, redacted JSON/SSE fixture files.

**Global Constraints:**

- Re-check the official source links on the implementation day before pinning
  modules; update dependency baseline and fixture metadata in the same commit.
- Construct every SDK client with retry count zero and prove one HTTP submission.
- Never expose an SDK type outside its adapter package.
- Strict conversion fails before invocation; best-effort conversion emits a
  structured field-path diagnostic.
- Each supported tier mapping has requested, actual, downgrade, and unknown-tier
  fixtures.

---

### Task 1: Pin SDKs and prove one-shot transports

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `docs/reference/dependency-baseline.md`
- Create: `llm/provider/openairesponses/client.go`
- Create: `llm/provider/openaichat/client.go`
- Create: `llm/provider/anthropicmessages/client.go`
- Create: `llm/provider/bedrockmessages/client.go`
- Test: `llm/provider/internal/contract/one_shot_test.go`
- Test: `llm/provider/openairesponses/client_test.go`
- Test: `llm/provider/anthropicmessages/client_test.go`

- [x] Inspect current official SDK release notes/source for minimum Go version,
  Responses/Messages types, AWS clients, retry option, raw response metadata,
  streaming, and base URL/auth options. Record exact selected versions.
- [x] Run:

```sh
go get github.com/openai/openai-go/v3@latest
go get github.com/anthropics/anthropic-sdk-go@latest
go mod tidy
```

Expected: `go.mod`/`go.sum` pin exact versions; no indirect SDK is selected as a
substitute.
- [x] Write a reusable test server that returns a retryable 500 twice and counts
  requests. Write failing constructor tests expecting exactly one request.
- [x] Run `go test ./llm/provider/... -run TestSDKRetriesDisabled`. Expected:
  FAIL because constructors are absent.
- [x] Implement adapter-local constructors with official retry option set to
  zero, injected `*http.Client`, validated base URL/auth, and an Activity-derived
  context. Do not expose retry as user configuration.
- [x] Add compile-time tests showing SDK client/parameter types cannot appear in
  exported provider-neutral structs.
- [x] Run `go test -race ./llm/provider/... -run 'TestSDKRetriesDisabled|TestClient'`.
  Expected: PASS and each counter equals one.
- [x] Commit: `build(provider): pin official SDKs with retries disabled`.

### Task 2: Implement OpenAI Responses lowering and lifting

**Files:**

- Create: `llm/provider/openairesponses/adapter.go`
- Create: `llm/provider/openairesponses/capability.go`
- Create: `llm/provider/openairesponses/lower.go`
- Create: `llm/provider/openairesponses/lift.go`
- Create: `llm/provider/openairesponses/error.go`
- Test: `llm/provider/openairesponses/lower_test.go`
- Test: `llm/provider/openairesponses/lift_test.go`
- Test: `llm/provider/openairesponses/error_test.go`
- Create: `testdata/contracts/openai-responses/manifest.yaml`
- Create: `testdata/contracts/openai-responses/*`

- [x] Create semantic fixtures for instructions, text/image, tools/tool results,
  strict schema, reasoning, continuation ID, refusal, usage/cache/reasoning,
  extension include, IDs, finish statuses, and size/unsupported failures.
- [x] Add class fixtures: economy lowers to `flex`, standard to `default`,
  priority to `priority`; responses cover each actual tier, priority downgrade
  to default, and unknown tier.
- [x] Write tests that compare normalized SDK parameter JSON to
  `request.wire.json` and lifted output to `response.semantic.json`.
- [x] Run `go test ./llm/provider/openairesponses`. Expected: FAIL because the
  adapter is absent.
- [x] Implement `Compile` using separate typed Responses input items. Preserve
  tool call/result IDs, instruction order, schema digest, reasoning controls,
  maximum output, provider state, and namespaced extension allow-list.
- [x] Implement `Invoke` once and `lift` with request/response IDs, status,
  typed output, actual service tier, full usage categories, and safe raw facts.
- [x] Decode OpenAI errors into common facts without returning raw bodies.
- [x] Run `go test -race ./llm/provider/openairesponses`. Expected: PASS.
- [x] Commit: `feat(provider): add OpenAI Responses adapter`.

### Task 3: Implement the profiled Chat Completions compiler

**Files:**

- Create: `llm/provider/openaichat/adapter.go`
- Create: `llm/provider/openaichat/profile.go`
- Create: `llm/provider/openaichat/lower.go`
- Create: `llm/provider/openaichat/lift.go`
- Create: `llm/provider/openaichat/error.go`
- Test: `llm/provider/openaichat/contract_test.go`
- Test: `llm/provider/openaichat/lower_test.go`
- Test: `llm/provider/openaichat/lift_test.go`
- Create: `testdata/contracts/common/chat/*`

- [ ] Write a fake strict profile and tests for system/developer instruction
  lowering, message roles, multimodal parts, assistant tool calls, tool-result
  messages, response format/tool schema alternatives, finish reason, usage, and
  unknown fields.
- [ ] Add tests proving a profile must explicitly declare every feature and
  provider tier; `unknown` is rejected in strict mode.
- [ ] Run `go test ./llm/provider/openaichat`. Expected: FAIL.
- [ ] Implement a shared compiler driven by immutable typed `Profile` hooks for
  capability, extension schema, error/usage/cost lift, and service mapping.
  Keep all differences explicit; do not branch on hostname strings.
- [ ] Implement tool/result ordering and local final JSON validation. A
  structured-output tool emulation must have a named capability transform and
  a diagnostic.
- [ ] Run `go test -race ./llm/provider/openaichat`. Expected: PASS.
- [ ] Commit: `feat(provider): add profiled Chat Completions compiler`.

### Task 4: Add Azure, OpenRouter, and Exa Chat profiles

**Files:**

- Create: `llm/provider/openaichat/azure.go`
- Create: `llm/provider/openaichat/openrouter.go`
- Create: `llm/provider/openaichat/exa.go`
- Test: `llm/provider/openaichat/azure_test.go`
- Test: `llm/provider/openaichat/openrouter_test.go`
- Test: `llm/provider/openaichat/exa_test.go`
- Create: `testdata/contracts/azure-responses/*`
- Create: `testdata/contracts/openrouter-chat/*`
- Create: `testdata/contracts/exa-chat/*`

- [ ] Write Azure fixtures for deployment base URL/auth headers, exact declared
  tier values, actual tier downgrade, usage, request IDs, and unsupported
  deployment fields. If the verified target uses Responses rather than Chat,
  reuse semantic contract helpers but place the adapter/profile under the exact
  family and keep separate fixtures.
- [ ] Write OpenRouter fixtures asserting provider order,
  `allow_fallbacks=false`, `require_parameters=true`, no caller override of
  these controls, generation IDs, usage, and pricing metadata.
- [ ] Write Exa fixtures for its accepted compatible request, citations/search
  content as typed or namespaced data, request ID, usage, and authoritative
  `costDollars` converted exactly to microUSD.
- [ ] Run the three profile tests. Expected: FAIL with missing constructors.
- [ ] Implement typed profile constructors that require exact base URL,
  capability version, tier mapping, and allowed extension config. Reject
  arbitrary compatible URLs at runtime.
- [ ] Ensure Exa provider cost takes reconciliation precedence but retains raw
  decimal provenance; unknown/malformed/negative costs return safe errors.
- [ ] Run `go test -race ./llm/provider/openaichat/...`. Expected: PASS.
- [ ] Commit: `feat(provider): add Azure OpenRouter and Exa profiles`.

### Task 5: Implement direct Anthropic Messages

**Files:**

- Create: `llm/provider/anthropicmessages/adapter.go`
- Create: `llm/provider/anthropicmessages/capability.go`
- Create: `llm/provider/anthropicmessages/lower.go`
- Create: `llm/provider/anthropicmessages/lift.go`
- Create: `llm/provider/anthropicmessages/error.go`
- Test: `llm/provider/anthropicmessages/lower_test.go`
- Test: `llm/provider/anthropicmessages/lift_test.go`
- Test: `llm/provider/anthropicmessages/state_test.go`
- Create: `testdata/contracts/anthropic-direct/*`

- [ ] Write fixtures for ordered system blocks, user/assistant content, image and
  document support/rejection, tool use/result, strict tool schema, thinking,
  redacted thinking, signature, cache usage, stop reason, and errors.
- [ ] Add tier tests: economy fails compile; standard lowers to
  `standard_only`; priority lowers to `auto` only when `priority_capacity` is
  present; response usage determines actual mapped class.
- [ ] Add byte-exact opaque state tests across lower/lift/lower and wrong-profile
  pinning rejection.
- [ ] Run `go test ./llm/provider/anthropicmessages`. Expected: FAIL.
- [ ] Implement conversion with official SDK parameter/block types. Preserve
  content order and signatures; do not merge alternating roles unless the
  selected explicit transform is enabled and diagnosed.
- [ ] Implement safe usage/tier/error lifting and exactly one SDK call.
- [ ] Run `go test -race ./llm/provider/anthropicmessages`. Expected: PASS.
- [ ] Commit: `feat(provider): add Anthropic Messages adapter`.

### Task 6: Add Anthropic AWS and Amazon Bedrock profiles

**Files:**

- Create: `llm/provider/anthropicmessages/aws.go`
- Create: `llm/provider/bedrockmessages/adapter.go`
- Create: `llm/provider/bedrockmessages/lower.go`
- Create: `llm/provider/bedrockmessages/lift.go`
- Create: `llm/provider/bedrockmessages/error.go`
- Test: `llm/provider/anthropicmessages/aws_test.go`
- Test: `llm/provider/bedrockmessages/adapter_test.go`
- Create: `testdata/contracts/anthropic-aws/*`
- Create: `testdata/contracts/bedrock-anthropic/*`

- [ ] Re-inspect the pinned Anthropic SDK's current AWS gateway and
  Bedrock/Mantle source. Record which Messages features each path supports and
  use that exact supported constructor; do not reproduce SigV4 manually.
- [ ] Write fixtures for AWS credential-provider injection without credential
  values, region/model identifiers, AWS request IDs/errors, content conversion,
  and unsupported non-Messages APIs.
- [ ] Write Bedrock service-tier fixtures for economy/flex, standard/default,
  priority/priority, each unsupported model/tier, actual tier, and reserved
  capacity rejection as a public value.
- [ ] Run `go test ./llm/provider/anthropicmessages ./llm/provider/bedrockmessages`.
  Expected: FAIL.
- [ ] Implement AWS clients through official SDK support and share only
  provider-neutral Anthropic semantic helpers. Keep auth, request envelopes,
  errors, usage, and tier lifting profile-specific.
- [ ] Assert no config/request serialization contains resolved AWS credentials.
- [ ] Run both packages with race detection. Expected: PASS.
- [ ] Commit: `feat(provider): add Anthropic AWS and Bedrock profiles`.

### Task 7: Implement streaming decoders and fragmentation harness

**Files:**

- Create: `llm/provider/internal/streamtest/fragment.go`
- Create: `llm/provider/openairesponses/stream.go`
- Create: `llm/provider/openaichat/stream.go`
- Create: `llm/provider/anthropicmessages/stream.go`
- Create: `llm/provider/bedrockmessages/stream.go`
- Test: `llm/provider/*/stream_test.go`
- Test: `llm/provider/*/stream_fuzz_test.go`
- Create: `testdata/contracts/*/events.wire`
- Create: `testdata/contracts/*/events.semantic.jsonl`

- [ ] For each profile, write expected typed events and final response for text,
  multiple items, tool argument fragments, reasoning/provider state, usage,
  tier, terminal success, provider error, and truncation.
- [ ] Implement the harness that feeds complete, every one-byte chunk, every
  split point, deterministic random chunks, and empty chunks. First run against
  absent decoders. Expected: FAIL.
- [ ] Implement bounded deterministic decoders using official SDK stream/event
  support where it preserves raw event semantics. Validate indexes, ordering,
  UTF-8, JSON completion, exactly one terminal, and assembled equivalence.
- [ ] Add fuzz targets with golden seeds; every input must return events/error
  within size/time bounds and never panic.
- [ ] Run:

```sh
go test -race ./llm/provider/...
go test ./llm/provider/openairesponses -run=^$ -fuzz=FuzzStream -fuzztime=60s
go test ./llm/provider/openaichat -run=^$ -fuzz=FuzzStream -fuzztime=60s
go test ./llm/provider/anthropicmessages -run=^$ -fuzz=FuzzStream -fuzztime=60s
go test ./llm/provider/bedrockmessages -run=^$ -fuzz=FuzzStream -fuzztime=60s
```

Expected: PASS, no divergence between fragmentation patterns.
- [ ] Commit: `feat(provider): normalize and fuzz provider streams`.

### Task 8: Enforce fixture completeness and register adapters

**Files:**

- Create: `llm/provider/registry.go`
- Create: `llm/provider/registry_test.go`
- Create: `llm/provider/internal/contract/manifest.go`
- Test: `llm/provider/internal/contract/manifest_test.go`
- Create: `testdata/contracts/required-cases.yaml`
- Create: `testdata/contracts/*/metadata.yaml`
- Modify: `Makefile`

- [ ] Encode every row from
  [fixture matrix](../../testing/fixture-matrix.md) in
  `required-cases.yaml`. Write `TestFixtureManifestComplete` so every registered
  profile must mark each case success or compile-rejection and reference files.
- [ ] Run the manifest test. Expected: FAIL listing missing cases/metadata.
- [ ] Complete manifests and redaction/provenance metadata. Add byte scans for
  credential/account/personal-data patterns and decoded safe-field checks.
- [ ] Implement a registry keyed by configured family/profile ID. Registration
  validates concrete adapter type, supported SDK parameter type, capabilities,
  tier mappings, and one-shot client construction.
- [ ] Add `make adapter-contracts` and ensure normal tests cannot rewrite goldens.
- [ ] Run `make adapter-contracts && go test -race ./llm/provider/...`.
  Expected: PASS.
- [ ] Commit: `test(provider): enforce exhaustive adapter fixtures`.

## Phase exit

- [ ] Run `go mod tidy && git diff --exit-code go.mod go.sum`. Expected: no
  uncommitted module change.
- [ ] Run `make verify`. Expected: PASS.
- [ ] Run the four 60-second stream fuzz commands. Expected: PASS.
- [ ] Inspect fixture bytes with the redaction test. Expected: no secret or
  personal content.
- [ ] Confirm no provider package imports another provider package except the
  approved SDK-free internal test helpers.
