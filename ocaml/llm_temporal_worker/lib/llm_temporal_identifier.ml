module type S = sig
  type t = private string

  val of_string : string -> t
  val to_string : t -> string
end

module Make () : S = struct
  type t = string

  let of_string value = value
  let to_string value = value
end

module Operation_key = Make ()
module Operation_id = Make ()
module Model_selector = Make ()
module Resolved_model_id = Make ()
module Endpoint_id = Make ()
module Route_id = Make ()
module Continuation_handle = Make ()
module Provider_id = Make ()
module Endpoint_family = Make ()
module Api_family = Make ()
module Provider_response_id = Make ()
module Provider_request_id = Make ()
module Provider_generation_id = Make ()
module Tool_name = Make ()
module Tool_call_id = Make ()
module Tenant_id = Make ()
module Project_id = Make ()
module Actor_id = Make ()
module Blob_digest = Make ()
module Diagnostic_code = Make ()
module Cost_catalog_version = Make ()
module Temporal_task_queue = Make ()
module Make_nonempty () : S = struct
  type t = string
  let of_string value = if value = "" then invalid_arg "identifier must not be empty" else value
  let to_string value = value
end

module Query_execution_id = Make_nonempty ()
module Budget_policy_key = Make_nonempty ()
module Budget_generation_id = Make_nonempty ()
module Provider_model_id = Make_nonempty ()
module Window_key = Make_nonempty ()

module Checkpoint = struct
  type t = string
  let of_string value = if value = "" then Error "checkpoint must not be empty" else Ok value
  let of_string_exn value = match of_string value with Ok value -> value | Error message -> invalid_arg message
  let to_string value = value
end

module Query_cursor = struct
  type t = string
  let of_string value = if value = "" then Error "query cursor must not be empty" else Ok value
  let of_string_exn value = match of_string value with Ok value -> value | Error message -> invalid_arg message
  let to_string value = value
end

module Budget_stream_id = struct
  type t = string
  let unsigned value = value <> "" && String.for_all (fun c -> c >= '0' && c <= '9') value
  let of_string value =
    match String.split_on_char '-' value with
    | [milliseconds; sequence] when unsigned milliseconds && unsigned sequence -> Ok value
    | _ -> Error "budget stream id must use unsigned milliseconds-sequence spelling"
  let of_string_exn value = match of_string value with Ok value -> value | Error message -> invalid_arg message
  let to_string value = value
end

module Sha256_digest = struct
  type t = string
  let of_hex value =
    if String.length value <> 64 || String.exists (fun c -> not ((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) value then
      Error "sha256 digest must be 64 lowercase hexadecimal characters"
    else Ok value
  let of_hex_exn value = match of_hex value with Ok value -> value | Error message -> invalid_arg message
  let to_hex value = value
end
