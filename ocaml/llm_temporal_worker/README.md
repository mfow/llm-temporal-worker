# `llm-temporal-ocaml`

This small package schedules the Go worker's exact `llm.generate.v1` Activity
from an OCaml Temporal workflow. It is deliberately a one-shot boundary:
there is no provider client, credential handling, token streaming,
continuation loop, or application retry loop in this package.

The Go worker remains the authority for the complete provider-neutral
`llm.Request` and `llm.Response` schema. This wrapper validates the stable
`llm.temporal/v1` Activity envelope, `json/plain` payload encoding, duplicate
JSON keys, canonical object/version fields, and the canonical request/response
identity fields. It preserves the rest of the canonical JSON exactly instead
of maintaining a second, drifting copy of the Go model.

## Install

Pin this package at the commit you intend to deploy (replace `<commit>`):

```sh
opam pin add --yes --kind=git llm-temporal-ocaml \
  'https://github.com/mfow/llm-temporal-worker.git#<commit>' \
  --subpath=ocaml/llm_temporal_worker
opam install --yes llm-temporal-ocaml
```

The package metadata pins `temporal-sdk` to
`52ddf8625fca25839250881bf37725368af8dc00`, so the consuming switch resolves
the tested SDK revision. For a deployed application, commit the generated
opam lock file after `opam lock .` and install with `opam install . --locked`.

Add the library to your Dune stanza:

```lisp
(libraries llm-temporal-ocaml)
```

## Use

Create canonical JSON with the Go contract's required inner API version,
operation key, and model. The Go worker will perform the full canonical schema
validation before calling a provider.

```ocaml
let request =
  Llm_temporal.Json.of_string
    {|{"api_version":"llm.temporal/v1","operation_key":"invoice-42",|}
    ^ {|"model":"gpt-5","input":[]}|}
  |> Result.bind Llm_temporal.request

let definition = Llm_temporal.workflow ~task_queue:"go-activities" ()
(* Register [definition] on the OCaml workflow worker. *)
```

`workflow` performs one `Temporal.Activity.execute` call against
`llm.generate.v1` and sets a Temporal retry policy with `maximum_attempts = 1`.
The Go Activity must be served on the supplied queue (or on the workflow
worker's default queue when the argument is omitted). Operational errors are
returned as `Temporal.Error.t`; the wrapper does not retry, continue, or
stream after an error.

For testing a workflow-level adapter without a Temporal service, use
`invoke_once` with a deterministic dispatcher. Production code should call
`execute` or register the `workflow` definition.
