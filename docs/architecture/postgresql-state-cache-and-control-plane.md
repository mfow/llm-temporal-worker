# PostgreSQL State, Cache, Accounting, and Control Plane

## Status and database boundary

This document specifies the post-v1 worker schema and transactional behavior.
It is design-only until the execution plan is implemented.

The worker receives its own PostgreSQL database, credentials, migration history,
backup policy, and connection pool. It must not create tables in, query, or
modify Temporal's PostgreSQL database. Temporal owns its schema and migrations.
Local Compose may run both databases in one PostgreSQL server for development,
but they use different databases and roles.

At the accepted cutover, PostgreSQL replaces Redis as authority for all existing
worker persistence as well as the new features:

| Existing/new responsibility | PostgreSQL representation |
| --- | --- |
| Operation idempotency and ambiguous-dispatch ledger | operations and attempts |
| Sliding-window budget reservations | policies, windows, buckets, reservations |
| Immutable continuation state | conversation checkpoints and blob references |
| Completed-result replay | operation result blob |
| Exact-response cache | entries, uses, and fill leases |
| Provider continuation/poll IDs | encrypted operation/attempt fields |
| Provider health and credit observations | event log and current projection |
| Provider model listing | inventory snapshots and normalized model rows |
| Price catalogs and future FX conversion | USD price catalog and FX snapshots |
| Budget/spend/status queries | indexed reads over the same authoritative rows |

There is no Redis/PostgreSQL dual-write mode. Because no production deployment
exists, implementation performs a clean breaking cutover. A short development
read validator may compare imported Redis fixtures with PostgreSQL, but it may
not serve requests from both stores.

## Production schema rules

- Use PostgreSQL 17 or a later explicitly qualified version.
- Use a dedicated **llm_worker** schema and least-privileged runtime/migration
  roles.
- All timestamps are **timestamptz** and use database time. Sessions set
  **TIME ZONE 'UTC'**; UTC is an interpretation guarantee, not a different
  PostgreSQL storage type.
- Application IDs are UUIDv7 generated in Go from an injected source. Public
  handles remain random, MAC-authenticated opaque strings.
- Sensitive lookup values use keyed HMAC columns. Reversible provider IDs and
  object locators use envelope-encrypted bytea plus a key ID. Secrets and raw
  prompt text do not enter indexed columns.
- Each operation stores its bounded canonical v2 Activity request as JSONB.
  Cache entries store a canonical request manifest JSONB. Neither column has a
  GIN index or serves hot lookup; access is restricted because the operation
  request can contain model content.
- SHA-256 and HMAC columns are exactly 32 bytes. No raw tenant, prompt, API key,
  provider poll token, or provider error body is stored.
- Every monetary column is **NUMERIC(38,18)**, non-negative, and denominated in
  USD. JSON encodes it as a decimal string. No **real**, **double precision**,
  PostgreSQL **money**, or Go floating point may enter accounting arithmetic.
- Foreign-key columns used for joins or deletion are explicitly indexed;
  PostgreSQL does not create those indexes automatically.
- Low-cardinality flags are handled with partial indexes, not stand-alone
  Boolean indexes. JSONB is not given a broad GIN index until an observed query
  requires a specific expression index.
- External blobs are immutable and content-addressed. Database rows store
  bounded metadata and encrypted locators.
- Migrations use a transaction except for **CREATE INDEX CONCURRENTLY** during
  later online upgrades. The initial no-data migration creates indexes normally.

PostgreSQL supports substantially more decimal precision than this design uses.
**NUMERIC(38,18)** gives 20 whole-dollar digits and 18 fractional digits while
keeping one uniform type across price, FX, budget, reservation, and actual-cost
arithmetic.

This deliberately replaces the current integer micro-USD authority. It can
represent costs below one millionth of a dollar while also representing whole
dollars, ten-dollar charges, and aggregate budgets up to 20 whole-dollar
digits. An unknown price or actual cost is **NULL**, never numeric zero. Zero
means the worker knows the charge is exactly zero. Every nullable money value is
paired with a closed status and, when unknown, a safe reason code. Estimates,
reservations, and conservative budget charges stay in separate non-null fields;
none is copied into **actual_cost_usd** merely to avoid a null.

## Final schema

The DDL below defines the intended final shape. Migration tooling may split it
into ordered files, but must not weaken a constraint or silently add a different
index.

### Scope, configuration, and blobs

~~~sql
CREATE SCHEMA llm_worker;
REVOKE ALL ON SCHEMA llm_worker FROM PUBLIC;

CREATE TABLE llm_worker.scopes (
    scope_id uuid PRIMARY KEY,
    tenant_hmac bytea NOT NULL CHECK (octet_length(tenant_hmac) = 32),
    project_hmac bytea NOT NULL CHECK (octet_length(project_hmac) = 32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    UNIQUE (tenant_hmac, project_hmac)
);

CREATE TABLE llm_worker.configuration_snapshots (
    config_digest bytea PRIMARY KEY CHECK (octet_length(config_digest) = 32),
    config_version text NOT NULL,
    source_digest bytea NOT NULL CHECK (octet_length(source_digest) = 32),
    sanitized_config jsonb NOT NULL,
    loaded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,
    UNIQUE (config_version, source_digest)
);

CREATE TABLE llm_worker.blobs (
    blob_id uuid PRIMARY KEY,
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    store_id text NOT NULL,
    locator_ciphertext bytea NOT NULL,
    locator_key_id text NOT NULL,
    sha256 bytea NOT NULL CHECK (octet_length(sha256) = 32),
    byte_length bigint NOT NULL CHECK (byte_length >= 0),
    media_type text NOT NULL,
    encryption_context_digest bytea NOT NULL
        CHECK (octet_length(encryption_context_digest) = 32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    deletion_state text NOT NULL DEFAULT 'retained'
        CHECK (deletion_state IN ('retained', 'eligible', 'deleting', 'deleted')),
    UNIQUE (scope_id, store_id, sha256, byte_length, media_type)
);

CREATE INDEX blobs_scope_created_idx
    ON llm_worker.blobs (scope_id, created_at DESC, blob_id);

CREATE INDEX blobs_expiry_idx
    ON llm_worker.blobs (expires_at, blob_id)
    WHERE expires_at IS NOT NULL AND deletion_state = 'retained';
~~~

**sanitized_config** is bounded, secret-free evidence of the compiled routing
snapshot. The source file remains operator configuration; the database copy
makes an operation auditable after reload. A schema validator, byte cap, and
key deny-list run before insertion.

### Operations and attempts

~~~sql
CREATE TABLE llm_worker.operations (
    operation_id uuid PRIMARY KEY,
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    operation_kind text NOT NULL
        CHECK (operation_kind IN ('generate', 'compact', 'query')),
    api_version text NOT NULL,
    operation_key_hmac bytea NOT NULL
        CHECK (octet_length(operation_key_hmac) = 32),
    request_fingerprint_hmac bytea NOT NULL
        CHECK (octet_length(request_fingerprint_hmac) = 32),
    request_schema_version integer NOT NULL
        CHECK (request_schema_version > 0),
    request_jsonb jsonb NOT NULL
        CHECK (jsonb_typeof(request_jsonb) = 'object'),
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    state text NOT NULL CHECK (
        state IN (
            'reserved',
            'dispatching',
            'provider_pending',
            'completed',
            'definite_failed',
            'ambiguous',
            'canceled'
        )
    ),
    parent_operation_id uuid
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    parent_checkpoint_id uuid,
    result_blob_id uuid
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    result_digest bytea CHECK (
        result_digest IS NULL OR octet_length(result_digest) = 32
    ),
    cache_entry_id uuid,
    route_id text,
    endpoint_id text,
    provider text,
    endpoint_family text,
    resolved_model text,
    model_equivalence_id text,
    requested_service_class text CHECK (
        requested_service_class IS NULL OR
        requested_service_class IN ('economy', 'standard', 'priority')
    ),
    attempted_service_class text CHECK (
        attempted_service_class IS NULL OR
        attempted_service_class IN ('economy', 'standard', 'priority')
    ),
    actual_service_class text CHECK (
        actual_service_class IS NULL OR
        actual_service_class IN ('economy', 'standard', 'priority')
    ),
    provider_idempotency_key_hmac bytea CHECK (
        provider_idempotency_key_hmac IS NULL OR
        octet_length(provider_idempotency_key_hmac) = 32
    ),
    provider_idempotency_key_ciphertext bytea,
    provider_operation_id_hmac bytea CHECK (
        provider_operation_id_hmac IS NULL OR
        octet_length(provider_operation_id_hmac) = 32
    ),
    provider_operation_id_ciphertext bytea,
    provider_reference_key_id text,
    provider_submit_started_at timestamptz,
    provider_accepted_at timestamptz,
    provider_pending_at timestamptz,
    poll_after timestamptz,
    last_polled_at timestamptz,
    poll_count integer NOT NULL DEFAULT 0 CHECK (poll_count >= 0),
    lease_owner uuid,
    lease_expires_at timestamptz,
    reserved_cost_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (reserved_cost_usd >= 0),
    incurred_cost_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (incurred_cost_usd >= 0),
    actual_cost_usd numeric(38,18)
        CHECK (actual_cost_usd IS NULL OR actual_cost_usd >= 0),
    cost_status text NOT NULL DEFAULT 'pending' CHECK (
        cost_status IN ('pending', 'exact', 'unknown')
    ),
    cost_method text CHECK (
        cost_method IS NULL OR
        cost_method IN (
            'provider_reported',
            'catalog_usage',
            'worker_cache_zero',
            'control_query_zero'
        )
    ),
    cost_unknown_reason_code text CHECK (
        cost_unknown_reason_code IS NULL OR
        cost_unknown_reason_code ~ '^[a-z0-9_]{1,64}$'
    ),
    cost_catalog_version text,
    fx_rate_snapshot_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    expires_at timestamptz NOT NULL,
    UNIQUE (scope_id, operation_kind, operation_key_hmac),
    CHECK (
        state <> 'provider_pending' OR (
            provider_operation_id_ciphertext IS NOT NULL AND
            provider_operation_id_hmac IS NOT NULL AND
            provider_reference_key_id IS NOT NULL AND
            provider_pending_at IS NOT NULL
        )
    ),
    CHECK (
        state <> 'completed' OR (
            completed_at IS NOT NULL AND
            result_blob_id IS NOT NULL AND
            result_digest IS NOT NULL AND
            cost_status IN ('exact', 'unknown')
        )
    ),
    CHECK (
        (cost_status = 'pending' AND
            actual_cost_usd IS NULL AND
            cost_method IS NULL AND
            cost_unknown_reason_code IS NULL) OR
        (cost_status = 'exact' AND
            actual_cost_usd IS NOT NULL AND
            cost_method IS NOT NULL AND
            cost_unknown_reason_code IS NULL) OR
        (cost_status = 'unknown' AND
            actual_cost_usd IS NULL AND
            cost_method IS NULL AND
            cost_unknown_reason_code IS NOT NULL)
    ),
    CHECK (
        provider_operation_id_ciphertext IS NULL =
        (provider_operation_id_hmac IS NULL)
    ),
    CHECK (
        provider_idempotency_key_ciphertext IS NULL =
        (provider_idempotency_key_hmac IS NULL)
    ),
    CHECK (
        provider_idempotency_key_ciphertext IS NULL OR
        provider_reference_key_id IS NOT NULL
    ),
    CHECK (
        provider_operation_id_ciphertext IS NULL OR
        endpoint_id IS NOT NULL
    ),
    CHECK (
        cost_method NOT IN ('worker_cache_zero', 'control_query_zero') OR
        actual_cost_usd = 0
    ),
    CHECK (completed_at IS NULL OR completed_at >= created_at)
);

CREATE TABLE llm_worker.operation_attempts (
    attempt_id uuid PRIMARY KEY,
    operation_id uuid NOT NULL
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    attempt_number integer NOT NULL CHECK (attempt_number > 0),
    route_index integer NOT NULL CHECK (route_index >= 0),
    fallback_index integer NOT NULL CHECK (fallback_index >= 0),
    route_id text NOT NULL,
    endpoint_id text NOT NULL,
    provider text NOT NULL,
    endpoint_family text NOT NULL,
    resolved_model text NOT NULL,
    model_equivalence_id text NOT NULL,
    state text NOT NULL CHECK (
        state IN (
            'planned',
            'pre_write_failed',
            'submitted',
            'provider_pending',
            'completed',
            'definite_failed',
            'ambiguous',
            'canceled'
        )
    ),
    provider_request_id_hmac bytea CHECK (
        provider_request_id_hmac IS NULL OR
        octet_length(provider_request_id_hmac) = 32
    ),
    provider_request_id_ciphertext bytea,
    provider_reference_key_id text,
    safe_error_code text,
    dispatch_disposition text CHECK (
        dispatch_disposition IS NULL OR
        dispatch_disposition IN (
            'not_dispatched',
            'rejected',
            'accepted',
            'ambiguous'
        )
    ),
    reserved_cost_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (reserved_cost_usd >= 0),
    actual_cost_usd numeric(38,18)
        CHECK (actual_cost_usd IS NULL OR actual_cost_usd >= 0),
    cost_status text NOT NULL DEFAULT 'pending' CHECK (
        cost_status IN ('pending', 'exact', 'unknown')
    ),
    cost_method text CHECK (
        cost_method IS NULL OR
        cost_method IN (
            'provider_reported',
            'catalog_usage',
            'definite_uncharged_zero'
        )
    ),
    cost_unknown_reason_code text CHECK (
        cost_unknown_reason_code IS NULL OR
        cost_unknown_reason_code ~ '^[a-z0-9_]{1,64}$'
    ),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    possible_write_at timestamptz,
    finished_at timestamptz,
    safe_diagnostics jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (operation_id, attempt_number),
    CHECK (
        provider_request_id_ciphertext IS NULL =
        (provider_request_id_hmac IS NULL)
    ),
    CHECK (
        provider_request_id_ciphertext IS NULL OR
        provider_reference_key_id IS NOT NULL
    ),
    CHECK (
        (cost_status = 'pending' AND
            actual_cost_usd IS NULL AND
            cost_method IS NULL AND
            cost_unknown_reason_code IS NULL) OR
        (cost_status = 'exact' AND
            actual_cost_usd IS NOT NULL AND
            cost_method IS NOT NULL AND
            cost_unknown_reason_code IS NULL) OR
        (cost_status = 'unknown' AND
            actual_cost_usd IS NULL AND
            cost_method IS NULL AND
            cost_unknown_reason_code IS NOT NULL)
    ),
    CHECK (finished_at IS NULL OR cost_status IN ('exact', 'unknown')),
    CHECK (
        cost_method <> 'definite_uncharged_zero' OR actual_cost_usd = 0
    ),
    CHECK (finished_at IS NULL OR finished_at >= started_at)
);

CREATE UNIQUE INDEX operations_provider_operation_uidx
    ON llm_worker.operations (endpoint_id, provider_operation_id_hmac)
    WHERE provider_operation_id_hmac IS NOT NULL;

CREATE INDEX operations_parent_operation_idx
    ON llm_worker.operations (parent_operation_id)
    WHERE parent_operation_id IS NOT NULL;

CREATE INDEX operations_parent_checkpoint_idx
    ON llm_worker.operations (parent_checkpoint_id)
    WHERE parent_checkpoint_id IS NOT NULL;

CREATE INDEX operations_cache_entry_idx
    ON llm_worker.operations (cache_entry_id)
    WHERE cache_entry_id IS NOT NULL;

CREATE INDEX operations_config_idx
    ON llm_worker.operations (config_digest, operation_id);

CREATE INDEX operations_result_blob_idx
    ON llm_worker.operations (result_blob_id)
    WHERE result_blob_id IS NOT NULL;

CREATE INDEX operations_lease_recovery_idx
    ON llm_worker.operations (lease_expires_at, operation_id)
    WHERE state IN ('reserved', 'dispatching');

CREATE INDEX operations_poll_due_idx
    ON llm_worker.operations (poll_after, operation_id)
    INCLUDE (endpoint_id, provider)
    WHERE state = 'provider_pending';

CREATE INDEX operations_scope_spend_idx
    ON llm_worker.operations (scope_id, completed_at DESC, operation_id)
    INCLUDE (
        operation_kind,
        actual_cost_usd,
        cost_status,
        cost_method,
        endpoint_id,
        resolved_model,
        model_equivalence_id
    )
    WHERE state = 'completed';

CREATE INDEX operations_unknown_cost_idx
    ON llm_worker.operations (scope_id, completed_at DESC, operation_id)
    INCLUDE (operation_kind, endpoint_id, resolved_model, cost_unknown_reason_code)
    WHERE state = 'completed' AND cost_status = 'unknown';

CREATE INDEX operations_terminal_expiry_idx
    ON llm_worker.operations (expires_at, operation_id)
    WHERE state IN (
        'completed', 'definite_failed', 'ambiguous', 'canceled'
    );

CREATE INDEX operations_completed_brin_idx
    ON llm_worker.operations USING brin (completed_at)
    WITH (pages_per_range = 64)
    WHERE completed_at IS NOT NULL;

CREATE INDEX operation_attempts_provider_request_idx
    ON llm_worker.operation_attempts (endpoint_id, provider_request_id_hmac)
    WHERE provider_request_id_hmac IS NOT NULL;

CREATE INDEX operation_attempts_route_time_idx
    ON llm_worker.operation_attempts
        (endpoint_id, resolved_model, started_at DESC, attempt_id)
    INCLUDE (state, safe_error_code, actual_cost_usd, cost_status);
~~~

The completed-operation check is intentionally explicit: a successful Generate,
Compact, or Query settles to either **exact** or **unknown** cost. Exact requires
a decimal USD amount and method. Unknown requires a safe reason and a NULL
amount/method; it is never rewritten to zero or to the reservation. A
provider-reported price is used when trustworthy; otherwise exact catalog
arithmetic over final usage is used. An ambiguous external dispatch outcome
remains operation state **ambiguous**, rather than masquerading as a completed
operation with a zero cost.

**updated_at** changes only in the same transaction as a state transition; no
generic trigger creates extra writes. State transitions use a compare-and-set
predicate on current state and lease owner. **operation_attempts** preserves
each route/fallback cost and classified outcome instead of overwriting the
operation with only its last attempt.

### Immutable checkpoints and provider state

~~~sql
CREATE TABLE llm_worker.conversation_checkpoints (
    checkpoint_id uuid PRIMARY KEY,
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    public_id_hmac bytea NOT NULL UNIQUE
        CHECK (octet_length(public_id_hmac) = 32),
    handle_key_id text NOT NULL,
    parent_checkpoint_id uuid
        REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
        ON DELETE RESTRICT,
    checkpoint_kind text NOT NULL CHECK (
        checkpoint_kind IN ('generation', 'compaction', 'cache_replay')
    ),
    depth integer NOT NULL CHECK (depth >= 0),
    origin_operation_id uuid NOT NULL UNIQUE
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    origin_cache_entry_id uuid,
    delta_blob_id uuid NOT NULL
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    response_blob_id uuid NOT NULL
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    settings_patch_blob_id uuid NOT NULL
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    materialized_snapshot_blob_id uuid
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    canonical_lineage_digest bytea NOT NULL
        CHECK (octet_length(canonical_lineage_digest) = 32),
    materialized_settings_digest bytea NOT NULL
        CHECK (octet_length(materialized_settings_digest) = 32),
    tool_frontier_digest bytea NOT NULL
        CHECK (octet_length(tool_frontier_digest) = 32),
    schema_version integer NOT NULL CHECK (schema_version > 0),
    compiler_epoch text NOT NULL,
    compaction_policy_version text,
    compaction_prompt_version text,
    compacted_through_checkpoint_id uuid
        REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
        ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    CHECK (
        (parent_checkpoint_id IS NULL AND depth = 0) OR
        (parent_checkpoint_id IS NOT NULL AND depth > 0)
    ),
    CHECK (parent_checkpoint_id IS DISTINCT FROM checkpoint_id)
);

CREATE TABLE llm_worker.checkpoint_provider_state (
    checkpoint_id uuid NOT NULL
        REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
        ON DELETE RESTRICT,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    provider text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_account_hmac bytea NOT NULL
        CHECK (octet_length(endpoint_account_hmac) = 32),
    region text NOT NULL,
    endpoint_family text NOT NULL,
    model_lineage text NOT NULL,
    state_kind text NOT NULL,
    state_blob_id uuid NOT NULL
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    state_digest bytea NOT NULL CHECK (octet_length(state_digest) = 32),
    required boolean NOT NULL,
    immutable_fork_safe boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    PRIMARY KEY (checkpoint_id, ordinal)
);

CREATE TABLE llm_worker.checkpoint_provider_affinities (
    checkpoint_id uuid NOT NULL
        REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
        ON DELETE RESTRICT,
    affinity_rank smallint NOT NULL CHECK (affinity_rank >= 0),
    provider text NOT NULL,
    route_id text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_account_hmac bytea NOT NULL
        CHECK (octet_length(endpoint_account_hmac) = 32),
    region text NOT NULL,
    endpoint_family text NOT NULL,
    model_lineage text NOT NULL,
    model_equivalence_id text NOT NULL,
    provider_cache_key_hmac bytea CHECK (
        provider_cache_key_hmac IS NULL OR
        octet_length(provider_cache_key_hmac) = 32
    ),
    cache_epoch text NOT NULL,
    hard_pinned boolean NOT NULL,
    observed_cache_read_tokens bigint NOT NULL DEFAULT 0
        CHECK (observed_cache_read_tokens >= 0),
    observed_cache_write_tokens bigint NOT NULL DEFAULT 0
        CHECK (observed_cache_write_tokens >= 0),
    last_success_at timestamptz NOT NULL,
    expires_at timestamptz,
    PRIMARY KEY (checkpoint_id, affinity_rank)
);

CREATE INDEX checkpoints_parent_idx
    ON llm_worker.conversation_checkpoints
        (parent_checkpoint_id, created_at, checkpoint_id)
    WHERE parent_checkpoint_id IS NOT NULL;

CREATE INDEX checkpoints_scope_created_idx
    ON llm_worker.conversation_checkpoints
        (scope_id, created_at DESC, checkpoint_id);

CREATE INDEX checkpoints_retention_idx
    ON llm_worker.conversation_checkpoints (expires_at, checkpoint_id);

CREATE INDEX checkpoints_compacted_through_idx
    ON llm_worker.conversation_checkpoints (compacted_through_checkpoint_id)
    WHERE compacted_through_checkpoint_id IS NOT NULL;

CREATE INDEX checkpoints_delta_blob_idx
    ON llm_worker.conversation_checkpoints (delta_blob_id);

CREATE INDEX checkpoints_response_blob_idx
    ON llm_worker.conversation_checkpoints (response_blob_id);

CREATE INDEX checkpoints_settings_blob_idx
    ON llm_worker.conversation_checkpoints (settings_patch_blob_id);

CREATE INDEX checkpoints_snapshot_blob_idx
    ON llm_worker.conversation_checkpoints (materialized_snapshot_blob_id)
    WHERE materialized_snapshot_blob_id IS NOT NULL;

CREATE INDEX checkpoint_provider_state_blob_idx
    ON llm_worker.checkpoint_provider_state (state_blob_id);

CREATE INDEX checkpoint_provider_state_expiry_idx
    ON llm_worker.checkpoint_provider_state (expires_at, checkpoint_id, ordinal)
    WHERE expires_at IS NOT NULL;

CREATE INDEX checkpoint_provider_state_route_idx
    ON llm_worker.checkpoint_provider_state
        (endpoint_id, endpoint_family, model_lineage, checkpoint_id);

CREATE INDEX checkpoint_affinity_route_idx
    ON llm_worker.checkpoint_provider_affinities
        (checkpoint_id, hard_pinned DESC, affinity_rank)
    INCLUDE (route_id, endpoint_id, model_equivalence_id, expires_at);

ALTER TABLE llm_worker.operations
    ADD CONSTRAINT operations_parent_checkpoint_fk
    FOREIGN KEY (parent_checkpoint_id)
    REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;
~~~

Checkpoint INSERT runs after the operation and all referenced blobs exist, then
operation completion and result publication occur in the same transaction.
The deferred parent foreign key permits that ordered finalization without
disabling integrity.

The runtime role receives SELECT and INSERT on checkpoint tables but no UPDATE
or DELETE. Retention uses a separate maintenance role and procedures. This
database permission makes immutability stronger than a repository convention.

### Certified model equivalence

Cross-provider exact-cache reuse depends on an explicit model artifact and
semantic equivalence catalog:

~~~sql
CREATE TABLE llm_worker.model_equivalence_classes (
    model_equivalence_id text PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('certified', 'isolated', 'retired')),
    model_artifact_revision text NOT NULL,
    weights_revision text NOT NULL,
    quantization_profile text NOT NULL,
    tokenizer_revision text NOT NULL,
    prompt_template_revision text NOT NULL,
    safety_profile_revision text NOT NULL,
    semantic_profile_version text NOT NULL,
    compiler_epoch text NOT NULL,
    evidence_digest bytea NOT NULL CHECK (octet_length(evidence_digest) = 32),
    certified_at timestamptz NOT NULL,
    retired_at timestamptz
);

CREATE TABLE llm_worker.model_equivalence_members (
    model_equivalence_id text NOT NULL
        REFERENCES llm_worker.model_equivalence_classes(model_equivalence_id)
        ON DELETE RESTRICT,
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    route_id text NOT NULL,
    provider text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_family text NOT NULL,
    resolved_model text NOT NULL,
    provider_revision text NOT NULL,
    verified_semantic_lowering boolean NOT NULL,
    enabled boolean NOT NULL,
    PRIMARY KEY (model_equivalence_id, config_digest, route_id),
    UNIQUE (config_digest, route_id)
);

CREATE INDEX model_equivalence_member_lookup_idx
    ON llm_worker.model_equivalence_members
        (config_digest, resolved_model, enabled, route_id)
    INCLUDE (
        model_equivalence_id,
        endpoint_id,
        endpoint_family,
        verified_semantic_lowering
    );
~~~

Provider, route, account, and region are provenance, not cache-key inputs, when
members share a **certified** model-equivalence class and the semantic compiler
profile is equal. This permits one GPT model artifact to share exact responses
across OpenAI, Azure OpenAI, and an OpenRouter pass-through route.

Never infer equivalence from a model display name. A third-party open-source
route joins a shared class only when weights revision, quantization, tokenizer,
chat template, hidden prompt/safety transforms, and compiler semantics are
known equal. Unknown quantization or undisclosed transformations get an
**isolated** class unique to that route. Operators can later certify and merge
classes through a new catalog version; existing entries are not silently
re-keyed.

### Exact-response cache

~~~sql
CREATE TABLE llm_worker.response_cache_entries (
    cache_entry_id uuid PRIMARY KEY,
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    fingerprint_version integer NOT NULL CHECK (fingerprint_version > 0),
    semantic_fingerprint_hmac bytea NOT NULL
        CHECK (octet_length(semantic_fingerprint_hmac) = 32),
    canonical_request_digest bytea NOT NULL
        CHECK (octet_length(canonical_request_digest) = 32),
    canonical_request_jsonb jsonb NOT NULL
        CHECK (jsonb_typeof(canonical_request_jsonb) = 'object'),
    variant integer NOT NULL CHECK (variant >= 0),
    model_equivalence_id text NOT NULL
        REFERENCES llm_worker.model_equivalence_classes(model_equivalence_id)
        ON DELETE RESTRICT,
    semantic_profile_version text NOT NULL,
    cache_epoch text NOT NULL,
    response_inline bytea,
    response_blob_id uuid
        REFERENCES llm_worker.blobs(blob_id) ON DELETE RESTRICT,
    response_digest bytea NOT NULL CHECK (octet_length(response_digest) = 32),
    origin_operation_id uuid NOT NULL UNIQUE
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    origin_checkpoint_id uuid NOT NULL
        REFERENCES llm_worker.conversation_checkpoints(checkpoint_id)
        ON DELETE RESTRICT,
    origin_provider text NOT NULL,
    origin_endpoint_id text NOT NULL,
    origin_resolved_model text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    use_count integer NOT NULL DEFAULT 1
        CHECK (use_count BETWEEN 1 AND 2147483647),
    state text NOT NULL DEFAULT 'ready'
        CHECK (state IN ('ready', 'tombstoned')),
    UNIQUE (
        scope_id,
        fingerprint_version,
        semantic_fingerprint_hmac,
        variant
    ),
    CHECK (
        (response_inline IS NOT NULL)::integer +
        (response_blob_id IS NOT NULL)::integer = 1
    ),
    CHECK (completed_at >= created_at),
    CHECK (last_used_at >= completed_at)
);

CREATE TABLE llm_worker.response_cache_uses (
    cache_entry_id uuid NOT NULL
        REFERENCES llm_worker.response_cache_entries(cache_entry_id)
        ON DELETE RESTRICT,
    operation_id uuid NOT NULL UNIQUE
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    first_used_at timestamptz NOT NULL,
    PRIMARY KEY (cache_entry_id, operation_id)
);

CREATE TABLE llm_worker.response_cache_fills (
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    fingerprint_version integer NOT NULL CHECK (fingerprint_version > 0),
    semantic_fingerprint_hmac bytea NOT NULL
        CHECK (octet_length(semantic_fingerprint_hmac) = 32),
    variant integer NOT NULL CHECK (variant >= 0),
    owner_operation_id uuid NOT NULL
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    state text NOT NULL CHECK (state IN ('filling', 'completed', 'failed')),
    lease_expires_at timestamptz NOT NULL,
    cache_entry_id uuid
        REFERENCES llm_worker.response_cache_entries(cache_entry_id)
        ON DELETE RESTRICT,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (
        scope_id,
        fingerprint_version,
        semantic_fingerprint_hmac,
        variant
    ),
    CHECK (state <> 'completed' OR cache_entry_id IS NOT NULL)
);

CREATE INDEX response_cache_gc_idx
    ON llm_worker.response_cache_entries (last_used_at, cache_entry_id)
    WHERE state = 'ready';

CREATE INDEX response_cache_model_invalidation_idx
    ON llm_worker.response_cache_entries
        (model_equivalence_id, completed_at DESC, cache_entry_id)
    WHERE state = 'ready';

CREATE INDEX response_cache_origin_checkpoint_idx
    ON llm_worker.response_cache_entries (origin_checkpoint_id);

CREATE INDEX response_cache_response_blob_idx
    ON llm_worker.response_cache_entries (response_blob_id)
    WHERE response_blob_id IS NOT NULL;

CREATE INDEX response_cache_fill_owner_idx
    ON llm_worker.response_cache_fills (owner_operation_id);

CREATE INDEX response_cache_fill_entry_idx
    ON llm_worker.response_cache_fills (cache_entry_id)
    WHERE cache_entry_id IS NOT NULL;

CREATE INDEX response_cache_fill_lease_idx
    ON llm_worker.response_cache_fills (lease_expires_at, owner_operation_id)
    WHERE state = 'filling';

ALTER TABLE llm_worker.operations
    ADD CONSTRAINT operations_cache_entry_fk
    FOREIGN KEY (cache_entry_id)
    REFERENCES llm_worker.response_cache_entries(cache_entry_id)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE llm_worker.conversation_checkpoints
    ADD CONSTRAINT checkpoints_origin_cache_entry_fk
    FOREIGN KEY (origin_cache_entry_id)
    REFERENCES llm_worker.response_cache_entries(cache_entry_id)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX checkpoints_origin_cache_entry_idx
    ON llm_worker.conversation_checkpoints (origin_cache_entry_id)
    WHERE origin_cache_entry_id IS NOT NULL;
~~~

PostgreSQL's integer type is signed int32, matching the wire variant and
**use_count** requirement. The application validates temperature/variant
semantics because temperature is part of materialized encrypted content, not a
cache table column.

The semantic fingerprint excludes provider/endpoint/account/region, requested
service class, operation identity, maximum cache age, deadlines, budgets,
tracing, timestamps, and actor-only tags. It includes conversation content,
tools/settings, all output-affecting extensions, model-equivalence identity,
semantic/compiler/cache epochs, and compaction versions. The database key adds
variant as its own int32 column. A supposedly
neutral provider extension may be excluded only when its catalog schema marks
it non-semantic and a fixture proves identical lowering.

Cache lookup uses the B-tree unique key containing
**semantic_fingerprint_hmac**, which is HMAC-SHA-256 over a versioned canonical
encoding. It never searches **canonical_request_jsonb**. After a key match, the
worker verifies **canonical_request_digest** and the canonical manifest before
reuse so a canonicalizer/version defect fails closed rather than serving the
wrong response.

The operation **request_jsonb** is the complete normalized v2 Activity request:
scope, parent handle, delta, sparse patch, and cache policy. Its size is already
bounded by the Activity contract and large parts are BlobRefs. The cache
**canonical_request_jsonb** is a materialized cache-key manifest: semantic
settings and ordered content references/digests, model-equivalence identity,
compiler/compaction epochs, and variant. It does not duplicate ancestor blob
bytes or the full transcript. The JSONB rows support audit, safe debugging, and
hash verification; all equality and freshness lookups use fixed-size columns.
Application canonical bytes, not PostgreSQL's JSONB text serialization, are
hashed.

On a hit, this statement pattern increments usage exactly once per logical
operation and saturates instead of overflowing:

~~~sql
WITH inserted AS (
    INSERT INTO llm_worker.response_cache_uses (
        cache_entry_id, operation_id, first_used_at
    )
    VALUES ($1, $2, clock_timestamp())
    ON CONFLICT (operation_id) DO NOTHING
    RETURNING 1
)
UPDATE llm_worker.response_cache_entries
SET last_used_at = clock_timestamp(),
    use_count = LEAST(2147483647, use_count + 1)
WHERE cache_entry_id = $1
  AND EXISTS (SELECT 1 FROM inserted);
~~~

The origin operation inserts both the entry with **use_count = 1** and its use
row in the same transaction. A Temporal retry cannot increment again because
**operation_id** is unique in uses. **last_used_at** changes only on a newly
inserted logical use, not on polling or replay of the same operation.

### Budgets and reservations

Every compiled budget policy is scoped, versioned, and normalized into one or
more windows. A configuration that conceptually matches several scopes is
materialized to explicit policy rows; request-time admission does not execute
an unindexed JSON query over all policies.

~~~sql
CREATE TABLE llm_worker.budget_policies (
    policy_id uuid PRIMARY KEY,
    scope_id uuid NOT NULL
        REFERENCES llm_worker.scopes(scope_id) ON DELETE RESTRICT,
    policy_key text NOT NULL,
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    selector_digest bytea NOT NULL CHECK (octet_length(selector_digest) = 32),
    sanitized_selector jsonb NOT NULL
        CHECK (jsonb_typeof(sanitized_selector) = 'object'),
    priority integer NOT NULL,
    enabled boolean NOT NULL,
    effective_from timestamptz NOT NULL,
    effective_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (scope_id, policy_key, config_digest),
    CHECK (
        effective_until IS NULL OR effective_until > effective_from
    )
);

CREATE TABLE llm_worker.budget_windows (
    window_id uuid PRIMARY KEY,
    policy_id uuid NOT NULL
        REFERENCES llm_worker.budget_policies(policy_id) ON DELETE RESTRICT,
    window_key text NOT NULL,
    duration_seconds bigint NOT NULL CHECK (duration_seconds > 0),
    bucket_seconds bigint NOT NULL CHECK (bucket_seconds > 0),
    limit_usd numeric(38,18) NOT NULL CHECK (limit_usd > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (policy_id, window_key),
    CHECK (bucket_seconds <= duration_seconds),
    CHECK (duration_seconds % bucket_seconds = 0)
);

CREATE TABLE llm_worker.budget_buckets (
    window_id uuid NOT NULL
        REFERENCES llm_worker.budget_windows(window_id) ON DELETE RESTRICT,
    bucket_start timestamptz NOT NULL,
    reserved_cost_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (reserved_cost_usd >= 0),
    accounted_cost_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (accounted_cost_usd >= 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (window_id, bucket_start)
);

CREATE TABLE llm_worker.operation_budget_reservations (
    operation_id uuid NOT NULL
        REFERENCES llm_worker.operations(operation_id) ON DELETE RESTRICT,
    window_id uuid NOT NULL
        REFERENCES llm_worker.budget_windows(window_id) ON DELETE RESTRICT,
    bucket_start timestamptz NOT NULL,
    state text NOT NULL CHECK (
        state IN ('reserved', 'finalized', 'retained_ambiguous', 'released')
    ),
    reserved_cost_usd numeric(38,18) NOT NULL
        CHECK (reserved_cost_usd >= 0),
    actual_cost_usd numeric(38,18)
        CHECK (actual_cost_usd IS NULL OR actual_cost_usd >= 0),
    actual_cost_status text NOT NULL DEFAULT 'pending' CHECK (
        actual_cost_status IN ('pending', 'exact', 'unknown')
    ),
    actual_cost_unknown_reason_code text,
    budget_charge_usd numeric(38,18) NOT NULL DEFAULT 0
        CHECK (budget_charge_usd >= 0),
    budget_charge_basis text NOT NULL DEFAULT 'reserved' CHECK (
        budget_charge_basis IN (
            'reserved', 'exact_actual', 'retained_bound', 'released'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finalized_at timestamptz,
    PRIMARY KEY (operation_id, window_id),
    FOREIGN KEY (window_id, bucket_start)
        REFERENCES llm_worker.budget_buckets(window_id, bucket_start)
        ON DELETE RESTRICT,
    CHECK (
        state = 'reserved' OR finalized_at IS NOT NULL
    ),
    CHECK (
        (actual_cost_status = 'pending' AND
            actual_cost_usd IS NULL AND
            actual_cost_unknown_reason_code IS NULL) OR
        (actual_cost_status = 'exact' AND
            actual_cost_usd IS NOT NULL AND
            actual_cost_unknown_reason_code IS NULL) OR
        (actual_cost_status = 'unknown' AND
            actual_cost_usd IS NULL AND
            actual_cost_unknown_reason_code IS NOT NULL)
    ),
    CHECK (
        (budget_charge_basis = 'reserved' AND state = 'reserved' AND
            budget_charge_usd = 0 AND actual_cost_status = 'pending') OR
        (budget_charge_basis = 'exact_actual' AND state = 'finalized' AND
            actual_cost_status = 'exact' AND
            budget_charge_usd = actual_cost_usd) OR
        (budget_charge_basis = 'retained_bound' AND
            state IN ('finalized', 'retained_ambiguous') AND
            actual_cost_status = 'unknown' AND
            budget_charge_usd = reserved_cost_usd) OR
        (budget_charge_basis = 'released' AND state = 'released' AND
            budget_charge_usd = 0 AND
            actual_cost_status = 'exact' AND actual_cost_usd = 0)
    )
);

CREATE INDEX budget_policies_scope_active_idx
    ON llm_worker.budget_policies
        (scope_id, enabled, priority, effective_from, policy_id)
    INCLUDE (effective_until, selector_digest);

CREATE INDEX budget_policies_config_idx
    ON llm_worker.budget_policies (config_digest, policy_id);

CREATE INDEX budget_windows_policy_idx
    ON llm_worker.budget_windows (policy_id, window_id)
    INCLUDE (duration_seconds, bucket_seconds, limit_usd);

CREATE INDEX budget_buckets_retention_idx
    ON llm_worker.budget_buckets (bucket_start, window_id);

CREATE INDEX operation_budget_window_idx
    ON llm_worker.operation_budget_reservations
        (window_id, bucket_start, state, operation_id)
    INCLUDE (
        reserved_cost_usd,
        budget_charge_usd,
        budget_charge_basis,
        actual_cost_usd,
        actual_cost_status
    );
~~~

Admission resolves matching policies from the immutable configuration snapshot,
sorts window UUIDs, creates current buckets if absent, and locks every affected
bucket in that order. It computes each active window from the database
transaction timestamp, checks accounted charges plus active reserved amounts,
then writes all reservations atomically. No provider call occurs inside the
transaction.

Finalization locks the operation and the same window rows in the same order.
When actual cost is exact, it moves that amount into
**budget_charge_usd** with basis **exact_actual**. When a successful operation's
real price is unknown, it records NULL actual cost and retains the full
reservation as **budget_charge_usd** with basis **retained_bound**. That bound
protects the budget but is never reported as actual spend. Definite pre-write
failures release reservations. Ambiguous dispatch also retains the conservative
bound until an operator/provider reconciliation changes both operation and
reservation in one transaction.

### USD prices and worker-owned FX

The current generic catalog **currency** field and external runtime currency
input are removed. All price columns are USD-specific by name and type. The Go
worker owns price loading, any future FX retrieval, validation, conversion,
versioning, and refresh. Downstream Go/JSON/OCaml contracts expose USD decimal
properties and never report **currency = USD**.

Providers are currently priced in USD, so price rows normally have no FX
reference. If a provider later publishes a non-USD price, the Go worker:

1. obtains an allow-listed rate from its configured FX adapter;
2. validates source, observation time, target USD, age, and decimal bounds;
3. stores the internal rate snapshot;
4. converts with exact rational/decimal arithmetic and conservative budget
   rounding;
5. persists only normalized USD unit prices plus rate/source digests; and
6. fails closed when a current trustworthy rate is unavailable.

The source foreign price is not persisted as an operation price and is never
returned. An FX snapshot is internal audit evidence, not a downstream currency
choice.

Catalog component amounts are nullable because a provider's real price can be
unknown or only partly verified. **price_status=exact** means every normalized
component required by the pinned **usage_formula_version** is known; an exact
zero is stored as zero. **partial** preserves known components and leaves
unknown components NULL. **unknown** leaves all components NULL. The closed
component codes and safe reason explain which evidence is missing. A NULL is
never interpreted as free, as “not applicable,” or as inheriting another rate;
the usage-formula compiler must normalize included/non-applicable billing rules
before an entry can be exact.

Only an **exact** catalog entry can produce a catalog-derived actual cost or a
monetary-budget reservation. A partial/unknown entry may still support a route
allowed by the explicit unpriced policy. If that provider later returns a
trustworthy authoritative billed amount, the operation can record an exact
provider-reported cost; otherwise it completes with NULL actual cost and an
unknown reason while retaining its conservative budget bound where one exists.

~~~sql
CREATE TABLE llm_worker.fx_rate_snapshots (
    fx_rate_snapshot_id uuid PRIMARY KEY,
    source_currency_code character(3) NOT NULL,
    usd_per_source_unit numeric(38,18) NOT NULL
        CHECK (usd_per_source_unit > 0),
    source_id text NOT NULL,
    source_observation_id_hmac bytea NOT NULL
        CHECK (octet_length(source_observation_id_hmac) = 32),
    source_digest bytea NOT NULL CHECK (octet_length(source_digest) = 32),
    observed_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    status text NOT NULL CHECK (status IN ('active', 'superseded', 'rejected')),
    CHECK (source_currency_code ~ '^[A-Z]{3}$'),
    CHECK (valid_until > valid_from),
    UNIQUE (
        source_id,
        source_currency_code,
        source_observation_id_hmac
    )
);

CREATE TABLE llm_worker.price_catalogs (
    price_catalog_id uuid PRIMARY KEY,
    catalog_version text NOT NULL UNIQUE,
    source_digest bytea NOT NULL CHECK (octet_length(source_digest) = 32),
    compiled_digest bytea NOT NULL UNIQUE
        CHECK (octet_length(compiled_digest) = 32),
    loaded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    effective_from timestamptz NOT NULL,
    retired_at timestamptz,
    status text NOT NULL CHECK (status IN ('active', 'retired', 'rejected'))
);

CREATE TABLE llm_worker.price_entries (
    price_entry_id uuid PRIMARY KEY,
    price_catalog_id uuid NOT NULL
        REFERENCES llm_worker.price_catalogs(price_catalog_id)
        ON DELETE RESTRICT,
    provider text NOT NULL,
    endpoint_family text NOT NULL,
    endpoint_id text NOT NULL,
    region text NOT NULL,
    resolved_model text NOT NULL,
    provider_tier text NOT NULL,
    usage_formula_version text NOT NULL,
    price_status text NOT NULL CHECK (
        price_status IN ('exact', 'partial', 'unknown')
    ),
    input_per_million_usd numeric(38,18)
        CHECK (input_per_million_usd IS NULL OR input_per_million_usd >= 0),
    output_per_million_usd numeric(38,18)
        CHECK (output_per_million_usd IS NULL OR output_per_million_usd >= 0),
    cache_read_per_million_usd numeric(38,18)
        CHECK (
            cache_read_per_million_usd IS NULL OR
            cache_read_per_million_usd >= 0
        ),
    cache_write_per_million_usd numeric(38,18)
        CHECK (
            cache_write_per_million_usd IS NULL OR
            cache_write_per_million_usd >= 0
        ),
    reasoning_per_million_usd numeric(38,18)
        CHECK (
            reasoning_per_million_usd IS NULL OR
            reasoning_per_million_usd >= 0
        ),
    per_request_usd numeric(38,18)
        CHECK (per_request_usd IS NULL OR per_request_usd >= 0),
    unknown_component_codes text[] NOT NULL DEFAULT '{}'::text[] CHECK (
        array_position(unknown_component_codes, NULL) IS NULL AND
        unknown_component_codes <@ ARRAY[
            'input_tokens',
            'output_tokens',
            'cache_read_tokens',
            'cache_write_tokens',
            'reasoning_tokens',
            'request'
        ]::text[]
    ),
    price_unknown_reason_code text CHECK (
        price_unknown_reason_code IS NULL OR
        price_unknown_reason_code ~ '^[a-z0-9_]{1,64}$'
    ),
    fx_rate_snapshot_id uuid
        REFERENCES llm_worker.fx_rate_snapshots(fx_rate_snapshot_id)
        ON DELETE RESTRICT,
    source_price_digest bytea NOT NULL CHECK (octet_length(source_price_digest) = 32),
    effective_from timestamptz NOT NULL,
    effective_until timestamptz,
    UNIQUE (
        price_catalog_id,
        provider,
        endpoint_family,
        endpoint_id,
        region,
        resolved_model,
        provider_tier,
        effective_from
    ),
    CHECK (
        effective_until IS NULL OR effective_until > effective_from
    ),
    CHECK (
        (price_status = 'exact' AND
            input_per_million_usd IS NOT NULL AND
            output_per_million_usd IS NOT NULL AND
            cache_read_per_million_usd IS NOT NULL AND
            cache_write_per_million_usd IS NOT NULL AND
            reasoning_per_million_usd IS NOT NULL AND
            per_request_usd IS NOT NULL AND
            cardinality(unknown_component_codes) = 0 AND
            price_unknown_reason_code IS NULL) OR
        (price_status = 'partial' AND
            num_nonnulls(
                input_per_million_usd,
                output_per_million_usd,
                cache_read_per_million_usd,
                cache_write_per_million_usd,
                reasoning_per_million_usd,
                per_request_usd
            ) BETWEEN 1 AND 5 AND
            cardinality(unknown_component_codes) > 0 AND
            price_unknown_reason_code IS NOT NULL) OR
        (price_status = 'unknown' AND
            num_nonnulls(
                input_per_million_usd,
                output_per_million_usd,
                cache_read_per_million_usd,
                cache_write_per_million_usd,
                reasoning_per_million_usd,
                per_request_usd
            ) = 0 AND
            cardinality(unknown_component_codes) > 0 AND
            price_unknown_reason_code IS NOT NULL)
    )
);

CREATE INDEX fx_rates_active_lookup_idx
    ON llm_worker.fx_rate_snapshots
        (source_currency_code, valid_from DESC, fx_rate_snapshot_id)
    INCLUDE (usd_per_source_unit, valid_until, source_id)
    WHERE status = 'active';

CREATE INDEX price_entries_resolution_idx
    ON llm_worker.price_entries (
        endpoint_id,
        region,
        resolved_model,
        provider_tier,
        price_status,
        effective_from DESC,
        price_entry_id
    )
    INCLUDE (
        effective_until,
        usage_formula_version,
        input_per_million_usd,
        output_per_million_usd,
        cache_read_per_million_usd,
        cache_write_per_million_usd,
        reasoning_per_million_usd,
        per_request_usd,
        price_catalog_id,
        fx_rate_snapshot_id
    );

CREATE INDEX price_entries_fx_idx
    ON llm_worker.price_entries (fx_rate_snapshot_id)
    WHERE fx_rate_snapshot_id IS NOT NULL;

CREATE INDEX price_entries_unresolved_idx
    ON llm_worker.price_entries
        (price_status, effective_from DESC, price_entry_id)
    INCLUDE (
        endpoint_id,
        resolved_model,
        provider_tier,
        unknown_component_codes,
        price_unknown_reason_code
    )
    WHERE price_status IN ('partial', 'unknown');

ALTER TABLE llm_worker.operations
    ADD CONSTRAINT operations_fx_snapshot_fk
    FOREIGN KEY (fx_rate_snapshot_id)
    REFERENCES llm_worker.fx_rate_snapshots(fx_rate_snapshot_id)
    ON DELETE RESTRICT;

CREATE INDEX operations_fx_snapshot_idx
    ON llm_worker.operations (fx_rate_snapshot_id)
    WHERE fx_rate_snapshot_id IS NOT NULL;
~~~

The public v2 schema names amounts **reserved_cost_usd** and
**actual_cost_usd**. A known amount is a validated arbitrary-precision
**Usd_decimal.t** in OCaml. Nullable catalog/actual prices decode to a closed
unknown-cost constructor, never to zero and never to **float** or a
currency-tagged record. The current v1 **currency** response field and
configuration **pricing.currency** are removed during the breaking contract
cutover; one-shot OCaml helpers are rebuilt on the v2 root-request path.

### Provider status, credit state, and inventory

Events are append-only classified observations. The current table is a
transactionally maintained projection used by routing and cheap queries. Raw
provider error bodies and account secrets never enter either table.

~~~sql
CREATE TABLE llm_worker.provider_status_events (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    route_id text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_account_hmac bytea NOT NULL
        CHECK (octet_length(endpoint_account_hmac) = 32),
    provider text NOT NULL,
    endpoint_family text NOT NULL,
    observed_at timestamptz NOT NULL,
    source text NOT NULL CHECK (
        source IN (
            'inference',
            'management_api',
            'startup_probe',
            'operator'
        )
    ),
    availability text NOT NULL CHECK (
        availability IN ('available', 'degraded', 'unavailable', 'unknown')
    ),
    credit_state text NOT NULL CHECK (
        credit_state IN ('ok', 'low', 'exhausted', 'unknown')
    ),
    billing_state text NOT NULL CHECK (
        billing_state IN ('ok', 'issue', 'unknown')
    ),
    safe_error_code text,
    provider_code text,
    evidence_digest bytea NOT NULL CHECK (octet_length(evidence_digest) = 32),
    config_epoch text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE llm_worker.provider_route_status (
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    route_id text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_account_hmac bytea NOT NULL
        CHECK (octet_length(endpoint_account_hmac) = 32),
    provider text NOT NULL,
    endpoint_family text NOT NULL,
    availability text NOT NULL CHECK (
        availability IN ('available', 'degraded', 'unavailable', 'unknown')
    ),
    credit_state text NOT NULL CHECK (
        credit_state IN ('ok', 'low', 'exhausted', 'unknown')
    ),
    billing_state text NOT NULL CHECK (
        billing_state IN ('ok', 'issue', 'unknown')
    ),
    circuit_state text NOT NULL CHECK (
        circuit_state IN ('closed', 'open', 'half_open')
    ),
    consecutive_definite_failures integer NOT NULL DEFAULT 0
        CHECK (consecutive_definite_failures >= 0),
    last_event_id bigint NOT NULL
        REFERENCES llm_worker.provider_status_events(event_id)
        ON DELETE RESTRICT,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    credit_confirmed_at timestamptz,
    observed_at timestamptz NOT NULL,
    stale_after timestamptz NOT NULL,
    projection_version bigint NOT NULL CHECK (projection_version > 0),
    PRIMARY KEY (config_digest, route_id)
);

CREATE TABLE llm_worker.provider_inventory_snapshots (
    inventory_snapshot_id uuid PRIMARY KEY,
    config_digest bytea NOT NULL
        REFERENCES llm_worker.configuration_snapshots(config_digest)
        ON DELETE RESTRICT,
    provider text NOT NULL,
    endpoint_id text NOT NULL,
    endpoint_account_hmac bytea NOT NULL
        CHECK (octet_length(endpoint_account_hmac) = 32),
    endpoint_family text NOT NULL,
    region text NOT NULL,
    source text NOT NULL CHECK (
        source IN ('provider_api', 'configured_only', 'unsupported')
    ),
    observed_at timestamptz NOT NULL,
    complete boolean NOT NULL,
    next_cursor_ciphertext bytea,
    next_cursor_key_id text,
    inventory_digest bytea NOT NULL CHECK (octet_length(inventory_digest) = 32),
    expires_at timestamptz NOT NULL,
    UNIQUE (
        config_digest,
        endpoint_id,
        endpoint_account_hmac,
        observed_at
    ),
    CHECK (
        next_cursor_ciphertext IS NULL =
        (next_cursor_key_id IS NULL)
    )
);

CREATE TABLE llm_worker.provider_inventory_models (
    inventory_snapshot_id uuid NOT NULL
        REFERENCES llm_worker.provider_inventory_snapshots(inventory_snapshot_id)
        ON DELETE RESTRICT,
    provider_model_id text NOT NULL,
    display_name text,
    owned_by text,
    created_at_provider timestamptz,
    lifecycle_state text NOT NULL CHECK (
        lifecycle_state IN ('available', 'deprecated', 'unavailable', 'unknown')
    ),
    capability_digest bytea NOT NULL CHECK (octet_length(capability_digest) = 32),
    safe_metadata jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(safe_metadata) = 'object'),
    PRIMARY KEY (inventory_snapshot_id, provider_model_id)
);

CREATE INDEX provider_status_route_time_idx
    ON llm_worker.provider_status_events
        (config_digest, route_id, observed_at DESC, event_id DESC)
    INCLUDE (
        availability,
        credit_state,
        billing_state,
        safe_error_code,
        source
    );

CREATE INDEX provider_status_credit_incident_idx
    ON llm_worker.provider_status_events
        (observed_at DESC, endpoint_id, event_id)
    WHERE credit_state IN ('low', 'exhausted')
       OR billing_state = 'issue';

CREATE INDEX provider_status_event_brin_idx
    ON llm_worker.provider_status_events USING brin (observed_at)
    WITH (pages_per_range = 64);

CREATE INDEX provider_status_expiry_idx
    ON llm_worker.provider_status_events (expires_at, event_id);

CREATE INDEX provider_route_current_problem_idx
    ON llm_worker.provider_route_status
        (availability, credit_state, billing_state, observed_at DESC, route_id)
    WHERE availability <> 'available'
       OR credit_state <> 'ok'
       OR billing_state <> 'ok';

CREATE INDEX provider_route_last_event_idx
    ON llm_worker.provider_route_status (last_event_id);

CREATE INDEX provider_inventory_latest_idx
    ON llm_worker.provider_inventory_snapshots
        (config_digest, endpoint_id, observed_at DESC, inventory_snapshot_id)
    INCLUDE (source, complete, expires_at);

CREATE INDEX provider_inventory_expiry_idx
    ON llm_worker.provider_inventory_snapshots
        (expires_at, inventory_snapshot_id);

CREATE INDEX provider_inventory_model_lookup_idx
    ON llm_worker.provider_inventory_models
        (provider_model_id, inventory_snapshot_id)
    INCLUDE (lifecycle_state, display_name, capability_digest);
~~~

A generic HTTP 429 is never classified as exhausted credit. **exhausted** or
**billing issue** requires a provider-specific code/field documented in the
adapter contract or an operator event. Transient success may clear degraded
health, but a confirmed billing/credit incident remains until a provider
management response, credential/config epoch change, or authorized operator
event clears it.

Model inventory is informational. Configured logical-model routes remain the
authorization source. A discovered model is not routable until a reviewed
configuration snapshot and capability/equivalence entry authorizes it. Provider
listing support is capability data; unsupported providers return a typed
**unsupported** source rather than a fabricated empty list.

Initial adapters reverify official provider list contracts during
implementation:

- [OpenAI Models API](https://platform.openai.com/docs/api-reference/models/object?lang=curl)
- [Anthropic Models API](https://platform.claude.com/docs/en/api/models/list)
- [Amazon Bedrock model discovery](https://docs.aws.amazon.com/bedrock/latest/userguide/models-get-info.html)

### Maintenance outbox

Blob deletion and other external cleanup cannot be atomic with PostgreSQL. A
small outbox makes the intent durable without performing object-store work
inside the committing transaction.

~~~sql
CREATE TABLE llm_worker.maintenance_outbox (
    outbox_id uuid PRIMARY KEY,
    event_kind text NOT NULL CHECK (
        event_kind IN (
            'delete_blob',
            'delete_provider_state',
            'refresh_inventory',
            'refresh_fx'
        )
    ),
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    dedupe_key bytea NOT NULL CHECK (octet_length(dedupe_key) = 32),
    safe_payload jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'processing', 'completed', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at timestamptz NOT NULL,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    UNIQUE (event_kind, dedupe_key)
);

CREATE INDEX maintenance_outbox_ready_idx
    ON llm_worker.maintenance_outbox (available_at, outbox_id)
    WHERE state IN ('pending', 'failed');

CREATE INDEX maintenance_outbox_lease_idx
    ON llm_worker.maintenance_outbox (lease_expires_at, outbox_id)
    WHERE state = 'processing';
~~~

Workers claim outbox rows in short transactions with **FOR UPDATE SKIP LOCKED**.
The payload contains encrypted locators or safe identifiers only. Completion is
idempotent; a missing external object is a successful delete.

### Update-heavy table storage

~~~sql
ALTER TABLE llm_worker.operations SET (fillfactor = 80);
ALTER TABLE llm_worker.budget_buckets SET (fillfactor = 80);
ALTER TABLE llm_worker.response_cache_entries SET (fillfactor = 80);
ALTER TABLE llm_worker.response_cache_fills SET (fillfactor = 80);
ALTER TABLE llm_worker.provider_route_status SET (fillfactor = 80);
ALTER TABLE llm_worker.maintenance_outbox SET (fillfactor = 80);
~~~

These tables receive in-place state or timestamp updates; spare page space
increases HOT-update probability. Immutable event/checkpoint/price tables keep
the default fillfactor. Production installation sets table-specific autovacuum
thresholds after load testing rather than embedding arbitrary scale assumptions
in the initial DDL.

## Query Activity contract

The Activity name is **llm.query.v1**. Request and response are tagged unions,
not an untyped envelope. Each query is scoped/authorized, bounded, and recorded
as an operation with **actual_cost_usd**. A stored-state query normally records
**0.000000000000000000**, **cost_status=exact**, and
**control_query_zero**. If a future management API is billable but the worker
cannot establish its real charge, the query still completes with
**actual_cost_usd=NULL**, **cost_status=unknown**, and a safe reason; it must not
report the estimate or reservation as actual.

Common request fields are:

~~~json
{
  "api_version": "llm.temporal/query/v1",
  "operation_key": "daily-provider-status-2026-07-18",
  "context": {
    "tenant": "acme",
    "project": "claims",
    "actor": "workflow:daily-smoke"
  },
  "kind": "provider_status",
  "query": {
    "provider": "openai",
    "include_healthy": false,
    "refresh_if_older_than_seconds": 300,
    "page_size": 100,
    "cursor": null
  }
}
~~~

The response repeats **kind** and uses exactly one associated result:

~~~json
{
  "api_version": "llm.temporal/query/v1",
  "operation_key": "daily-provider-status-2026-07-18",
  "operation_id": "op_01J...",
  "kind": "provider_status",
  "observed_at": "2026-07-18T03:14:15.926535Z",
  "source": "persisted_and_refreshed",
  "freshness": "current",
  "complete": true,
  "next_cursor": null,
  "result": {
    "routes": [
      {
        "route_id": "openai-primary",
        "availability": "available",
        "credit_state": "ok",
        "billing_state": "ok",
        "observed_at": "2026-07-18T03:14:15.100000Z"
      }
    ]
  },
  "cost_status": "exact",
  "actual_cost_usd": "0.000000000000000000",
  "cost_method": "control_query_zero"
}
~~~

For an unknown charge, **cost_status** is **unknown**,
**actual_cost_usd** is JSON null, **cost_method** is omitted, and
**cost_unknown_reason_code** is present. The decoder rejects every other
combination.

Supported tag/result pairs are:

| Kind | Query-specific filters | Result shape |
| --- | --- | --- |
| **provider_status** | provider, endpoint, availability, include healthy, freshness | page of typed route-status rows |
| **model_inventory** | provider, endpoint, model prefix, lifecycle, freshness | page of typed model rows plus inventory support/completeness |
| **credit_status** | provider, endpoint, include ok, freshness | page of typed credit/billing rows and confirmed-at evidence |
| **budget_status** | policy key, active-at, include windows | matching policies/windows with exact limit, reserved, accounted charge, available USD, and charge basis |
| **spend_summary** | half-open UTC interval, group-by dimensions, operation kinds | typed buckets with known actual-cost USD, exact/unknown operation counts, and completeness |

The common response has **observed_at**, **source**, **freshness**, **complete**,
and an opaque signed cursor. Page size has an operator cap, ordering is stable,
and cursor contents bind query tag, scope, filters, sort key, snapshot horizon,
expiry, and MAC. Unknown tags, mismatched result tags, unknown fields, or
expired/tampered cursors are typed non-retryable errors.

**refresh_if_older_than_seconds** is optional. Omission reads durable state
only. When present, the worker may call a documented provider management/list
API and then store the observation before answering. It never invokes an
inference endpoint, bypasses provider rate limits, or returns raw provider JSON.
One refresh owner collapses concurrent requests per endpoint.

Spend aggregation filters **operations.state = 'completed'** and sums only rows
with **cost_status=exact**. It returns **known_actual_cost_usd** plus exact and
unknown operation counts; any unknown row makes the bucket's cost completeness
**partial**. SQL must not **COALESCE(actual_cost_usd, 0)**. Cache hits and free
queries remain visible exact-zero operations rather than absent records.
Half-open intervals and database UTC time prevent boundary double counting.

## Transaction protocols

### Begin and operation replay

1. Derive tenant/project HMACs and upsert scope.
2. Insert operation using the unique scoped key.
3. On conflict, lock the existing row and compare kind, API version,
   request-fingerprint HMAC, schema version, and canonical request JSONB.
4. Return stored result for **completed**; resume **provider_pending** by poll
   ID; resume only a provably pre-write expired lease; return typed conflict or
   ambiguous state for every unsafe case.
5. Resolve parent and materialize outside a long transaction, then revalidate
   its immutable digests when the short admission transaction begins.

### Exact cache lookup and fill

1. Plan authorized model-equivalence classes from the configuration snapshot.
   Provider health and budget do not block reuse of an otherwise authorized
   cached result.
2. Build one fingerprint per distinct equivalence class in deterministic route
   order. Unknown/isolated quantizations remain separate.
3. Lock a ready entry and compare **completed_at** with one database timestamp
   minus caller **max_age**.
4. On a hit, insert use, saturating-update last use/count, create the
   cache-replay child/result, complete the operation at zero USD, and commit.
5. On a miss, insert or claim the fill row. Do not hold a PostgreSQL transaction
   while calling a provider.
6. Waiters observe the fill with bounded database notification/polling and
   never dispatch while a valid owner exists. An expired owner is recoverable
   only after resolving its operation state; **dispatching** is not assumed
   safe.
7. The owner finalization transaction writes output/checkpoint, cache entry,
   origin use, exact-or-unknown cost state, releases the fill, and completes the
   operation.

The cache may serve an entry produced on a different provider only through a
currently authorized certified model-equivalence class. Response provenance
reports **served_by = worker_cache** and the origin model/route; it never
pretends a current provider was invoked.

### Resumable provider submission

1. Lock operation and set route, attempt, **dispatching**, deterministic
   provider idempotency key, and possible-write boundary before network I/O.
2. Submit once through an adapter whose retry count is zero.
3. If the provider returns a pollable ID, immediately envelope-encrypt it and
   transition to **provider_pending** before the first poll or heartbeat.
4. Poll attempts lock/verify the row, release the transaction, poll the same ID,
   then persist status and next poll time.
5. A restarted Activity begins from the operation row and polls the stored ID.
6. The acceptance-before-ID persistence gap is recoverable only through a
   provider idempotency/lookup contract. Otherwise record **ambiguous** and do
   not resubmit.

Heartbeats contain operation ID and phase only. The database is authoritative
for provider IDs, poll count, status, and cost.

### Completion and cache-use accounting

Operation, attempt, budget reconciliation, result blob reference, checkpoint,
cache use/fill, provider status event/projection, and exact-or-unknown USD cost
status finalize in one serializable or explicitly locked transaction. External
blob bytes are written first under an uncommitted locator; the transaction
publishes their rows. Failed publication schedules idempotent orphan cleanup.

Transactions retry serialization/deadlock failures with a small bounded policy
before possible provider write. They never retry a provider submission. Lock
order is documented and tested:

~~~text
scope -> operation -> cache fill/entry -> budget windows sorted by UUID
      -> checkpoint parent -> status projection
~~~

## Index rationale and query plans

| Index family | Production query served | Why this order |
| --- | --- | --- |
| Scoped operation unique | Activity Begin/replay | equality on scope, kind, HMAC |
| Operation lease/poll partial | crash recovery and due polling | excludes terminal rows and orders by due time |
| Scope spend covering | budget/spend query | scope and time range; included cost/dimensions reduce heap reads |
| Unknown-cost partial | reconciliation queue | scoped completed unknown rows only |
| Operation BRIN | global retention/audit scans | append-correlated time at low index cost |
| Parent operation/checkpoint/cache | FK enforcement, lineage, deletion | PostgreSQL does not index FK children |
| Attempt route/time | provider incident queries | endpoint/model then recent observations |
| Checkpoint parent | fork traversal and retention | equality parent then stable child order |
| Checkpoint scope/time | tenant audit and deletion | scoped recent range |
| Cache unique | direct exact lookup | all equality key components |
| Cache last-use partial | future unused-entry cleanup | ready entries in oldest-use order |
| Cache fill lease partial | abandoned-fill recovery | only active fills ordered by expiry |
| Budget bucket primary | active-window sum and row locking | window equality plus bucket time range |
| Reservation window | budget status/reconciliation | window/bucket/state range with covered amounts |
| Price resolution covering | route/model/tier at time | equality identity then newest effective price |
| Unresolved-price partial | catalog review/refresh | only partial or unknown entries by status/time |
| Status route/time | latest/history query | configuration/route equality then newest event |
| Status problem partial | outage/credit dashboards | indexes only exceptional current rows |
| Inventory latest/model | latest snapshot and model lookup | endpoint/time and model equality paths |
| Outbox partial | maintenance claim/recovery | only runnable or leased rows |

Implementation captures **EXPLAIN (ANALYZE, BUFFERS)** for cache lookup, Begin,
active budget admission, spend summary, latest status, model inventory, and
180-day cache cleanup at representative cardinalities. The reconciliation suite
also proves the unknown-cost and unresolved-price partial indexes. Tests reject
sequential scans on large seeded tables for those bounded queries. Index count
is also measured: an index that does not serve a named query is removed.

## Retention, vacuum, and growth

- Operations/checkpoints/results outlive the maximum Workflow retry and
  retention horizons plus an operator safety margin.
- Cache cleanup defaults may later choose 180 days since **last_used_at**. The
  protocol does not hard-code that duration.
- Cleanup selects a bounded batch through **response_cache_gc_idx**, locks rows
  with **SKIP LOCKED**, rechecks last use/no fill/no retained descendant, marks
  tombstoned, and schedules blob deletion.
- Status events and inventory snapshots have independent bounded retention;
  current projections remain.
- Budget buckets are deleted only after every matching window and operation
  reconciliation horizon has expired.
- The maintenance role runs frequent small deletes. It never performs an
  unbounded delete or vacuum from an Activity.
- Monitor dead tuples, HOT update ratio, autovacuum lag, transaction age, index
  bloat, WAL volume, pool saturation, lock wait, cache-table growth, and blob
  orphan count.

Initial schema is unpartitioned because global idempotency/cache uniqueness is
more valuable than premature time partitioning. BRIN indexes and batched
retention support the initial scale. Before the largest table reaches a tested
threshold such as 100 million rows, capacity review may introduce partitioning
through a shadow table and a separate global key registry. It must prove global
uniqueness and query plans before cutover; ad hoc partitions are not an
emergency maintenance step.

## PostgreSQL operating requirements

- Require TLS certificate verification, separate migration/runtime/maintenance
  roles, password or workload-identity rotation, and schema-qualified SQL.
- Set connection, statement, lock, idle-in-transaction, and transaction
  timeouts. Size each replica's pool against the database connection budget.
- Use prepared statements or typed generated queries; never concatenate
  identifiers or filters from Activity input.
- Use synchronous commit for operation/provider-ID/checkpoint/cache/budget
  transitions. A deployment may not weaken durability for these tables.
- Enable encrypted storage, encrypted verified backups, WAL archiving, point-in-
  time recovery, cross-failure-domain copies, and scheduled restore drills.
- Readiness checks a transaction, migration version, required indexes, server
  time, and read/write role capability. It does not apply migrations.
- Startup rejects missing/invalid constraints, an unexpected schema version,
  non-UTC session behavior, or a database confused with Temporal's database.
- Metrics/traces contain safe IDs and durations, never SQL parameters with
  tenant/content/provider IDs.

## Schema cutover sequence

This sequence also modernizes existing Redis-backed functionality; it is not
limited to the new cache:

1. Freeze the accepted DDL in migration and schema-contract fixtures.
2. Provision a separate worker database/roles and apply the schema in an
   ephemeral environment.
3. Implement exact decimal USD and remove public/config currency plus integer
   microUSD assumptions from the authoritative store.
4. Add PostgreSQL repositories for configuration, blobs, operations/attempts,
   budgets, continuations, prices, and results; run the existing memory/Redis
   conformance cases against PostgreSQL with stricter decimal assertions.
5. Add checkpoints, cache, model equivalence, provider status/inventory, FX,
   and outbox repositories.
6. Run a one-time fixture importer only to compare existing Redis semantic
   records with PostgreSQL. Do not ship an online dual writer.
7. Switch the engine composition and readiness checks to PostgreSQL in one
   commit; disable Redis production configuration and reject it at startup.
8. Run crash-boundary, concurrent budget/cache/fork, query-plan, retention, and
   restore tests with at least two worker replicas.
9. Remove Redis operation/continuation/budget code, Functions, deployment,
   configuration, docs, and dependencies after PostgreSQL gates pass.
10. Record clean-database cutover evidence and keep rollback as application
    version plus database restore, never a partial return to dual authority.

Because there is no production dataset, no live backfill or dual-read period is
required. The sequence still isolates schema, repository, cutover, and deletion
commits so a cheaper implementation agent can test and review each boundary.

## Database acceptance gates

- Exact decimal arithmetic round-trips values with 18 fractional digits through
  PostgreSQL, Go, JSON, and OCaml without float conversion.
- Operation requests and cache manifests are retained as bounded JSONB, while
  indexed lookup uses HMAC-SHA-256 fingerprints and never a JSONB scan.
- Every completed operation, including every Query and cache hit, has a valid
  exact-or-unknown cost state. Exact has non-null **actual_cost_usd** and a
  method; unknown has NULL amount/method and a safe reason. Cache hits and local
  queries are exact zero.
- Price-catalog component NULLs remain unknown through Go, SQL, JSON, and OCaml;
  they are never coerced to zero. Tests cover exact $0, sub-micro-dollar values,
  $1, $10, the maximum **NUMERIC(38,18)** value, and overflow rejection.
- The database exposes no generic downstream currency column.
- All foreign keys have a named supporting index or a documented parent-side
  access path.
- Concurrent operation-key reuse produces one operation or a digest conflict.
- Concurrent identical cache misses produce one provider submission.
- A Temporal retry increments cache **use_count** at most once.
- An old entry used yesterday survives a 180-day unused-entry sweep.
- A worker restart polls a persisted provider operation ID without submission.
- Budget admission never exceeds any window under concurrent transactions.
- Credit exhaustion is not inferred from an unclassified 429.
- Certified cross-provider model routes share a cache entry; unknown
  quantization does not.
- Checkpoint tables reject runtime UPDATE and retention cannot delete a needed
  ancestor/blob.
- Named hot queries use the intended indexes at representative volume.
- Backup restore plus blob integrity verification reconstructs completed replay
  and pending polling state.
