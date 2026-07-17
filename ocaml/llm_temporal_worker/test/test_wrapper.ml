open Llm_temporal

let expect_ok = function
  | Ok value -> value
  | Error error -> failwith (Temporal.Error.message error)

let expect_error = function
  | Ok _ -> failwith "expected an error"
  | Error _ -> ()

let canonical_request_json =
  expect_ok (Json.of_string
    {|{"api_version":"llm.temporal/v1","operation_key":"order-42","model":"gpt-test","input":[]}|})

let canonical_response_json =
  expect_ok (Json.of_string
    {|{"api_version":"llm.temporal/v1","operation_key":"order-42","status":"completed","output":[]}|})

let request_value = expect_ok (request canonical_request_json)
let response_value = expect_ok (response ~operation_id:"operation-42" canonical_response_json)

let assert_equal expected actual =
  if expected <> actual then failwith (Printf.sprintf "expected %S, got %S" expected actual)

let () =
  assert_equal "llm.generate.v1" (Temporal.Activity.name generate_activity);
  assert_equal "llm.generate.workflow.v1" (Temporal.Workflow.name (workflow ()));
  if Temporal.Activity.implementation generate_activity <> None then
    failwith "a remote Go activity must not have an OCaml implementation";

  let payload = expect_ok (Temporal.Codec.encode request_codec request_value) in
  assert_equal "json/plain" (List.assoc "encoding" payload.metadata);
  let decoded = expect_ok (Temporal.Codec.decode request_codec payload) in
  assert_equal (Json.to_string canonical_request_json) (Json.to_string (request_json decoded));

  let result_payload = expect_ok (Temporal.Codec.encode response_codec response_value) in
  let decoded_response = expect_ok (Temporal.Codec.decode response_codec result_payload) in
  assert_equal "operation-42" (Option.get (operation_id decoded_response));
  assert_equal (Json.to_string canonical_response_json) (Json.to_string (response_json decoded_response));

  expect_error (Json.of_string {|{"duplicate":1,"\u0064uplicate":2}|});
  expect_error (request (expect_ok (Json.of_string
    {|{"api_version":"llm.temporal/v9","operation_key":"order-42","model":"gpt-test"}|})));
  expect_error (Temporal.Codec.decode request_codec {
    Temporal.Codec.metadata = [ ("encoding", "json/plain") ];
    data = Bytes.of_string {|{"api_version":"llm.temporal/v1","request":{},"extra":true}|};
  });

  let calls = ref 0 in
  let dispatch ?task_queue activity input =
    incr calls;
    if task_queue <> Some "go-activities" then failwith "task queue was not forwarded";
    assert_equal activity_name (Temporal.Activity.name activity);
    assert_equal (Json.to_string canonical_request_json) (Json.to_string (request_json input));
    Ok response_value
  in
  let invoked = expect_ok (invoke_once ~task_queue:"go-activities" ~dispatch request_value) in
  if !calls <> 1 then failwith "one-shot wrapper dispatched more than once";
  assert_equal "operation-42" (Option.get (operation_id invoked));
  print_endline "llm_temporal wrapper tests passed"
