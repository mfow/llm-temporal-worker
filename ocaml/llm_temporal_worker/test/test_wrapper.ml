open Llm_temporal

let expect_ok = function Ok value -> value | Error error -> failwith (Temporal.Error.message error)
let assert_equal expected actual = if expected <> actual then failwith (Printf.sprintf "expected %S, got %S" expected actual)

let request_value = {
  operation_key = "order-42";
  context = Some { tenant = Some "tenant"; project = Some "project"; actor = None; tags = [ ("region", "au") ] };
  model = "gpt-test";
  service_class = Priority;
  service_class_fallbacks = [ Standard ];
  portability = Strict;
  instructions = [ Text_instruction { level = Application; text = "Be concise." } ];
  input = [ Message { actor = Human; content = [ Text "Hello"; Image { media_type = "image/png"; source = Url "https://example.invalid/image.png"; detail = Some "high" } ] } ];
  tools = [];
  tool_policy = { choice = Auto; parallel = false };
  output = Some { max_tokens = Some 32; format = Text_format };
  sampling = None;
  reasoning = None;
  continuation = None;
  extensions = [ ("test.extension", `Assoc [ ("enabled", `Bool true) ]) ];
}

let service_value = { requested = Priority; attempted = Priority; actual = Some Priority; provider_value = Some "priority"; fallback_index = 0 }
let usage_value = { input_tokens = 2L; output_tokens = 1L; reasoning_tokens = 0L; cache_read_tokens = 0L; cache_write_tokens = 0L; provider_raw = [] }

let response_value = {
  operation_key = "order-42";
  operation_id = Some "operation-42";
  status = Completed;
  output = [ Message { actor = Model; content = [ Refusal { message = "No."; provider_code = Some "policy" } ] } ];
  route = { route_id = Some "route-1"; endpoint_id = Some "openai"; api_family = Some "responses"; requested_model = Some "gpt-test"; resolved_model = Some "gpt-test" };
  service = service_value;
  usage = usage_value;
  cost = { currency = "USD"; reserved_microusd = 10L; actual_microusd = 8L; method_ = "catalog"; catalog_version = "v1" };
  provider = { response_id = Some "resp-1"; request_id = Some "req-1"; generation_id = None; finish_reason = Some "stop"; raw = [ ("provider_field", `String "value") ] };
  continuation = None;
  diagnostics = [ { code = "translated"; message = "typed"; severity = Info; path = Some "request"; details = Some [ ("source", "test") ] } ];
}

let () =
  assert_equal "llm.generate.v1" (Temporal.Activity.name generate_activity);
  assert_equal "llm.generate.workflow.v1" (Temporal.Workflow.name (workflow ()));
  if Temporal.Activity.implementation generate_activity <> None then failwith "remote Go activity has an OCaml implementation";
  let request_payload = expect_ok (Temporal.Codec.encode request_codec request_value) in
  assert_equal "json/plain" (List.assoc "encoding" request_payload.metadata);
  let request_envelope = Yojson.Safe.from_string (Bytes.to_string request_payload.data) in
  let request_fields = match request_envelope with `Assoc fields -> fields | _ -> failwith "request envelope is not an object" in
  assert_equal api_version (match List.assoc "api_version" request_fields with `String value -> value | _ -> failwith "request api_version");
  let encoded_request = match List.assoc "request" request_fields with `Assoc fields -> fields | _ -> failwith "request field" in
  assert_equal "priority" (match List.assoc "service_class" encoded_request with `String value -> value | _ -> failwith "service_class");
  assert_equal "strict" (match List.assoc "portability" encoded_request with `String value -> value | _ -> failwith "portability");
  ignore (List.assoc "service_class_fallbacks" encoded_request);
  ignore (List.assoc "tool_policy" encoded_request);
  let encoded_input = match List.assoc "input" encoded_request with `List [ `Assoc message ] -> message | _ -> failwith "input message" in
  let encoded_content = match List.assoc "content" encoded_input with `List [ _; `Assoc image ] -> image | _ -> failwith "image content" in
  assert_equal "https://example.invalid/image.png" (match List.assoc "url" encoded_content with `String value -> value | _ -> failwith "flat image url");
  assert_equal "high" (match List.assoc "detail" encoded_content with `String value -> value | _ -> failwith "image detail");
  if List.mem_assoc "source" encoded_content then failwith "media source must be flat";
  let decoded_request = expect_ok (Temporal.Codec.decode request_codec request_payload) in
  assert_equal "order-42" decoded_request.operation_key;
  assert_equal "priority" (match decoded_request.service_class with Economy -> "economy" | Standard -> "standard" | Priority -> "priority");
  let response_payload = expect_ok (Temporal.Codec.encode response_codec response_value) in
  let response_envelope = Yojson.Safe.from_string (Bytes.to_string response_payload.data) in
  let response_fields = match response_envelope with `Assoc fields -> fields | _ -> failwith "response envelope is not an object" in
  assert_equal api_version (match List.assoc "api_version" response_fields with `String value -> value | _ -> failwith "response api_version");
  ignore (List.assoc "response" response_fields);
  ignore (List.assoc "metadata" response_fields);
  let encoded_response = match List.assoc "response" response_fields with `Assoc fields -> fields | _ -> failwith "response field" in
  let output_item = match List.assoc "output" encoded_response with `List [ `Assoc item ] -> item | _ -> failwith "response item" in
  let refusal = match List.assoc "content" output_item with `List [ `Assoc item ] -> item | _ -> failwith "refusal content" in
  assert_equal "No." (match List.assoc "text" refusal with `String value -> value | _ -> failwith "refusal text");
  let decoded_response = expect_ok (Temporal.Codec.decode response_codec response_payload) in
  assert_equal "operation-42" (Option.get decoded_response.operation_id);
  let calls = ref 0 in
  let dispatch ?task_queue activity request =
    incr calls;
    assert_equal "go-activities" (Option.get task_queue);
    assert_equal activity_name (Temporal.Activity.name activity);
    assert_equal "order-42" request.operation_key;
    Ok response_value
  in
  ignore (expect_ok (invoke_once ~task_queue:"go-activities" ~dispatch request_value));
  if !calls <> 1 then failwith "wrapper dispatched more than once";
  print_endline "llm_temporal typed wrapper tests passed"
