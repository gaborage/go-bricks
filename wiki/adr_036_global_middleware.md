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
`RegisterGlobalMiddleware`, which wraps each with a health/ready probe skipper and appends via
`s.echo.Use`.

**Amendment (2026-09-26).** The probe exemption is keyed on the route the ROUTER matched, not on
the request URL, and it covers only the methods the probes answer. `RegisterGlobalMiddleware` wraps
each middleware with the internal, template-keyed predicate (`newProbeSkipper` over
`isProbeRequest`, `server/probe_skip.go`), which the four other framework seats — OTel, tenant
resolution, the forwarded-client-cert identity and the access logger — read as well, so one
decision serves them all. A URL-keyed exemption exempted requests the probe routes never served:
echo's router matches the ESCAPED path, so `<base>/%72eady` decodes onto the ready path while
routing to a module param or wildcard route; route registration is per method+path, so a module may
own a non-probe method on a probe path; and a global middleware earlier in the chain can rewrite
`r.URL.Path` outright. Because a global middleware is the documented seat for cross-cutting auth,
each of those ran consumer code with the gate skipped. The template cannot be moved by any of the
three, and it identifies the probe HANDLER because the server refuses a module that claims a
probe path (see the amendment below). `CreateProbeSkipper` remains exported for consumer
middleware built on `server.SkipperFunc`, and answers from the same key: echo stamps the matched
template on the request as `r.Pattern`, with the raw path as the fallback when no template was
stamped. Consumer exemptions written inside a middleware body follow the same rule — match
`c.RouteTemplate()`, never `c.Request().URL.Path` (see [global_middleware.md](global_middleware.md)).

**Amendment (2026-09-27, #1818).** That refusal now lives in the server itself, without `app`; see
[ADR-124](adr_124_server_level_duplicate_route_refusal.md).

Registration goes on the **raw root chain** (`s.echo.Use`), not a group. Echo's root `Use`
recompiles the whole global chain (`buildRouterChains`) and applies to every request after
routing, independent of route-registration order — dissolving the group-scoping/order trap.
Appended after the built-in chain, global middleware lands at the innermost slot: after
tenant resolution, inside `Recover`, before the handler.

The app invokes the server method through an optional (inline) type assertion rather than
adding a method to the exported `ServerRunner` interface — keeping `ServerRunner`
byte-identical and the change apidiff-additive (`feat:`).
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
