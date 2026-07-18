# Package and Artifact Layout

The Go worker is a standalone module under `golang/` with module path
`github.com/mfow/llm-temporal-worker/golang`; its Go baseline is 1.26. The
repository root is intentionally available for additional clients such as the
OCaml wrapper. Run Go commands from `golang/` (or use the root Makefile
forwarder). Public reusable packages avoid `internal/`; process wiring and
helpers that are not stable APIs use `internal/`.

The tree below describes the packages and artifacts checked into the current
implementation. It is not a promise that every item in the v1 completion plans
already exists; planned work remains marked in those plans rather than in this
current-layout reference.

```text
.
├── golang/                         Go Temporal worker module
│   ├── api/schema/v1/              Versioned request, response, and config schemas
├── golang/cmd/llm-temporal-worker/ Process entry point only
├── golang/llm/                     Provider-neutral public domain API
│   ├── schema/                     JSON Schema normalization and local validation
│   └── provider/                   Adapter interfaces and provider-neutral calls
│       ├── contracttest/           Fixture manifest and coverage contract harness
│       ├── internal/               Shared adapter configuration and test helpers
│       │   ├── clientconfig/       Provider endpoint and URL configuration helpers
│       │   ├── contract/           Shared adapter contract-test helpers
│       │   ├── streamdecode/       Shared SSE and event-stream decoding
│       │   └── streamtest/          Deterministic stream-fragmentation helpers
│       ├── openairesponses/        OpenAI Responses lowering/lifting
│       ├── openaichat/             OpenAI-compatible Chat lowering/lifting
│       ├── anthropicmessages/      Direct and Claude Platform AWS Messages
│       └── bedrockmessages/        Bedrock Mantle and isolated legacy runtime profile
├── golang/llm/testdata/            Shared normalized request and response fixtures
├── golang/engine/                  End-to-end inference lifecycle composition
├── golang/routing/                 Route planning, health, fallback, pinning
├── golang/pricing/                 Catalogs, decimal arithmetic, and quotes
├── golang/budget/                  Policy matching, estimation, sliding-window semantics
├── golang/admission/               Operation state machine and atomic store port
├── golang/state/                   Continuation records, handles, blob references
├── golang/activity/                Temporal payloads and Activity implementation
├── golang/integration/             Offline Temporal, Compose, and Kubernetes gates
│   ├── temporal_lifecycle_test.go  Offline Temporal lifecycle gate
│   ├── compose/                    Local compose-stack smoke tests
│   └── kubernetes/                 Manifest and deployment verification tests
├── golang/config/                  External config structs and snapshot compiler
├── golang/storage/blob/            Shared content-addressed blob-store port
├── golang/storage/memory/          In-process conformance implementation
├── golang/storage/redis/           Redis admission and continuation implementation
├── golang/storage/redis/functions/ Redis Function source and loader
├── golang/storage/fileblob/        Development-only content-addressed blobs
├── golang/storage/s3blob/          Production object-store blob implementation
├── golang/internal/                Process wiring and repository-only verification
│   ├── app/                        Process construction, reload, worker lifecycle
│   ├── architecturetest/           Import-boundary and workflow-structure tests
│   ├── catalog/                    Verified capability and price catalog loading
│   ├── documentationtest/          Markdown link and documentation invariant tests
│   ├── httpserver/                  Health, readiness, and metrics HTTP server
│   ├── observability/               slog, metrics, and trace wiring
│   ├── runtime/                     CLI process composition and Temporal client wiring
│   └── secrets/                     Environment/file secret resolution
├── golang/deploy/local/provider-mock/ Local provider-mock container
├── golang/deploy/kubernetes/base/  Kustomize base
├── golang/deploy/kubernetes/examples/ Non-secret example overlays
├── golang/llm/provider/*/testdata/contracts/ Redacted provider wire fixtures by adapter
│   ├── golang/Dockerfile            Multi-stage non-root worker image
│   ├── golang/compose.yaml          Local Temporal, Redis, and worker smoke stack
│   ├── golang/config.example.yaml   Safe, non-secret complete example
│   ├── golang/go.mod
│   └── golang/go.sum
├── ocaml/                          OCaml Temporal client wrapper
└── docs/                           Repository-wide architecture and release docs
```

## Dependency direction

The permitted dependency direction is:

```text
cmd -> internal/runtime -> internal/app
internal/app -> activity/engine/config/storage/provider/observability
activity -> engine + Temporal SDK
engine -> llm/routing/pricing/budget/admission/state
provider adapters -> llm/provider + one official provider SDK
storage implementations -> admission/state/budget ports + backend client
domain packages -> Go standard library and narrowly selected pure libraries
```

The following imports are forbidden:

- `llm`, `routing`, `pricing`, `budget`, `admission`, or `state` importing a
  provider SDK, Temporal SDK, Redis client, or process configuration package.
- One provider adapter importing another adapter.
- The Temporal Activity performing provider conversion directly.
- The router reading environment variables or secrets.
- Storage packages importing Activity payload types.

An import-boundary test runs `go list -deps -json` and fails when these rules are
violated.

## Stable API surface

The compatibility promise covers exported types in:

- `llm`
- `llm/provider`
- `routing`
- `pricing`
- `budget`
- `admission`
- `state`
- `activity`
- `engine`

Concrete storage and provider packages may add fields compatibly but callers
should construct them through documented constructors. `internal/` has no
compatibility guarantee.

## File ownership by implementation phase

| Phase | Owns |
| --- | --- |
| Foundation | `go.mod`, `llm`, schemas, config primitives |
| Adapters | `llm/provider/*`, redacted contract fixtures |
| Routing and continuation | `routing`, `state`, memory state |
| Pricing and budgets | `pricing`, `budget`, `admission`, Redis storage |
| Worker and deployment | `engine`, `activity`, `cmd`, app wiring, observability, Docker, Kubernetes |
| Verification and release | cross-package conformance, fuzz, race, integration, live gates |

No phase may add a shortcut across a dependency boundary to make a test pass.
If an interface is insufficient, update the owning plan and its contract tests
before adding the new capability.
