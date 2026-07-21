open Llm_temporal

let failf format = Printf.ksprintf failwith format
let ok = function Ok value -> value | Error error -> failf "%s" (Temporal.Error.message error)
let cursor value = match Query_cursor.of_string value with Ok value -> value | Error message -> failwith message
let tagged_cursor kind value =
  match Query_cursor.of_string_for_kind kind value with
  | Ok value -> value
  | Error message -> failwith message
let stream_id value = match Budget_stream_id.of_string value with Ok value -> value | Error message -> failwith message
let digest value = match Sha256_digest.of_hex value with Ok value -> value | Error message -> failwith message
let time value =
  match Ptime.of_rfc3339 value with
  | Ok (value, _, _) -> value
  | Error _ -> failwith "invalid test timestamp"

let context = { tenant = None; project = None; actor = None; tags = [] }
let operation_key = Operation_key.of_string "query-test"

let provider_filter ?cursor () =
  { provider = None; endpoint = None; availability = None; include_healthy = true;
    refresh_if_older_than_seconds = None; page_size = 20; cursor }

let model_filter () =
  { provider = None; endpoint = None; model_prefix = None; lifecycle = None;
    refresh_if_older_than_seconds = None; page_size = 20; cursor = None }

let credit_filter () =
  { provider = None; endpoint = None; include_ok = false;
    refresh_if_older_than_seconds = None; page_size = 20; cursor = None }

let budget_filter () = { policy_key = None; active_at = None; include_windows = true }

let spend_filter () =
  { start_time = time "2026-01-01T00:00:00Z";
    end_time = time "2026-01-02T00:00:00Z";
    group_by = [ By_operation_kind ]; operation_kinds = [ Generate ] }

let response result =
  { api_version = V1_codec.query_api_version;
    operation_key;
    query_execution_id = Query_execution_id.of_string "execution-1";
    observed_at = time "2026-01-01T00:00:00Z";
    source = Persisted; freshness = Current; complete = true; next_cursor = None;
    result;
    cost = Exact_cost { actual_cost_usd = Usd_decimal.zero;
                        method_ = Control_query_zero; catalog_version = None } }

let response_for = function
  | Provider_status_request _ -> response (Provider_status_result { routes = [] })
  | Model_inventory_request _ -> response (Model_inventory_result { models = [] })
  | Credit_status_request _ -> response (Credit_status_result { endpoints = [] })
  | Budget_status_request _ ->
      response (Budget_status_result {
        active_at = time "2026-01-01T00:00:00Z";
        generation_id = Budget_generation_id.of_string "generation-1";
        manifest_digest = digest (String.make 64 'a');
        stream_high_water_mark = stream_id "1-0";
        windows = [] })
  | Spend_summary_request filter ->
      response (Spend_summary_result {
        start_time = filter.start_time; end_time = filter.end_time; buckets = [] })

let dispatch ?task_queue:_ activity envelope =
  if Temporal.Activity.name activity <> "llm.query.v1" then
    failwith "Query used the wrong Activity descriptor";
  Ok (response_for envelope.query)

let run query = ok (Query.execute_with ~dispatch ~operation_key ~context query)

let () =
  (match Budget_stream_id.of_string "not-a-stream-id" with
   | Error _ -> ()
   | Ok _ -> failwith "invalid budget stream id was accepted");
  let provider : provider_status_page Query.t = Query.Provider_status (provider_filter ()) in
  let model : model_inventory_page Query.t = Query.Model_inventory (model_filter ()) in
  let credit : credit_status_page Query.t = Query.Credit_status (credit_filter ()) in
  let budget : budget_status Query.t = Query.Budget_status (budget_filter ()) in
  let spend : spend_summary Query.t = Query.Spend_summary (spend_filter ()) in
  if (run provider).value.routes <> [] then failwith "provider result changed";
  if (run model).value.models <> [] then failwith "model result changed";
  if (run credit).value.endpoints <> [] then failwith "credit result changed";
  ignore (run budget);
  ignore (run spend);

  let cursor = cursor "provider:page-2" in
  let paged = Query.Provider_status (provider_filter ~cursor ()) in
  let envelope = Query.to_envelope ~operation_key ~context paged in
  (match envelope.query with
   | Provider_status_request { cursor = Some value; _ } when value = cursor -> ()
   | _ -> failwith "query cursor was not retained");

  let provider_cursor = tagged_cursor Query_cursor.Provider_status "provider:page-3" in
  let wrong_kind =
    Query.Model_inventory
      { (model_filter ()) with cursor = Some provider_cursor }
  in
  let dispatch_called = ref false in
  let should_not_dispatch ?task_queue:_ _activity _envelope =
    dispatch_called := true;
    failwith "dispatch should not be called for a mismatched cursor"
  in
  (match Query.execute_with ~dispatch:should_not_dispatch ~operation_key ~context wrong_kind with
   | Error error when String.equal (Temporal.Error.message error)
                          "query cursor kind mismatch: expected model_inventory, got provider_status" -> ()
   | Error error -> failf "unexpected cursor mismatch error: %s" (Temporal.Error.message error)
   | Ok _ -> failwith "mismatched query cursor was accepted");
  if !dispatch_called then failwith "mismatched cursor was dispatched";

  let encoded =
    ok (V1_codec.encode_query_response
          { (response (Provider_status_result { routes = [] })) with
            next_cursor = Some provider_cursor })
  in
  (match V1_codec.decode_query_response encoded with
   | Ok { next_cursor = Some value; _ }
     when Query_cursor.kind value = Some Query_cursor.Provider_status -> ()
   | Ok _ -> failwith "decoded response cursor lost its query kind"
   | Error error -> failf "response cursor failed to round-trip: %s" (Temporal.Error.message error));

  let reject_non_paginated_cursor kind result =
    let candidate =
      { (response result) with
        next_cursor = Some (tagged_cursor kind "snapshot-page-2") }
    in
    match V1_codec.encode_query_response candidate with
    | Error _ -> ()
    | Ok _ -> failwith "non-paginated query response encoded a cursor"
  in
  reject_non_paginated_cursor Query_cursor.Budget_status
    (Budget_status_result {
       active_at = time "2026-01-01T00:00:00Z";
       generation_id = Budget_generation_id.of_string "generation-1";
       manifest_digest = digest (String.make 64 'a');
       stream_high_water_mark = stream_id "1-0";
       windows = [] });
  reject_non_paginated_cursor Query_cursor.Spend_summary
    (Spend_summary_result {
       start_time = time "2026-01-01T00:00:00Z";
       end_time = time "2026-01-02T00:00:00Z";
       buckets = [] });

  let mismatched =
    { (response (Provider_status_result { routes = [] })) with
      operation_key = Operation_key.of_string "mismatch" }
  in
  (match Query.of_response budget mismatched with
   | Error error when String.equal (Temporal.Error.message error)
                          "query result kind mismatch: expected budget_status, got provider_status" -> ()
   | Error error -> failf "unexpected mismatch error: %s" (Temporal.Error.message error)
   | Ok _ -> failwith "mismatched query result was accepted");

  let activity_error = Temporal.Error.make ~category:`Activity ~message:"query failed" () in
  let failing_dispatch ?task_queue:_ _activity _envelope = Error activity_error in
  (match Query.execute_with ~dispatch:failing_dispatch ~operation_key ~context provider with
   | Error error when String.equal (Temporal.Error.message error) "query failed" -> ()
   | Error error -> failf "unexpected Activity error: %s" (Temporal.Error.message error)
   | Ok _ -> failwith "Activity error was swallowed");

  let unknown = Bytes.of_string
    {|{"api_version":"llm.temporal/query/v1","operation_key":"query-1","query_execution_id":"execution-1","kind":"future_kind","observed_at":"2026-01-01T00:00:00Z","source":"persisted","freshness":"current","complete":true,"next_cursor":null,"result":{},"cost_status":"unknown","actual_cost_usd":null,"cost_unknown_reason_code":"state_unavailable"}|}
  in
  (match V1_codec.decode_query_response unknown with
   | Error _ -> ()
   | Ok _ -> failwith "unknown query result tag was accepted");
  print_endline "typed query tests passed"
