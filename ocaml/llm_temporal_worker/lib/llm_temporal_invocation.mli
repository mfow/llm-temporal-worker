open Llm_temporal_models

val api_version : string
val activity_name : string
val workflow_name : string
val request_codec : request Temporal.Codec.t
val response_codec : response Temporal.Codec.t
val generate_activity : (request, response) Temporal.Activity.t

(** The fixed Temporal retry policy used by [execute].  It permits exactly one
    attempt; Temporal does not retry a failed provider activity. *)
val activity_retry_policy : Temporal.Activity.Retry_policy.t

val invoke_once :
  ?task_queue:Temporal_task_queue.t ->
  dispatch:(?task_queue:Temporal_task_queue.t -> (request, response) Temporal.Activity.t -> request -> (response, Temporal.Error.t) result) ->
  request ->
  (response, Temporal.Error.t) result

val execute : ?task_queue:Temporal_task_queue.t -> request -> (response, Temporal.Error.t) result
val workflow : ?task_queue:Temporal_task_queue.t -> unit -> (request, response) Temporal.Workflow.t
