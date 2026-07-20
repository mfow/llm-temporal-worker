open Llm_temporal

let failf format = Printf.ksprintf failwith format
let expect_ok = function Ok value -> value | Error error -> failf "unexpected Temporal error: %s" (Temporal.Error.message error)
let expect_valid = function Ok value -> value | Error error -> failf "unexpected validation error: %s" error
let context = {
  tenant = Some (Tenant_id.of_string "tenant");
  project = Some (Project_id.of_string "project");
  actor = Some (Actor_id.of_string "actor");
  tags = [ ("suite", "conversation") ] }
let model = Model_selector.of_string "gpt-test"
let operation_key value = Operation_key.of_string value
let message text = Message { actor = Human; content = [ Text text ] }
let checkpoint value = Checkpoint.of_string_exn value
let tool = { kind = Function; name = Tool_name.of_string "lookup"; description = "";
             input_schema = `Assoc []; output_schema = None }
let output = { max_tokens = Some 32; format = Json_format }

let response (request : generate_request) ~kind ~handle =
  let parent = request.parent in
  let depth = match parent with None -> 0l | Some _ -> 1l in
  { api_version = V1_codec.generate_api_version;
    operation_key = request.operation_key;
    operation_id = Operation_id.of_string (Operation_key.to_string request.operation_key ^ "-operation");
    status = Completed;
    output = [ message "ok" ];
    checkpoint = { handle; parent; kind; depth };
    cache = { disposition = Cache_disabled; variant = 0l; entry_age_seconds = None };
    route = None; usage = None;
    cost = Exact_cost { actual_cost_usd = Usd_decimal.zero;
                        method_ = Control_query_zero; catalog_version = None };
    diagnostics = [] }

let () =
  let settings =
    Conversation.Settings.make
      ~service_class:Priority
      ~service_class_fallbacks:[ Standard ]
      ~instructions:[ Text_instruction { level = Application; text = "Be brief." } ]
      ~tools:[ tool ] ~output ()
  in
  let cache = expect_valid (Conversation.Cache_policy.accept_up_to ~max_age_seconds:60L ~variant:1l ()) in
  let patch =
    Conversation.Settings.Patch.set_temperature
      (expect_valid (Usd_decimal.of_string "0.25"))
      (Conversation.Settings.Patch.set_service_class Economy Conversation.Settings.Patch.keep)
  in
  let parent = Conversation.root ~context ~model ~settings () in
  let parent_request = Conversation.to_request ~operation_key:(operation_key "parent") ~append:[] parent in
  (match parent_request.parent, parent_request.settings_patch.model with
   | None, Set value when Model_selector.to_string value = "gpt-test" -> ()
   | _ -> failwith "root did not emit model patch");
  (match parent_request.settings_patch.service_class with Set Priority -> () | _ -> failwith "root settings omitted");
  let calls = ref [] in
  let dispatch ?task_queue activity (request : generate_request) =
    (match task_queue with Some queue when Temporal_task_queue.to_string queue = "conversation-queue" -> () | _ -> failwith "task queue dropped");
    if Temporal.Activity.name activity <> "llm.generate.v1" then failwith "wrong Generate descriptor";
    calls := request :: !calls;
    let handle = checkpoint (Operation_key.to_string request.operation_key ^ "-checkpoint") in
    Ok (response request ~kind:Generation_checkpoint ~handle)
  in
  let branch_a = expect_ok (Conversation.respond_with
      ~task_queue:(Temporal_task_queue.of_string "conversation-queue") ~dispatch
      ~cache ~operation_key:(operation_key "a") ~append:[ message "A" ] parent) in
  let branch_b = expect_ok (Conversation.respond_with
      ~task_queue:(Temporal_task_queue.of_string "conversation-queue") ~dispatch
      ~settings_patch:patch ~operation_key:(operation_key "b") ~append:[ message "B" ]
      (Conversation.fork parent)) in
  if List.length !calls <> 2 then failwith "expected two immutable sibling dispatches";
  if Conversation.checkpoint parent <> None then failwith "parent was mutated";
  (match Conversation.checkpoint branch_a.conversation, Conversation.checkpoint branch_b.conversation with
   | Some a, Some b when Checkpoint.to_string a <> Checkpoint.to_string b -> ()
   | _ -> failwith "children did not retain distinct checkpoints");
  let child_request = Conversation.to_request ~operation_key:(operation_key "child") ~append:[] branch_a.conversation in
  (match child_request.parent with Some _ -> () | None -> failwith "child omitted checkpoint parent");
  if child_request.cache <> None then failwith "cache leaked between calls";

  let clear_patch =
    Conversation.Settings.Patch.clear_output
      (Conversation.Settings.Patch.clear_tools Conversation.Settings.Patch.keep)
  in
  let cleared = expect_ok (Conversation.respond_with
      ~task_queue:(Temporal_task_queue.of_string "conversation-queue") ~dispatch
      ~settings_patch:clear_patch ~operation_key:(operation_key "clear")
      ~append:[] branch_a.conversation) in

  let compact_dispatch ?task_queue activity (request : compact_request) =
    (match task_queue with Some queue when Temporal_task_queue.to_string queue = "compact-queue" -> () | _ -> failwith "compact task queue dropped");
    if Temporal.Activity.name activity <> "llm.compact.v1" then failwith "wrong Compact descriptor";
    let handle = checkpoint (Operation_key.to_string request.operation_key ^ "-compact") in
    Ok { api_version = V1_codec.compact_api_version; operation_key = request.operation_key;
         operation_id = Operation_id.of_string "compact-operation";
         checkpoint = { handle; parent = Some request.parent; kind = Compaction_checkpoint; depth = 2l };
         cache = { disposition = Cache_disabled; variant = 0l; entry_age_seconds = None };
         provenance = None; usage = None;
         cost = Exact_cost { actual_cost_usd = Usd_decimal.zero; method_ = Control_query_zero; catalog_version = None };
         diagnostics = [] }
  in
  let invalid_cache = expect_valid (Conversation.Cache_policy.accept_up_to ~max_age_seconds:60L ~variant:1l ()) in
  (match Conversation.compact_with ~dispatch:compact_dispatch ~cache:invalid_cache
      ~operation_key:(operation_key "compact-invalid") cleared.conversation with
   | Error _ -> ()
   | Ok _ -> failwith "compact accepted a nonzero cache variant");
  let _, compacted = expect_ok (Conversation.compact_with
      ~task_queue:(Temporal_task_queue.of_string "compact-queue") ~dispatch:compact_dispatch
      ~operation_key:(operation_key "compact") cleared.conversation) in
  let after = Conversation.to_request ~operation_key:(operation_key "after") ~append:[] compacted in
  (match after.parent, after.settings_patch.tools, after.settings_patch.output with
   | Some _, Set [], Clear -> ()
   | _ -> failwith "post-compaction Generate did not restore settings");
  (match Conversation.Cache_policy.variant cache with 1l -> () | _ -> failwith "cache variant lost");
  print_endline "immutable v1 conversation tests passed"
