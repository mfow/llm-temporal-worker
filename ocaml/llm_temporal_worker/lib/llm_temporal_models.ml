module Identifier = Llm_temporal_identifier

module type Opaque_identifier = sig
  type t = private string
  val of_string : string -> t
  val to_string : t -> string
end

module Operation_key = Identifier.Operation_key
module Operation_id = Identifier.Operation_id
module Model_selector = Identifier.Model_selector
module Resolved_model_id = Identifier.Resolved_model_id
module Endpoint_id = Identifier.Endpoint_id
module Route_id = Identifier.Route_id
module Continuation_handle = Identifier.Continuation_handle
module Provider_id = Identifier.Provider_id
module Endpoint_family = Identifier.Endpoint_family
module Api_family = Identifier.Api_family
module Provider_response_id = Identifier.Provider_response_id
module Provider_request_id = Identifier.Provider_request_id
module Provider_generation_id = Identifier.Provider_generation_id
module Tool_name = Identifier.Tool_name
module Tool_call_id = Identifier.Tool_call_id
module Tenant_id = Identifier.Tenant_id
module Project_id = Identifier.Project_id
module Actor_id = Identifier.Actor_id
module Blob_digest = Identifier.Blob_digest
module Diagnostic_code = Identifier.Diagnostic_code
module Cost_catalog_version = Identifier.Cost_catalog_version
module Temporal_task_queue = Identifier.Temporal_task_queue

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

type request_context = {
  tenant : Tenant_id.t option;
  project : Project_id.t option;
  actor : Actor_id.t option;
  tags : (string * string) list;
}

type provider_state = {
  provider : Provider_id.t;
  endpoint_family : Endpoint_family.t;
  media_type : string;
  opaque : string;
}

type media_source =
  | Url of string
  | Bytes of string
  | Blob of {
      locator : string;
      digest : Blob_digest.t;
      byte_length : int64;
      media_type : string;
    }

type content =
  | Text of string
  | Image of { media_type : string; source : media_source; detail : string option }
  | Document of { media_type : string; source : media_source; title : string option }
  | Json of Yojson.Safe.t
  | Refusal of { message : string; provider_code : string option }
  | Content_provider_state of provider_state

type message = { actor : actor; content : content list }
type instruction =
  | Text_instruction of { level : instruction_level; text : string }
  | Parts_instruction of { level : instruction_level; content : content list }
type reference = { uri : string; metadata : (string * Yojson.Safe.t) list }
type item =
  | Message of message
  | Tool_call of { id : Tool_call_id.t; name : Tool_name.t; arguments : Yojson.Safe.t }
  | Tool_result of { call_id : Tool_call_id.t; name : Tool_name.t option; content : content list; is_error : bool }
  | Provider_state of provider_state
  | Reference of reference

type tool_kind = Function | Provider | Remote_mcp
type function_tool = {
  kind : tool_kind;
  name : Tool_name.t;
  description : string;
  input_schema : Yojson.Safe.t;
  output_schema : Yojson.Safe.t option;
}

type tool_choice = Auto | None_allowed | Required | Named of Tool_name.t
type tool_policy = { choice : tool_choice; parallel : bool }
type output_format = Text_format | Json_format | Json_schema_format of { name : string; description : string option; schema : Yojson.Safe.t; strict : bool }
type output_spec = { max_tokens : int option; format : output_format }
type sampling = { temperature : float option; top_p : float option; top_k : int option; seed : int64 option; presence_penalty : float option; frequency_penalty : float option; stop_sequences : string list option }
type reasoning = { mode : reasoning_mode; effort : reasoning_effort; token_budget : int option; summary : reasoning_summary }
type continuation = {
  handle : Continuation_handle.t;
  endpoint_id : Endpoint_id.t option;
  model : Resolved_model_id.t option;
  expires_at : string option;
  pinned : bool;
  provider_state : provider_state list option;
}

type request = {
  operation_key : Operation_key.t;
  context : request_context option;
  model : Model_selector.t;
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

type route = {
  route_id : Route_id.t option;
  endpoint_id : Endpoint_id.t option;
  api_family : Api_family.t option;
  requested_model : Model_selector.t option;
  resolved_model : Resolved_model_id.t option;
}
type service = { requested : service_class; attempted : service_class; actual : service_class option; provider_value : string option; fallback_index : int }
type usage = { input_tokens : int64; output_tokens : int64; reasoning_tokens : int64; cache_read_tokens : int64; cache_write_tokens : int64; provider_raw : (string * Yojson.Safe.t) list option }
type cost = { status : cost_status option; currency : string; reserved_microusd : int64; actual_microusd : int64; method_ : string; catalog_version : Cost_catalog_version.t }
type provider = { response_id : Provider_response_id.t option; request_id : Provider_request_id.t option; generation_id : Provider_generation_id.t option; finish_reason : string option; raw : (string * Yojson.Safe.t) list }
type diagnostic_severity = Info | Warning | Diagnostic_error
type diagnostic = { code : Diagnostic_code.t; message : string; severity : diagnostic_severity; path : string option; details : (string * string) list option }
type response_metadata = { operation_id : Operation_id.t option }
type response = {
  operation_key : Operation_key.t;
  operation_id : Operation_id.t option;
  status : response_status;
  output : item list;
  route : route;
  service : service;
  usage : usage;
  cost : cost;
  provider : provider;
  continuation : continuation option;
  diagnostics : diagnostic list;
  metadata : response_metadata;
}
