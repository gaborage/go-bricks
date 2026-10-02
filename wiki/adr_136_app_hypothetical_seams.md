# ADR-136: `app` Drops Its Hypothetical Public Seams

- **Status**: Accepted
- **Date**: 2026-10-01
- **Related**: [ADR-066](adr_066_readiness_one_module.md) (amended: `Prober` is no longer the exported seam) · [ADR-091](adr_091_streams_opt_in_registration.md) (amended: Decision item 1 loses its exported alias) · [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) (the `HealthStatus` SECURITY rule, kept)

## Context

A shallow-module sweep of `app` found five exported names with no variation behind them.
Each one failed the deletion test: removing it changes nothing a consumer can do, because
either nothing calls it, nothing accepts it, or no code outside the module can satisfy it.

1. **`TimeoutProvider`, `StandardTimeoutProvider`, `Options.TimeoutProvider`.** The only
   adapter was `return context.WithTimeout(parent, timeout)`. Its godoc said the seam existed
   "for testing", but its one test double returned a real 10s context and only recorded the
   10s, which `TestShutdownTimeouts` already pins through `shutdownTimeouts()`. The inner
   shutdown timeout is config-driven (`server.timeout.shutdown`).
2. **`SignalHandler.WaitForSignal`.** `waitForShutdownOrServerError` calls only `Notify` and
   selects on the channel itself. Nothing called `WaitForSignal`.
3. **`app.Prober`.** Its own godoc said only `probeDescription` implements it and there is no
   registration door for a foreign one. No parameter, field or map named it; only a compile
   check did.
4. **`app.RegisterStreamRuntime` and the `app.StreamRuntime` alias.** `streamruntime.Runtime`'s
   methods take and return internal types, so no code outside the module can implement it.
   The one real adapter, `messaging/streams`, calls `streamruntime.Register` directly from
   `init`, and a second registration panics.
5. **`App.MessagingDeclarations()`.** Its doc ("used by tenant managers to replay
   infrastructure") was stale: replay and validation read the unexported field. The getter
   handed out a mutable `*messaging.Declarations` the framework never read back.

## Decision

Delete all five. The shutdown path calls `context.WithTimeout` directly, and the resolver
becomes `resolveSignalHandler`. `SignalHandler` keeps `Notify` alone: that is the real test
seam, which lets a test drive shutdown without signaling its own process. `Prober`'s SECURITY
paragraph (which `HealthStatus` fields reach the log or `<debug.pathprefix>/health-debug`, and
that none reaches the unauthenticated `/ready` body) moves onto `HealthStatus`, so it keeps
one home. The unexported stream helpers take `streamruntime.Runtime` directly.
`ErrStreamsNotLinked`, the `HeldMessage`/`HoldLedger`/`HoldReplayer` aliases, the
`internal/streamruntime` seam, `DBManager()` and `CacheManager()` stay.

## Alternatives considered

- **Keep each name with `Deprecated:`.** Rejected: a deprecation implies a supported
  migration window, and none of these names has a use to migrate from.
- **Keep them until a major version.** Rejected: GoBricks breaks its own surface when
  justified (Backward Compatibility principle), and an exported seam with no variation is a
  standing invitation to depend on nothing.
- **Unexport `TimeoutProvider` as a func field.** Kept as the fallback if the lifecycle tests
  could not observe shutdown without the double. They can: both observe the server's
  `Shutdown` count and the closers' expectations, so the field was not needed.

## Consequences

- Compile-breaks, caught by `go build`, for code that named any of the five. A consumer
  `SignalHandler` that implements `WaitForSignal` still compiles; the method is never called.
- `app.HealthStatus` is now an exported type no exported API returns or accepts. It stays as
  the verdict type the readiness judge renders and the home of its SECURITY rule.
- A future consumer readiness door would define its own contract
  (`.out-of-scope/readiness-contribution-door.md`).

## References

- [migrations.md](migrations.md) `[C71.8]`–`[C71.12]`
- `app/lifecycle.go` (`App.Run`), `app/app.go` (`resolveSignalHandler`),
  `app/health.go` (`HealthStatus`), `app/stream_runtime.go`
