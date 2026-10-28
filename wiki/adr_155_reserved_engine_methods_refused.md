# ADR-155: Module Route Doors Refuse Echo's Reserved Engine Methods

**Status:** Accepted
**Date:** 2026-10-28
**Issue:** #2222

## Context

Echo gives two method strings a meaning of its own. `echo.RouteNotFound`
(`"echo_route_not_found"`) is the route that answers when nothing else matches at its path, and
`echo.RouteAny` (`"echo_route_any"`) matches every request method at its path. `Group.Use`, which
`Group(prefix, mw...)` also calls, registers `RouteNotFound` at `<prefix>` and `<prefix>/*` with
overwrite allowed, so a middleware-bearing group runs its middleware on unmatched sub-paths
([migrations.md](migrations.md) `[C51.1]`). The `/_sys` IP-range gate and the debug gates rely on this.

Every go-bricks route door accepted any method string: `RouteRegistrar.Add` on `ModuleGroup`,
`RootGroup` and every registrar `Group` returns, and `server.RegisterHandler` with the typed
helpers built on it. A module that registered `echo_route_not_found` at `/partner/v2/*` beside a
middleware-bearing group at `/partner/v2` and that group's catch-all replaced each other in
registration order. Registered after the group, the module handler answered unmatched sub-paths
and the group middleware never ran. Registered before it, the group's `Use` replaced the module
handler. `echo_route_any` at the same template answered every method, in either order, without
the group middleware. The catch-alls never pass through the duplicate-route tracker, so nothing
was recorded. The module's descriptor reached `DefaultRouteRegistry` with the sentinel as its
method, and from there `PostRegisterRoutes`, the startup route log and OpenAPI.

[ADR-124](adr_124_server_level_duplicate_route_refusal.md) kept echo's catch-alls outside the
tracker on the premise that they "serve no GET/HEAD handler and no module handler". While a door
accepted the sentinel, that premise was false.

## Decision

- **Admission refuses the two sentinel methods before keying.** `routeAdmission.admit` is the
  one step every door goes through (ADR-124 amendment), so the check exists once. A route whose
  method is exactly `echo.RouteNotFound` or `echo.RouteAny` is recorded as a `RouteConflict`
  whose `Duplicate` is the refused registrant, with no `First` and no `FirstPath`. The match is
  case-sensitive, as echo's is. The route is neither described nor added, and it never claims a
  router node, so it cannot turn a later registration into a duplicate. The guarantee covers the
  registrars the framework builds (`ModuleGroup()`, `RootGroup()`, every registrar their `Group`
  returns) and the probes. `RegisterHandler` given a registrar the framework did not build (a test
  fake) describes the route and calls that registrar's own `Add`, with no admission step, so
  neither the refusal nor the descriptor rule applies there.
- **`RouteConflict` gains `Reason`.** Every recorded conflict sets one:
  `RouteConflictReasonDuplicate` (`"duplicate"`) or `RouteConflictReasonReservedMethod`
  (`"reserved engine method"`). No reader may treat an empty `Reason` as a duplicate.
- **One error, one sentinel.** `Server.Start` fails with `*server.DuplicateRouteError`, as for a
  duplicate, before either listener binds. App startup fails the same way before
  `PostRegisterRoutes` when its server reports `RouteConflicts()`; the framework's server does, and
  an injected `ServerRunner` that does not is refused at its own `Start`, as a duplicate is under
  ADR-124.
  `errors.Is(err, server.ErrDuplicateRoute)` holds for both reasons. An error holding only
  duplicates reads byte for byte as before. When any conflict has another reason, the head reads
  `route registration refused (N conflict(s))`, so a reserved refusal is never called a
  duplicate. A reserved line reads
  `<method> <template> — reserved engine method, refused: <handler> (<package>)`.
- **Echo's own catch-alls are unaffected.** `Group.Use` registers them on the echo group
  directly, never through admission. The engines keep `NoGroupAutoRegister404Routes` off, so
  `[C51.1]` holds as it is.
- **No replacement.** Per-module custom 404 handlers are unsupported, and go-bricks provides no
  replacement. An unmatched path answers the framework's own 404, after the group middleware
  when a middleware-bearing group covers it.

## Alternatives considered

**A separate sentinel error, such as `ErrReservedRouteMethod`.** Rejected. It adds public surface,
and a startup check would have to match two sentinels. `Reason` already tells the two refusals
apart for `errors.As` callers, and `errors.Is(err, server.ErrDuplicateRoute)` stays the one
question to ask of a refused startup.

**An allowlist of standard HTTP methods.** Rejected. Custom methods, such as WebDAV-style ones,
are legitimate routes. Only these two strings carry an engine meaning.

**Building the engines with `NoGroupAutoRegister404Routes`.** Rejected. It would change what an
unauthenticated not-found under a protected group answers, and `[C51.1]` promises that the group
middleware runs there.

## Consequences

- **Breaking for a module that registers either sentinel.** It used to boot, with its handler
  replacing or replaced by a group's catch-all, or answering every method at its template. Now
  `Start` and app startup refuse. Migration is [migrations.md](migrations.md) `[C73.12]`.
- **ADR-124's catch-all premise is enforced.** No module handler can occupy a catch-all node, so
  the `/_sys` IP-range gate and the debug gates cover unmatched sub-paths whatever the modules
  register.
- **Source-breaking for unkeyed `RouteConflict` literals.** `RouteConflict` gains `Reason`, so an
  unkeyed `server.RouteConflict{...}` literal stops compiling until its fields are keyed. A
  literal built by hand must set `Reason`: one left empty renders with the refusal head, not as a
  duplicate.
- **A refused sentinel is never described.** It is absent from `DefaultRouteRegistry`,
  `PostRegisterRoutes`, the `server.logroutes` startup log and OpenAPI.
- **A sentinel registered after `Start` is dropped.** As with a late duplicate, it is visible only
  through `RouteConflicts()`.

## References

- [ADR-124](adr_124_server_level_duplicate_route_refusal.md): the duplicate-route refusal path and
  the admission step this check lives in
- [startup_defaults.md](startup_defaults.md#duplicate-route-detection): behavior and error shape
- `server/route_conflicts.go` (`RouteConflictReason`, `routeAdmission.claim`,
  `DuplicateRouteError`)
