(** Typed query Activities.

    The GADT associates each wire filter with exactly one result page.  This
    keeps a provider-status result from being accidentally consumed as a
    budget or spend result while retaining the closed wire representation in
    {!Llm_temporal_models}.
*)

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

val to_envelope :
  operation_key:Operation_key.t ->
  context:request_context ->
  'a t -> query_envelope

val of_response :
  'a t -> query_response -> ('a response, Temporal.Error.t) result

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (query_envelope, query_response) Temporal.Activity.t ->
  query_envelope -> (query_response, Temporal.Error.t) result

val execute_with :
  ?task_queue:Temporal_task_queue.t ->
  dispatch:dispatcher ->
  operation_key:Operation_key.t ->
  context:request_context ->
  'a t -> ('a response, Temporal.Error.t) result

val execute :
  ?task_queue:Temporal_task_queue.t ->
  operation_key:Operation_key.t ->
  context:request_context ->
  'a t -> ('a response, Temporal.Error.t) result

val start :
  ?task_queue:Temporal_task_queue.t ->
  operation_key:Operation_key.t ->
  context:request_context ->
  'a t ->
  (('a response, Temporal.Error.t) result, Temporal.Error.t) Temporal.Future.t
