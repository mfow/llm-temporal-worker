open Llm_temporal_models

type _ t =
  | Provider_status : provider_status_filter -> provider_status_page t
  | Model_inventory : model_inventory_filter -> model_inventory_page t
  | Credit_status : credit_status_filter -> credit_status_page t
  | Budget_status : budget_status_filter -> budget_status t
  | Spend_summary : spend_summary_filter -> spend_summary t

type 'a response = {
  value : 'a;
  query_execution_id : Query_execution_id.t;
  observed_at : Ptime.t;
  source : query_source;
  freshness : freshness;
  complete : bool;
  next_cursor : Query_cursor.t option;
  cost : settled_cost;
}

let query_request : type a. a t -> query_request = function
  | Provider_status filter -> Provider_status_request filter
  | Model_inventory filter -> Model_inventory_request filter
  | Credit_status filter -> Credit_status_request filter
  | Budget_status filter -> Budget_status_request filter
  | Spend_summary filter -> Spend_summary_request filter

let to_envelope ~operation_key ~context query =
  { api_version = Llm_temporal_v1_codec.query_api_version;
    operation_key;
    context;
    query = query_request query }

let mismatch expected actual =
  Temporal.Error.codec
    ~message:(Printf.sprintf "query result kind mismatch: expected %s, got %s" expected actual)

let result_kind = function
  | Provider_status_result _ -> "provider_status"
  | Model_inventory_result _ -> "model_inventory"
  | Credit_status_result _ -> "credit_status"
  | Budget_status_result _ -> "budget_status"
  | Spend_summary_result _ -> "spend_summary"

let response_metadata (response : query_response) value =
  { value;
    query_execution_id = response.query_execution_id;
    observed_at = response.observed_at;
    source = response.source;
    freshness = response.freshness;
    complete = response.complete;
    next_cursor = response.next_cursor;
    cost = response.cost }

let of_response : type a. a t -> query_response -> (a response, Temporal.Error.t) result =
  fun query response ->
    match query, response.result with
    | Provider_status _, Provider_status_result value -> Ok (response_metadata response value)
    | Model_inventory _, Model_inventory_result value -> Ok (response_metadata response value)
    | Credit_status _, Credit_status_result value -> Ok (response_metadata response value)
    | Budget_status _, Budget_status_result value -> Ok (response_metadata response value)
    | Spend_summary _, Spend_summary_result value -> Ok (response_metadata response value)
    | Provider_status _, result -> Error (mismatch "provider_status" (result_kind result))
    | Model_inventory _, result -> Error (mismatch "model_inventory" (result_kind result))
    | Credit_status _, result -> Error (mismatch "credit_status" (result_kind result))
    | Budget_status _, result -> Error (mismatch "budget_status" (result_kind result))
    | Spend_summary _, result -> Error (mismatch "spend_summary" (result_kind result))

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (query_envelope, query_response) Temporal.Activity.t ->
  query_envelope -> (query_response, Temporal.Error.t) result

let execute_with ?task_queue ~dispatch ~operation_key ~context query =
  let envelope = to_envelope ~operation_key ~context query in
  match Llm_temporal_invocation.invoke_query_once ?task_queue ~dispatch envelope with
  | Error error -> Error error
  | Ok response -> of_response query response

let activity_dispatch ?task_queue activity input =
  Temporal.Activity.execute
    ?task_queue:(Option.map Temporal_task_queue.to_string task_queue)
    ~retry_policy:Llm_temporal_invocation.activity_retry_policy
    activity input

let execute ?task_queue ~operation_key ~context query =
  execute_with ?task_queue ~dispatch:activity_dispatch ~operation_key ~context query

let start ?task_queue ~operation_key ~context query =
  let envelope = to_envelope ~operation_key ~context query in
  let future =
    Temporal.Activity.start
      ?task_queue:(Option.map Temporal_task_queue.to_string task_queue)
      ~retry_policy:Llm_temporal_invocation.activity_retry_policy
      Llm_temporal_invocation.query_v1_activity envelope
  in
  Temporal.Future.map
    (fun response ->
      match of_response query response with
      | Ok value -> value
      | Error error -> invalid_arg (Temporal.Error.message error))
    future
