open Llm_temporal

let failf format = Printf.ksprintf failwith format
let ok = function Ok value -> value | Error error -> failf "codec error: %s" (Temporal.Error.message error)
let error = function Ok _ -> failwith "expected codec error" | Error _ -> ()
let time value = match Ptime.of_rfc3339 value with Ok (value, _, _) -> value | Error _ -> failwith "invalid test time"

let () =
  let () = match Usd_decimal.of_string "000.0100" with Ok _ -> failwith "leading zero accepted" | Error _ -> () in
  let decimal = match Usd_decimal.of_string "1.2300" with Ok value -> value | Error message -> failwith message in
  if Usd_decimal.to_string decimal <> "1.23" then failwith "decimal was not canonicalized";
  if Usd_decimal.of_string "1.0000000000000000001" |> Result.is_ok then failwith "19 fractional digits accepted";
  let context = { tenant = Some (Tenant_id.of_string "tenant"); project = Some (Project_id.of_string "project"); actor = Some (Actor_id.of_string "actor"); tags = [] } in
  let keep_patch = { model = Keep; service_class = Keep; service_class_fallbacks = Keep; portability = Keep; instructions = Keep; tools = Keep; tool_policy = Keep; output = Keep; temperature = Keep; reasoning_effort = Keep; reasoning_summary = Keep; compaction_policy = Keep; extensions = Keep } in
  let request = {
    api_version = V1_codec.generate_api_version; operation_key = Operation_key.of_string "op-1"; context;
    parent = Some (Checkpoint.of_string "cp-0"); append = [Message { actor = Human; content = [Text "hello"] }];
    settings_patch = keep_patch; cache = Some { max_age_seconds = 60L; variant = 0l };
  } in
  let request' = ok (V1_codec.decode_generate_request (ok (V1_codec.encode_generate_request request))) in
  if request'.operation_key <> request.operation_key || request'.append <> request.append then failwith "generate round trip";
  let compact = { api_version = V1_codec.compact_api_version; operation_key = Operation_key.of_string "compact-1"; context; parent = Checkpoint.of_string "cp-1"; policy = Some { target_tokens = Some 100L; summary_style = Some Concise }; cache = None } in
  ignore (ok (V1_codec.decode_compact_request (ok (V1_codec.encode_compact_request compact))));
  let query = Provider_status_request { provider = Some (Provider_id.of_string "openai"); endpoint = None; availability = None; include_healthy = true; refresh_if_older_than_seconds = None; page_size = 20; cursor = None } in
  let envelope = { api_version = V1_codec.query_api_version; operation_key = Operation_key.of_string "query-1"; context; query } in
  let envelope' = ok (V1_codec.decode_query_envelope (ok (V1_codec.encode_query_envelope envelope))) in
  if envelope'.operation_key <> envelope.operation_key then failwith "query envelope round trip";
  let query_response = {
    api_version = V1_codec.query_api_version; operation_key = Operation_key.of_string "query-1"; query_execution_id = Query_execution_id.of_string "qx-1";
    observed_at = time "2026-01-01T00:00:00Z"; source = Persisted; freshness = Current; complete = true; next_cursor = None;
    result = Provider_status_result { routes = [] }; cost = Exact_cost { actual_cost_usd = Usd_decimal.zero; method_ = Control_query_zero; catalog_version = None };
  } in
  ignore (ok (V1_codec.decode_query_response (ok (V1_codec.encode_query_response query_response))));
  error (V1_codec.decode_generate_request (Bytes.of_string "{\"api_version\":\"llm.temporal/v1\",\"operation_key\":\"x\",\"context\":{\"tenant\":\"t\",\"project\":\"p\",\"actor\":\"a\"},\"parent\":null,\"append\":[],\"settings_patch\":{},\"cache\":null,\"extra\":true}"));
  print_endline "v1 protocol tests passed"
