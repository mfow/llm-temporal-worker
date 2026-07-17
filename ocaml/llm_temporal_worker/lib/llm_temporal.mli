(** A narrow OCaml boundary for one durable, non-streaming LLM operation.

    The Go worker owns provider credentials and execution.  This library only
    schedules its stable [llm.generate.v1] Activity from a Temporal workflow. *)

val api_version : string
val activity_name : string
val workflow_name : string

module Json : sig
  (** Canonical JSON that has been parsed once, rejects duplicate object keys,
      and is retained without a lossy OCaml value conversion. *)
  type t

  val of_string : string -> (t, Temporal.Error.t) result
  val of_yojson : Yojson.Safe.t -> (t, Temporal.Error.t) result
  val to_string : t -> string
  val to_yojson : t -> Yojson.Safe.t
end

(** The opaque canonical [llm.Request] JSON used inside the versioned Go
    Activity envelope.  Its full provider-neutral schema stays owned by the Go
    module so the wrapper cannot drift from the activity contract. *)
type generate_request

(** The opaque canonical [llm.Response] JSON and Activity metadata returned by
    the Go worker. *)
type generate_response

val request : Json.t -> (generate_request, Temporal.Error.t) result
val request_json : generate_request -> Json.t

val response : ?operation_id:string -> Json.t -> (generate_response, Temporal.Error.t) result
val response_json : generate_response -> Json.t
val operation_id : generate_response -> string option

val request_codec : generate_request Temporal.Codec.t
val response_codec : generate_response Temporal.Codec.t

val generate_activity : (generate_request, generate_response) Temporal.Activity.t

(** One dispatcher invocation.  This is deliberately a single call rather
    than a polling, continuation, streaming, or retry loop. *)
val invoke_once :
  ?task_queue:string ->
  dispatch:(?task_queue:string ->
    (generate_request, generate_response) Temporal.Activity.t ->
    generate_request ->
    (generate_response, Temporal.Error.t) result) ->
  generate_request ->
  (generate_response, Temporal.Error.t) result

(** Schedules [generate_activity] with a maximum of one Temporal attempt. *)
val execute :
  ?task_queue:string ->
  generate_request ->
  (generate_response, Temporal.Error.t) result

(** A local workflow definition that performs exactly one [execute] call.
    Register the returned definition on the OCaml worker that owns the
    workflow task queue; the Go worker must be reachable on [task_queue] (or
    the same queue when it is omitted). *)
val workflow :
  ?task_queue:string ->
  unit ->
  (generate_request, generate_response) Temporal.Workflow.t
