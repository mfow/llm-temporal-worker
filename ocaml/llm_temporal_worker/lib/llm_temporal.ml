let api_version = "llm.temporal/v1"
let activity_name = "llm.generate.v1"
let workflow_name = "llm.generate.workflow.v1"

let codec_error format = Printf.ksprintf (fun message -> Temporal.Error.codec ~message) format

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

type tool_kind = Function | Provider | Remote_mcp
type function_tool = {
  kind : tool_kind;
  name : string;
  description : string;
  input_schema : Yojson.Safe.t;
  output_schema : Yojson.Safe.t option;
}

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
type service = {
  requested : service_class;
  attempted : service_class;
  actual : service_class option;
  provider_value : string option;
  fallback_index : int;
}

type usage = {
  input_tokens : int64;
  output_tokens : int64;
  reasoning_tokens : int64;
  cache_read_tokens : int64;
  cache_write_tokens : int64;
  provider_raw : (string * Yojson.Safe.t) list;
}
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

let service_class_to_string = function Economy -> "economy" | Standard -> "standard" | Priority -> "priority"

let service_class_of_string = function
  | "economy" -> Ok Economy
  | "standard" -> Ok Standard
  | "priority" -> Ok Priority
  | value -> Error (codec_error "invalid service_class %S; expected economy, standard, or priority" value)

let actor_to_string = function Human -> "human" | Model -> "model"

let actor_of_string = function
  | "human" -> Ok Human
  | "model" -> Ok Model
  | value -> Error (codec_error "invalid message actor %S" value)

let status_to_string = function
  | Completed -> "completed"
  | Tool_calls -> "tool_calls"
  | Refused -> "refused"
  | Length -> "length"
  | Content_filtered -> "content_filtered"

let status_of_string = function
  | "completed" -> Ok Completed
  | "tool_calls" -> Ok Tool_calls
  | "refused" -> Ok Refused
  | "length" -> Ok Length
  | "content_filtered" -> Ok Content_filtered
  | value -> Error (codec_error "invalid response status %S" value)

let object_fields context = function
  | `Assoc fields -> Ok fields
  | _ -> Error (codec_error "%s must be a JSON object" context)

let required context name fields =
  match List.assoc_opt name fields with
  | Some value -> Ok value
  | None -> Error (codec_error "%s is missing required field %S" context name)

let optional name fields = List.assoc_opt name fields

let string context = function
  | `String value -> Ok value
  | _ -> Error (codec_error "%s must be a string" context)

let bool context = function
  | `Bool value -> Ok value
  | _ -> Error (codec_error "%s must be a boolean" context)

let int64 context = function
  | `Int value -> Ok (Int64.of_int value)
  | `Intlit value -> (try Ok (Int64.of_string value) with Failure _ -> Error (codec_error "%s must be an integer" context))
  | _ -> Error (codec_error "%s must be an integer" context)

let list context = function
  | `List values -> Ok values
  | _ -> Error (codec_error "%s must be a JSON array" context)

let nonempty context value =
  if String.trim value = "" then Error (codec_error "%s must not be empty" context) else Ok value

let map_result f values =
  let rec loop reversed = function
    | [] -> Ok (List.rev reversed)
    | value :: remaining ->
        match f value with
        | Ok mapped -> loop (mapped :: reversed) remaining
        | Error _ as error -> error
  in
  loop [] values

let option_field name encode = function None -> [] | Some value -> [ (name, encode value) ]

let provider_state_payload_to_json state =
  `Assoc [ ("provider", `String state.provider); ("endpoint_family", `String state.endpoint_family); ("media_type", `String state.media_type); ("opaque", `String state.opaque) ]

let media_source_to_json = function
  | Url value -> [ ("url", `String value) ]
  | Bytes value -> [ ("bytes", `String value) ]
  | Blob { locator; digest; byte_length; media_type } ->
      [ ("blob", `Assoc [ ("locator", `String locator); ("digest", `String digest); ("byte_length", `Intlit (Int64.to_string byte_length)); ("media_type", `String media_type) ]) ]

let content_to_json = function
  | Text text -> `Assoc [ ("kind", `String "text"); ("text", `String text) ]
  | Image { media_type; source; detail } ->
      `Assoc
        ([ ("kind", `String "image"); ("media_type", `String media_type) ]
        @ media_source_to_json source
        @ option_field "detail" (fun value -> `String value) detail)
  | Document { media_type; source; title } ->
      `Assoc (match title with
        | None -> [ ("kind", `String "document"); ("media_type", `String media_type) ] @ media_source_to_json source
        | Some value -> ([ ("kind", `String "document"); ("media_type", `String media_type); ("title", `String value) ] @ media_source_to_json source))
  | Json value -> `Assoc [ ("kind", `String "json"); ("value", value) ]
  | Refusal { message; provider_code } -> `Assoc ([ ("kind", `String "refusal"); ("text", `String message) ] @ option_field "provider_code" (fun value -> `String value) provider_code)
  | Content_provider_state state -> `Assoc (("kind", `String "provider_state") :: match provider_state_payload_to_json state with `Assoc fields -> fields | _ -> assert false)

let provider_state_of_fields fields =
  match required "provider state" "provider" fields, required "provider state" "endpoint_family" fields,
        required "provider state" "media_type" fields, required "provider state" "opaque" fields with
  | Ok provider, Ok endpoint_family, Ok media_type, Ok opaque ->
      (match string "provider state provider" provider, string "provider state endpoint_family" endpoint_family,
             string "provider state media_type" media_type, string "provider state opaque" opaque with
       | Ok provider, Ok endpoint_family, Ok media_type, Ok opaque -> Ok { provider; endpoint_family; media_type; opaque }
       | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error)
  | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error

let media_source_of_fields fields =
  match optional "url" fields, optional "bytes" fields, optional "blob" fields with
  | Some value, None, None -> Result.map (fun value -> Url value) (string "media url" value)
  | None, Some value, None -> Result.map (fun value -> Bytes value) (string "media bytes" value)
  | None, None, Some value ->
      (match object_fields "media blob" value with
       | Error _ as error -> error
       | Ok blob ->
           match required "media blob" "locator" blob, required "media blob" "digest" blob,
                 required "media blob" "byte_length" blob, required "media blob" "media_type" blob with
           | Ok locator, Ok digest, Ok byte_length, Ok media_type ->
               (match string "media blob locator" locator, string "media blob digest" digest,
                      int64 "media blob byte_length" byte_length, string "media blob media_type" media_type with
                | Ok locator, Ok digest, Ok byte_length, Ok media_type -> Ok (Blob { locator; digest; byte_length; media_type })
                | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error)
           | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error)
  | _ -> Error (codec_error "media part must contain exactly one of url, bytes, or blob")

let content_of_json value =
  match object_fields "content part" value with
  | Error _ as error -> error
  | Ok fields ->
      match required "content part" "kind" fields with
      | Error _ as error -> error
      | Ok kind ->
          match string "content part kind" kind with
          | Ok "text" ->
              (match required "text content part" "text" fields with
               | Ok text -> Result.map (fun value -> Text value) (string "text content part text" text)
               | Error _ as error -> error)
          | Ok "image" ->
              (match required "image content part" "media_type" fields with
               | Ok media_type ->
                   (match string "image media_type" media_type, media_source_of_fields fields with
                    | Ok media_type, Ok source ->
                        let detail =
                          match optional "detail" fields with
                          | None -> Ok None
                          | Some value ->
                              Result.map Option.some (string "image detail" value)
                        in
                        (match detail with
                         | Ok detail -> Ok (Image { media_type; source; detail })
                         | Error _ as error -> error)
                    | Error _ as error, _ | _, Error _ as error -> error)
               | Error _ as error -> error)
          | Ok "document" ->
              (match required "document content part" "media_type" fields with
               | Ok media_type ->
                   (match string "document media_type" media_type, media_source_of_fields fields with
                    | Ok media_type, Ok source ->
                        let title = match optional "title" fields with None -> Ok None | Some title -> Result.map Option.some (string "document title" title) in
                        (match title with Ok title -> Ok (Document { media_type; source; title }) | Error _ as error -> error)
                    | Error _ as error, _ | _, Error _ as error -> error)
               | Error _ as error -> error)
          | Ok "json" ->
              (match required "json content part" "value" fields with Ok value -> Ok (Json value) | Error _ as error -> error)
          | Ok "refusal" ->
              (match required "refusal content part" "text" fields with
               | Ok text ->
                   (match string "refusal text" text with
                    | Ok message ->
                        let provider_code = match optional "provider_code" fields with None -> Ok None | Some value -> Result.map Option.some (string "refusal provider_code" value) in
                        (match provider_code with Ok provider_code -> Ok (Refusal { message; provider_code }) | Error _ as error -> error)
                    | Error _ as error -> error)
               | Error _ as error -> error)
          | Ok "provider_state" -> Result.map (fun state -> Content_provider_state state) (provider_state_of_fields fields)
          | Ok kind -> Error (codec_error "unsupported content part kind %S" kind)
          | Error _ as error -> error

let message_to_json message =
  `Assoc [
    ("kind", `String "message");
    ("actor", `String (actor_to_string message.actor));
    ("content", `List (List.map content_to_json message.content));
  ]

let message_of_json value =
  match object_fields "message" value with
  | Error _ as error -> error
  | Ok fields ->
      match required "message" "actor" fields, required "message" "content" fields with
      | Ok actor, Ok content ->
          (match string "message actor" actor, list "message content" content with
           | Ok actor, Ok content ->
               (match actor_of_string actor, map_result content_of_json content with
                | Ok actor, Ok content -> Ok { actor; content }
                | Error _ as error, _ | _, Error _ as error -> error)
           | Error _ as error, _ | _, Error _ as error -> error)
      | Error _ as error, _ | _, Error _ as error -> error

let item_to_json = function
  | Message message -> message_to_json message
  | Tool_call { id; name; arguments } ->
      `Assoc [ ("kind", `String "tool_call"); ("id", `String id); ("name", `String name); ("arguments", arguments) ]
  | Tool_result { call_id; name; content; is_error } ->
      let fields = [
        ("kind", `String "tool_result"); ("call_id", `String call_id);
        ("content", `List (List.map content_to_json content)); ("is_error", `Bool is_error);
      ] in
      `Assoc (match name with None -> fields | Some value -> ("name", `String value) :: fields)
  | Provider_state state ->
      `Assoc
        (("kind", `String "provider_state")
        :: match provider_state_payload_to_json state with
           | `Assoc fields -> fields
           | _ -> assert false)
  | Reference { uri; metadata } ->
      `Assoc
        ([ ("kind", `String "reference"); ("uri", `String uri) ]
        @ if metadata = [] then [] else [ ("metadata", `Assoc metadata) ])

let item_of_json value =
  match object_fields "item" value with
  | Error _ as error -> error
  | Ok fields ->
      match required "item" "kind" fields with
      | Error _ as error -> error
      | Ok kind ->
          match string "item kind" kind with
          | Ok "message" -> Result.map (fun message -> Message message) (message_of_json value)
          | Ok "tool_call" ->
              (match required "tool call" "id" fields, required "tool call" "name" fields,
                     required "tool call" "arguments" fields with
               | Ok id, Ok name, Ok arguments ->
                   (match string "tool call id" id, string "tool call name" name with
                    | Ok id, Ok name ->
                        (match nonempty "tool call id" id, nonempty "tool call name" name with
                         | Ok id, Ok name -> Ok (Tool_call { id; name; arguments })
                         | Error _ as error, _ | _, Error _ as error -> error)
                    | Error _ as error, _ | _, Error _ as error -> error)
               | Error _ as error, _, _ | _, Error _ as error, _ | _, _, Error _ as error -> error)
          | Ok "tool_result" ->
              (match required "tool result" "call_id" fields, required "tool result" "content" fields with
               | Ok call_id, Ok content ->
                   (match string "tool result call_id" call_id, list "tool result content" content with
                    | Ok call_id, Ok content ->
                        (match map_result content_of_json content with
                         | Error _ as error -> error
                         | Ok content ->
                             let name = match optional "name" fields with None -> Ok None | Some value -> Result.map Option.some (string "tool result name" value) in
                             let is_error = match optional "is_error" fields with None -> Ok false | Some value -> bool "tool result is_error" value in
                             (match name, is_error with
                              | Ok name, Ok is_error -> Ok (Tool_result { call_id; name; content; is_error })
                              | Error _ as error, _ | _, Error _ as error -> error))
                    | Error _ as error, _ | _, Error _ as error -> error)
               | Error _ as error, _ | _, Error _ as error -> error)
          | Ok "provider_state" ->
              Result.map (fun state -> Provider_state state) (provider_state_of_fields fields)
          | Ok "reference" ->
              (match required "reference" "uri" fields with
               | Error _ as error -> error
               | Ok uri ->
                   (match string "reference uri" uri with
                    | Error _ as error -> error
                    | Ok uri ->
                        let metadata =
                          match optional "metadata" fields with
                          | None -> Ok []
                          | Some (`Assoc values) -> Ok values
                          | Some _ ->
                              Error
                                (codec_error
                                   "reference metadata must be a JSON object")
                        in
                        (match metadata with
                         | Ok metadata -> Ok (Reference { uri; metadata })
                         | Error _ as error -> error)))
          | Ok kind -> Error (codec_error "unsupported item kind %S" kind)
          | Error _ as error -> error

let instruction_to_json = function
  | Text_instruction { level; text } ->
      let level = match level with Application -> "application" | Policy -> "policy" in
      `Assoc [ ("kind", `String "text"); ("level", `String level); ("text", `String text) ]
  | Parts_instruction { level; content } ->
      let level = match level with Application -> "application" | Policy -> "policy" in
      `Assoc [ ("kind", `String "parts"); ("level", `String level); ("content", `List (List.map content_to_json content)) ]

let tool_to_json tool =
  let kind = match tool.kind with Function -> "function" | Provider -> "provider" | Remote_mcp -> "remote_mcp" in
  let fields = [
    ("kind", `String kind); ("name", `String tool.name); ("description", `String tool.description);
    ("input_schema", tool.input_schema);
  ] in
  `Assoc (match tool.output_schema with None -> fields | Some schema -> ("output_schema", schema) :: fields)

let policy_to_json policy =
  match policy.choice with
  | Auto -> `Assoc [ ("mode", `String "auto"); ("parallel", `Bool policy.parallel) ]
  | None_allowed -> `Assoc [ ("mode", `String "none"); ("parallel", `Bool policy.parallel) ]
  | Required -> `Assoc [ ("mode", `String "required"); ("parallel", `Bool policy.parallel) ]
  | Named name -> `Assoc [ ("mode", `String "named"); ("name", `String name); ("parallel", `Bool policy.parallel) ]

let context_to_json context =
  `Assoc (
    option_field "tenant" (fun value -> `String value) context.tenant
    @ option_field "project" (fun value -> `String value) context.project
    @ option_field "actor" (fun value -> `String value) context.actor
    @ if context.tags = [] then [] else [ ("tags", `Assoc (List.map (fun (key, value) -> (key, `String value)) context.tags)) ])

let output_to_json output =
  let format = match output.format with
    | Text_format -> `Assoc [ ("kind", `String "text") ]
    | Json_format -> `Assoc [ ("kind", `String "json") ]
    | Json_schema_format { name; description; schema; strict } ->
        `Assoc ([ ("kind", `String "json_schema"); ("name", `String name); ("schema", schema); ("strict", `Bool strict) ] @ option_field "description" (fun value -> `String value) description) in
  `Assoc ([ ("format", format) ] @ option_field "max_tokens" (fun value -> `Int value) output.max_tokens)

let sampling_to_json sampling =
  `Assoc (
    option_field "temperature" (fun value -> `Float value) sampling.temperature
    @ option_field "top_p" (fun value -> `Float value) sampling.top_p
    @ option_field "top_k" (fun value -> `Int value) sampling.top_k
    @ option_field "seed" (fun value -> `Intlit (Int64.to_string value)) sampling.seed
    @ option_field "presence_penalty" (fun value -> `Float value) sampling.presence_penalty
    @ option_field "frequency_penalty" (fun value -> `Float value) sampling.frequency_penalty
    @ option_field "stop_sequences" (fun value -> `List (List.map (fun item -> `String item) value)) sampling.stop_sequences)

let reasoning_to_json reasoning =
  let mode = match reasoning.mode with Provider_default -> "provider_default" | Reasoning_disabled -> "disabled" | Adaptive -> "adaptive" | Reasoning_enabled -> "enabled" in
  let effort = match reasoning.effort with Effort_default -> "provider_default" | Minimal -> "minimal" | Low -> "low" | Medium -> "medium" | High -> "high" | Maximum -> "maximum" in
  let summary = match reasoning.summary with Summary_default -> "provider_default" | Summary_none -> "none" | Summary_auto -> "auto" | Concise -> "concise" | Detailed -> "detailed" in
  `Assoc ([ ("mode", `String mode); ("effort", `String effort); ("summary", `String summary) ] @ option_field "token_budget" (fun value -> `Int value) reasoning.token_budget)

let provider_state_to_json state =
  `Assoc [ ("kind", `String "provider_state"); ("provider", `String state.provider); ("endpoint_family", `String state.endpoint_family); ("media_type", `String state.media_type); ("opaque", `String state.opaque) ]

let continuation_to_json continuation =
  `Assoc (
    [ ("handle", `String continuation.handle); ("endpoint_id", `String continuation.endpoint_id); ("model", `String continuation.model); ("pinned", `Bool continuation.pinned); ("provider_state", `List (List.map item_to_json continuation.provider_state)) ]
    @ option_field "expires_at" (fun value -> `String value) continuation.expires_at)

let request_to_json request =
  `Assoc (
    [
    ("api_version", `String api_version); ("operation_key", `String request.operation_key);
    ("model", `String request.model); ("service_class", `String (service_class_to_string request.service_class));
    ("service_class_fallbacks", `List (List.map (fun value -> `String (service_class_to_string value)) request.service_class_fallbacks));
    ("portability", `String (match request.portability with Strict -> "strict" | Best_effort -> "best_effort"));
    ("instructions", `List (List.map instruction_to_json request.instructions));
    ("input", `List (List.map item_to_json request.input));
    ("tools", `List (List.map tool_to_json request.tools));
    ("tool_policy", policy_to_json request.tool_policy);
    ("extensions", `Assoc request.extensions);
    ]
    @ option_field "context" context_to_json request.context
    @ option_field "output" output_to_json request.output
    @ option_field "sampling" sampling_to_json request.sampling
    @ option_field "reasoning" reasoning_to_json request.reasoning
    @ option_field "continuation" continuation_to_json request.continuation)

let service_to_json service =
  let fields = [
    ("requested", `String (service_class_to_string service.requested));
    ("attempted", `String (service_class_to_string service.attempted));
    ("fallback_index", `Int service.fallback_index);
  ] in
  let fields = match service.actual with None -> fields | Some value -> ("actual", `String (service_class_to_string value)) :: fields in
  `Assoc (match service.provider_value with None -> fields | Some value -> ("provider_value", `String value) :: fields)

let usage_to_json usage =
  `Assoc [
    ("input_tokens", `Intlit (Int64.to_string usage.input_tokens));
    ("output_tokens", `Intlit (Int64.to_string usage.output_tokens));
    ("reasoning_tokens", `Intlit (Int64.to_string usage.reasoning_tokens));
    ("cache_read_tokens", `Intlit (Int64.to_string usage.cache_read_tokens));
    ("cache_write_tokens", `Intlit (Int64.to_string usage.cache_write_tokens));
    ("provider_raw", `Assoc usage.provider_raw);
  ]

let route_to_json route = `Assoc (
  option_field "route_id" (fun value -> `String value) route.route_id
  @ option_field "endpoint_id" (fun value -> `String value) route.endpoint_id
  @ option_field "api_family" (fun value -> `String value) route.api_family
  @ option_field "requested_model" (fun value -> `String value) route.requested_model
  @ option_field "resolved_model" (fun value -> `String value) route.resolved_model)

let cost_to_json cost =
  `Assoc [ ("currency", `String cost.currency); ("reserved_microusd", `Intlit (Int64.to_string cost.reserved_microusd)); ("actual_microusd", `Intlit (Int64.to_string cost.actual_microusd)); ("method", `String cost.method_); ("catalog_version", `String cost.catalog_version) ]

let provider_to_json provider = `Assoc (
  option_field "response_id" (fun value -> `String value) provider.response_id
  @ option_field "request_id" (fun value -> `String value) provider.request_id
  @ option_field "generation_id" (fun value -> `String value) provider.generation_id
  @ option_field "finish_reason" (fun value -> `String value) provider.finish_reason
  @ if provider.raw = [] then [] else [ ("raw", `Assoc provider.raw) ])

let diagnostic_to_json diagnostic =
  let severity = match diagnostic.severity with Info -> "info" | Warning -> "warning" | Error -> "error" in
  `Assoc ([ ("code", `String diagnostic.code); ("message", `String diagnostic.message); ("severity", `String severity) ] @ option_field "path" (fun value -> `String value) diagnostic.path @ option_field "details" (fun value -> `Assoc (List.map (fun (key, text) -> (key, `String text)) value)) diagnostic.details)

let response_to_json response =
  let fields = [
    ("api_version", `String api_version); ("operation_key", `String response.operation_key);
    ("status", `String (status_to_string response.status));
    ("output", `List (List.map item_to_json response.output));
    ("route", route_to_json response.route); ("service", service_to_json response.service); ("usage", usage_to_json response.usage);
    ("cost", cost_to_json response.cost); ("provider", provider_to_json response.provider);
    ("diagnostics", `List (List.map diagnostic_to_json response.diagnostics));
  ] in
  `Assoc (fields @ option_field "operation_id" (fun value -> `String value) response.operation_id @ option_field "continuation" continuation_to_json response.continuation)

let parse_json decoder bytes =
  try decoder (Yojson.Safe.from_string (Bytes.to_string bytes))
  with Yojson.Json_error message -> Error (codec_error "invalid json/plain payload: %s" message)

let require_version fields =
  match required "payload" "api_version" fields with
  | Error _ as error -> error
  | Ok version ->
      match string "api_version" version with
      | Ok value when value = api_version -> Ok ()
      | Ok value -> Error (codec_error "unsupported api_version %S" value)
      | Error _ as error -> error

let request_of_json value =
  match object_fields "canonical llm.Request" value with
  | Error _ as error -> error
  | Ok fields ->
      match require_version fields, required "request" "operation_key" fields, required "request" "model" fields,
             required "request" "service_class" fields, required "request" "input" fields with
      | Ok (), Ok operation_key, Ok model, Ok priority, Ok input ->
          (match string "request operation_key" operation_key, string "request model" model,
                 string "request service_class" priority, list "request input" input with
           | Ok operation_key, Ok model, Ok priority, Ok input ->
               (match nonempty "request operation_key" operation_key, nonempty "request model" model,
                      service_class_of_string priority, map_result item_of_json input with
                | Ok operation_key, Ok model, Ok service_class, Ok input ->
                    Ok { operation_key; context = None; model; service_class; service_class_fallbacks = []; portability = Strict; instructions = []; input; tools = []; tool_policy = { choice = Auto; parallel = false }; output = None; sampling = None; reasoning = None; continuation = None; extensions = [] }
                | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error)
           | Error _ as error, _, _, _ | _, Error _ as error, _, _ | _, _, Error _ as error, _ | _, _, _, Error _ as error -> error)
      | Error _ as error, _, _, _, _ | _, Error _ as error, _, _, _ | _, _, Error _ as error, _, _ | _, _, _, Error _ as error, _ | _, _, _, _, Error _ as error -> error

let service_of_json value =
  match object_fields "response service" value with
  | Error _ as error -> error
  | Ok fields ->
      match required "response service" "requested" fields, required "response service" "attempted" fields with
      | Ok requested, Ok attempted ->
          (match string "response service requested" requested, string "response service attempted" attempted with
           | Ok requested, Ok attempted ->
               (match service_class_of_string requested, service_class_of_string attempted with
                | Ok requested, Ok attempted -> Ok { requested; attempted; actual = None; provider_value = None; fallback_index = 0 }
                | Error _ as error, _ | _, Error _ as error -> error)
           | Error _ as error, _ | _, Error _ as error -> error)
      | Error _ as error, _ | _, Error _ as error -> error

let usage_of_json value =
  match object_fields "response usage" value with
  | Error _ as error -> error
  | Ok fields ->
      let get name = match optional name fields with None -> Ok 0L | Some value -> int64 ("response usage " ^ name) value in
      match get "input_tokens", get "output_tokens", get "reasoning_tokens", get "cache_read_tokens", get "cache_write_tokens" with
      | Ok input_tokens, Ok output_tokens, Ok reasoning_tokens, Ok cache_read_tokens, Ok cache_write_tokens ->
          Ok { input_tokens; output_tokens; reasoning_tokens; cache_read_tokens; cache_write_tokens; provider_raw = [] }
      | Error _ as error, _, _, _, _ | _, Error _ as error, _, _, _ | _, _, Error _ as error, _, _ | _, _, _, Error _ as error, _ | _, _, _, _, Error _ as error -> error

let response_of_json value =
  match object_fields "canonical llm.Response" value with
  | Error _ as error -> error
  | Ok fields ->
      match require_version fields, required "response" "operation_key" fields, required "response" "status" fields,
             required "response" "output" fields, required "response" "service" fields, required "response" "usage" fields with
      | Ok (), Ok operation_key, Ok status, Ok output, Ok service, Ok usage ->
          (match string "response operation_key" operation_key, string "response status" status, list "response output" output with
           | Ok operation_key, Ok status, Ok output ->
               (match nonempty "response operation_key" operation_key, status_of_string status, map_result item_of_json output,
                      service_of_json service, usage_of_json usage with
                | Ok operation_key, Ok status, Ok output, Ok service, Ok usage ->
                    let operation_id = match optional "operation_id" fields with None -> Ok None | Some value -> Result.map Option.some (string "response operation_id" value) in
                    (match operation_id with
                     | Ok operation_id ->
                         Ok {
                           operation_key; operation_id; status; output;
                           route = { route_id = None; endpoint_id = None; api_family = None; requested_model = None; resolved_model = None };
                           service; usage;
                           cost = { currency = ""; reserved_microusd = 0L; actual_microusd = 0L; method_ = ""; catalog_version = "" };
                           provider = { response_id = None; request_id = None; generation_id = None; finish_reason = None; raw = [] };
                           continuation = None;
                           diagnostics = [];
                         }
                     | Error _ as error -> error)
                | Error _ as error, _, _, _, _ | _, Error _ as error, _, _, _ | _, _, Error _ as error, _, _ | _, _, _, Error _ as error, _ | _, _, _, _, Error _ as error -> error)
           | Error _ as error, _, _ | _, Error _ as error, _ | _, _, Error _ as error -> error)
      | Error _ as error, _, _, _, _, _ | _, Error _ as error, _, _, _, _ | _, _, Error _ as error, _, _, _ | _, _, _, Error _ as error, _, _ | _, _, _, _, Error _ as error, _ | _, _, _, _, _, Error _ as error -> error

let encode_request request = Ok (Bytes.of_string (Yojson.Safe.to_string (`Assoc [ ("api_version", `String api_version); ("request", request_to_json request) ])))
let encode_response response = Ok (Bytes.of_string (Yojson.Safe.to_string (`Assoc [ ("api_version", `String api_version); ("response", response_to_json response); ("metadata", `Assoc []) ])))

let decode_request bytes =
  parse_json (fun value -> match object_fields "llm.generate.v1 request" value with
    | Error _ as error -> error
    | Ok fields -> match require_version fields, required "llm.generate.v1 request" "request" fields with
      | Ok (), Ok request -> request_of_json request
      | Error _ as error, _ | _, Error _ as error -> error) bytes

let decode_response bytes =
  parse_json (fun value -> match object_fields "llm.generate.v1 response" value with
    | Error _ as error -> error
    | Ok fields -> match require_version fields, required "llm.generate.v1 response" "response" fields with
      | Ok (), Ok response -> response_of_json response
      | Error _ as error, _ | _, Error _ as error -> error) bytes

let request_codec = Temporal.Codec.make ~encoding:"json/plain" ~encode:encode_request ~decode:decode_request
let response_codec = Temporal.Codec.make ~encoding:"json/plain" ~encode:encode_response ~decode:decode_response
let generate_activity = Temporal.Activity.remote ~name:activity_name ~input:request_codec ~output:response_codec

type dispatcher = ?task_queue:string -> (request, response) Temporal.Activity.t -> request -> (response, Temporal.Error.t) result
let invoke_once ?task_queue ~dispatch input = dispatch ?task_queue generate_activity input

let one_shot_retry_policy =
  match Temporal.Activity.Retry_policy.make ~initial_interval:(Temporal.Duration.of_ms 1L)
          ~backoff_coefficient:1.0 ~maximum_interval:(Temporal.Duration.of_ms 1L)
          ~maximum_attempts:1 () with
  | Ok policy -> policy
  | Error error -> invalid_arg (Temporal.Error.message error)

let activity_dispatch ?task_queue activity input = Temporal.Activity.execute ?task_queue ~retry_policy:one_shot_retry_policy activity input
let execute ?task_queue input = invoke_once ?task_queue ~dispatch:activity_dispatch input
let workflow ?task_queue () = Temporal.Workflow.define ~name:workflow_name ~input:request_codec ~output:response_codec (fun input -> execute ?task_queue input)
