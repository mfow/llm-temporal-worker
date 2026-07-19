(** Exact, non-negative USD values with at most 18 fractional digits.

    The representation is a checked decimal string; it never passes through a
    binary floating point value.  Values are bounded to PostgreSQL
    [NUMERIC(38,18)]. *)
type t

val zero : t
val of_string : string -> (t, string) result
val to_string : t -> string
val compare : t -> t -> int
val add : t -> t -> t
val add_checked : t -> t -> (t, string) result
