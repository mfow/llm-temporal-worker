# Testing Strategy

## Test contract

Tests prove semantic preservation, durable retry safety, and budget invariants.
They do not merely prove that SDK constructors compile. All deterministic tests
run without credentials or internet access.

The standard implementation gate is:

```sh
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
docker build --tag llm-temporal-worker:local .
```

`gofmt -l` reports files that need formatting without changing the checkout.
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
currently implemented targets. CI checks formatting with `gofmt -l` rather than
modifying files.

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

For each representative stream byte sequence, a fragmentation harness feeds:

- the complete stream;
- one byte at a time;
- every two-part split point;
- deterministic random chunking;
- empty chunks and CR/LF boundary splits.

All chunkings must produce the same typed events/final response. Tests also cover
duplicate/out-of-order IDs, invalid UTF-8, partial JSON/tool arguments, missing
terminal event, terminal error after deltas, cancellation, and oversized event.

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

Seed corpora contain every golden fixture plus minimized past failures. CI runs
short deterministic fuzz smoke; scheduled master CI runs longer fuzz jobs once
implementation exists.

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

Each fixture directory contains:

```text
request.semantic.json
request.wire.json
response.wire.json
response.semantic.json
events.wire
events.semantic.jsonl
metadata.yaml
```

`metadata.yaml` records profile ID, SDK version, upstream contract URL/date,
capture/synthetic provenance, redactions, expected capability, and tier/cost
facts. Fixtures contain no real credentials, personal content, account IDs, or
unstable timestamps. A redaction test scans both decoded and raw bytes.

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
