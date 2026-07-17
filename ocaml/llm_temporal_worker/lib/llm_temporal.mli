(** Typed, one-shot OCaml bindings for the Go [llm.generate.v1] Activity.

    The Go worker owns provider credentials and execution. This package owns
    typed v1 Temporal payloads and schedules exactly one Activity attempt. *)

val api_version : string
val activity_name : string
val workflow_name : string

type service_class = Economy | Standard | Priority
type portability = Strict | Best_effort
type actor = Human | Model
type instruction_level = Application | Policy
type response_status = Completed | Tool_calls | Refused | Length | Content_filtered
type output_kind = Output_text | Output_json | Output_json_schema
type reasoning_mode = Provider_default | Reasoning_disabled | Adaptive | Reasoning_enabled
type reasoning_effort = Effort_default | Minimal | Low | Medium | High | Maximum
type reasoning_summary = Summary_default | Summary_none | Summary_auto | Concise | Detailed
type cost_status = Cost_known | Cost_unknown

type request_context = { tenant : string option; project : string option; actor : string option; tags : (string * string) list }
type provider_state = { provider : string; endpoint_family : string; media_type : string; opaque : string }
type media_source = Url of string | Bytes of string | Blob of { locator : string; digest : string; byte_length : int64; media_type : string }
type content =
  | Text of string
  | Image of { media_type : string; source : media_source; detail : string option }
  | Document of { media_type : string; source : media_source; title : string option }
  | Json of Yojson.Safe.t
  | Refusal of { message : string; provider_code : string option }
  | Content_provider_state of provider_state
type message = { actor : actor; content : content list }
type instruction = Text_instruction of { level : instruction_level; text : string } | Parts_instruction of { level : instruction_level; content : content list }
type reference = { uri : string; metadata : (string * Yojson.Safe.t) list }
type item =
  | Message of message
  | Tool_call of { id : string; name : string; arguments : Yojson.Safe.t }
  | Tool_result of { call_id : string; name : string option; content : content list; is_error : bool }
  | Provider_state of provider_state
  | Reference of reference

(** Schema, tool arguments, reference metadata, extensions, and provider raw
    fields are the intentional open JSON leaves. *)
type tool_kind = Function | Provider | Remote_mcp
type function_tool = { kind : tool_kind; name : string; description : string; input_schema : Yojson.Safe.t; output_schema : Yojson.Safe.t option }
type tool_choice = Auto | None_allowed | Required | Named of string
type tool_policy = { choice : tool_choice; parallel : bool }
type output_format = Text_format | Json_format | Json_schema_format of { name : string; description : string option; schema : Yojson.Safe.t; strict : bool }
type output_spec = { max_tokens : int option; format : output_format }
type sampling = { temperature : float option; top_p : float option; top_k : int option; seed : int64 option; presence_penalty : float option; frequency_penalty : float option; stop_sequences : string list option }
type reasoning = { mode : reasoning_mode; effort : reasoning_effort; token_budget : int option; summary : reasoning_summary }
type continuation = { handle : string; endpoint_id : string; model : string; expires_at : string option; pinned : bool; provider_state : item list }

type request = {
  operation_key : string;
  context : request_context option;
  model : string;
  service_class : service_class;
  service_class_fallbacks : service_class list;
  portability : portability;
  instructions : instruction list;
  input : item list;
  tools : function_tool list;
  tool_policy : tool_policy;
  output : output_spec option;
  sampling : sampling option;
  reasoning : reasoning option;
  continuation : continuation option;
  extensions : (string * Yojson.Safe.t) list;
}

type route = { route_id : string option; endpoint_id : string option; api_family : string option; requested_model : string option; resolved_model : string option }
type service = { requested : service_class; attempted : service_class; actual : service_class option; provider_value : string option; fallback_index : int }
type usage = { input_tokens : int64; output_tokens : int64; reasoning_tokens : int64; cache_read_tokens : int64; cache_write_tokens : int64; provider_raw : (string * Yojson.Safe.t) list }
type cost = { currency : string; reserved_microusd : int64; actual_microusd : int64; method_ : string; catalog_version : string }
type provider = { response_id : string option; request_id : string option; generation_id : string option; finish_reason : string option; raw : (string * Yojson.Safe.t) list }
type diagnostic_severity = Info | Warning | Error
type diagnostic = { code : string; message : string; severity : diagnostic_severity; path : string option; details : (string * string) list option }

type response = {
  operation_key : string;
  operation_id : string option;
  status : response_status;
  output : item list;
  route : route;
  service : service;
  usage : usage;
  cost : cost;
  provider : provider;
  continuation : continuation option;
  diagnostics : diagnostic list;
}

val request_codec : request Temporal.Codec.t
val response_codec : response Temporal.Codec.t
val generate_activity : (request, response) Temporal.Activity.t

(** One dispatcher invocation, intentionally without polling, continuation,
    streaming, or retry loops. *)
val invoke_once : ?task_queue:string -> dispatch:(?task_queue:string -> (request, response) Temporal.Activity.t -> request -> (response, Temporal.Error.t) result) -> request -> (response, Temporal.Error.t) result
val execute : ?task_queue:string -> request -> (response, Temporal.Error.t) result
val workflow : ?task_queue:string -> unit -> (request, response) Temporal.Workflow.t
