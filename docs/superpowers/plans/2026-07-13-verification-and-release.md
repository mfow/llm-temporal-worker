# Verification and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn package-level proof into a repeatable release gate with exhaustive
contract traceability, security/fuzz/chaos coverage, opt-in live verification,
and signed container evidence while retaining separate PR and master workflows.

**Architecture:** Local `make` targets are the source of truth; GitHub Actions
orchestrate the same commands. Deterministic offline suites gate every PR.
Scheduled master runs repetition/fuzz/chaos without secrets. Live and release
operations require explicit protected-environment authorization.

**Tech Stack:** Go race/fuzz/coverage, actionlint, govulncheck, static/security
analysis, Docker/Compose, Kustomize, SBOM and container scanners, Cosign/SLSA
provenance where repository policy permits.

**Global Constraints:**

- Pull requests receive no provider or production credentials.
- Live failures never rewrite capability/price catalogs automatically.
- A scheduled job validates only; it never deploys or releases.
- Release artifacts trace to a clean, reviewed master commit.
- Test evidence and logs pass content/secret redaction before upload.

---

### Task 1: Harden the two GitHub Actions workflows

**Files:**

- Modify: `.github/workflows/pull-request.yml`
- Modify: `.github/workflows/master.yml`
- Create: `integration/actions/workflows_test.go`
- Modify: `Makefile`
- Modify during implementation: `docs/reference/dependency-baseline.md`

- [ ] Write YAML-structure tests asserting exactly the intended workflow split:
  PR targets master with read-only permissions; master handles master push,
  manual dispatch, and `cron: "0 5 * * *"` with
  `timezone: Australia/Sydney`.
- [ ] Assert neither scheduled nor PR jobs reference live secret names or deploy
  commands. Assert concurrency, timeouts, Go 1.26.x, race, vet, build, docs,
  schema, adapter manifest, image, and integration jobs.
- [ ] Run `go test ./integration/actions` and actionlint. Expected: tests expose
  every gate not yet added.
- [ ] Add jobs using reusable local Make targets. Pin every third-party Action
  to a reviewed full commit SHA with a major-version comment; record it in the
  dependency baseline. Keep `permissions` least-privileged per job.
- [ ] Master schedule adds repeated unit/property tests and bounded fuzz seed
  replay; Docker integration uses service isolation and always cleans up.
- [ ] Add `make actions-verify` and run it locally. Expected: PASS.
- [ ] Commit: `ci: harden separate pull request and master verification`.

### Task 2: Enforce requirement and fixture traceability

**Files:**

- Create: `testdata/traceability/requirements.yaml`
- Create: `integration/contracts/traceability_test.go`
- Create: `integration/contracts/conversion_test.go`
- Create: `integration/contracts/non_stream_stream_test.go`
- Modify: `testdata/contracts/required-cases.yaml`
- Modify: `Makefile`

- [ ] Encode every v1 completion statement from `docs/index.md` and every
  fixture-matrix row as a stable requirement ID with owning package tests and
  expected success/rejection profiles.
- [ ] Write a test that fails for missing requirement owner, missing test/fixture
  file, duplicate ID, stale profile, or an exported semantic field/enum absent
  from required cases.
- [ ] Add cross-adapter semantic tests that feed one request to every eligible
  profile and compare provider-neutral output/diagnostics. Ineligible profiles
  must fail compile before observer/invocation.
- [ ] Add non-stream/stream final-response equivalence for every profile.
- [ ] Run `go test ./integration/contracts`. Expected: FAIL until manifest is
  complete.
- [ ] Complete traceability without weakening semantic expectations. Add
  `make contract-matrix`.
- [ ] Run `make contract-matrix` twice and compare fixture manifests. Expected:
  PASS and byte-identical.
- [ ] Commit: `test: enforce v1 contract traceability matrix`.

### Task 3: Execute security and privacy verification

**Files:**

- Create: `integration/security/tenant_test.go`
- Create: `integration/security/egress_test.go`
- Create: `integration/security/content_leak_test.go`
- Create: `integration/security/corrupt_state_test.go`
- Create: `integration/security/resource_limits_test.go`
- Create: `integration/security/container_test.go`
- Create: `testdata/contracts/security/markers.yaml`
- Modify: `Makefile`

- [ ] Seed unique markers for auth headers, environment/file secrets, prompt,
  tool arguments/results, output, provider body, continuation, opaque thinking,
  tenant/account IDs, and blob content.
- [ ] Drive every success/error/cancel/reload/reconcile path and scan logs,
  metrics, traces, Temporal heartbeat/error details, Redis keys/ledger values,
  and uploaded CI evidence. Every marker must be absent from forbidden sinks.
- [ ] Test cross-tenant operation/handle/blob access, key rotation, MAC/digest
  tampering, malicious Redis/blob codecs, duplicate/deep JSON, oversized stream,
  redirect, DNS rebinding/metadata/private URL policy, and compatible base URL.
- [ ] Run the suite before any remediation. Expected: new tests may identify
  owning-layer failures; retain minimized fixtures.
- [ ] Fix failures at validation/redaction boundaries and add a regression case
  to the owning unit package.
- [ ] Run `govulncheck ./...` and the selected static/security scanners; record
  versions/suppressions with evidence, never broad ignores.
- [ ] Add `make security-verify` and run with race detection. Expected: PASS.
- [ ] Commit: `test(security): verify tenant content and egress boundaries`.

### Task 4: Expand fuzz, property, race, and mutation gates

**Files:**

- Create: `integration/fuzz/seeds_test.go`
- Create: `integration/property/end_to_end_test.go`
- Create: `scripts/run-fuzz.sh`
- Create: `scripts/run-mutation.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/master.yml`

- [ ] Inventory all fuzz targets and assert each contains every relevant golden
  fixture/minimized regression seed. The script takes explicit duration and
  package filters and exits nonzero on missing targets.
- [ ] Add end-to-end generated scenarios spanning routes/classes,
  continuation/pins, prices/windows, failures, and replay. Assert no
  unauthorized class/route, no duplicate post-write submission, no budget
  overspend, and deterministic terminal outcome for the same seed.
- [ ] Configure focused mutation operators for money round-up/comparisons,
  bucket boundaries, tier mappings, dispatch certainty, and state transitions.
  Define the checked-in mutation baseline as every listed critical mutation
  killed.
- [ ] Run 5-minute local fuzz per target family, `go test -race -count=100` for
  concurrent packages, and mutation script. Expected: PASS.
- [ ] Master daily workflow runs longer bounded fuzz/property shards and uploads
  only redacted failure seeds. PR workflow runs seed replay plus short fuzz.
- [ ] Commit: `test: add fuzz property and mutation release gates`.

### Task 5: Add cost-capped live contract tests

**Files:**

- Create: `integration/live/options.go`
- Create: `integration/live/harness.go`
- Create: `integration/live/openai_test.go`
- Create: `integration/live/azure_test.go`
- Create: `integration/live/openrouter_test.go`
- Create: `integration/live/exa_test.go`
- Create: `integration/live/anthropic_test.go`
- Create: `integration/live/anthropic_aws_test.go`
- Create: `integration/live/bedrock_test.go`
- Modify: `.github/workflows/master.yml`
- Modify: `Makefile`

- [ ] Write harness tests proving live is disabled unless build tag, per-profile
  flag, credential reference, allow-listed model, test tenant, and positive
  `LLMTW_LIVE_MAX_MICRO_USD` are all present.
- [ ] Implement one minimal text request per profile plus supported tier/usage/
  cost/request-ID and continuation checks. Use unique operation keys, no side-
  effect tools, no personal content, and a hard aggregate admission policy.
- [ ] For economy/priority paths, assert returned actual tier is captured; do not
  fail merely because a documented provider downgrade occurred—fail only if
  reporting/mapping is wrong.
- [ ] Ensure test cleanup cannot submit another provider request and raw provider
  bodies are not uploaded.
- [ ] Add protected manual `live-contracts` job to master workflow. It is absent
  from schedule and requires the repository live-test environment.
- [ ] Run harness unit tests offline. With authorized credentials, run:

```sh
LLMTW_LIVE_MAX_MICRO_USD=500000 go test -tags=live ./integration/live/...
```

Expected: enabled profiles pass; unconfigured profiles skip with named reason;
aggregate reservation cannot exceed the cap.
- [ ] Commit: `test(live): add explicit cost-capped provider contracts`.

### Task 6: Produce signed release evidence

**Files:**

- Create: `scripts/release-verify.sh`
- Create: `release/evidence.schema.json`
- Create: `internal/buildinfo/buildinfo.go`
- Test: `internal/buildinfo/buildinfo_test.go`
- Modify: `Dockerfile`
- Modify: `.github/workflows/master.yml`
- Modify: `Makefile`
- Modify: `docs/architecture/deployment-and-operations.md` only for final
  measured limits/runbook links

- [ ] Write build-info tests for version, commit, dirty status, Go version,
  capability/price schema versions, and image labels. A dirty release fails.
- [ ] Write release script tests using a temporary Git repository; assert it
  refuses non-master/unreviewed/dirty commits, failed gates, absent SBOM/scan,
  mismatched image digest, or unredacted evidence.
- [ ] Implement deterministic build flags/OCI labels and evidence JSON
  containing test summaries, dependency/fixture manifests, image digest, SBOM,
  scan result, signature/provenance references, and source commit.
- [ ] Add protected manual release inputs/job to `master.yml`. Grant package/
  id-token permissions only to that job. Scheduled and normal master validation
  retain read-only permissions and cannot release.
- [ ] Build twice from clean checkouts and compare binary/image layers where
  reproducible metadata permits. Explain and minimize any measured difference.
- [ ] Sign/attest with repository-approved keyless identity and verify the
  signature/provenance before publishing.
- [ ] Run `scripts/release-verify.sh`. Expected: PASS and a redacted evidence
  bundle that validates against the schema.
- [ ] Commit: `build: produce verifiable signed release evidence`.

## Final release gate

- [ ] Run `make verify`, `make contract-matrix`, `make security-verify`,
  `make redis-integration`, `make temporal-integration`,
  `make compose-smoke`, and `make kustomize-verify` from a clean checkout.
- [ ] Run scheduled-equivalent fuzz/property/repetition locally or through a
  successful master dispatch.
- [ ] Run authorized live contracts with the hard aggregate cap.
- [ ] Verify master and PR workflow YAML plus the recorded 05:00 Sydney schedule.
- [ ] Run `scripts/release-verify.sh` and verify signature/provenance.
- [ ] Confirm `git diff --exit-code` and `git status --short` are empty.
- [ ] Create the v1 tag from the verified master commit; the scheduled workflow
  remains validation-only.
