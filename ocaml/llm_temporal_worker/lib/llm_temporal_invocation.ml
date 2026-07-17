open Llm_temporal_models

let api_version = Llm_temporal_codec.api_version
let activity_name = "llm.generate.v1"
let workflow_name = "llm.generate.workflow.v1"

let request_codec =
  Temporal.Codec.make ~encoding:"json/plain"
    ~encode:Llm_temporal_codec.encode_request
    ~decode:Llm_temporal_codec.decode_request

let response_codec =
  Temporal.Codec.make ~encoding:"json/plain"
    ~encode:Llm_temporal_codec.encode_response
    ~decode:Llm_temporal_codec.decode_response

let generate_activity =
  Temporal.Activity.remote ~name:activity_name ~input:request_codec ~output:response_codec

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (request, response) Temporal.Activity.t ->
  request ->
  (response, Temporal.Error.t) result

let invoke_once ?task_queue ~(dispatch : dispatcher) input =
  dispatch ?task_queue generate_activity input

let one_shot_retry_policy =
  match Temporal.Activity.Retry_policy.make ~initial_interval:(Temporal.Duration.of_ms 1L)
          ~backoff_coefficient:1.0 ~maximum_interval:(Temporal.Duration.of_ms 1L)
          ~maximum_attempts:1 () with
  | Ok policy -> policy
  | Error error -> invalid_arg (Temporal.Error.message error)

let activity_dispatch ?task_queue activity input =
  Temporal.Activity.execute
    ?task_queue:(Option.map Temporal_task_queue.to_string task_queue)
    ~retry_policy:one_shot_retry_policy activity input

let execute ?task_queue input = invoke_once ?task_queue ~dispatch:activity_dispatch input
let workflow ?task_queue () =
  Temporal.Workflow.define ~name:workflow_name ~input:request_codec ~output:response_codec
    (fun input -> execute ?task_queue input)
