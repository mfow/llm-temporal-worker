(** Ergonomic one-shot helpers for the non-streaming [llm.generate.v1]
    Temporal Activity.  This module is additive: the legacy [Request] API
    remains available for compatibility, while new code can use the exact v1
    request/response types. *)

open Llm_temporal_models

type request = generate_request
type response = generate_response

module Settings : module type of Llm_temporal_conversation.Settings
module Cache_policy : module type of Llm_temporal_conversation.Cache_policy

val make :
  operation_key:Operation_key.t ->
  context:request_context ->
  model:Model_selector.t ->
  ?settings:Settings.t ->
  ?cache:Cache_policy.t ->
  input:item list ->
  unit -> request

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (request, response) Temporal.Activity.t ->
  request -> (response, Temporal.Error.t) result

val invoke_with :
  ?task_queue:Temporal_task_queue.t ->
  dispatch:dispatcher ->
  request -> (response, Temporal.Error.t) result

val invoke :
  ?task_queue:Temporal_task_queue.t ->
  request -> (response, Temporal.Error.t) result

val start :
  ?task_queue:Temporal_task_queue.t ->
  request -> (response, Temporal.Error.t) Temporal.Future.t
