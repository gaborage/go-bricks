# ADR-166: `server.SetupMiddlewares` Is Unexported

**Status:** Accepted
**Date:** 2026-11-03
**Amends:** [ADR-034](adr_034_echo_boundary_types.md) (closes its Phase-1 scope note that kept `SetupMiddlewares` exported)
**Related:** [ADR-026](adr_026_zero_overhead_request_path.md) (the `observabilityEnabled` parameter) · [ADR-034](adr_034_echo_boundary_types.md) (no `echo.*` type on the consumer surface) · [ADR-135](adr_135_dead_public_surface.md) (dead public surface is deleted, not deprecated) · #2212

## Context

`server.SetupMiddlewares(e *echo.Echo, log, cfg, observabilityEnabled, healthPath, readyPath)`
registered the framework's middleware chain on any echo engine the caller passed. It was the last
exported door that took an `*echo.Echo`, so it let a consumer build their own engine with
`echo.New()` and give it the framework chain.

Every engine go-bricks builds now comes from the unexported `newEngine`, whose router is the
masking router that learns each accepted template's masked rendering at registration (#2212, L2
and L2e). An engine built outside the framework carries no such router. #2212 renders every
inbound path seat from that router, so on a foreign engine each path would print as the bare
`REDACTED`, or not at all, and nothing would say why. Inside the framework, `newServer` (which
`server.New` and `server.NewWithOptions` both reach) already called the unexported `setupMiddlewares` directly, which also takes the Server's tenant-owned
family record. The exported wrapper had no framework caller; its callers were the package's own
tests.

## Decision

**Remove `server.SetupMiddlewares`.** The chain is installed only by `newServer`, which
`server.New`, `server.NewWithOptions` and so the App reach, through the unexported
`setupMiddlewares`, on an engine `newEngine` built. A foreign engine can no longer get the
framework chain, so the mistake fails at compile time instead of rendering unusable paths at run
time. There is no replacement export and no shim.

This closes ADR-034's Phase-1 scope note, which kept `SetupMiddlewares` exported on echo-native
internals. One `echo.*` type remains on the consumer surface: `WrapHandler` still returns an
`echo.HandlerFunc`. It takes no engine, so it cannot install the chain on a foreign one, and it is
outside this decision.

## Alternatives considered

- **Keep it and check the engine's router at run time.** Rejected. It would turn a compile-time
  certainty into a startup or request-time failure, and keep an `*echo.Echo` parameter on the
  consumer surface that ADR-034 otherwise removed.
- **Deprecate first.** Rejected. ADR-135 and the project rule remove an obsolete path outright.

## Consequences

- **Breaking**, at compile time only: a call to `server.SetupMiddlewares` stops compiling. A
  service built through `server.New`, `server.NewWithOptions` or the App is unaffected and its
  chain is unchanged.
- Consumer middleware keeps its doors: `RouteRegistrar.Use`, a route group, and
  `app.GlobalMiddlewareRegisterer` (ADR-036). None of them moves.
- This link changes no request path, no middleware order and no rendering; the path seats move to
  the masking router in later #2212 links.

## References

- `server/middleware.go`: `setupMiddlewares`
- `server/server.go`: `newServer`, the only caller (reached by `New` and `NewWithOptions`)
- `server/handler.go`: `WrapHandler`, the remaining `echo.HandlerFunc` return
- `server/router.go`: `newEngine`
- See [migrations.md](migrations.md) `[C73.28]`.
