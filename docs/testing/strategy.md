# Testing Strategy

## Test contract

Tests prove semantic preservation, durable retry safety, and budget invariants.
They do not merely prove that SDK constructors compile. All deterministic tests
run without credentials or internet access.

The standard implementation gate is run from the nested Go module:

```sh
cd golang
make fmt-check
go vet ./...
go test -race ./...
go build ./...
docker build --tag llm-temporal-worker:local .
```

`make fmt-check` delegates to `golang/scripts/check-go-format.sh`. The helper passes
NUL-delimited Go source paths to `gofmt -l`, excludes `vendor` and
`.worktrees`, and returns formatter failures instead of treating them as a
clean result. It never modifies the checkout.
The fuzz target is selected explicitly rather than through a placeholder name;
for example, a 30-second legacy provider decoder smoke is:

```sh
go test ./llm/provider/openairesponses -run=^$ -fuzz=FuzzDecodeStream -fuzztime=30s
```

For the repository's complete offline gate, run `make verify`. It checks
formatting, schemas, documentation links and invariants, vet, the ordinary
test suite, and every Go package build. It does not run the race detector or
the Docker, Compose, or Kubernetes gates shown below.

## Local performance proxy

Run the opt-in in-memory `Generate` benchmark with:

```sh
make benchmark
```

It emits Go's aggregate allocation and throughput metrics plus a sampled
`p99_ms/op` for deterministic successful `Generate` calls using the in-memory
admission store and a content-free adapter. Inputs are prebuilt and each timed
call has a fresh operation key, so the path includes normalization, planning,
pricing, memory admission, and adapter compilation rather than an operation
replay.

This is a local proxy, not a Redis or provider performance SLO proof: it makes
no Redis connection or provider network request, and it is deliberately not a
normal CI gate because runner scheduling and hardware change latency samples.
Do not compare its p99 output with the service objectives until repeatable,
controlled memory and same-region Redis evidence is available.

## Controlled Redis measurement

For a controlled, same-region Redis measurement, an operator may run:

```sh
LLMTW_REDIS_BENCHMARK=1 \
LLMTW_REDIS_BENCHMARK_ALLOW_MUTATION=1 \
LLMTW_REDIS_BENCHMARK_ADDR=redis.example.internal:6379 \
make redis-benchmark
```

This is deliberately not a normal CI target: it refuses any `CI` environment,
requires both explicit operator gates and an explicit Redis address, and does
not start Redis, load a Redis Function, or invoke a live provider. The target
requires a dedicated non-production Redis 7+ deployment with the exact
preloaded `llmtw_admission_v1` / `admission_v1` Function. It creates state
only beneath a randomly generated bounded key prefix and cleans up only keys
under that prefix; it never flushes a database.

The reported `p99_ms/op` is a measurement to retain with the deployment,
region, Redis configuration, sample count, and run timestamp. The benchmark
does not itself assert that a p99 or error-rate objective has been met.
Both CI workflows run `make redis-benchmark-compile`, which compiles the
build-tagged code with tests and benchmarks disabled; it has no Redis address,
operator gate, provider call, or Docker dependency.

## Local release gates

The repository also exposes bounded release-gate targets. They are safe to run
from a clean checkout without provider credentials, a Temporal server, a Redis
server, or a Kubernetes cluster:

```sh
make integration
make image-verify
make compose-smoke
make deployment-policy-verify
KUBECTL=/path/to/pinned/kubectl make kustomize-verify
make workflow-verify
```

`make integration` runs the offline integration packages with the race
detector. `make compose-smoke` parses the checked-in fixture and runs
`docker compose config --quiet`; it does not start containers. The Compose
worker remains a separate, explicitly authorized live operation because it
requires a continuation key and a provider/state runtime. Setting
`LLMTW_COMPOSE_LIVE=1` makes this boundary fail closed with instructions rather
than silently starting services. `make deployment-policy-verify` renders every
Kustomize overlay locally through `kubectl kustomize`, then checks the rendered
workload policy and static Kubernetes tests. It never applies anything to a
cluster or requires credentials. It keeps the worker's non-root UID/GID and
`fsGroup` aligned, requires group-readable runtime Secret files (`0440`), and
requires the Redis TLS patch to preserve one projected Secret volume rather
than combine mutually exclusive volume source types.

`make kustomize-verify` is the pinned-`kubectl` companion for the rendered
check. Set `KUBECTL` to a reviewed executable before invoking it; both targets
verify the same local render and never contact a cluster.

`make workflow-verify` runs pinned `actionlint` syntax validation and a strict
YAML contract test for the two checked-in GitHub Actions workflows. The test
requires immutable action commits with readable major-version comments,
read-only pull-request permissions with no provider credentials, and master
push/manual triggers plus the exact 05:00 `Australia/Sydney` schedule. It also
requires both workflows to run the same verification target before the Go
release gates and to compile the build-tagged provider contract harness with
`go test -tags=live ./integration/live -run '^$'`. That compile-only step has
no live gates or provider credentials; the master schedule runs the same safe
check. This target validates only checked-in workflow definitions; it does not
deploy, publish, or contact a provider.

The release plan may list additional future gates; the commands above are the
currently implemented targets. Both CI workflows call the same formatting
helper as `make fmt-check`, and it checks rather than modifies files.

`make image-verify` requires Docker. It builds an image from the checked-out
revision, compares its five build-metadata fields in both OCI labels and the
binary's `version` output, and starts the image without a shell or root
override. That runtime check requires numeric UID/GID `65532:65532`, a
read-only root filesystem, and only the documented `/tmp` tmpfs writable
mount before probing `/health/live` and `/health/ready`.

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

Tests use official SDK types and redacted wire fixtures so an SDK upgrade cannot
silently change encoding.

### Legacy decoder regression tests

The checked-in provider decoder tests currently exercise representative provider
event payloads
as complete input, one byte at a time, and deterministic seeded random chunks.
The chunk readers tolerate zero-length reads, but the provider tests do not yet
inject empty chunks or enumerate every two-part split point and CR/LF boundary.
Those cases remain part of the v1 completion matrix and must be added before
claiming exhaustive fragmentation coverage. For every case that is exercised,
the decoder must produce the same typed events and final response. Tests also cover
duplicate/out-of-order IDs, invalid UTF-8, partial JSON/tool arguments, missing
terminal event, terminal error after deltas, cancellation, and oversized event.

These decoder tests do not establish a streaming API, engine dispatch path, or
Temporal capability. Activity and integration tests prove that one-shot
`Generate` remains live through a delayed provider response and returns its
final response. Live raw token deltas never appear in heartbeat details or as
separate return records; only bounded progress and the final semantic response
cross the Temporal boundary.

### Property and fuzz tests

Fuzz targets include:

- canonical JSON parse/encode idempotence and duplicate-key rejection;
- request normalization idempotence;
- semantic item encode/decode;
- schema depth/size/subset validation;
- retained provider event-payload decoders;
- provider error-body decoder with leak checks;
- decimal price parsing/multiplication/ceil/overflow;
- budget window boundary and retry-after calculation;
- continuation handle parse/MAC/tenant binding;
- Redis Function argument codec;
- response aggregation under bounded arbitrary event sequences, each ending in
  either a semantic terminal result, a terminal error, or an explicit
  state-machine rejection.

Seed corpora contain every governed golden fixture plus minimized past failures.
Pull requests replay all checked-in seeds deterministically and verify the
focused mutation manifest:

```sh
make fuzz-smoke
make mutation-verify
```

`make fuzz-smoke` runs each `Fuzz*` target once with ordinary `go test`, so the
gate is reproducible and does not mutate a shared corpus. To reproduce a saved
corpus input directly, run:

```sh
go test <package> -run '^<FuzzTarget>/<corpus-entry>$' -count=1
```

The trusted master workflow separately runs three bounded `-fuzz` shards
(`FUZZ_TIME=250000x`, 250,000 executions and one worker per target).
`250000x` retains at least the slowest observed 45-second workload (248,282
executions) while avoiding deadline-based cancellation on busy runners.
`scripts/run-fuzz.sh shard <0|1|2>` uses the same explicit target list locally.
`make mutation-verify` compiles a reviewed overlay for each boundary mutation—
decimal round-up, budget comparison, dispatch certainty, omitted service class,
and legal state transition—and requires its named invariant to fail. A mutation
survivor is therefore a gate failure; adding a mutation requires either a
failing invariant or a documented semantic boundary before it can enter the
manifest.

### Store conformance

`storage/conformance` holds one black-box suite that accepts a `StoreFactory`.
The memory adapter runs it in the ordinary storage test suite; the Redis adapter
runs the unchanged suite against the isolated pinned daemon started by
`make redis-integration`. It tests:

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
- continuation immutable branching, MAC/tenant/digest checks, BlobRefs, and
  namespace isolation across prefix/hash-tag/key-secret changes;
- continuation record, handle-index, and child operation-idempotency TTLs,
  plus expired-index versus dangling-index fail-closed behavior.

Concurrency tests coordinate goroutines with barriers rather than sleeps and
assert accepted total never exceeds the limit. Race tests run for memory and
domain packages.

Run the full shared-state contract locally with:

```sh
make redis-integration
```

The target starts one uniquely named, loopback-only Redis 7.4.2 image pinned
by digest, discovers its ephemeral port, enables AOF plus snapshot persistence,
and removes only that container on completion. It exercises timeout-after-write
read resolution, Function/Lua mismatch handling, configured persistence across
a restart, and the configured single hash slot. Redis logs are emitted only on
failure after redaction. The target is invoked by the trusted master workflow,
not the pull-request workflow.

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

- block before read, read partially, then close, and accept and delay;
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

Shared legacy decoder fixtures may live once at the parent `testdata/contracts` root:

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
code-owned case matrix selected by capability facts. A profile may use
`bootstrap` while its dedicated fixture task is in progress, but repository
validation requires every checked-in production profile to be `enforced`. The
offline release target makes that requirement visible:

```sh
make adapter-contracts
```

For each enforced profile, its adapter fixture test uses the shared
`contracttest.VerifySemanticRoundTrip` helper for reversible semantic
conversion. Decoder and semantic-assembly helpers remain parser-regression
coverage only; they do not establish a v1 dispatch API.

Golden updates require an intentional `-update` test flag locally, a human
reviewable diff, and source-contract update. Normal tests never rewrite files.

## Optional live tests

Live provider contracts are build-tagged `live` and disabled by default. Their
full safety model, pinned profile matrix, and protected-workflow handoff are in
[Guarded live-provider contracts](../reference/live-provider-contracts.md).

The compile-only gate is safe in ordinary CI:

```sh
go test -tags=live ./integration/live -run '^$'
```

An invocation needs all three exact `"1"` gates: `LLMTW_LIVE_TESTS`,
`LLMTW_LIVE_AUTHORIZED`, and the selected profile enable flag. Fork and normal
pull-request workflows receive neither those gates nor live credentials;
scheduled workflows, master pushes, and release/publication paths must not
invoke the suite. The protected manual live-provider workflow dispatches one
selected profile from `master` after protected-environment approval, with its
allow-listed model, tenant, and cost ceiling. It verifies and uploads only
redacted live-provider evidence; that evidence does not prove a release was
signed or published. A live failure never auto-updates capabilities or prices.

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
