(* This project intentionally lives outside the package directory.  It models
   the smallest downstream Dune consumer of the public library. *)
open Llm_temporal

let () =
  let request =
    Request.make
      ~operation_key:(Operation_key.of_string "consumer-smoke")
      ~model:(Model_selector.of_string "arbitrary-model")
      ~service_class:Standard
      ~input:[ Message { actor = Human; content = [ Text "hello" ] } ]
      ()
  in
  assert (Operation_key.to_string request.operation_key = "consumer-smoke")
