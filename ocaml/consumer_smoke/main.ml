(* This project intentionally lives outside the package directory.  It models
   the smallest downstream Dune consumer of the public library. *)
open Llm_temporal

let () =
  let request =
    Request.make
      ~operation_key:(Operation_key.of_string "consumer-smoke")
      ~model:(Model_selector.of_string "arbitrary-model")
      ~service_class:Standard
      ~input:[ Message { actor = Human; content = [ Text "hello" ] } ]
      ()
  in
  assert (Operation_key.to_string request.operation_key = "consumer-smoke");

  (* Compile-time coverage for the public typed query facade.  The fixture
     deliberately does not schedule Activities; it only proves that a
     downstream package can construct every closed query variant and retain
     its associated result type. *)
  let provider : provider_status_page Query.t =
    Query.Provider_status {
      provider = None; endpoint = None; availability = None;
      include_healthy = false; refresh_if_older_than_seconds = None;
      page_size = 20; cursor = None }
  in
  let model : model_inventory_page Query.t =
    Query.Model_inventory {
      provider = None; endpoint = None; model_prefix = None; lifecycle = None;
      refresh_if_older_than_seconds = None; page_size = 20; cursor = None }
  in
  let credit : credit_status_page Query.t =
    Query.Credit_status {
      provider = None; endpoint = None; include_ok = false;
      refresh_if_older_than_seconds = None; page_size = 20; cursor = None }
  in
  let budget : budget_status Query.t =
    Query.Budget_status { policy_key = None; active_at = None; include_windows = true }
  in
  let spend : spend_summary Query.t =
    Query.Spend_summary {
      start_time = Ptime.epoch; end_time = Ptime.epoch;
      group_by = [ By_operation_kind ]; operation_kinds = [ Generate ] }
  in
  ignore (provider, model, credit, budget, spend)
