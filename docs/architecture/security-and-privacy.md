# Security and Privacy

## Trust boundaries

Untrusted inputs include Activity payloads, continuation handles, configuration
files, provider responses/streams, Redis values, blob objects, compatible API
endpoints, and all namespaced extensions. Authentication does not make content
structurally trusted.

The worker validates before:

- allocating from claimed lengths;
- following a URL or blob locator;
- compiling JSON Schema;
- sending data to a configured endpoint;
- decoding provider-state bytes;
- using provider cost/usage in arithmetic;
- emitting error or observability fields.

## Tenant isolation

Every request has a validated tenant scope. Operation keys, continuation records,
result blobs, budgets, and audit identifiers bind to that scope. Store lookup
requires the scope and handle MAC; knowing a continuation ID alone is
insufficient.

Logical model routes declare allowed tenants/projects, regions, and data
classifications. A continuation cannot cross those restrictions. Shared Redis
keys use keyed hashes so tenant identifiers are not exposed in key scans.

## Secrets

Configuration contains references, not secret values:

```yaml
auth:
  kind: bearer_env
  name: OPENAI_API_KEY
```

`auth.kind` names the authentication mode (`bearer_env`, `header_env`, or a
provider workload-identity/default chain). Standalone secret references use
`kind: env`, `file`, or `workload_identity` instead.

V1 resolvers support environment, mounted file, and platform workload identity
where the official SDK supports it. Secret values:

- are resolved only at process/client construction;
- never enter snapshots, digests, Temporal payloads, logs, metrics, traces,
  errors, continuation records, or test fixtures;
- are held for the minimum useful lifetime;
- are redacted by allow-list logging, not regex alone;
- rotate by building a new client snapshot.

Arbitrary command execution as a secret resolver is not supported.

## Egress

Endpoints are operator-configured and validated:

- HTTPS is required outside explicit local development;
- base URLs cannot contain user info, query, or fragment;
- redirects are disabled or restricted to the same validated origin;
- DNS/IP policy can reject loopback, link-local, private, or metadata ranges;
- proxy use is explicit;
- custom CA roots are file references;
- compatible endpoints require an allow-listed hostname and profile ID.

User-provided image/reference URLs are not fetched by the worker in v1. They are
passed only to endpoint profiles that accept remote references and only after
scheme/host policy validation. Blob locators address configured stores, not
arbitrary URLs.

## Content and history

Prompts, outputs, tool arguments/results, images, documents, reasoning, and
provider state are sensitive by default. The worker:

- avoids content in logs, metrics, trace attributes, heartbeat details, and
  ledger records;
- uses BlobRefs to limit Temporal history content/size;
- supports an external Temporal Payload Codec for encryption;
- encrypts production blob/Redis traffic in transit and relies on configured
  at-rest encryption;
- applies explicit retention/expiry and garbage collection;
- records provider storage/retention choices in endpoint profiles;
- provides content-free audit events for access and deletion.

Provider-hosted continuation is enabled only when the operator accepts that
provider's retention behavior. A local canonical transcript can be disabled for
data-minimization, at the cost of route portability.

## Schema and tool safety

The worker validates and transports tool definitions and calls; it never runs
tools. Tool names, descriptions, schemas, call IDs, JSON depth, byte size, and
argument shape are bounded. JSON parsers reject duplicate keys and excessive
nesting.

Structured model output is untrusted even after provider strict-schema claims.
It is parsed with limits and validated locally against the canonical schema.
Callers remain responsible for authorization and side effects when executing a
tool.

## Provider-state safety

Opaque state is tagged with provenance, media type, digest, and size. It is
never interpreted by a different adapter and never copied to another provider.
Logs show only digest prefix and safe provenance. Unknown blocks fail strict
conversion.

Thinking/reasoning content may have provider-specific integrity signatures.
Adapters preserve exact bytes and ordering, and tests prove round-trip behavior.

## Supply chain

- Go modules and Actions are version-pinned and updated through reviewed PRs.
- Official provider SDKs are preferred; raw HTTP requires an ADR, scoped package,
  wire fixtures, and a migration/removal condition.
- CI runs vulnerability scanning, static analysis, race tests, license checks,
  SBOM generation, and container scanning as implementation phases land.
- Release artifacts are reproducible where practical, signed, and accompanied
  by provenance.
- The runtime image is non-root, read-only, capability-dropped, and contains no
  compiler or package manager.

## Abuse and resource limits

Configuration bounds:

- request, item, part, schema, and inline byte counts;
- context and maximum output tokens;
- tools, parallel calls, extension size, and JSON depth;
- provider/Activity deadlines and route attempts;
- concurrent Activities per task queue and per endpoint;
- Redis/client pool sizes;
- continuation depth and retained bytes.

Admission runs after structural validation but before expensive provider work.
Limits fail closed with safe errors and do not echo attacker-controlled content.

## Security verification

Required tests include cross-tenant handle access, MAC/key rotation, key-name
privacy, malicious Redis/blob data, SSRF-shaped URLs, redirect behavior,
duplicate/deep JSON, oversized streams, log/trace/heartbeat leak assertions,
secret rotation, corrupt provider state, and untrusted provider error bodies.

Live credentials are never required for the core security suite.
