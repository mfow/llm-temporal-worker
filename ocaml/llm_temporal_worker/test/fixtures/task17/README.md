# Task 17 protocol golden fixtures

These JSON files are the small cross-language conformance corpus for the
Generate, Compact, and Query v1 wire contracts.  They are intentionally
canonical (the field order is the order emitted by the OCaml codec) so a
Go-side codec can consume the same bytes and compare canonical re-encoding.

Positive fixtures must decode and re-encode byte-for-byte after JSON
canonicalisation.  Negative fixtures exercise the closed-object and exact
decimal gates; both implementations must reject them.

The legacy pre-Task-17 response model is not represented here.  In particular,
v1 settings temperature is always an exact decimal string value and never a
JSON number.
