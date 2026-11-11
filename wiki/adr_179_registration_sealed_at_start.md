# ADR-179: Start Seals Every Route, Group and Global-Middleware Registration

**Status:** Accepted
**Date:** 2026-11-10
**Related:** [ADR-036](adr_036_global_middleware.md) (module-contributed global middleware) · [ADR-124](adr_124_server_level_duplicate_route_refusal.md) (duplicate route refusal) · [ADR-155](adr_155_reserved_engine_methods_refused.md) (reserved engine methods) · [ADR-164](adr_164_tenant_owned_route_family.md) (tenant-owned route family) · [ADR-168](adr_168_framework_route_roots_reserved.md) (framework route roots) · #2306

## Context

`Server.Start` seals route admission: it marks the server started and reads the recorded route
conflicts under the admission lock. Through v0.73.0 that seal only refused two cases afterwards:
a tenant-owned trespass (ADR-164) and a route under a framework root (ADR-168), both by panic.
Everything else still mutated the live echo engine after Start:

- a route added through any registrar, retained or fresh, was served and described;
- a `Group` or `Use` with middleware registered echo's catch-all routes and took effect;
- `RegisterGlobalMiddleware` installed middleware on the root chain of a serving engine.

Each of these raced request serving: echo's router and middleware chain are not safe for
concurrent mutation, and the race detector fires inside them. A late duplicate or reserved-method
route was recorded in `RouteConflicts()` and dropped (ADR-124, ADR-155), where nothing reads it.

The seal was also not atomic with the registrations it judged. Admission claimed a route under
its lock and added it to the engine after releasing it, so a route claimed before the seal could
reach the engine while Start served. `RegisterGlobalMiddleware` read the started flag without the
admission lock and kept an unsynchronized counter, so a call racing Start could install global
middleware beside a tenant-owned family after Start's `ErrTenantFamilyWithGlobalMiddleware` check
had passed. A typed route through a registrar the framework did not build (a decorator around its
registrar) was described before `Add` ran, so a refused registration left a descriptor behind.

## Decision

1. **Once Start has claimed the server, every registration panics at the call site** with an
   `error` matching the new `server.ErrRegistrationAfterStart` (`errors.Is`), naming the call:
   `RouteRegistrar.Add` and typed registration (`route GET /orders`), `Group`
   (`Group /orders`) and `Use` (`Use /api`) on every registrar the server hands out, including
   retained ones, fresh `ModuleGroup()`/`RootGroup()` results, nested groups and tenant-owned
   family registrars, and an installing `RegisterGlobalMiddleware`. `Group` and `Use` are refused
   even with no middleware. The refused call changes nothing: no route, descriptor, conflict,
   middleware or global-middleware count. A recovered caller's retry panics again.
2. **"Claimed" is ADR-164's definition:** the seal is the first thing Start does after its
   started flag, so a Start that returned a setup error, or followed a `Shutdown`, seals too.
3. **A late conflict panics instead of being recorded.** A late duplicate or reserved-method
   route, a trespass and a framework-root refusal panic with an error matching both
   `ErrRegistrationAfterStart` and `ErrDuplicateRoute` (its `*DuplicateRouteError` is recoverable
   with `errors.As`); none reaches `RouteConflicts()`. Global middleware after Start beside a
   family also matches `ErrTenantFamilyWithGlobalMiddleware`.
4. **One lock, one state.** A route's claim, description and engine add; a `Group` or `Use` and
   its catch-alls; a family's mount; and `RegisterGlobalMiddleware`'s install and count all run
   under the admission lock Start's seal takes. The seal returns the conflicts, the family
   prefixes and the global-middleware count from one read, and Start's duplicate-route and
   family-with-global-middleware checks judge that snapshot. A registration racing Start
   completes entirely before the seal or panics; the engine never changes after it. Lock order:
   admission, then the route registry, a family's catch-all lock and the echo engine. The lock
   itself landed in the commit before this one (`fix(server): make registration atomic with
   Start's seal`), which changed no accepted registration; both ship in the same release, so
   relative to v0.73.0 they are one change.
5. **The `RegisterHandler` fallback** for a registrar the framework did not build describes the
   route only after the registrar's `Add` returns.
6. **Still callable after Start:** an empty or all-nil `RegisterGlobalMiddleware` (a no-op),
   `ModuleGroup()`, `RootGroup()`, `RegisterReadyHandler`, `RouteConflicts()` and
   `GlobalMiddlewareCount()`.

Framework boot is unaffected: app startup registers the probes, the debug endpoints, scheduler
routes, global middleware and every module's routes before it calls Start.

**Out of scope:** `TenantOwnedGroup` after Start keeps the string panic ADR-164 gave it
(`server: tenant-owned group registration failed for "<prefix>": build tenant-owned families in
RegisterRoutes, before Start`); it does not match `ErrRegistrationAfterStart`. Also out of scope:
`RegisterReadyHandler`, which swaps a handler under its own lock and adds no route; any supported
way to add routes at runtime; the pre-Start conflict model; and the pre-Start double description
of a typed route through a decorator registrar.

## Alternatives considered

- **Keep late registration working and make the engine safe for concurrent mutation.** Rejected:
  it means wrapping echo's router in a lock on the request hot path, and a route added after
  Start's checks still bypasses the duplicate, trespass and family checks it would have failed.
- **Refuse only middleware-bearing `Group` and `Use`.** Rejected: a group without middleware is
  harmless on its own, but a refused registrar family is easier to reason about than one whose
  `Group` sometimes works; the maintainer decision (2026-11-09) refuses both.
- **Return an error instead of panicking.** Rejected: `RouteRegistrar` methods return nothing,
  and a late registration is a wiring defect, not a runtime condition (Fail Fast).
- **Record late conflicts and keep dropping them.** Rejected: after Start nothing reads the
  record, so the refusal was silent.

## Consequences

- **Breaking:** a route, `Group`, `Use` or installing `RegisterGlobalMiddleware` after Start,
  served or not, panics where it used to take effect. Register everything before Start.
- **Breaking:** a late duplicate or reserved-method route panics instead of being recorded in
  `RouteConflicts()`.
- The after-Start trespass and framework-root panics keep their text and sentinels and add
  `ErrRegistrationAfterStart`. The global-middleware-beside-family panic keeps
  `ErrTenantFamilyWithGlobalMiddleware` and adds the new sentinel, but its text now reads
  `server: registration after Start: RegisterGlobalMiddleware: ...` instead of
  `server: global middleware registered after Start: ...`.
- A typed route refused through a decorator registrar leaves no descriptor. Before Start a typed
  route through a decorator is still described twice; that is a separate issue.
- `TenantOwnedGroup` after Start keeps its own string panic (out of scope, above); its engine
  mount runs under the admission lock.

## References

- `server/route_conflicts.go`: `ErrRegistrationAfterStart`, `routeAdmission.admit`, `seal`, `claimFamily`
- `server/route_registrar.go`: `routeGroup.Group`, `routeGroup.Use`
- `server/global_middleware.go`: `RegisterGlobalMiddleware`, `GlobalMiddlewareCount`
- `server/handler.go`: `RegisterHandler`
- See [migrations.md](migrations.md) `[C74.8]`.
