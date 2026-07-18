# Architecture Decision Records

The first five decisions describe the current pre-release implementation.
Decisions 0006 through 0008 define the accepted initial-release design; their
status does not claim that the implementation has shipped. Because there has
been no release, these decisions replace the unreleased v1 contract in place.
A later post-release change adds a superseding ADR and an explicit compatibility
plan when required.

1. [Semantic IR and official SDKs](0001-semantic-ir-and-official-sdks.md)
2. [Public service classes](0002-service-classes.md)
3. [Durable ledger and retry boundary](0003-durable-ledger-and-retry-boundary.md)
4. [Redis shared state](0004-redis-shared-state.md)
5. [Streaming boundary](0005-streaming-boundary.md)
6. [Forkable conversation checkpoints](0006-forkable-conversation-checkpoints.md)
7. [PostgreSQL durable state, Redis budgets, and exact-response cache](0007-postgresql-authoritative-state-and-response-cache.md)
8. [Resumable provider operations and typed queries](0008-resumable-provider-operations-and-typed-queries.md)
