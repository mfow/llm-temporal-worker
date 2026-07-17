(** Nominal wrappers for protocol identifiers.  Values remain JSON strings on
    the wire, but distinct OCaml types prevent unrelated identifiers from
    being interchanged accidentally. *)

module type S = sig
  type t = private string

  val of_string : string -> t
  val to_string : t -> string
end

module Operation_key : S
module Operation_id : S
module Model_selector : S
module Resolved_model_id : S
module Endpoint_id : S
module Route_id : S
module Continuation_handle : S
module Provider_id : S
module Endpoint_family : S
module Api_family : S
module Provider_response_id : S
module Provider_request_id : S
module Provider_generation_id : S
module Tool_name : S
module Tool_call_id : S
module Tenant_id : S
module Project_id : S
module Actor_id : S
module Blob_digest : S
module Diagnostic_code : S
module Cost_catalog_version : S
module Temporal_task_queue : S
