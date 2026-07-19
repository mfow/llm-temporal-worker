open Llm_temporal_models

module Settings = struct
  type t = {
    service_class : service_class;
    service_class_fallbacks : service_class list;
    portability : portability;
    instructions : instruction list;
    tools : function_tool list;
    tool_policy : tool_policy;
    output : output_spec option;
    sampling : sampling option;
    reasoning : reasoning option;
    extensions : (string * Yojson.Safe.t) list;
  }

  let make
      ?(service_class = Standard)
      ?(service_class_fallbacks = [])
      ?(portability = Strict)
      ?(instructions = [])
      ?(tools = [])
      ?(tool_policy = { choice = Auto; parallel = false })
      ?output
      ?sampling
      ?reasoning
      ?(extensions = [])
      () =
    { service_class;
      service_class_fallbacks;
      portability;
      instructions;
      tools;
      tool_policy;
      output;
      sampling;
      reasoning;
      extensions }

  let default = make ()
end

type t = {
  context : request_context;
  model : Model_selector.t;
  settings : Settings.t;
  continuation : continuation option;
}

type turn = {
  response : response;
  conversation : t;
}

let root ~context ~model ?service_class ?(settings = Settings.default) () =
  let settings =
    match service_class with
    | None -> settings
    | Some service_class -> { settings with Settings.service_class }
  in
  { context; model; settings; continuation = None }

let of_continuation ~context ~model ?(settings = Settings.default) ~continuation () =
  { context; model; settings; continuation = Some continuation }

let context conversation = conversation.context
let model conversation = conversation.model
let continuation conversation = conversation.continuation
let fork conversation = conversation

let to_request ~operation_key ~append conversation =
  let settings = conversation.settings in
  Request.make
    ~operation_key
    ~model:conversation.model
    ~service_class:settings.service_class
    ~input:append
    ~context:conversation.context
    ~service_class_fallbacks:settings.service_class_fallbacks
    ~portability:settings.portability
    ~instructions:settings.instructions
    ~tools:settings.tools
    ~tool_policy:settings.tool_policy
    ?output:settings.output
    ?sampling:settings.sampling
    ?reasoning:settings.reasoning
    ?continuation:conversation.continuation
    ~extensions:settings.extensions
    ()

type dispatcher =
  ?task_queue:Temporal_task_queue.t ->
  (request, response) Temporal.Activity.t ->
  request ->
  (response, Temporal.Error.t) result

let child (conversation : t) (response : response) : t =
  { conversation with continuation = response.continuation }

let respond_with ?task_queue ~dispatch ~operation_key ~append conversation =
  let input = to_request ~operation_key ~append conversation in
  match Llm_temporal_invocation.invoke_once ?task_queue ~dispatch input with
  | Error error -> Error error
  | Ok response -> Ok { response; conversation = child conversation response }

let respond ?task_queue ~operation_key ~append conversation =
  let input = to_request ~operation_key ~append conversation in
  match Llm_temporal_invocation.execute ?task_queue input with
  | Error error -> Error error
  | Ok response -> Ok { response; conversation = child conversation response }

let start_respond ?task_queue ~operation_key ~append conversation =
  let input = to_request ~operation_key ~append conversation in
  let future =
    Temporal.Activity.start
      ?task_queue:(Option.map Temporal_task_queue.to_string task_queue)
      ~retry_policy:Llm_temporal_invocation.activity_retry_policy
      Llm_temporal_invocation.generate_activity input
  in
  Temporal.Future.map
    (fun response -> { response; conversation = child conversation response })
    future
