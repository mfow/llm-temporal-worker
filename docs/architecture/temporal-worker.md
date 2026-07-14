# Temporal Worker

## Activity boundary

The worker registers one versioned Activity:

```text
llm.generate.v1
```

The Activity performs exactly one inference turn. It can return model output or
tool calls, but it never executes tools or loops until a final answer. Agent
orchestration belongs in caller Workflows so every tool result and decision is
durable and visible.

The reusable engine is usable without Temporal. The Activity is a thin adapter:

```go
type Activities struct {
	Engine       llm.Engine
	Heartbeater  Heartbeater
	Logger       *slog.Logger
}

func (a *Activities) Generate(
	ctx context.Context,
	req activity.GenerateRequest,
) (activity.GenerateResponse, error)
```

Dependencies are injected into an Activity struct and registered as methods.
The Activity does not construct clients, read environment variables, or mutate
global state.

The process-level composition lives in `internal/runtime`. It validates and
publishes one non-secret configuration snapshot, creates the Temporal client
with TLS roots loaded from bounded regular files, starts separate health and
metrics listeners, and injects the Activity's engine through a snapshot lease.
Provider/state construction is an explicit `EngineFactory` seam. The CLI uses
`ProductionEngineFactory` to compose verified catalogs, provider adapters,
Redis state, and blob-backed results; tests and custom deployments can inject a
different factory, and unsupported configured dependencies still fail closed
before a worker starts.

## Payload contract

`GenerateRequest` wraps the canonical `llm.temporal/v1` request with only
Temporal-boundary metadata. `GenerateResponse` contains the normalized response
and safe execution metadata. Both have JSON fixtures and deterministic
round-trip tests.

Provider SDK types, clients, secrets, raw prompts in errors, and unbounded binary
content never enter Temporal payloads. Inline payload limits default well below
Temporal's service limits. Larger inputs and completed responses use immutable
`BlobRef` values containing store, locator, digest, byte length, media type, and
expiry. The worker verifies the digest after reading.

Installations that require history confidentiality configure a Temporal Payload
Codec outside the worker. The worker's contract remains codec-agnostic and does
not claim that a Data Converter alone encrypts data.

## Required caller options

The library exports validated helpers for callers to build Activity options:

```go
type ActivityPolicy struct {
	StartToClose        time.Duration
	ScheduleToClose     time.Duration
	HeartbeatTimeout    time.Duration
	InitialRetry        time.Duration
	BackoffCoefficient float64
	MaximumRetry        time.Duration
	MaximumAttempts     int32
}
```

Defaults are documented examples, not universal provider timeouts. Validation
requires:

- schedule-to-close greater than start-to-close;
- heartbeat timeout shorter than start-to-close for long calls;
- retry horizon no longer than operation-record retention;
- maximum attempts bounded;
- no Temporal retry for application errors marked non-retryable;
- the request's provider deadline shorter than Activity start-to-close, leaving
  time to finalize ledger state.

Temporal task-queue priority, when configured by a deployment, is an
orchestration concern. It is never derived from the request's
`economy | standard | priority` provider processing class.

## Retry ownership

Provider SDK retries are set to zero. One Activity attempt may traverse safe
route candidates under the engine's bounded plan, but it never resubmits after
an ambiguous write. Temporal retries an Activity only after the common error
classifier marks the outcome safe.

On Activity retry:

1. the same `operation_key` and normalized request digest reach `Begin`;
2. a completed operation returns its stored result;
3. a reserved operation with a proven expired pre-dispatch lease can resume;
4. a dispatching/ambiguous operation returns a non-retryable ambiguity error;
5. a conflicting digest returns a non-retryable conflict.

This makes worker crashes recoverable without assuming an external provider has
exactly-once semantics.

## Heartbeats

`Generate` first asks `llm.Engine.Stream` for a real provider stream. It
observes `response_started` and `content_completed` only to report bounded
streaming progress, then returns the sole `response_completed` value.
`Stream` returns a direct typed `unsupported_capability` error with
`not_dispatched` certainty when its pre-admission route/capability preflight
finds no real `StreamingAdapter`. Only in that narrow case, before an
`EventStream` or durable operation exists, the Activity invokes native
`Engine.Generate` and returns its final semantic response. The fallback match
also requires stream phase and an empty operation ID, so an unsupported
compile/planning error or any operation-bearing error cannot enter the native
path. It never falls back after a stream has been returned or after a stream
terminal event. Text/JSON
deltas, tool arguments, and opaque provider-state events are deliberately
drained without being copied as live event payloads into a Temporal heartbeat;
only the final normalized response crosses the Activity return boundary. A
stream error is converted by the normal common-error classifier.

Heartbeats contain small, redacted progress only:

```go
type HeartbeatDetails struct {
	OperationID  string
	Phase        string
	RouteIndex   int
	ClassIndex   int
	StartedAt    time.Time
	LastEventAt  time.Time
	OutputItems  int
}
```

Phases are `planning`, `admitted`, `dispatching`, `streaming`, and
`finalizing`. No text, tool arguments/results, provider state, secret, raw error,
or SDK object is allowed.

The Activity heartbeats:

- while planning, and for real streams when a response starts or an output
  item completes, with only bounded counts;
- before returning a finalized semantic response, including the native
  pre-admission fallback path;

Heartbeat failure does not cancel the provider call by itself; context
cancellation does. The implementation watches `ctx.Done()` through all
provider, blob, and storage calls.

## Cancellation

Cancellation before a possible write releases the reservation and records
`canceled`. Cancellation after possible write is ambiguous unless a provider
response or status lookup proves rejection/acceptance. The Activity attempts a
bounded, shielded final ledger write during shutdown, then returns the
classified error.

Adapters must use the Activity context in official SDK calls. A detached
background context is allowed only for the short, bounded ambiguity/finalization
record and must retain tracing identifiers without prompt data.

## Error mapping

The Activity converts common errors to Temporal Application Errors:

| Common class | Temporal type | Retry |
| --- | --- | --- |
| invalid request/config/capability | `llm_invalid_argument` | non-retryable |
| auth/permission | `llm_authentication` | non-retryable until configuration changes |
| budget denial within retry horizon | `llm_budget_wait` with safe details | retryable with calculated delay |
| definite transient provider failure | `llm_provider_transient` | retryable |
| ambiguous provider outcome | `llm_ambiguous_dispatch` | non-retryable |
| operation digest conflict | `llm_operation_conflict` | non-retryable |
| canceled | Temporal cancellation | controlled by caller |
| corrupt durable state | `llm_state_corrupt` | non-retryable and alert |

Error details include operation ID, safe code, retry-after, and request ID only.

## Worker lifecycle

Startup order:

1. parse and validate configuration;
2. resolve secret references;
3. create provider/Redis/blob/telemetry clients and compile the immutable
   snapshot, including bounded checks of every required Redis and bucket
   dependency before the snapshot is published;
4. construct the Temporal client and register Activities on the configured
   task queue;
5. bind the health and metrics listeners;
6. recheck required dependencies and start the Temporal worker only when all
   checks pass;
7. mark readiness true and keep periodically checking dependencies, pausing
   and resuming polling as their combined state changes.

Redis readiness verifies connectivity, the configured persistence and eviction
policy, and the configured preloaded Function or Lua digest without loading or
replacing server-side code. Blob readiness performs a bucket-only check without
reading a tenant object. An initial failed check rejects the unpublished
snapshot; a reload failure leaves the old snapshot in place.

On `SIGTERM`/`SIGINT`, readiness turns false first. The process stops polling,
allows the Temporal worker's configured graceful stop timeout, flushes telemetry
within a bound, and exits nonzero when shutdown integrity fails. Kubernetes
termination grace must exceed worker graceful stop plus telemetry flush.

`cmd/llm-temporal-worker` installs a signal-aware context and delegates the
worker command to `internal/runtime`. Runtime shutdown closes probe listeners,
drains the captured snapshot clients, and closes the Temporal SDK client after
polling has stopped. Runtime errors are bounded, actionable messages and never
include resolved secret bytes or provider payloads.

Liveness proves only that the process event loop is responsive. Readiness
requires a valid snapshot, a polling Temporal worker, and healthy required
Redis and blob dependencies. A later dependency failure keeps liveness
responsive, makes readiness false, and stops polling; the bounded monitor
resumes polling only after every required check recovers. Provider availability
is a route-health concern and is evaluated by request planning rather than by
this probe.

## Local Compose recovery proof

`LLMTW_COMPOSE_LIVE=1 make compose-live-integration` is the opt-in local
operational proof. It starts a uniquely named Compose project with pinned
Postgres-backed Temporal and Redis services, the development-only file blob
store, and the same worker health endpoints used by Kubernetes. It checks
liveness and readiness through the worker binary, stops Redis while the worker
runs, and requires readiness to become unavailable while liveness remains
available before polling can resume after Redis returns.

The same gate runs a real Temporal SDK Activity with two worker replicas and a
shared Redis admission store. Its adapter is content-free and in-process: it
records a possible provider write, the first worker stops, and the replacement
must receive either a completed durable replay or the conservative ambiguity
terminal result. The fixture asserts one dispatch and one bounded shared-budget
reservation, never makes a provider-network request, and does not relax the
Docker-private provider-egress denial.

## Temporal tests

The Temporal Go SDK Activity test environment covers:

- registration under the exact versioned name;
- payload round trips and oversized BlobRef behavior;
- heartbeat detail schema and cancellation delivery;
- completed-operation replay after simulated worker loss;
- definite pre-write retry and ambiguous post-write non-retry;
- budget retry-after details;
- non-retryable type names and safe details;
- graceful worker shutdown with an in-flight Activity.

The opt-in Compose gate complements those deterministic tests with a real
Temporal service, shared Redis admission ledger, worker-stop recovery, and
the live readiness probe transition. It is intentionally excluded from normal
offline tests and pull-request CI.

Workflow-level tests live in an example package and demonstrate caller-owned
tool execution, but the worker does not ship a general agent Workflow.
