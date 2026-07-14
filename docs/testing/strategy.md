# Testing Strategy

## Test contract

Tests prove semantic preservation, durable retry safety, and budget invariants.
They do not merely prove that SDK constructors compile. All deterministic tests
run without credentials or internet access.

The standard implementation gate is:

```sh
make fmt-check
go vet ./...
go test -race ./...
go build ./...
docker build --tag llm-temporal-worker:local .
```

`make fmt-check` delegates to `scripts/check-go-format.sh`. The helper passes
NUL-delimited Go source paths to `gofmt -l`, excludes `vendor` and
`.worktrees`, and returns formatter failures instead of treating them as a
clean result. It never modifies the checkout.
The fuzz target is selected explicitly rather than through a placeholder name;
for example, a 30-second provider stream smoke is:

```sh
go test ./llm/provider/openairesponses -run=^$ -fuzz=FuzzDecodeStream -fuzztime=30s
```

For the repository's complete offline gate, run `make verify`. It checks
formatting, schemas, documentation links and invariants, vet, the ordinary
test suite, and every Go package build. It does not run the race detector or
the Docker, Compose, or Kubernetes gates shown below.

## Local release gates

The repository also exposes bounded release-gate targets. They are safe to run
from a clean checkout without provider credentials, a Temporal server, a Redis
server, or a Kubernetes cluster:

```sh
make integration
make compose-smoke
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
```

`make integration` runs the offline integration packages with the race
detector. `make compose-smoke` parses the checked-in fixture and runs
`docker compose config --quiet`; it does not start containers. The Compose
worker remains a separate, explicitly authorized live operation because it
requires a continuation key and a provider/state runtime. Setting
`LLMTW_COMPOSE_LIVE=1` makes this boundary fail closed with instructions rather
than silently starting services. `make kustomize-verify` runs static manifest
tests and `deploy/verify.sh`, which renders each overlay locally through
`kubectl kustomize` and never applies anything to a cluster. Set `KUBECTL` to a
reviewed, pinned executable when `kubectl` is not on `PATH`.

The release plan may list additional future gates; the commands above are the
currently implemented targets. Both CI workflows call the same formatting
helper as `make fmt-check`, and it checks rather than modifies files.

## Test layers

### Domain unit tests

Table tests cover request normalization, canonical JSON/digests, schema subset
validation, content limits, service-class validation, route ordering, price
arithmetic, budget matching/windows, error classification, continuation
pinning, and legal ledger transitions.

Clocks, ID generators, and endpoint health are injected. Tests never depend on
wall time, map iteration, random route order, or external pricing.

### Adapter contract tests

Every adapter package runs the same suite:

- semantic request -> exact SDK/wire parameters;
- provider response -> exact semantic response;
- semantic request -> provider -> semantic round-trip where reversible;
- every supported content/item/tool/schema/reasoning/control field;
- strict failure and best-effort diagnostic for unsupported/lossy features;
- requested/provider/actual service class;
- usage, cache, reasoning, cost, finish status, IDs, and errors;
- provider-state byte and order preservation;
- continuation compatible/incompatible route facts;
- non-streaming and streaming equivalence.

Tests use official SDK types and redacted wire fixtures so an SDK upgrade cannot
silently change encoding.

### Streaming tests

The checked-in provider decoder tests currently exercise representative streams
as complete input, one byte at a time, and deterministic seeded random chunks.
The chunk readers tolerate zero-length reads, but the provider tests do not yet
inject empty chunks or enumerate every two-part split point and CR/LF boundary.
Those cases remain part of the v1 completion matrix and must be added before
claiming exhaustive fragmentation coverage. For every case that is exercised,
the decoder must produce the same typed events and final response. Tests also cover
duplicate/out-of-order IDs, invalid UTF-8, partial JSON/tool arguments, missing
terminal event, terminal error after deltas, cancellation, and oversized event.

The engine stream contract suite additionally proves that a non-streaming
adapter is rejected before admission or provider dispatch with a direct typed
error (rather than an EventStream or a fabricated `Generate` result), event
order is preserved, and every returned stream has exactly one typed terminal
followed by EOF. It covers bounded-buffer backpressure, cancellation closing
the provider source, duplicate-terminal rejection, byte-exact opaque provider
state, completed-operation replay, pre-write fallback retry, ambiguous
post-write replay refusal, filtered stream-only budget reservations, and
equivalent finalized stream/non-stream responses. Activity and integration
tests prove that a real stream is consumed when available, while the exact
pre-admission unsupported error uses native `Generate` for a non-streaming
production-style adapter. Live raw stream deltas never appear in heartbeat
details or as separate return records; only bounded progress and the final
semantic response cross the Temporal boundary.

### Property and fuzz tests

Fuzz targets include:

- canonical JSON parse/encode idempotence and duplicate-key rejection;
- request normalization idempotence;
- semantic item encode/decode;
- schema depth/size/subset validation;
- every provider stream decoder;
- provider error-body decoder with leak checks;
- decimal price parsing/multiplication/ceil/overflow;
- budget window boundary and retry-after calculation;
- continuation handle parse/MAC/tenant binding;
- Redis Function argument codec;
- response aggregation under arbitrary event sequences.

Seed corpora contain every golden fixture plus minimized past failures. The
current pull-request and master workflows replay those seeds through ordinary
`go test` but do not run Go's `-fuzz` mode. Short deterministic fuzz smoke and
longer scheduled fuzz jobs remain planned release gates; run a named target
locally as shown above when investigating a decoder or parser change.

### Store conformance

One black-box suite accepts a `StoreFactory` and runs unchanged against memory
and a real Redis container. It tests:

- all operation transitions and invalid compare-and-set tokens;
- idempotent Begin/Complete/Fail;
- completed replay and request digest conflict;
- concurrent admission at exactly below/equal/above each limit;
- multiple matching policies and overlapping windows;
- same timestamp, boundary timestamp, clock rollback, and expiry;
- refunds tied to original bucket and underestimation excess;
- ambiguous reservation retention;
- Redis timeout after mutation followed by read resolution;
- Function version/digest mismatch;
- continuation immutable branching, MAC/tenant/digest checks, TTL, and BlobRefs.

Concurrency tests coordinate goroutines with barriers rather than sleeps and
assert accepted total never exceeds the limit. Race tests run for memory and
domain packages.

### Temporal tests

The Temporal SDK test environment verifies Activity registration, payload/error
types, heartbeats, cancellation, retry behavior, completed replay, budget wait,
and safe details. A local Temporal integration test verifies real task polling,
worker restart after dispatch, schedule/start/heartbeat timeouts, and graceful
shutdown.

The offline lifecycle gate in `integration/temporal_lifecycle_test.go` covers the
same process boundary without credentials or a running Temporal service. It
captures the SDK Activity registry to assert the exact `llm.generate.v1` name,
round-trips the versioned payload, invokes the real Activity and engine twice to
prove completed replay performs one provider dispatch and one result write, and
checks the `economy | standard | priority` contract (omission defaults to
`standard`). It also starts the worker through its lifecycle seam, checks
readiness and polling transitions, bounds shutdown while a fake poller drains,
and scans logs, traces, and metrics for tenant/content markers. Run it with:

```sh
go test -race ./integration
```

### Deployment tests

- strict configuration schema/golden effective config;
- Docker non-root/read-only execution and health probes;
- Kubernetes Kustomize build and policy assertions;
- Compose one- and two-replica smoke;
- Redis restart/failover and persistence configuration;
- signal/reload behavior with valid and invalid snapshots;
- log/trace/heartbeat deny-field assertions;
- SBOM and vulnerability scanner gates.

## Provider mocks

Tests use `httptest.Server` or SDK-supported transports that can:

- block before read, read partially, then close, accept and delay, and stream;
- capture exact headers/body after secret redaction;
- return fixed request IDs, tiers, usage, and costs;
- assert one network submission despite Activity retries;
- simulate rate, auth, invalid request, capacity, malformed response, and
  ambiguous timeout outcomes.

TLS mock variants verify certificate/redirect/base-URL policy. No mock behavior
is inferred from HTTP status alone; the fixture declares dispatch/cost contract.

## Golden fixture governance

Each profile directory contains a manifest and metadata file plus the local
artifacts it currently declares:

```text
manifest.yaml
metadata.yaml
request.semantic.json
request.wire.json
response.wire.json
response.semantic.json
```

Shared stream fixtures may live once at the parent `testdata/contracts` root:

```text
events.wire
events.semantic.jsonl
```

`metadata.yaml` records profile ID, SDK version, HTTPS upstream contract URL,
source review date, capture/synthetic provenance, redactions, capability
facts, and generated-field exemptions. Fixtures contain no real credentials,
personal content, account IDs, or unstable timestamps. The adapter-contract
harness strictly parses manifests and metadata, verifies declared files stay
within the profile, and scans every raw file below `testdata/contracts` for
credential-like bytes. It only reports clean repository-relative paths.

`coverage: bootstrap` means the profile is structurally governed but is not a
claim of complete adapter coverage. `coverage: enforced` activates the full
code-owned case matrix selected by capability facts. No production profile is
enforced until its dedicated fixture task supplies every required case. The
offline release target makes that distinction visible:

```sh
make adapter-contracts
```

For each enforced profile, its adapter fixture test uses the shared
`contracttest.VerifySemanticRoundTrip` helper for reversible semantic
conversion and `contracttest.VerifyStreamAssemblyEquivalent` for
stream/non-stream response assembly. Both helpers compare JSON after removing
only that profile's explicit generated-field exemptions.

That adapter-contract helper verifies decoder and semantic-assembly behavior;
it does not implement the live `StreamingAdapter` port or exercise
`llm.Engine.Stream` provider dispatch. The typed engine-stream lifecycle stays
separate until a production adapter supplies that port.

Golden updates require an intentional `-update` test flag locally, a human
reviewable diff, and source-contract update. Normal tests never rewrite files.

## Optional live tests

Live tests are build-tagged `live` and disabled by default. Each endpoint needs
an explicit enable flag, credentials, allow-listed model, maximum microUSD, and
test tenant. Tests use tiny deterministic prompts and no tools with side effects.

They verify only facts mocks cannot: authentication, current wire acceptance,
reported actual tier, request IDs, usage/cost, and continuation. A live failure
does not auto-update capabilities or prices. Fork pull requests never receive
credentials.

## Coverage and mutation expectations

Line coverage is reported per package but invariants are the gate. Critical
state transitions, money arithmetic, capability rejection, adapters, and error
classifiers require branch coverage for every enumerated case. Mutation tests
target comparison boundaries, round-up direction, dispatch certainty, tier
mapping, and state transitions before v1 release.

## Release evidence

The release job retains:

- Go test/race/fuzz summaries;
- adapter fixture manifest and source dates;
- Redis/Temporal/Compose integration logs with content redacted;
- configuration schema and example validation;
- Kustomize rendered manifest;
- image digest, SBOM, scan, signature, and provenance;
- dependency/module inventory.
