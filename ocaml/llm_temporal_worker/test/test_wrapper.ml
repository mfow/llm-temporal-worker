open Llm_temporal

let expect_ok = function Ok value -> value | Error error -> failwith (Temporal.Error.message error)
let expect_error = function Ok _ -> failwith "expected codec error" | Error _ -> ()
let assert_equal expected actual = if expected <> actual then failwith (Printf.sprintf "expected %S, got %S" expected actual)
let replace_field name value fields =
  List.map (fun (field, current) -> if field = name then (field, value) else (field, current)) fields

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
  continuation =
    Some {
      handle = "continuation-42";
      endpoint_id = None;
      model = None;
      expires_at = None;
      pinned = true;
      provider_state =
        Some [{
          provider = "openai";
          endpoint_family = "responses";
          media_type = "application/json";
          opaque = "c3RhdGU=";
        }];
    };
  extensions = [ ("test.extension", `Assoc [ ("enabled", `Bool true) ]) ];
}

let service_value = { requested = Priority; attempted = Priority; actual = Some Priority; provider_value = Some "priority"; fallback_index = 0 }
let usage_value = {
  input_tokens = 2L; output_tokens = 1L; reasoning_tokens = 0L;
  cache_read_tokens = 0L; cache_write_tokens = 0L;
  provider_raw = Some [ ("provider_usage", `Int 3) ];
}

let response_value = {
  operation_key = "order-42";
  operation_id = Some "operation-42";
  status = Completed;
  output = [ Message { actor = Model; content = [ Refusal { message = "No."; provider_code = Some "policy" } ] } ];
  route = { route_id = Some "route-1"; endpoint_id = Some "openai"; api_family = Some "responses"; requested_model = Some "gpt-test"; resolved_model = Some "gpt-test" };
  service = service_value;
  usage = usage_value;
  cost = {
    status = Some Cost_known;
    currency = "USD"; reserved_microusd = 10L; actual_microusd = 8L;
    method_ = "catalog"; catalog_version = "v1";
  };
  provider = { response_id = Some "resp-1"; request_id = Some "req-1"; generation_id = None; finish_reason = Some "stop"; raw = [ ("provider_field", `String "value") ] };
  continuation = None;
  diagnostics = [ { code = "translated"; message = "typed"; severity = Info; path = Some "request"; details = Some [ ("source", "test") ] } ];
  metadata = { operation_id = Some "operation-42" };
}

let () =
  assert_equal "llm.generate.v1" (Temporal.Activity.name generate_activity);
  assert_equal "llm.generate.workflow.v1" (Temporal.Workflow.name (workflow ()));
  if Temporal.Activity.implementation generate_activity <> None then failwith "remote Go activity has an OCaml implementation";
  let valid_tool = {
    kind = Function; name = "lookup"; description = "lookup";
    input_schema = `Assoc []; output_schema = None;
  } in
  expect_error
    (Temporal.Codec.encode request_codec
       { request_value with tools = [{ valid_tool with input_schema = `String "invalid" }] });
  expect_error
    (Temporal.Codec.encode request_codec
       { request_value with tools = [{ valid_tool with output_schema = Some (`List []) }] });
  expect_error
    (Temporal.Codec.encode request_codec
       { request_value with
         output = Some { max_tokens = None;
                         format = Json_schema_format {
                           name = "result"; description = None; schema = `Bool true;
                           strict = true; } } });
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
  let request_payload_with request =
    { request_payload with
      data = Bytes.of_string (Yojson.Safe.to_string (`Assoc [
        ("api_version", `String api_version); ("request", `Assoc request);
      ])) }
  in
  let duplicate_outer =
    Printf.sprintf
      "{\"api_version\":\"%s\",\"api_version\":\"%s\",\"request\":%s}"
      api_version api_version (Yojson.Safe.to_string (`Assoc encoded_request))
  in
  expect_error
    (Temporal.Codec.decode request_codec
       { request_payload with data = Bytes.of_string duplicate_outer });
  expect_error
    (Temporal.Codec.decode request_codec
       { request_payload with
         data = Bytes.of_string (Yojson.Safe.to_string (`Assoc [
           ("api_version", `String api_version); ("request", `Assoc encoded_request);
           ("unexpected", `Bool true);
         ])) });
  let unknown_part_input =
    match List.assoc "input" encoded_request with
    | `List [ `Assoc message ] ->
        (match List.assoc "content" message with
         | `List (`Assoc text :: rest) ->
             `List [ `Assoc (replace_field "content"
               (`List (`Assoc (("unexpected", `Bool true) :: text) :: rest)) message) ]
         | _ -> failwith "request text content")
    | _ -> failwith "request input"
  in
  expect_error
    (Temporal.Codec.decode request_codec
       (request_payload_with (replace_field "input" unknown_part_input encoded_request)));
  let unknown_blob_input =
    `List [ `Assoc [
      ("kind", `String "message"); ("actor", `String "human");
      ("content", `List [ `Assoc [
        ("kind", `String "image"); ("media_type", `String "image/png");
        ("blob", `Assoc [ ("locator", `String "blob://test");
                            ("digest", `String "sha256:test");
                            ("byte_length", `Int 1); ("media_type", `String "image/png");
                            ("unexpected", `Bool true) ]);
      ] ]);
    ] ]
  in
  expect_error
    (Temporal.Codec.decode request_codec
       (request_payload_with (replace_field "input" unknown_blob_input encoded_request)));
  let request_without_continuation = { request_value with continuation = None } in
  let without_continuation_payload =
    expect_ok (Temporal.Codec.encode request_codec request_without_continuation)
  in
  let without_continuation_envelope =
    Yojson.Safe.from_string (Bytes.to_string without_continuation_payload.data)
  in
  let without_continuation_request =
    match without_continuation_envelope with
    | `Assoc fields -> (match List.assoc "request" fields with `Assoc request -> request | _ -> failwith "request envelope")
    | _ -> failwith "request envelope"
  in
  (match List.assoc "continuation" without_continuation_request with
   | `Null -> () | _ -> failwith "continuation must be encoded as explicit null");
  (match (expect_ok (Temporal.Codec.decode request_codec without_continuation_payload)).continuation with
   | None -> () | Some _ -> failwith "explicit null continuation did not decode");
  let tool_without_kind =
    `Assoc [ ("name", `String "lookup"); ("description", `String "lookup");
             ("input_schema", `Assoc []) ]
  in
  let request_with_default_tool =
    request_payload_with
      (replace_field "tools" (`List [ tool_without_kind ]) encoded_request)
  in
  (match (expect_ok (Temporal.Codec.decode request_codec request_with_default_tool)).tools with
   | [ { kind = Function; _ } ] -> ()
   | _ -> failwith "omitted tool kind did not default to function");
  let invalid_tool field value =
    let fields = [
      ("kind", `String "function"); ("name", `String "lookup");
      ("description", `String "lookup"); ("input_schema", `Assoc []);
    ] in
    `Assoc
      (if field = "output_schema" then (field, value) :: fields
       else replace_field field value fields)
  in
  let request_with_tool tool =
    request_payload_with (replace_field "tools" (`List [ tool ]) encoded_request)
  in
  expect_error
    (Temporal.Codec.decode request_codec
       (request_with_tool (invalid_tool "input_schema" (`String "invalid"))));
  expect_error
    (Temporal.Codec.decode request_codec
       (request_with_tool
          (invalid_tool "output_schema" (`List []))));
  let invalid_output =
    `Assoc [
      ("format", `Assoc [ ("kind", `String "json_schema");
                            ("name", `String "result");
                            ("schema", `String "invalid"); ("strict", `Bool true) ]);
    ]
  in
  expect_error
    (Temporal.Codec.decode request_codec
       (request_payload_with (replace_field "output" invalid_output encoded_request)));
  let decoded_request = expect_ok (Temporal.Codec.decode request_codec request_payload) in
  assert_equal "order-42" decoded_request.operation_key;
  assert_equal "priority" (match decoded_request.service_class with Economy -> "economy" | Standard -> "standard" | Priority -> "priority");
  (match decoded_request.continuation with
   | Some { endpoint_id = None; model = None; provider_state = Some [ _ ]; _ } -> ()
   | _ -> failwith "request continuation did not preserve typed optional fields");
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
  assert_equal "operation-42" (Option.get decoded_response.metadata.operation_id);
  let derived_metadata_payload =
    expect_ok
      (Temporal.Codec.encode response_codec
         { response_value with metadata = { operation_id = None } })
  in
  assert_equal "operation-42"
    (Option.get
       (expect_ok (Temporal.Codec.decode response_codec derived_metadata_payload)).metadata.operation_id);
  expect_error
    (Temporal.Codec.encode response_codec
       { response_value with metadata = { operation_id = Some "different-operation" } });
  let mismatched_metadata_payload =
    { response_payload with
      data = Bytes.of_string (Yojson.Safe.to_string (`Assoc [
        ("api_version", `String api_version);
        ("response", `Assoc encoded_response);
        ("metadata", `Assoc [ ("operation_id", `String "different-operation") ]);
      ])) }
  in
  expect_error (Temporal.Codec.decode response_codec mismatched_metadata_payload);
  let response_without_optional_fields = {
    response_value with
    usage = { usage_value with provider_raw = None };
    cost = { response_value.cost with status = None };
    continuation = Some {
      handle = "continuation-response-42";
      endpoint_id = None;
      model = None;
      expires_at = None;
      pinned = false;
      provider_state = None;
    };
  } in
  let response_without_optional_payload =
    expect_ok (Temporal.Codec.encode response_codec response_without_optional_fields)
  in
  let decoded_without_optional =
    expect_ok (Temporal.Codec.decode response_codec response_without_optional_payload)
  in
  (match decoded_without_optional.usage.provider_raw with
   | None -> ()
   | Some _ -> failwith "optional provider_raw should remain absent");
  (match decoded_without_optional.cost.status with
   | None -> ()
   | Some _ -> failwith "optional cost_status should remain absent");
  (match decoded_without_optional.continuation with
   | Some { handle = "continuation-response-42"; endpoint_id = None; model = None;
            provider_state = None; pinned = false; _ } -> ()
   | _ -> failwith "response continuation optional fields did not decode");
  let unknown_cost_response = {
    response_value with cost = { response_value.cost with status = Some Cost_unknown }
  } in
  let unknown_cost_payload =
    expect_ok (Temporal.Codec.encode response_codec unknown_cost_response)
  in
  (match (expect_ok (Temporal.Codec.decode response_codec unknown_cost_payload)).cost.status with
   | Some Cost_unknown -> ()
   | _ -> failwith "unknown cost_status did not round trip");
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
