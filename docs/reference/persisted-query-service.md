# Persisted control-plane query composition

The runtime now exposes an explicit `runtime.NewPersistedQueryService`
composition for the PostgreSQL-backed provider-status, model-inventory, and
credit-status query families. It binds every page to the immutable
configuration snapshot digest and uses the storage pages only after the
control layer has authenticated the tenant scope and signed cursor.

Deployments must supply all three security/observability seams:

- `control.AuthorizeFunc` for tenant/project/actor authorization;
- a keyed `control.CursorCodec` for scope/filter/horizon-bound cursors; and
- a `control.AuditFunc` that records the completed query before the Activity
  returns.

The production factory accepts these choices through
`ProductionFactoryOptions.QueryServiceBuilder`. It does not invent keys,
authorization, or an audit repository. A PostgreSQL closer may expose the
read repositories through `PostgresQueryRepositoriesSource`; missing
repositories remain a permanent unsupported-capability response rather than
an empty result.

The composition is persisted-only. Refresh requests are rejected until an
explicit management refresh adapter is supplied. Budget status and spend
summary remain fail-closed because their Redis budget-generation and completed
operation-cost repositories have not been composed yet. No provider call or
streaming path is used by these Temporal query Activities.

Example deployment wiring:

```go
factory, _ := runtime.NewProductionEngineFactory(runtime.ProductionFactoryOptions{
    QueryServiceBuilder: func(ctx context.Context, snapshot *config.Snapshot, repos runtime.PostgresQueryRepositories) (activity.QueryService, error) {
        return runtime.NewPersistedQueryService(snapshot, repos, runtime.PersistedQueryOptions{
            Authorize: authorizeTenant,
            Cursor:    &control.CursorCodec{Key: cursorKey, TTL: 15 * time.Minute},
            Audit:     auditQuery,
        })
    },
})
```

The builder receives the same immutable snapshot used to construct the worker;
it must not resolve credentials or mutate that snapshot.
