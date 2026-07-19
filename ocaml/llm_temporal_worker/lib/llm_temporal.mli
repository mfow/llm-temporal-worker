(** Typed, one-shot bindings for the Go [llm.generate.v1] Temporal Activity.

    Identifier modules intentionally wrap arbitrary strings nominally.  They
    preserve the wire protocol while preventing unrelated IDs from being
    accidentally interchanged in OCaml code. *)

include module type of Llm_temporal_models
include module type of Llm_temporal_invocation

module Conversation : module type of Llm_temporal_conversation
