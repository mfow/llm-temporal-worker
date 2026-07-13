# LLM Temporal Worker

This repository is the documentation-first design for a reusable Go library and
Temporal Activity Worker that performs large-language-model inference. The
worker presents one versioned request contract, selects a configured endpoint,
enforces cost budgets, invokes the provider through an official SDK, and
returns a normalized response plus a durable continuation handle.

The repository contains a verified implementation foundation alongside the
documents and implementation plans. The plans remain the reviewed contract for
the staged Temporal runtime, shared-state backends, deployment, and release
work that follows the foundation.

## Non-negotiable v1 decisions

| Area | Decision |
| --- | --- |
| Public processing class | Exactly `economy`, `standard`, or `priority`; omission normalizes to `standard` |
| Provider abstraction | Semantic item IR compiled to a provider request; never a lowest-common-denominator text blob |
| Provider clients | Official OpenAI, Anthropic, AWS, and Temporal Go SDKs; raw HTTP is isolated to a documented SDK gap |
| Retry ownership | Provider SDK retries are disabled; the worker classifies outcomes and Temporal owns durable retries |
| Ambiguous dispatch | Never resend automatically when a provider may have accepted a billable request |
| Continuation | Immutable opaque handles backed by a state store and pinned to an endpoint when provider state requires it |
| Budget accounting | Conservative preflight reservation across every matching sliding window, followed by refund/finalization |
| Shared state | Redis is the v1 production backend; memory is for tests and single-process development only |
| Activity scope | Inference only; tool execution and agent-loop orchestration stay in caller workflows |
| Streaming | Typed stream events are a reusable library API; the Temporal Activity consumes them and returns a final value |
| Deployment | One stateless worker image, horizontally scalable when Redis is enabled |
| Go baseline | Go 1.26, using the latest security patch in that release line |

## Read in this order

1. [Scope and success criteria](scope.md)
2. [System overview](architecture/system-overview.md)
3. [Unified API](architecture/unified-api.md)
4. [Provider adapters](architecture/provider-adapters.md)
5. [Routing and continuation](architecture/routing-and-continuation.md)
6. [Pricing and budgets](architecture/pricing-and-budgets.md)
7. [Temporal worker](architecture/temporal-worker.md)
8. [State and storage](architecture/state-and-storage.md)
9. [Deployment and operations](architecture/deployment-and-operations.md)
10. [Security and privacy](architecture/security-and-privacy.md)
11. [Testing strategy](testing/strategy.md)
12. [Master implementation sequence](superpowers/plans/2026-07-13-master-sequence.md)

## Reference material

- [Package layout](architecture/package-layout.md)
- [Configuration reference](reference/configuration.md)
- [Error model](reference/error-model.md)
- [Verified upstream contracts](reference/source-contracts.md)
- [Adapter fixture matrix](testing/fixture-matrix.md)
- [Architecture decisions](decisions/)

## v1 completion gate

The first release is complete only when all of these statements are true:

- The same semantic request passes contract tests against OpenAI Responses,
  OpenAI-compatible Chat Completions, Anthropic Messages, Claude Platform on
  AWS, and Amazon Bedrock Anthropic profiles supported by the capability
  matrix.
- Every adapter has request, response, error, usage, and fragmented-stream
  golden tests; strict-mode lossy conversions fail before dispatch.
- The router is deterministic for a fixed configuration snapshot and never
  changes service class without an explicit request fallback.
- The operation ledger returns a cached completed result on an Activity retry
  and refuses to replay an ambiguous provider dispatch.
- Memory and Redis budget backends pass the same conformance suite, including
  concurrent overlapping-window admission tests.
- The worker passes Temporal Activity tests for retry, heartbeat, cancellation,
  graceful shutdown, and non-retryable error typing.
- The container runs as a non-root user, Kubernetes probes exercise real
  readiness dependencies, and two-replica Redis-backed smoke tests pass.
- Pull-request and master GitHub Actions are green; master also builds daily at
  05:00 in `Australia/Sydney`.
