# ADR-133: Remove Compatibility Shims from the Public API

**Status:** Accepted
**Date:** 2026-10-01
**Related:** [ADR-034](adr_034_echo_boundary_types.md) (`NewHandlerContextForTest`), #462 (the vendor `Statement`/`Transaction` aliases), #662 (`MessagingClientFactoryWithOptions`)

## Context

The CLAUDE.md manifesto's Backward Compatibility principle says GoBricks does not keep
compatibility layers, fallbacks or migration shims in its own API. Old paths are removed and
the break is documented (ADR + `wiki/migrations.md`). A sweep of the exported surface on
2026-10-01 found five places that break this rule. Each one describes itself as a
compatibility layer in its own doc comment:

1. **`logger.WithAMQPCounter` / `logger.WithDBCounter`:** "Retained for backward
   compatibility". Both bodies are `return WithRequestCounters(ctx)`. Production code seeds
   the per-request counters only through `WithRequestCounters`.
2. **`app.FactoryResolver.MessagingClientFactory(connectionTimeout, maxPublishAttempts)`:**
   marked `Deprecated:`, "kept for backward compatibility (its signature cannot change without
   breaking apidiff)". It packs its two arguments into `MessagingClientFactoryOptions` and calls
   `MessagingClientFactoryWithOptions`. The rest of the options get no value, so a client built
   this way silently loses `ReadyTimeout`, `PublishTimeout` and the four reconnect delays.
3. **`server.RouteRegistry.AddRoute` / `RoutesByModule`:** aliases of `Register` / `ByModule`
   kept "for consistency with test expectations". Only the package's own tests called them.
4. **`server.NewHandlerContextForTestWithOptions`:** a second constructor that exists only
   because "adding a variadic changes a function's type identity — apidiff classifies it as
   incompatible". `NewHandlerContextForTest` was a one-line forward to it.
5. **`postgresql.Statement` / `postgresql.Transaction` and `oracle.Statement` /
   `oracle.Transaction`:** type aliases of `database/internal/wrapper`, added by #462 so the
   names from before the wrapper extraction would keep compiling. Nothing outside the vendor
   packages names them. Consumers already get `types.Statement` / `types.Tx` from `Prepare` /
   `Begin`, and the wrapper keeps `*sql.Stmt` / `*sql.Tx` unexported, so a type assertion to
   the concrete alias gave the caller nothing.

The deletion test asked of each name: if it disappeared, would any behavior be lost, or would
complexity reappear somewhere else? For all five the answer is no. Each one forwards to a
name that stays, or (item 2) forwards less than that name does. Keeping them gives the API two
spellings for one operation, and in item 2's case the older spelling is the worse one.

## Decision

Delete all five. The remaining name takes over each role:

| Removed | Use instead |
| --- | --- |
| `logger.WithAMQPCounter(ctx)`, `logger.WithDBCounter(ctx)` | `logger.WithRequestCounters(ctx)` |
| `(*app.FactoryResolver).MessagingClientFactory(ct, n)` | `MessagingClientFactoryWithOptions(app.MessagingClientFactoryOptions{ConnectionTimeout: ct, MaxPublishAttempts: n})` |
| `(*server.RouteRegistry).AddRoute(d)`, `.RoutesByModule(m)` | `.Register(d)`, `.ByModule(m)` |
| `server.NewHandlerContextForTestWithOptions(w, r, cfg, opts...)` | `server.NewHandlerContextForTest(w, r, cfg, opts...)` |
| `postgresql.Statement`/`Transaction`, `oracle.Statement`/`Transaction` | `types.Statement` / `types.Tx` (what `Prepare` / `Begin` return) |

`NewHandlerContextForTest` takes over the variadic signature
`(w http.ResponseWriter, r *http.Request, cfg *config.Config, opts ...TestContextOption)`. Every
existing call compiles unchanged. Only a value of its function type changes (a
`func(http.ResponseWriter, *http.Request, *config.Config) HandlerContext` variable).

Kept on purpose: `WithRequestCounters` and every `Increment*`/`Get*`/`Add*` counter function;
`MessagingClientFactoryWithOptions` and the `Options.MessagingClientFactory` field;
`RouteRegistry.Register/Routes/ByModule/ByPath/RoutesByMethod/Clear/Count`;
`TestContextOption` and `WithRouteTemplate`; `wrapper.Statement`/`wrapper.Transaction`. The
`database.PostgreSQL` / `database.Oracle` re-exports in `database/vendors.go` were considered
and also stay. They are documented consumer API (`database.NewQueryBuilder(database.Oracle)` in
`wiki/database.md`) used across the framework's integration tests, so removing them would push
a `database/types` import onto every caller. That is a cost, not a shim being retired.

## Alternatives considered

- **Keep each name with a `Deprecated:` marker.** Rejected. Item 2 already had one and nothing
  migrated. A deprecation notice is a compatibility layer with a reminder attached, and the
  manifesto rules out the layer itself.
- **Keep the names until a major version.** Rejected. GoBricks is pre-1.0, and the manifesto
  puts breaking changes in minors and documents each one in an ADR and a migrations atom. A
  deferral would keep the two-spelling surface with no date to remove it.
- **Unexport instead of delete.** Rejected. None of the five has an in-package caller that
  needs a private spelling. The surviving name already does the job.

## Consequences

- Compile break for any consumer that calls a removed name. `go build ./... && go vet ./...`
  finds every call site, `_test.go` files included. Each replacement is a one-token rename or
  an options literal (atoms in E71).
- A consumer that called `MessagingClientFactory(ct, n)` and switches to
  `MessagingClientFactoryWithOptions` with only those two fields gets exactly what it had
  before. To pick up `ReadyTimeout` or the reconnect delays it must now set them explicitly,
  which the old method could not do.
- `apidiff` reports each removal. The PR title's `!` marker is the CI escape hatch.
- `NewHandlerContextForTest`'s type identity changes. A test that stores the constructor in a
  variable of the old three-parameter function type must widen that type.

## References

- CLAUDE.md "Developer Manifesto": Backward Compatibility
- `logger/context.go`, `app/factory_resolver.go`, `server/descriptor.go`, `server/handler.go`,
  `database/postgresql/connection.go`, `database/oracle/connection.go`
- `wiki/migrations.md` E71 atoms; `.claude/skills/breaking-changes/SKILL.md`
