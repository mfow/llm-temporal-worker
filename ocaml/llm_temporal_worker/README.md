# `llm-temporal-ocaml`

Typed OCaml bindings for one durable, non-streaming `llm.generate.v1` Go
Activity. The package contains no provider credentials, token streaming,
continuation loop, polling, or application retry loop. The generated workflow
schedules exactly one activity attempt.

The public API mirrors the v1 request and response records: service classes
are exactly `Economy | Standard | Priority`; request controls include
portability, instructions, items, tools, output, temperature, reasoning
effort/summary, and extensions; responses retain route, service, usage, cost,
provider, continuation, and diagnostics. Only deliberately open contract
leaves (schemas, tool arguments, extension/provider metadata) use
`Yojson.Safe.t`.

## Install

Pin the nested package at the deployment commit:

```sh
opam pin add --yes --kind=git llm-temporal-ocaml \
  'git+https://github.com/mfow/llm-temporal-worker.git#<commit>' \
  --subpath=ocaml/llm_temporal_worker
opam install --yes llm-temporal-ocaml
```

Its metadata pins `temporal-sdk` to immutable commit
`e6c2e494a82eb36d48333f970beee109a6fa2ed2`. Commit an application lock file
after `opam lock .`, then deploy with `opam install . --locked`.

Add `(libraries llm-temporal-ocaml)` to your Dune stanza.

The repository also contains a separate [downstream Dune consumer smoke
project](../consumer_smoke). CI installs this package from the Git subpath,
then builds that project against the installed public library. This catches
packaging and public-name regressions that a build from the source directory
cannot detect.

## Use

```ocaml
open Llm_temporal

let request =
  Request.make
    ~operation_key:(Operation_key.of_string "invoice-42")
    ~model:(Model_selector.of_string "gpt-5")
    ~service_class:Priority
    ~input:[ Message { actor = Human; content = [ Text "Summarise this invoice." ] } ]
    ~instructions:[ Text_instruction { level = Application; text = "Return JSON." } ]
    ~service_class_fallbacks:[ Standard ]
    ~output:{ max_tokens = Some 200; format = Json_format }
    ()

let definition =
  Llm_temporal.workflow
    ~task_queue:(Llm_temporal.Temporal_task_queue.of_string "go-activities") ()
(* Register [definition] with the OCaml workflow worker. *)
```

## Immutable conversations

For a multi-turn workflow, `Llm_temporal.Conversation` keeps the v1 checkpoint
branch head as an immutable value. `fork` is a cheap persistent branch
operation: it does not schedule an Activity or mutate the parent. A successful
`respond` returns the v1 provider response together with a child conversation
carrying the returned checkpoint. Callers therefore choose explicitly which
child to retain.

```ocaml
let settings =
  Llm_temporal.Conversation.Settings.make
    ~service_class:Llm_temporal.Priority ()
in
let root =
  Llm_temporal.Conversation.root ~context ~model ~settings ()
in
let branch = Llm_temporal.Conversation.fork root in
match Llm_temporal.Conversation.respond
        ~operation_key:(Llm_temporal.Operation_key.of_string "turn-1")
        ~append:[ Message { actor = Human; content = [ Text question ] } ]
        branch with
| Ok { response; conversation } ->
    (* [conversation] is the next immutable branch head. *)
    ignore (response, conversation)
| Error error -> handle_temporal_error error
```

`Conversation.to_request` is available when a workflow needs to inspect or
inject the exact low-level v1 request. `respond_with` accepts an injectable
typed dispatcher for deterministic tests; production code normally uses
`respond` or `start_respond`. Settings changes are explicit persistent
builders, for example:

```ocaml
let patch =
  Llm_temporal.Conversation.Settings.Patch.set_service_class
    Llm_temporal.Economy
    Llm_temporal.Conversation.Settings.Patch.keep
in
let cache =
  Llm_temporal.Conversation.Cache_policy.accept_up_to
    ~max_age_seconds:60L ~variant:1l ()
in
let branch =
  Llm_temporal.Conversation.respond ~settings_patch:patch ?cache
    ~operation_key:(Llm_temporal.Operation_key.of_string "turn-2")
    ~append:[ Message { actor = Human; content = [ Text "Continue." ] } ] branch
in
```

`Conversation.compact` creates an explicit compaction child from a checkpoint;
the following Generate restores the branch's application tools and output
configuration. The wrapper does not stream, retain a mutable implicit head, or
schedule any Activity outside the exact `llm.generate.v1` and
`llm.compact.v1` descriptors. The protocol layer also exposes exact Compact
and typed Query v1 records and Yojson codecs; those low-level records remain
separate from the ergonomic facade.

## Typed query facade

`Llm_temporal.Query` adds a closed GADT over the five query Activities. Each
constructor carries its filter and fixes the result type, so pagination and
result handling remain associated at the call site:

```ocaml
let query =
  Llm_temporal.Query.Budget_status {
    policy_key = None; active_at = None; include_windows = true;
  }

match Llm_temporal.Query.execute
        ~operation_key:(Llm_temporal.Operation_key.of_string "budget-check")
        ~context query with
| Ok { value = budget; cost; _ } -> inspect_budget budget cost
| Error error -> handle_temporal_error error
```

The `Provider_status`, `Model_inventory`, `Credit_status`, `Budget_status`,
and `Spend_summary` constructors are exhaustive. The facade validates that
the closed result tag matches the requested constructor and returns a codec
`Temporal.Error.t` for mismatches or unknown future tags; it never uses an
unchecked JSON cast or `Obj.magic`. `Query.start` returns a workflow-owned
Temporal future whose successful value is a typed `result` (so protocol-kind
mismatches stay on the error channel without raising in a workflow callback),
while `Query.execute_with` is available for deterministic dispatch injection
in tests.

Each `*_id` module is an opaque wrapper around arbitrary text—not a provider
enum or whitelist.  For example, `Operation_key.t`, `Endpoint_id.t`, and
`Provider_request_id.t` cannot be interchanged, while `of_string` and
`to_string` make construction and logging explicit.  The encoded payload
continues to use the unchanged v1 JSON strings.

`Image` and `Document` sources carrying `Bytes raw` treat `raw` as the byte
string itself. The codec emits standard padded base64 in the JSON `bytes`
field and decodes it back to the original bytes. URLs are checked for an
absolute, non-`data:`/non-`javascript:` URI; tool names are restricted to the
v1 ASCII `[A-Za-z0-9_-]{1,64}` form. Request encoding and response decoding
fail with `Temporal.Error.t` for invalid identifiers, duplicate service
fallbacks, negative token/cost limits, invalid media, duplicate open-JSON
members, and other protocol violations before a Temporal activity is called.

`workflow` calls `Temporal.Activity.execute` once against the exact Go
activity name `llm.generate.v1` with the exported
`Llm_temporal.activity_retry_policy` (`maximum_attempts = 1`). The Go worker
must serve the provided task queue (or the SDK's worker queue when omitted).
Errors are returned unchanged as `Temporal.Error.t`; the wrapper does not
retry, continue, or stream after an activity result. Callers that inject a
`dispatch` through `invoke_once` can use the same one-shot/error-propagation
contract in deterministic unit tests.

Continuation handles and provider-state identifiers/media types are required
to be non-empty. `continuation.expires_at`, when present, must be an RFC3339
timestamp (the same format emitted by the Go worker). These cross-language
invariants are checked during encode and decode so malformed values fail as a
codec error before an Activity is scheduled or invalid data enters Temporal
history.
