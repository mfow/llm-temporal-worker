# OCaml Conversation, Compaction, and Typed Query Client

## Status and compatibility

This document specifies changes to the existing
**ocaml/llm_temporal_worker** package. It does not create another opam package
or a parallel client. The implementation extends the same **Llm_temporal**
facade, nominal identifiers, codecs, Activity descriptors, retry policy, and
test conventions.

The package currently models one **llm.generate.v1** invocation. The post-v1
cutover deliberately removes the generic currency field, adds decimal USD
types, and rebuilds the existing one-shot convenience API as a root
**llm.generate.v2** call. Because this service is not deployed, the package may
make that breaking change in one coordinated release rather than preserving two
inconsistent money models.

Before implementation, rebase and inspect any open OCaml validation work,
especially PR 109 as observed when this design was written. Preserve landed
validation improvements and regenerate fixtures from the final Go contract.

## Two public layers, one package

The package has two conceptual layers:

| Layer | Purpose | Public stability |
| --- | --- | --- |
| Protocol | Exact closed OCaml representation of Go JSON and Activity names | Wire-compatible and exhaustive |
| Ergonomic workflow API | Natural immutable conversations, sparse patch builders, and typed query execution | Source-friendly and hides tag matching |

Existing private modules continue to own identifier validation, model records,
JSON codecs, and invocation. New modules are included through the existing
**Llm_temporal** facade:

~~~text
lib/
  llm_temporal_identifier.ml/.mli       existing, extend
  llm_temporal_models.ml/.mli           existing v2 protocol models
  llm_temporal_codec.ml/.mli            existing, extend fixtures
  llm_temporal_invocation.ml/.mli       existing, add activity descriptors
  llm_temporal_conversation.ml/.mli     new ergonomic stateful API
  llm_temporal_query.ml/.mli            new typed query API
  llm_temporal.ml/.mli                  existing unified facade
~~~

The protocol modules may remain private Dune modules while their selected types
are exposed through the facade. Callers need only
**(libraries llm-temporal-ocaml)**.

## Nominal and decimal types

Add opaque wrappers for values that must not be interchanged:

~~~ocaml
module Checkpoint : sig
  type t
  val of_string : string -> (t, validation_error) result
  val to_string : t -> string
end

module Query_cursor : sig
  type t
  val of_string : string -> (t, validation_error) result
  val to_string : t -> string
end

module Model_equivalence_id : Identifier
module Budget_policy_key : Identifier
~~~

Money has an explicitly USD-denominated arbitrary-precision decimal type:

~~~ocaml
module Usd_decimal : sig
  type t

  val zero : t
  val of_string : string -> (t, validation_error) result
  val to_string : t -> string
  val compare : t -> t -> int
  val add : t -> t -> t
end
~~~

The accepted spelling is a non-negative canonical base-10 decimal with at most
18 fractional digits and within PostgreSQL **NUMERIC(38,18)**. JSON uses a
string. The OCaml implementation uses an exact decimal/big-integer
representation or a checked coefficient plus scale; **float** is absent from
constructors and records. There is no **currency** value, string, or enum in
the Go v2 wire model or OCaml facade. A field named **actual_cost_usd** is known
to be USD.

Unknown is represented structurally, not by a sentinel decimal. A catalog unit
price or actual cost that the worker cannot establish is JSON null at the wire
boundary and becomes an OCaml **Unknown_cost** constructor. Exact zero remains
an ordinary **Usd_decimal.t** inside **Exact_cost**. Estimates and retained
budget bounds use different types/fields and cannot be substituted for actual
cost.

Durations use a checked non-negative integer seconds type at the wire boundary.
Cache **variant** uses **int32**, not OCaml's platform-width **int**.

## Exact protocol records

### Generate and compact

The low-level v2 request preserves omitted versus explicit patch operations:

~~~ocaml
type 'a patch =
  | Keep
  | Set of 'a
  | Clear

type settings_patch = {
  model : Model_selector.t patch;
  service_class : service_class patch;
  service_class_fallbacks : service_class list patch;
  portability : portability patch;
  instructions : instruction list patch;
  tools : tool list patch;
  tool_policy : tool_policy patch;
  output : output_config patch;
  temperature : Decimal.t patch;
  reasoning_effort : reasoning_effort patch;
  reasoning_summary : reasoning_summary patch;
  compaction_policy : compaction_policy patch;
  extensions : extensions patch;
}

type cache_policy = {
  max_age_seconds : Int64.t;
  variant : Int32.t;
}

type generate_v2_request = {
  api_version : string;
  operation_key : Operation_key.t;
  context : request_context;
  parent : Checkpoint.t option;
  append : item list;
  settings_patch : settings_patch;
  cache : cache_policy option;
}
~~~

The encoder omits every **Keep** leaf and omits **settings_patch** when all
leaves are Keep. **Set []** emits an empty list and differs from **Clear**.
**Clear** emits **{"clear":true}**. It never serializes inherited settings into
the Activity payload.

Effective temperature is validated by the Go worker after inheritance. The
OCaml builder rejects immediately when it can prove temperature zero with a
positive variant, but it does not guess an inherited value. Server validation
remains authoritative and returns a typed Activity error.

Responses use new-turn output and a checkpoint:

~~~ocaml
type settled_cost =
  | Exact_cost of {
      actual_cost_usd : Usd_decimal.t;
      method_ : cost_method;
      catalog_version : string option;
    }
  | Unknown_cost of { reason : cost_unknown_reason }

type cost = {
  reserved_cost_usd : Usd_decimal.t;
  settled : settled_cost;
}

type generate_v2_response = {
  operation_key : Operation_key.t;
  operation_id : Operation_id.t;
  status : response_status;
  output : item list;
  checkpoint : checkpoint_metadata;
  cache : cache_disposition;
  route : route option;
  usage : usage;
  cost : cost;
  diagnostics : diagnostic list;
}
~~~

There is no transcript field. The Compact protocol has its own request/response
types and Activity name. Its result identifies a compaction checkpoint,
provenance, usage, and USD cost. Compaction protocol records contain no
application tool list, tool policy, or structured-output field. Those are
inherited checkpoint settings restored only by a later Generate.

### Tagged Query wire contract

The exact JSON union is closed:

~~~ocaml
type query_request =
  | Provider_status_request of provider_status_filter
  | Model_inventory_request of model_inventory_filter
  | Credit_status_request of credit_status_filter
  | Budget_status_request of budget_status_filter
  | Spend_summary_request of spend_summary_filter

type query_result =
  | Provider_status_result of provider_status_page
  | Model_inventory_result of model_inventory_page
  | Credit_status_result of credit_status_page
  | Budget_status_result of budget_status
  | Spend_summary_result of spend_summary

type query_response = {
  operation_key : Operation_key.t;
  operation_id : Operation_id.t;
  observed_at : Ptime.t;
  source : query_source;
  freshness : freshness;
  complete : bool;
  next_cursor : Query_cursor.t option;
  result : query_result;
  cost : settled_cost;
}
~~~

Each codec validates both outer **kind** and inner result constructor. The
decoder rejects an unknown tag, duplicate/unknown member, malformed cursor,
unbounded page, invalid timestamp/decimal, or a response whose tag and result
shape disagree. It never returns **Yojson.Safe.t** for a known query result.
Open JSON remains limited to deliberately open provider metadata already
allowed by the package.

The JSON codec enforces the exact cost-state invariant. **cost_status=exact**
requires a decimal-string **actual_cost_usd** and method. **unknown** requires
JSON null plus a closed safe reason and forbids a method. This is decoded
directly to **settled_cost** so ordinary OCaml callers cannot accidentally sum
an unknown price as zero.

Result records are closed and typed:

- provider status has route ID, availability, credit/billing state, circuit
  state, observation/staleness timestamps, and safe code;
- model inventory has provider model ID, display/lifecycle values, typed known
  capabilities, safe open metadata, source, and completeness;
- credit status has confirmed state/time/source and safe evidence code;
- budget status has policy/window IDs and exact USD limit, reserved, finalized,
  available, and retry-after;
- spend summary has a half-open interval, closed grouping keys, exact USD total,
  and completed-operation count.

Unknown future enum values are decoding errors until the OCaml library is
updated. This is intentional for Workflow decisions: silently treating a new
credit state as healthy would be unsafe.

## Ergonomic immutable Conversation API

A conversation value is a pure branch head plus effective settings known to the
caller. It contains no network client, mutable reference, or process-global
state:

~~~ocaml
module Conversation : sig
  type t

  type turn = {
    response : generate_v2_response;
    conversation : t;
  }

  val root :
    context:request_context ->
    model:Model_selector.t ->
    ?service_class:service_class ->
    ?settings:Settings.t ->
    unit ->
    t

  val of_checkpoint :
    context:request_context ->
    checkpoint:Checkpoint.t ->
    t

  val checkpoint : t -> Checkpoint.t option

  val fork : t -> t

  val respond :
    ?task_queue:Temporal_task_queue.t ->
    operation_key:Operation_key.t ->
    ?settings_patch:Settings.Patch.t ->
    ?cache:Cache_policy.t ->
    append:item list ->
    t ->
    (turn, Temporal.Error.t) result

  val compact :
    ?task_queue:Temporal_task_queue.t ->
    operation_key:Operation_key.t ->
    ?policy:Compaction_policy.t ->
    t ->
    (compaction_response * t, Temporal.Error.t) result
end
~~~

**fork** is intentionally trivial: it returns another immutable value naming
the same checkpoint. It exists to make application intent clear, not to allocate
server state. The same value may also be passed directly to three **respond**
calls with different operation keys:

~~~ocaml
let parent = turn.conversation in
let branch_a = Conversation.fork parent in
let branch_b = Conversation.fork parent in
let branch_c = Conversation.fork parent in

let a =
  Conversation.respond
    ~operation_key:(Operation_key.of_string_exn "case-a")
    ~append:[Message { actor = Human; content = [Text "Try A"] }]
    branch_a
in
let b =
  Conversation.respond
    ~operation_key:(Operation_key.of_string_exn "case-b")
    ~append:[Message { actor = Human; content = [Text "Try B"] }]
    branch_b
in
let c =
  Conversation.respond
    ~operation_key:(Operation_key.of_string_exn "case-c")
    ~append:[Message { actor = Human; content = [Text "Try C"] }]
    branch_c
in
...
~~~

The library does not mutate **parent** when one branch completes. Each success
returns its own child conversation. Temporal Workflow code stores the desired
child in its deterministic Workflow state.

### Natural settings and cache builders

The ergonomic settings patch module uses the same tri-state explicitly:

~~~ocaml
module Settings : sig
  type t

  module Patch : sig
    type t
    val keep : t
    val set_temperature : Decimal.t -> t -> t
    val clear_temperature : t -> t
    val set_reasoning_effort : reasoning_effort -> t -> t
    val replace_tools : tool list -> t -> t
    val clear_tools : t -> t
    val replace_output : output_config -> t -> t
    val clear_output : t -> t
  end
end

module Cache_policy : sig
  type t
  val accept_up_to :
    max_age_seconds:Int64.t ->
    ?variant:Int32.t ->
    unit ->
    (t, validation_error) result
end
~~~

There is no Boolean **use_cache** that leaves freshness undefined. Omitting
**cache** means no read and no population. Variant defaults to **Int32.zero**.
No random key is generated automatically; callers deliberately choose stable
variants for smoke-test fixtures.

The one-shot convenience **Request.make/execute/workflow** remains recognizable
to current callers but constructs a Conversation root and schedules one v2
Generate. It does not maintain a loop or hide a checkpoint. The result includes
the child conversation so callers can migrate to multi-turn use without a
second package.

## GADT-typed Query API

The ergonomic query layer associates each filter with exactly one result type:

~~~ocaml
module Query : sig
  type _ t =
    | Provider_status :
        provider_status_filter -> provider_status_page t
    | Model_inventory :
        model_inventory_filter -> model_inventory_page t
    | Credit_status :
        credit_status_filter -> credit_status_page t
    | Budget_status :
        budget_status_filter -> budget_status t
    | Spend_summary :
        spend_summary_filter -> spend_summary t

  type 'a response = {
    value : 'a;
    operation_id : Operation_id.t;
    observed_at : Ptime.t;
    source : query_source;
    freshness : freshness;
    complete : bool;
    next_cursor : Query_cursor.t option;
    cost : settled_cost;
  }

  val execute :
    ?task_queue:Temporal_task_queue.t ->
    operation_key:Operation_key.t ->
    context:request_context ->
    'a t ->
    ('a response, Temporal.Error.t) result
end
~~~

Call-site type inference prevents asking a provider-status query and treating
the answer as a spend summary:

~~~ocaml
let result =
  Query.execute
    ~operation_key:(Operation_key.of_string_exn "budget-check-481")
    ~context
    (Query.Budget_status { policy_key = None; include_windows = true })
in
match result with
| Ok { value = budget; cost = Exact_cost { actual_cost_usd; _ }; _ } ->
    assert (Usd_decimal.compare actual_cost_usd Usd_decimal.zero = 0);
    inspect_budget budget
| Ok { cost = Unknown_cost { reason }; _ } -> report_unknown_cost reason
| Error error -> handle_temporal_error error
~~~

Internally, **execute** existentially packages the wire request, schedules the
single Activity, decodes the closed response, and pattern-matches the matching
result constructor. An impossible mismatch is returned as a protocol
**Temporal.Error.t** with safe details. It never uses **Obj.magic**, polymorphic
variants with catch-all values, or an unchecked JSON cast.

Pagination remains typed: the cursor can be supplied only to the same query
constructor/filter digest. The server is authoritative for cursor binding; the
OCaml client also retains query kind in the cursor wrapper to reject obvious
cross-kind reuse before dispatch.

## Activity descriptors and Workflow determinism

The invocation module exposes three exact names:

~~~ocaml
val generate_v2_activity :
  (generate_v2_request, generate_v2_response) Temporal.Activity.t

val compact_v1_activity :
  (compact_request, compaction_response) Temporal.Activity.t

val query_v1_activity :
  (query_envelope, query_response) Temporal.Activity.t
~~~

The package reuses validated Activity options and the current one-attempt SDK
policy; the Go operation ledger owns durable retry/recovery. It does not poll a
provider from Workflow code. A Go Activity retry sees **provider_pending** and
continues polling the persisted provider ID.

Every builder is pure. Current time, randomness, environment variables, model
discovery, status refresh, FX retrieval, and cache selection occur in the Go
Activity, not OCaml Workflow code. Callers supply stable operation keys and
explicit variants. Decoding a response is deterministic for its payload.

## Error surface

Keep **Temporal.Error.t** as the invocation error surface, with helpers that
recognize new safe application types:

- invalid checkpoint/patch/variant;
- operation conflict;
- cache/state unavailable;
- cache fill wait expired;
- checkpoint pinned/corrupt/expired;
- compaction failed;
- provider pending/ambiguous;
- unsupported query refresh/inventory;
- invalid query cursor;
- budget wait; and
- protocol tag mismatch.

Errors never embed raw prompt, output, provider error body, poll ID, cache
fingerprint, or database value.

## OCaml implementation order

1. Regenerate/freeze v2 Generate, Compact, Query, and decimal JSON fixtures from
   Go before changing public OCaml types.
2. Add nominal checkpoint/cursor/equivalence IDs and exact **Usd_decimal** with
   property tests.
3. Replace generic currency/microUSD response fields with USD decimal fields in
   protocol models, codecs, README, and fixtures.
4. Add patch/cache/checkpoint/compaction wire records and exhaustive validation.
5. Add the three Activity descriptors and low-level invoke functions.
6. Implement immutable Conversation and settings/cache builders.
7. Implement every closed query filter/result record and codec.
8. Implement the Query GADT and safe internal tag matcher.
9. Rebuild the existing one-shot facade on a v2 root without a second package.
10. Update Dune module lists/interfaces, opam metadata if a decimal dependency is
    selected, examples, and downstream compile fixtures.

## OCaml acceptance gates

- Go and OCaml golden JSON is byte-equivalent after canonicalization for every
  request/result kind and error.
- Omitted/Set/Clear survive round trips as three distinct values.
- Inherited temperature zero plus positive variant is rejected by the server;
  locally known zero is rejected by the builder.
- No float or currency field exists in public money types.
- Decimal values with 18 fractional digits round-trip exactly.
- The same immutable parent can produce three distinct child conversations.
- Cache omission emits no cache object; variant zero is omitted or encoded
  consistently with the canonical fixture.
- Compact requests contain neither application tools nor structured output;
  the next Generate restores them exactly.
- Every Query GADT constructor decodes only its associated result; mismatch and
  unknown tags fail safely.
- Query pagination preserves its static result type.
- Activity names and payload codecs match the Go worker constants exactly.
- Existing one-shot sample code has a documented mechanical migration and no
  second OCaml package/import path is introduced.
- Dune builds libraries, interfaces, examples, unit tests, and an external
  downstream package under the pinned Temporal SDK version.
