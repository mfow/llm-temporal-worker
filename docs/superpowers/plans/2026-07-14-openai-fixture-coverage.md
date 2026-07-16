# OpenAI Responses Fixture Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Current v1 scope (2026-07-15; supersedes this plan's streaming prerequisite)

V1 exposes only one-shot `Generate` and a completed normalized response.
Current v1 enforcement depends on each profile's declared non-streaming
capability facts; it does not require a streaming adapter, SDK stream dispatch,
or Temporal runtime dispatch. The Task 6 streaming prerequisite below is
retained as historical planning context only: decoder fixtures remain
parser-regression coverage and do not create a client-dispatch requirement or
prevent a checked-in production profile from being `enforced` for its declared
facts. See [ADR 0005](../../decisions/0005-streaming-boundary.md) and the
[fixture matrix](../../testing/fixture-matrix.md) for the current boundary and
enforcement rule.

**Goal:** Record the direct OpenAI and Azure Responses fixture work that
independently proves each implemented request/response conversion fact. The
original plan also made its then-unresolved end-to-end streaming boundary
explicit rather than overstating parser coverage; that prerequisite is now
superseded by the Generate-only v1 boundary above.

**Architecture:** Each provider profile owns a separate manifest, metadata record, and synthetic redacted fixture set below `llm/provider/openairesponses/testdata/contracts/`. Package tests deserialize the semantic fixtures, exercise the real SDK lowering/lifting or Azure transport construction, and compare canonical JSON. At the time of this plan, the harness was to classify both profiles as `bootstrap` until Task 6 supplied a typed streaming adapter and official SDK stream dispatch. That condition is historical and is not a current v1 enforcement rule.

**Tech Stack:** Go 1.26, OpenAI Go SDK v3.42.0, `go.yaml.in/yaml/v4`, offline HTTP transports, Go test.

## Global Constraints

- Keep direct OpenAI and Azure fixtures separate; no Azure fact is inferred from a direct fixture.
- Fixtures are synthetic, use only public example URLs and explicit redaction markers, and never contain credentials, deployment secrets, or unstable provider IDs.
- **Historical prerequisite, superseded:** `streaming` meant end-to-end adapter dispatch. The existing decoder remains fixture-tested but could not have established that capability without Task 6 adding the stream port and SDK client path.
- Keep the public service classes limited to economy, standard, and priority. Azure's fixture represents its declared deployment profile independently of the direct OpenAI mapping.
- **Superseded enforcement rule:** this plan did not mark a profile `enforced` while its required streaming dispatch prerequisite was absent. The current Generate-only v1 rule instead evaluates the profile's declared non-streaming capability facts.

---

### Task 1: Establish independently governed profile roots

**Files:**
- Move `llm/provider/openaichat/testdata/contracts/azure-responses/` to `llm/provider/openairesponses/testdata/contracts/azure-responses/`
- Modify `llm/provider/openairesponses/testdata/contracts/openai-responses/manifest.yaml`
- Modify `llm/provider/openairesponses/testdata/contracts/openai-responses/metadata.yaml`
- Create `llm/provider/openairesponses/testdata/contracts/azure-responses/manifest.yaml`
- Create `llm/provider/openairesponses/testdata/contracts/azure-responses/metadata.yaml`

- [x] Give each Responses profile its own `responses`-family manifest and source metadata.
- [x] Record all registry capability facts with `streaming: unsupported` and `stream_decoder: supported` until Task 6 wires stream dispatch.
- [x] Preserve profile-specific service-class facts and redaction/provenance records; retain `bootstrap` coverage while streaming remains blocked.

### Task 2: Add full synthetic request/response/error fixture matrices

**Files:**
- Create and modify fixture files under `llm/provider/openairesponses/testdata/contracts/openai-responses/`
- Create fixture files under `llm/provider/openairesponses/testdata/contracts/azure-responses/`

- [x] Add separate semantic request and captured wire fixtures covering text, image, document, tools, named tool choice, JSON-schema output, reasoning/include controls, and all declared service classes.
- [x] Add separate completed response and semantic response fixtures covering output items, opaque reasoning state, usage/cache/reasoning facts, zero/unreported provider cost, request/response IDs, and actual service class facts.
- [x] Add redacted classified-error and security fixtures without credential-like bytes.
- [x] Add a decoder-only compound stream fixture and fragmentation corpus for each profile; keep these artifacts clearly outside an end-to-end capability assertion.
- [x] Add strict-loss fixtures for deterministic pre-dispatch rejection; mark best-effort and continuation unsupported where the implementation has no safe transform or endpoint/model pinning proof.

### Task 3: Test fixture behavior before marking any coverage status

**Files:**
- Modify `llm/provider/openairesponses/fixture_test.go`
- Modify `llm/provider/openairesponses/azure_test.go`
- Create `llm/provider/openairesponses/contract_fixture_test.go`
- Modify `llm/provider/openairesponses/capability.go`

- [x] Write failing fixture-driven tests that deserialize both profiles independently and compare canonical lowered wire JSON and lifted semantic JSON.
- [x] Prove direct and Azure service-class, usage, error, opaque-state, and redaction facts independently through the real package boundary.
- [x] Prove the decoder fixture is invariant for full payloads, every split point, single-byte fragments, deterministic random fragments, and empty reads, without presenting it as SDK stream dispatch.
- [x] Change the advertised adapter streaming capability to unsupported until a typed stream interface exists, with a regression test for that honest boundary.

### Task 4: Document the prerequisite and verify the bounded change

**Files:**
- Modify `docs/reference/source-contracts.md`
- Modify `docs/testing/fixture-matrix.md`

- [x] Correct the Azure Responses fixture ownership path in the matrix.
- [x] Document that Task 6 is required for end-to-end Responses streaming enforcement, identifying the absent typed stream adapter/official SDK dispatch path.
- [x] Run `go test ./llm/provider/openairesponses`, the focused fragmentation and race checks, `make adapter-contracts`, `make docs-verify`, and `git diff --check`.
