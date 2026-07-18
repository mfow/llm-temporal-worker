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
`a5e565dd0defcde03c429b4fe60c23fecaaa5685`. Commit an application lock file
after `opam lock .`, then deploy with `opam install . --locked`.

Add `(libraries llm-temporal-ocaml)` to your Dune stanza.

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

Each `*_id` module is an opaque wrapper around arbitrary text—not a provider
enum or whitelist.  For example, `Operation_key.t`, `Endpoint_id.t`, and
`Provider_request_id.t` cannot be interchanged, while `of_string` and
`to_string` make construction and logging explicit.  The encoded payload
continues to use the unchanged v1 JSON strings.

`workflow` calls `Temporal.Activity.execute` once against the exact Go
activity name `llm.generate.v1` with `maximum_attempts = 1`. The Go worker
must serve the provided task queue (or the default queue when omitted).
Errors return as `Temporal.Error.t`; this package never retries, continues, or
streams after an activity result.
