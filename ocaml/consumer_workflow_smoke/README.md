# Downstream workflow-sample compile fixture

This is an external Dune project, separate from the package sources. It imports
only the installed `llm-temporal-ocaml` package and type-checks the
architecture's one-shot, non-streaming workflow shape:

- all five typed `Query` constructors;
- a cached immutable root and three sibling `Conversation.start_respond`
  futures composed with `Temporal.Future.all`;
- explicit `Conversation.compact`; and
- a post-compaction `Conversation.respond` that restores application settings.

The executable is compile-only and does not contact Temporal or an LLM
provider. Run it with:

```sh
opam exec -- dune build --root ocaml/consumer_workflow_smoke
```
