# Foundation and Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish a compiling Go module with stable v1 semantic contracts,
canonical normalization/digests, schema validation, provider ports, common
errors/events, and strict configuration snapshots.

**Architecture:** Domain packages depend only on the standard library and narrow
pure validation libraries. Provider, Temporal, Redis, and process concerns enter
later through interfaces. JSON fixtures lock the Activity/public wire contract.

**Tech Stack:** Go 1.26, standard `encoding/json` plus a duplicate-key scanner,
`github.com/santhosh-tekuri/jsonschema/v6` for local Draft 2020-12 validation,
`go.yaml.in/yaml/v4` for strict YAML, standard testing/fuzzing.

**Global Constraints:**

- No provider SDK, Temporal SDK, or Redis dependency in domain packages.
- Missing service class normalizes to `standard` before hashing.
- Public JSON rejects unknown enum values and duplicate keys.
- Tests and schema fixtures land in the same commit as each contract.
- Exact dependency versions are selected from current official releases at
  implementation time and pinned by `go.mod`/`go.sum`.

---

### Task 1: Initialize the module and API version

**Files:**

- Create: `go.mod`
- Create: `go.sum`
- Create: `.go-version`
- Create: `Makefile`
- Create: `llm/version.go`
- Test: `llm/version_test.go`
- Create during implementation: `docs/reference/dependency-baseline.md`

- [ ] Verify the current Go 1.26 patch and intended module releases against
  official release/source pages. Record date, module path, selected version,
  minimum Go version, retry default, and license in
  `docs/reference/dependency-baseline.md`.
- [ ] Run `go mod init github.com/mfow/llm-temporal-worker` and write `1.26` to
  `.go-version`.
- [ ] Write the failing test:

```go
func TestAPIVersion(t *testing.T) {
	if got, want := llm.APIVersion, "llm.temporal/v1"; got != want {
		t.Fatalf("APIVersion = %q, want %q", got, want)
	}
}
```

- [ ] Run `go test ./llm`. Expected: FAIL because `llm.APIVersion` is undefined.
- [ ] Add `const APIVersion = "llm.temporal/v1"` and no other behavior.
- [ ] Add Make targets `fmt-check`, `vet`, `test`, `build`, and `verify` using
  the same commands as Actions.
- [ ] Run `go test ./llm && make verify`. Expected: PASS.
- [ ] Commit: `chore: initialize Go module and API version`.

### Task 2: Define semantic request and response types

**Files:**

- Create: `llm/service_class.go`
- Create: `llm/request.go`
- Create: `llm/item.go`
- Create: `llm/tool.go`
- Create: `llm/response.go`
- Create: `llm/diagnostic.go`
- Create: `api/schema/v1/generate-request.schema.json`
- Create: `api/schema/v1/generate-response.schema.json`
- Test: `llm/request_json_test.go`
- Test: `llm/testdata/request/minimal.json`
- Test: `llm/testdata/request/full.json`
- Test: `llm/testdata/response/tool-calls.json`
- Test: `llm/testdata/response/completed.json`

- [ ] Write table tests for omitted/each/unknown service class, duplicate
  fallback entries, requested class repeated in fallback, actor/item/part
  unions, tools, output format, extensions, and response route/service facts.
- [ ] Include this exact assertion:

```go
func TestNormalizeServiceClass(t *testing.T) {
	tests := []struct{ in, want llm.ServiceClass }{
		{"", llm.ServiceClassStandard},
		{llm.ServiceClassEconomy, llm.ServiceClassEconomy},
		{llm.ServiceClassStandard, llm.ServiceClassStandard},
		{llm.ServiceClassPriority, llm.ServiceClassPriority},
	}
	for _, tt := range tests {
		got, err := llm.NormalizeServiceClass(tt.in)
		if err != nil || got != tt.want {
			t.Fatalf("NormalizeServiceClass(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
```

- [ ] Run `go test ./llm -run 'TestNormalizeServiceClass|TestRequestJSON'`.
  Expected: FAIL with missing types/functions.
- [ ] Implement closed enums and tagged unions with custom JSON decode where
  `encoding/json` would accept unknown/ambiguous fields. Use `[]Item` and
  `[]Part` to preserve order; copy byte slices on construction.
- [ ] Implement fallback validation: values must be one of the three classes,
  differ from the requested class, be unique, and retain order.
- [ ] Write request/response JSON Schemas with `additionalProperties: false` at
  closed boundaries and fixture-validation tests.
- [ ] Run `go test ./llm`. Expected: PASS.
- [ ] Commit: `feat(llm): define versioned semantic contracts`.

### Task 3: Normalize and hash requests canonically

**Files:**

- Create: `llm/normalize.go`
- Create: `llm/canonicaljson.go`
- Create: `llm/digest.go`
- Test: `llm/normalize_test.go`
- Test: `llm/canonicaljson_test.go`
- Test: `llm/canonicaljson_fuzz_test.go`

- [ ] Write failing tests proving normalization is idempotent, omitted class
  becomes explicit standard, fallback order is retained, JSON object key order
  does not affect digest, decoded bytes not base64 spelling determine digest,
  and any semantic/context/route-authorization change does affect digest.
- [ ] Define digest input as every normalized request field except
  `operation_key`; tenant remains separately scoped in the ledger and is also
  represented in the normalized context digest.
- [ ] Run `go test ./llm -run 'TestNormalize|TestRequestDigest'`. Expected: FAIL.
- [ ] Implement deterministic canonical JSON with sorted object keys, exact JSON
  numbers, duplicate-key rejection, finite size/depth, and SHA-256 domain prefix
  `llmtw/request/v1\x00`.
- [ ] Add `FuzzCanonicalJSONIdempotent` and seed minimal/full/schema/tool
  fixtures. Assert invalid duplicate/deep/oversized inputs remain errors.
- [ ] Run `go test ./llm` and a 30-second local fuzz run. Expected: PASS.
- [ ] Commit: `feat(llm): add canonical normalization and request digests`.

### Task 4: Implement canonical JSON Schema handling

**Files:**

- Create: `llm/schema/schema.go`
- Create: `llm/schema/subset.go`
- Create: `llm/schema/validate.go`
- Create: `llm/schema/testdata/valid/*.json`
- Create: `llm/schema/testdata/invalid/*.json`
- Test: `llm/schema/schema_test.go`
- Test: `llm/schema/subset_test.go`
- Test: `llm/schema/schema_fuzz_test.go`

- [ ] Add the JSON Schema module dependency only after writing tests for valid
  object/array/enum/number cases, invalid instances, unsupported keyword policy,
  unresolved references, remote references, duplicate keys, depth, and size.
- [ ] Run `go test ./llm/schema`. Expected: FAIL because the package is absent.
- [ ] Implement local Draft 2020-12 parsing and validation. V1 rejects remote
  `$ref` fetches and compiles only the documented supported subset; adapters can
  further restrict but cannot broaden it.
- [ ] Return errors with canonical JSON pointer paths and safe keyword codes,
  never the whole instance value.
- [ ] Add fuzz seeds for all schemas and run
  `go test ./llm/schema && go test ./llm/schema -run=^$ -fuzz=FuzzSchema -fuzztime=30s`.
  Expected: PASS with bounded allocations.
- [ ] Commit: `feat(schema): validate canonical structured output`.

### Task 5: Define provider ports, events, and common errors

**Files:**

- Create: `llm/provider/adapter.go`
- Create: `llm/provider/capability.go`
- Create: `llm/provider/call.go`
- Create: `llm/provider/event.go`
- Create: `llm/provider/error.go`
- Create: `llm/provider/observer.go`
- Test: `llm/provider/capability_test.go`
- Test: `llm/provider/event_test.go`
- Test: `llm/provider/error_test.go`

- [ ] Write compile-time interface assertions and table tests for
  native/emulated/unsupported/unknown capability resolution, event order,
  terminal response assembly, safe error serialization, and the four dispatch
  certainties.
- [ ] Run `go test ./llm/provider`. Expected: FAIL because ports are absent.
- [ ] Implement the interfaces from
  [provider adapters](../../architecture/provider-adapters.md) without importing
  SDK packages. `Call.SDKParams` remains `any` but adapters validate its concrete
  type internally.
- [ ] Implement an event assembler that rejects index/order/terminal violations
  and produces the same `llm.Response` shape as non-streaming adapters.
- [ ] Implement common error codes/phases/retry disposition from
  [error model](../../reference/error-model.md). Ensure `Error()` exposes only
  `SafeMessage`.
- [ ] Run `go test -race ./llm/...`. Expected: PASS.
- [ ] Commit: `feat(provider): define adapter capability and event ports`.

### Task 6: Parse and compile strict configuration

**Files:**

- Create: `config/types.go`
- Create: `config/load.go`
- Create: `config/validate.go`
- Create: `config/snapshot.go`
- Create: `config/secretref.go`
- Create: `api/schema/v1/config.schema.json`
- Create: `config.example.yaml`
- Test: `config/load_test.go`
- Test: `config/validate_test.go`
- Test: `config/snapshot_test.go`
- Test: `config/testdata/valid/complete.yaml`
- Test: `config/testdata/invalid/*.yaml`

- [ ] Copy the complete documented configuration shape into a valid fixture and
  add invalid fixtures for unknown/duplicate fields, fourth service class,
  unsafe URLs/timeouts/retention, route cycles, numeric
  overflow, unmatched references, and secret literal.
- [ ] Run `go test ./config`. Expected: FAIL because package is absent.
- [ ] Add strict YAML dependency and implement parse -> structural validate ->
  default -> reference resolve interface -> compile immutable snapshot. Map
  iteration is sorted before digesting.
- [ ] Store endpoint service mappings as closed
  `map[llm.ServiceClass]ProviderTier` validated against each profile. Do not add
  a configuration default service class.
- [ ] Implement `SnapshotSource` with an atomic pointer; failed compilation never
  replaces the prior snapshot. Secret values are omitted from snapshot digest
  and redacted rendering.
- [ ] Validate `config.example.yaml` against both Go types and JSON Schema.
- [ ] Run `go test -race ./config`. Expected: PASS.
- [ ] Commit: `feat(config): compile strict immutable snapshots`.

### Task 7: Enforce dependency and repository gates

**Files:**

- Create: `internal/architecturetest/imports_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/pull-request.yml`
- Modify: `.github/workflows/master.yml`
- Test: `internal/architecturetest/imports_test.go`

- [ ] Write an import-boundary test that runs `go list -deps -json ./...` and
  fails if domain packages import provider SDKs, Temporal, Redis, `config`, or
  another adapter.
- [ ] First add a temporary violating test package and run
  `go test ./internal/architecturetest`. Expected: FAIL naming the forbidden
  edge; remove the temporary package after proving the test.
- [ ] Add `make schema-verify` and `make docs-verify` without changing generated
  files. Update `make verify` to run format check, schemas, unit/race tests, vet,
  architecture test, and build.
- [ ] Remove no-longer-needed conditional gaps from Actions only for gates now
  guaranteed by the repository. Keep Docker conditional until its phase.
- [ ] Run `make verify` and inspect both workflow files with a YAML parser.
  Expected: PASS, no working-tree changes.
- [ ] Commit: `ci: enforce foundation contract and dependency gates`.

## Phase exit

- [ ] Run `git diff --check`. Expected: no output.
- [ ] Run `make verify` twice. Expected: both runs PASS and do not rewrite
  fixtures/schemas.
- [ ] Run `git status --short`. Expected: empty after commits.
- [ ] Hand the exact exported API/schema diff to adapter, routing, and pricing
  reviewers before Phase 2 begins.
