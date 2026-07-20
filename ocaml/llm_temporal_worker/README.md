# `llm-temporal-ocaml`

Typed OCaml bindings for one durable, non-streaming `llm.generate.v1` Go
Activity. The package contains no provider credentials, token streaming,
continuation loop, polling, or application retry loop. The generated workflow
schedules exactly one activity attempt.

The public API mirrors the v1 request and response records: service classes
are exactly `Economy | Standard | Priority`; request controls include
portability, instructions, items, tools, output, sampling, reasoning,
continuation, and extensions; responses retain route, service, usage, cost,
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

For a multi-turn workflow, `Llm_temporal.Conversation` keeps the branch head
as an immutable value.  `fork` is a cheap persistent branch operation: it does
not schedule an Activity or mutate the parent.  A successful `respond` returns
the provider response together with a child conversation carrying the returned
continuation.  Callers therefore choose explicitly which child to retain.

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
inject the exact low-level request.  `respond_with` accepts an injectable
typed dispatcher for deterministic tests; production code normally uses
`respond` or `start_respond`.  The conversation wrapper remains focused on the
Generate activity.  The protocol layer now also exposes exact Compact and
typed Query v1 records, Yojson codecs, and the `llm.compact.v1`/`llm.query.v1`
Activity descriptors; these low-level records stay separate from the
ergonomic conversation facade.

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
