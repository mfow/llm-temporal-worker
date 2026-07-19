(** Cohesive public facade for typed payload models and the one-shot activity. *)

include Llm_temporal_models
include Llm_temporal_invocation

module Conversation = Llm_temporal_conversation
module V1_codec = Llm_temporal_v1_codec
