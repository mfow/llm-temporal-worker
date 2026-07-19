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
module Query_execution_id = Make ()
module Budget_policy_key = Make ()
module Budget_generation_id = Make ()

module Checkpoint = struct
  type t = string
  let of_string value = value
  let to_string value = value
end

module Query_cursor = struct
  type t = string
  let of_string value = value
  let to_string value = value
end

module Budget_stream_id = struct
  type t = string
  let of_string value = value
  let to_string value = value
end

module Sha256_digest = struct
  type t = string
  let of_hex value = value
  let to_hex value = value
end
