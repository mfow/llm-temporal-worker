(** Immutable, typed convenience bindings for a sequence of Generate calls.

    A conversation is only local workflow state.  It contains the context,
    model, effective request settings, and the latest continuation returned by
    the Go Activity; it never owns a client, mutable head, or provider state.
    The Go Activity remains authoritative for inherited settings and validation.
*)

open Llm_temporal_models

module Settings : sig
  type t

  val make :
    ?service_class:service_class ->
    ?service_class_fallbacks:service_class list ->
    ?portability:portability ->
    ?instructions:instruction list ->
    ?tools:function_tool list ->
    ?tool_policy:tool_policy ->
    ?output:output_spec ->
    ?sampling:sampling ->
    ?reasoning:reasoning ->
    ?extensions:(string * Yojson.Safe.t) list ->
    unit -> t

  val default : t
end

type t

type turn = {
  response : response;
  conversation : t;
}

val root :
  context:request_context ->
  model:Model_selector.t ->
  ?service_class:service_class ->
  ?settings:Settings.t ->
  unit -> t

val of_continuation :
  context:request_context ->
  model:Model_selector.t ->
  ?settings:Settings.t ->
  continuation:continuation ->
  unit -> t

val context : t -> request_context
val model : t -> Model_selector.t
val continuation : t -> continuation option
val fork : t -> t

(** Build the exact low-level Generate request without scheduling an Activity. *)
val to_request :
  operation_key:Operation_key.t ->
  append:item list ->
  t -> request

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (request, response) Temporal.Activity.t ->
  request ->
  (response, Temporal.Error.t) result

val respond_with :
  ?task_queue:Temporal_task_queue.t ->
  dispatch:dispatcher ->
  operation_key:Operation_key.t ->
  append:item list ->
  t -> (turn, Temporal.Error.t) result

val respond :
  ?task_queue:Temporal_task_queue.t ->
  operation_key:Operation_key.t ->
  append:item list ->
  t -> (turn, Temporal.Error.t) result

val start_respond :
  ?task_queue:Temporal_task_queue.t ->
  operation_key:Operation_key.t ->
  append:item list ->
  t -> (turn, Temporal.Error.t) Temporal.Future.t
