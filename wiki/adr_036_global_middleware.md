# ADR-036: Module-Contributed Global Middleware

**Status:** Accepted
**Date:** 2026-07-05

## Context

Applications need app-wide HTTP middleware — canonically an auth gate — that runs once
per request, cannot be skipped per-route, runs after tenant resolution but before handlers,
and skips the health/ready probes. The framework already runs exactly such a chain
internally (`SetupMiddlewares`: tenant resolution, rate limiting, recovery, ...) via echo's
root `e.Use`, but there was no consumer-facing seam into it.

The reachable seams were all wrong for this. `RouteRegistrar.Use` delegates to
`echo.Group.Use`, which is group-scoped and order-fragile: echo bakes group middleware into
a route at `Add` time, so it only applies to routes added *after* the `Use` call, and
multiple modules stack duplicate copies. `RootGroup()` / `ModuleGroup()` return sibling
groups whose middleware never crosses. Per-route middleware is skippable by definition.

## Decision

Add an optional duck-typed module interface, detected at startup like `RouteRegisterer` and
`MessagingDeclarer`:

```go
type GlobalMiddlewareRegisterer interface {
    GlobalMiddleware() []server.MiddlewareFunc
}
```

The framework collects implementers' middleware in `prepareRuntime` (after every module
`Init()`, before route registration and `Start`), so the middleware may capture
dependencies a module wired up in `Init()` (e.g. a keystore-backed token verifier). It is
registered once on the root engine chain via a new `*server.Server` method,
`RegisterGlobalMiddleware`, which wraps each with a health/ready probe skipper
(`CreateProbeSkipper`) and appends via `s.echo.Use`.

Registration goes on the **raw root chain** (`s.echo.Use`), not a group. Echo's root `Use`
recompiles the whole global chain (`buildRouterChains`) and applies to every request after
routing, independent of route-registration order — dissolving the group-scoping/order trap.
Appended after the built-in chain, global middleware lands at the innermost slot: after
tenant resolution, inside `Recover`, before the handler.

The app invokes the server method through an optional type assertion
(`globalMiddlewareRegistrar`) rather than adding a method to the exported `ServerRunner`
interface — keeping `ServerRunner` byte-identical and the change apidiff-additive (`feat:`).
If a module registers global middleware but the configured `ServerRunner` does not implement
the assertion, startup **fails closed** with an error: silently dropping a security gate is
unsafe, so the app refuses to start rather than serve unguarded traffic.

## Consequences

- Auth / API-key / audit gates install once, un-skippable, with the tenant already in context.
- Runs after rate-limit / timeout / gzip / bodyLimit (innermost slot): an unauthenticated
  request still consumes rate-limit budget. This is an auth-gate hook, not a general
  "run early" hook.
- Runs before per-route JOSE decryption (JOSE runs in the typed-handler leaf): a global gate
  authenticates on headers / token / tenant, never the decrypted body. Body-dependent
  authorization belongs at the handler.
- On a raw-response (Strangler-Fig) route a global rejection emits the standard envelope,
  not the legacy raw shape (the raw-mode marker is set in the leaf, after middleware) — the
  same property tenant middleware already has.
- Fires before 404/405, so unauthenticated requests to unknown paths get 401 (does not leak
  route existence). Only health/ready are exempt; `/_sys` and `/debug` are gated (their own
  CIDR/bearer checks still apply underneath).
- Ordering across multiple implementers is deterministic (module-registration order).

## Alternatives Considered

- **`app.Options` field / `app.Use()` method** — both build the middleware at
  app-construction time, before module `Init()`, so an auth verifier built from the
  keystore/DB is unavailable. Rejected in favor of the post-`Init` module interface.
- **A method on `ServerRunner`** — apidiff-incompatible for external implementers; the
  optional type assertion achieves the same wiring additively.
- **`RouteRegistrar.Use` / `RootGroup().Use`** — group-scoped and order-fragile; would
  silently miss routes and stack duplicates.

## Related

- [ADR-034](adr_034_echo_boundary_types.md) — the echo-free `MiddlewareFunc` / `HandlerContext` this builds on
- [ADR-026](adr_026_zero_overhead_request_path.md) — typed handlers register via `addEcho`; global middleware uses the raw root chain
- Design spec: `docs/superpowers/specs/2026-07-05-global-middleware-registerer-design.md`
