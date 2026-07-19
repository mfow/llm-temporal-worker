# Checkpoint graph materializer

The worker now has a pure Go checkpoint graph contract in `golang/state`.
`CheckpointGraph` publishes immutable root and child nodes, while
`Materialize` walks parent links from a tenant-scoped leaf and applies every
ancestor delta, response, and sparse settings patch in order. A parent is
never mutated, so any number of operation-keyed sibling branches can share it.

`state.Patch` keeps omitted, `Set`, and `Clear` distinct. Collection `Set`
replaces the complete collection and `Clear` restores the documented root
default. Materialization returns a defensive `MaterializedState`, including
the effective model configuration and the current tool-call frontier.

The implementation enforces bounded depth, row, item, and canonical-byte
limits. It validates tool-call/result pairing across checkpoint boundaries,
rejects reused call IDs, and refuses a child that starts a new turn while an
ancestor still has unmatched tool calls. Optional self-contained snapshots
carry the same digest as a full replay; their use does not change handles or
the logical graph.

This is the durable-state-independent slice of the design. PostgreSQL
publication, blob ordering, Activity payload wiring, and retention remain
separate concerns and must use this contract rather than reimplementing
materialization in an adapter.

