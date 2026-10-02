# ADR-134: Remove the Dead Database Tracking Surface

**Status:** Accepted
**Date:** 2026-10-01
**Related:** [ADR-133](adr_133_remove_compatibility_shims.md) (compatibility shims, same sweep), [ADR-026](adr_026_zero_overhead_request_path.md) (`TrackDBOperation` as the single tracking dispatch)

## Context

`database/tracking.go` re-exports the internal tracking package
(`database/internal/tracking`) as public API. A sweep of the exported surface on 2026-10-01
found that most of those re-exports have no caller outside package `database`, and that one
of them exposes a whole wrapper the framework itself no longer uses:

1. **Ten dead re-exports.** The type aliases `TrackingContext`, `TrackedStatement`,
   `TrackedStmt`, `TrackedTransaction`, `TrackedTx`, the function aliases `TrackDBOperation`,
   `NewTrackingSettings`, `RegisterConnectionPoolMetrics`, and the constants
   `DefaultSlowQueryThreshold`, `DefaultMaxQueryLength`. No production code in either module
   uses them, and no document names them (ADR-026 names `TrackDBOperation` only as the
   internal dispatch, which stays). `TrackedStmt` and `TrackedTx` are second names for
   `TrackedStatement` and `TrackedTransaction`. The vendor packages call
   `tracking.RegisterConnectionPoolMetrics` directly. Only `database`'s own tests used the
   aliases.
2. **`TrackedDB` / `NewTrackedDB` and the `*sql.DB` wrapper behind them.** `tracking.DB`
   wraps a raw `*sql.DB`. It dates from the framework's first commits and was superseded by
   `tracking.Connection`, which wraps a `types.Interface` and is what `database.NewConnection`
   builds (`database/factory.go`). `tracking.DB` was reachable only through `NewTrackedDB`,
   and no document mentions it. Its prepared statements came back as
   `tracking.BasicStatement`, which duplicates the kept `wrapper.Statement`.

The deletion test asked of each name: if it disappeared, would any behavior be lost, or would
complexity reappear somewhere else? For the aliases, no: every one is a second name for
something consumers either never need (the tracking internals) or already get through
`NewTrackedConnection`, which drives `TrackDBOperation` itself. For `tracking.DB`, the
behavior it offers (tracked queries on a connection) is exactly what `tracking.Connection`
already does on the live path, so keeping it means maintaining and testing two wrappers for
one job.

## Decision

Delete the ten re-exports (stack link 1) and then `TrackedDB`, `NewTrackedDB`, `tracking.DB`
with its constructor and methods, and `tracking.BasicStatement` (stack link 2).

| Removed | Use instead |
| --- | --- |
| `database.TrackingContext`, `TrackedStatement`/`TrackedStmt`, `TrackedTransaction`/`TrackedTx`, `TrackDBOperation`, `NewTrackingSettings` | wrap a `types.Interface` with `database.NewTrackedConnection`; it builds the settings and context and calls the tracking dispatch itself |
| `database.RegisterConnectionPoolMetrics` | none needed: `database.NewConnection` registers the pool metrics for every vendor |
| `database.DefaultSlowQueryThreshold`, `DefaultMaxQueryLength` | the values (`200ms`, `1000`) inline, or leave `database.query.slow.threshold` / `database.query.log.max` unset to get them |
| `database.TrackedDB`, `database.NewTrackedDB(*sql.DB, …)` | `database.NewTrackedConnection(conn, log, cfg)` over a `types.Interface`; the raw-`*sql.DB` wrapper is gone |

Kept on purpose: `NewTrackedConnection` and `TrackedConnection` (the inbox and outbox tests
drive real stores through them, and `TrackedConnection` is a consumer's only nameable handle
on `SetServerInfo`/`Session`), `SetObservabilityEnabled`, `WithRepositoryMethod`,
`RepositoryMethodFromContext`, `WithExpectedError`. Inside `database/internal/tracking`,
`Connection`, `Statement`, `Transaction`, `NewSettings`, `TrackDBOperation`,
`RegisterConnectionPoolMetrics` and the constants stay. `database`'s own tests now import the
internal package directly, which the `database/` subtree allows.

## Alternatives considered

- **Keep the names with a `Deprecated:` marker.** Rejected. A deprecation notice keeps the
  surface and its tests alive with a reminder attached; the manifesto's Backward
  Compatibility principle rules out the layer itself.
- **Keep them until a major version.** Rejected. GoBricks is pre-1.0, and the manifesto
  accepts a justified breaking change that is documented in an ADR and a migrations atom.
- **Unexport instead of delete.** Rejected for the aliases: the internal names already exist.
  Rejected for `tracking.DB`: an unexported second wrapper still has to be maintained and
  tested, and nothing in the framework builds one.

## Consequences

- Compile break for any consumer that names a removed identifier. `go build ./... && go vet
  ./... && go vet -tags=integration ./...` finds every site.
- A consumer that tracked a raw `*sql.DB` through `NewTrackedDB` must wrap a `types.Interface`
  (what `database.NewConnection` returns) with `NewTrackedConnection` instead. There is no
  framework wrapper for a bare `*sql.DB` any more.
- Two stack links share this ADR (precedent: ADR-128 covered the three links of #1853). If
  review changes link 2's scope, link 2 appends an amendment here.

## References

- `database/tracking.go`, `database/internal/tracking/connection.go`,
  `database/internal/tracking/statement.go`, `database/factory.go`
- `wiki/migrations.md` E71 atoms; `.claude/skills/breaking-changes/SKILL.md`
