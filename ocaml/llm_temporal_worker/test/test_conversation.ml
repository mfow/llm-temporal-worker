open Llm_temporal

let failf format = Printf.ksprintf failwith format

let expect_ok = function
  | Ok value -> value
  | Error error -> failf "unexpected Temporal error: %s" (Temporal.Error.message error)

let assert_equal expected actual =
  if expected <> actual then failf "expected %S, got %S" expected actual

let context = { tenant = None; project = None; actor = None; tags = [ ("suite", "conversation") ] }
let model = Model_selector.of_string "gpt-test"
let operation_key value = Operation_key.of_string value
let message text = Message { actor = Human; content = [ Text text ] }

let response ~operation_key ~continuation =
  { operation_key;
    operation_id = None;
    status = Completed;
    output = [ Message { actor = Model; content = [ Text "ok" ] } ];
    route = { route_id = None; endpoint_id = None; api_family = None;
              requested_model = Some model; resolved_model = None };
    service = { requested = Standard; attempted = Standard; actual = Some Standard;
                provider_value = None; fallback_index = 0 };
    usage = { input_tokens = 1L; output_tokens = 1L; reasoning_tokens = 0L;
              cache_read_tokens = 0L; cache_write_tokens = 0L; provider_raw = None };
    cost = { status = Some Cost_known; currency = "USD"; reserved_microusd = 0L;
             actual_microusd = 0L; method_ = "test";
             catalog_version = Cost_catalog_version.of_string "test" };
    provider = { response_id = None; request_id = None; generation_id = None;
                 finish_reason = Some "stop"; raw = [] };
    continuation;
    diagnostics = [];
    metadata = { operation_id = None } }

let () =
  let settings =
    Conversation.Settings.make
      ~service_class:Priority
      ~service_class_fallbacks:[ Standard ]
      ~instructions:[ Text_instruction { level = Application; text = "Be brief." } ]
      ()
  in
  let parent = Conversation.root ~context ~model ~settings () in
  let parent_request =
    Conversation.to_request ~operation_key:(operation_key "parent") ~append:[] parent
  in
  assert_equal "gpt-test" (Model_selector.to_string parent_request.model);
  if parent_request.service_class <> Priority then failwith "settings were not retained";
  if parent_request.continuation <> None then failwith "root has a continuation";

  let calls = ref [] in
  let dispatch ?task_queue activity (request : Llm_temporal.request) =
    (match task_queue with
     | Some queue -> assert_equal "conversation-queue" (Temporal_task_queue.to_string queue)
     | None -> failwith "task queue was dropped");
    assert_equal activity_name (Temporal.Activity.name activity);
    calls := request :: !calls;
    let handle = Continuation_handle.of_string (Operation_key.to_string request.operation_key ^ "-next") in
    Ok (response ~operation_key:request.operation_key
          ~continuation:(Some { handle; endpoint_id = None; model = None;
                                expires_at = None; pinned = true; provider_state = None }))
  in
  let branch_a =
    expect_ok
      (Conversation.respond_with
         ~task_queue:(Temporal_task_queue.of_string "conversation-queue")
         ~dispatch ~operation_key:(operation_key "a") ~append:[ message "A" ] parent)
  in
  let branch_b =
    expect_ok
      (Conversation.respond_with
         ~task_queue:(Temporal_task_queue.of_string "conversation-queue")
         ~dispatch ~operation_key:(operation_key "b") ~append:[ message "B" ]
         (Conversation.fork parent))
  in
  if List.length !calls <> 2 then failwith "expected exactly two dispatches";
  if Conversation.continuation parent <> None then failwith "parent was mutated";
  let request_a = Conversation.to_request ~operation_key:(operation_key "a2") ~append:[] branch_a.conversation in
  let request_b = Conversation.to_request ~operation_key:(operation_key "b2") ~append:[] branch_b.conversation in
  (match request_a.continuation, request_b.continuation with
   | Some a, Some b ->
       assert_equal "a-next" (Continuation_handle.to_string a.handle);
       assert_equal "b-next" (Continuation_handle.to_string b.handle)
   | _ -> failwith "child conversations did not retain continuations");
  if Conversation.continuation (Conversation.fork parent) <> None then
    failwith "fork unexpectedly changed the parent";
  let omitted_settings = Conversation.root ~context ~model () in
  let omitted_request = Conversation.to_request ~operation_key:(operation_key "defaults") ~append:[] omitted_settings in
  if omitted_request.service_class <> Standard then failwith "default service class changed";
  if omitted_request.portability <> Strict then failwith "default portability changed";
  print_endline "immutable conversation tests passed"
