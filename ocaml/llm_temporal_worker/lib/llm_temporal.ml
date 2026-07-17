let api_version = "llm.temporal/v1"
let activity_name = "llm.generate.v1"
let workflow_name = "llm.generate.workflow.v1"

let codec_error format = Printf.ksprintf (fun message -> Temporal.Error.codec ~message) format

module Json = struct
  type t = string

  type scanner = {
    source : string;
    length : int;
    mutable position : int;
  }

  let fail scanner format =
    Printf.ksprintf
      (fun message -> Error (codec_error "invalid JSON at byte %d: %s" scanner.position message))
      format

  let is_space = function ' ' | '\n' | '\r' | '\t' -> true | _ -> false

  let rec skip_space scanner =
    if scanner.position < scanner.length && is_space scanner.source.[scanner.position] then begin
      scanner.position <- scanner.position + 1;
      skip_space scanner
    end

  let is_delimiter = function ' ' | '\n' | '\r' | '\t' | ',' | ']' | '}' -> true | _ -> false

  let parse_string scanner =
    let start = scanner.position in
    if scanner.position >= scanner.length || scanner.source.[scanner.position] <> '"' then
      fail scanner "expected string"
    else begin
      scanner.position <- scanner.position + 1;
      let rec loop () =
        if scanner.position >= scanner.length then fail scanner "unterminated string"
        else
          match scanner.source.[scanner.position] with
          | '"' ->
              scanner.position <- scanner.position + 1;
              let token = String.sub scanner.source start (scanner.position - start) in
              (try
                 match Yojson.Safe.from_string token with
                 | `String key -> Ok key
                 | _ -> assert false
               with Yojson.Json_error message ->
                 Error (codec_error "invalid JSON string: %s" message))
          | '\\' ->
              scanner.position <- scanner.position + 1;
              if scanner.position >= scanner.length then fail scanner "unterminated escape"
              else begin
                let escaped = scanner.source.[scanner.position] in
                scanner.position <- scanner.position + 1;
                if escaped = 'u' then begin
                  if scanner.position + 4 > scanner.length then fail scanner "truncated unicode escape"
                  else begin
                    scanner.position <- scanner.position + 4;
                    loop ()
                  end
                end else loop ()
              end
          | character when Char.code character < 0x20 -> fail scanner "control character in string"
          | _ ->
              scanner.position <- scanner.position + 1;
              loop ()
      in
      loop ()
    end

  let rec parse_value scanner =
    skip_space scanner;
    if scanner.position >= scanner.length then fail scanner "expected JSON value"
    else
      match scanner.source.[scanner.position] with
      | '{' -> parse_object scanner
      | '[' -> parse_array scanner
      | '"' -> Result.map (fun _ -> ()) (parse_string scanner)
      | _ -> parse_atom scanner

  and parse_object scanner =
    scanner.position <- scanner.position + 1;
    skip_space scanner;
    let seen = Hashtbl.create 8 in
    let rec members () =
      skip_space scanner;
      if scanner.position >= scanner.length then fail scanner "unterminated object"
      else if scanner.source.[scanner.position] = '}' then begin
        scanner.position <- scanner.position + 1;
        Ok ()
      end else
        match parse_string scanner with
        | Error _ as error -> error
        | Ok key ->
            if Hashtbl.mem seen key then fail scanner "duplicate object key %S" key
            else begin
              Hashtbl.add seen key ();
              skip_space scanner;
              if scanner.position >= scanner.length || scanner.source.[scanner.position] <> ':' then
                fail scanner "expected ':' after object key"
              else begin
                scanner.position <- scanner.position + 1;
                match parse_value scanner with
                | Error _ as error -> error
                | Ok () ->
                    skip_space scanner;
                    if scanner.position >= scanner.length then fail scanner "unterminated object"
                    else if scanner.source.[scanner.position] = ',' then begin
                      scanner.position <- scanner.position + 1;
                      members ()
                    end else if scanner.source.[scanner.position] = '}' then begin
                      scanner.position <- scanner.position + 1;
                      Ok ()
                    end else fail scanner "expected ',' or '}' in object"
              end
            end
    in
    members ()

  and parse_array scanner =
    scanner.position <- scanner.position + 1;
    skip_space scanner;
    let rec values () =
      skip_space scanner;
      if scanner.position >= scanner.length then fail scanner "unterminated array"
      else if scanner.source.[scanner.position] = ']' then begin
        scanner.position <- scanner.position + 1;
        Ok ()
      end else
        match parse_value scanner with
        | Error _ as error -> error
        | Ok () ->
            skip_space scanner;
            if scanner.position >= scanner.length then fail scanner "unterminated array"
            else if scanner.source.[scanner.position] = ',' then begin
              scanner.position <- scanner.position + 1;
              values ()
            end else if scanner.source.[scanner.position] = ']' then begin
              scanner.position <- scanner.position + 1;
              Ok ()
            end else fail scanner "expected ',' or ']' in array"
    in
    values ()

  and parse_atom scanner =
    let start = scanner.position in
    while scanner.position < scanner.length && not (is_delimiter scanner.source.[scanner.position]) do
      scanner.position <- scanner.position + 1
    done;
    if scanner.position = start then fail scanner "expected JSON value" else Ok ()

  let of_string source =
    let scanner = { source; length = String.length source; position = 0 } in
    match parse_value scanner with
    | Error _ as error -> error
    | Ok () ->
        skip_space scanner;
        if scanner.position <> scanner.length then
          Error (codec_error "invalid JSON at byte %d: trailing data" scanner.position)
        else
          try
            ignore (Yojson.Safe.from_string source);
            Ok source
          with Yojson.Json_error message -> Error (codec_error "invalid JSON: %s" message)

  let of_yojson value = of_string (Yojson.Safe.to_string value)
  let to_string value = value
  let to_yojson value = Yojson.Safe.from_string value
end

let object_fields ~context json =
  match Json.to_yojson json with
  | `Assoc fields -> Ok fields
  | _ -> Error (codec_error "%s must be a JSON object" context)

let required fields name =
  match List.assoc_opt name fields with
  | Some value -> Ok value
  | None -> Error (codec_error "missing required JSON field %S" name)

let required_string fields name =
  match required fields name with
  | Ok (`String value) -> Ok value
  | Ok _ -> Error (codec_error "JSON field %S must be a string" name)
  | Error _ as error -> error

let reject_unknown ~context ~allowed fields =
  match List.find_opt (fun (name, _) -> not (List.mem name allowed)) fields with
  | None -> Ok ()
  | Some (name, _) -> Error (codec_error "%s has unknown JSON field %S" context name)

let ensure_nonempty ~context value =
  if String.trim value = "" then Error (codec_error "%s must not be empty" context) else Ok ()

let validate_version fields =
  match required_string fields "api_version" with
  | Ok value when value = api_version -> Ok ()
  | Ok value -> Error (codec_error "unsupported api_version %S" value)
  | Error _ as error -> error

let request_fields =
  [ "api_version"; "operation_key"; "context"; "model"; "service_class";
    "service_class_fallbacks"; "portability"; "instructions"; "input"; "tools";
    "tool_policy"; "output"; "sampling"; "reasoning"; "continuation"; "extensions" ]

let response_fields =
  [ "api_version"; "operation_key"; "operation_id"; "status"; "output"; "route";
    "service"; "usage"; "cost"; "provider"; "continuation"; "diagnostics" ]

let validate_request_json json =
  match object_fields ~context:"canonical llm.Request" json with
  | Error _ as error -> error
  | Ok fields ->
      match reject_unknown ~context:"canonical llm.Request" ~allowed:request_fields fields with
      | Error _ as error -> error
      | Ok () ->
          match validate_version fields with
          | Error _ as error -> error
          | Ok () ->
              match required_string fields "operation_key" with
              | Error _ as error -> error
              | Ok operation_key ->
                  match ensure_nonempty ~context:"request operation_key" operation_key with
                  | Error _ as error -> error
                  | Ok () ->
                      match required_string fields "model" with
                      | Error _ as error -> error
                      | Ok model -> ensure_nonempty ~context:"request model" model

let validate_response_json json =
  match object_fields ~context:"canonical llm.Response" json with
  | Error _ as error -> error
  | Ok fields ->
      match reject_unknown ~context:"canonical llm.Response" ~allowed:response_fields fields with
      | Error _ as error -> error
      | Ok () ->
          match validate_version fields with
          | Error _ as error -> error
          | Ok () ->
              match required_string fields "operation_key" with
              | Error _ as error -> error
              | Ok operation_key ->
                  match ensure_nonempty ~context:"response operation_key" operation_key with
                  | Error _ as error -> error
                  | Ok () ->
                      match required_string fields "status" with
                      | Ok ("completed" | "tool_calls" | "refused" | "length" | "content_filtered") -> Ok ()
                      | Ok status -> Error (codec_error "response status %S is invalid" status)
                      | Error _ as error -> error

type generate_request = { request_json : Json.t }
type generate_response = { response_json : Json.t; operation_id : string option }

let request request_json =
  match validate_request_json request_json with
  | Ok () -> Ok { request_json }
  | Error _ as error -> error

let request_json request = request.request_json

let response ?operation_id response_json =
  match validate_response_json response_json with
  | Ok () -> Ok { response_json; operation_id }
  | Error _ as error -> error

let response_json response = response.response_json
let operation_id response = response.operation_id

let encode_request request =
  Ok (Bytes.of_string (Printf.sprintf "{\"api_version\":\"%s\",\"request\":%s}" api_version (Json.to_string request.request_json)))

let encode_response response =
  let operation_id =
    match response.operation_id with
    | None -> "{}"
    | Some value -> Printf.sprintf "{\"operation_id\":%s}" (Yojson.Safe.to_string (`String value))
  in
  Ok (Bytes.of_string (Printf.sprintf "{\"api_version\":\"%s\",\"response\":%s,\"metadata\":%s}" api_version (Json.to_string response.response_json) operation_id))

let decode_envelope ~context ~allowed bytes =
  match Json.of_string (Bytes.to_string bytes) with
  | Error _ as error -> error
  | Ok json ->
      match object_fields ~context json with
      | Error _ as error -> error
      | Ok fields ->
          match reject_unknown ~context ~allowed fields with
          | Error _ as error -> error
          | Ok () ->
              match validate_version fields with
              | Error _ as error -> error
              | Ok () -> Ok fields

let json_field fields name =
  match required fields name with
  | Error _ as error -> error
  | Ok value -> Json.of_yojson value

let decode_request bytes =
  match decode_envelope ~context:"llm.generate.v1 request" ~allowed:[ "api_version"; "request" ] bytes with
  | Error _ as error -> error
  | Ok fields ->
      match json_field fields "request" with
      | Error _ as error -> error
      | Ok json -> request json

let decode_response bytes =
  match decode_envelope ~context:"llm.generate.v1 response" ~allowed:[ "api_version"; "response"; "metadata" ] bytes with
  | Error _ as error -> error
  | Ok fields ->
      match json_field fields "response", json_field fields "metadata" with
      | (Error _ as error), _ -> error
      | _, (Error _ as error) -> error
      | Ok response_json, Ok metadata_json ->
          match object_fields ~context:"llm.generate.v1 response metadata" metadata_json with
          | Error _ as error -> error
          | Ok metadata ->
              match reject_unknown ~context:"llm.generate.v1 response metadata" ~allowed:[ "operation_id" ] metadata with
              | Error _ as error -> error
              | Ok () ->
                  match List.assoc_opt "operation_id" metadata with
                  | None -> response response_json
                  | Some (`String operation_id) -> response ~operation_id response_json
                  | Some _ -> Error (codec_error "response metadata operation_id must be a string")

let request_codec = Temporal.Codec.make ~encoding:"json/plain" ~encode:encode_request ~decode:decode_request
let response_codec = Temporal.Codec.make ~encoding:"json/plain" ~encode:encode_response ~decode:decode_response

let generate_activity =
  Temporal.Activity.remote ~name:activity_name ~input:request_codec ~output:response_codec

type dispatcher =
  ?task_queue:string ->
  (generate_request, generate_response) Temporal.Activity.t ->
  generate_request ->
  (generate_response, Temporal.Error.t) result

let invoke_once ?task_queue ~dispatch input = dispatch ?task_queue generate_activity input

let one_shot_retry_policy =
  match Temporal.Activity.Retry_policy.make
          ~initial_interval:(Temporal.Duration.of_ms 1L)
          ~backoff_coefficient:1.0
          ~maximum_interval:(Temporal.Duration.of_ms 1L)
          ~maximum_attempts:1
          () with
  | Ok policy -> policy
  | Error error -> invalid_arg (Temporal.Error.message error)

let activity_dispatch ?task_queue activity input =
  Temporal.Activity.execute ?task_queue ~retry_policy:one_shot_retry_policy activity input

let execute ?task_queue input = invoke_once ?task_queue ~dispatch:activity_dispatch input

let workflow ?task_queue () =
  Temporal.Workflow.define
    ~name:workflow_name
    ~input:request_codec
    ~output:response_codec
    (fun input -> execute ?task_queue input)
