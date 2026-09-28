# ADR-124: The Server Keeps the First Registration of a Route and Refuses to Start on a Duplicate

**Status:** Accepted
**Date:** 2026-09-27
**Issue:** #1818

## Context

Echo v5's router refuses a second registration of one method + path only when
`AllowOverwritingRoute` is off. `echo.New()` turns it on, and go-bricks builds its engines with
`echo.New()`, so `Add` replaced the handler of an existing route and kept the route template.
The duplicate-route check lived only in `app` (`checkRouteConflicts`), which failed startup on a
recorded conflict. A service wiring `server.New` and `ModuleGroup()` itself booted with the later
handler serving and the first one dead on arrival.

The tracker behind that check keyed a route on its literal template string. Echo keys a route on
its router node, where a parameter's or wildcard's name is not part of the identity:
`GET /users/:id` then `GET /users/:uid` is one route, and the second replaced the first silently
even under `app`.

The health/ready probe exemption is keyed on the route template the router matched
(ADR-036, GHSA-h4jw follow-up). That template identifies the probe HANDLER only while no module
can take over a probe path. An overwrite keeps the template and swaps the handler, so the
exemption rested on a check that `app` alone ran.

## Decision

- **Key the tracker on echo's node identity.** `routeNodeKey` mirrors `DefaultRouter.Add`: a
  missing leading slash is added, an unescaped `:` starts a parameter anywhere in the path and its
  name (up to the next `/`) is dropped, an escaped `\:` is kept literally, and the first `*`
  outside a parameter name ends the path. `RouteConflict.Path` stays the duplicate's literal
  template; `RouteConflict.FirstPath` carries the first registration's, and the error line shows it
  only when the two differ.
- **The first registration wins for every route registered through the server's registrars and
  for the probe routes.** `routeConflictTracker.record` reports whether the route is new.
  `addEcho`, `RouteRegistrar.Add` and the probe wiring skip the engine `Add` for a duplicate.
  The probes register in `New`, before any module, so a module route on a probe path is always
  the refused duplicate. With the probe listener enabled, the probe arm also skips the probe
  engine's `Add` and its route-registry entry; at `server.probes.port` 0 the probe wiring goes
  through `registerRoute`, which writes the descriptor before reporting the duplicate.
- **`Server.Start` refuses before either bind.** Any recorded conflict fails `Start` with a
  `*server.DuplicateRouteError` (`errors.As` recovers `Conflicts`), which matches
  `server.ErrDuplicateRoute` with `errors.Is`. For identical templates the text is the one `app`
  already printed; a pair differing only in a parameter or wildcard name adds ` at <first path>` to
  the first registrant.
- **`app` keeps the earlier check.** It still fails at registration time, with the same error type,
  so a conflict aborts before `app.Options.PostRegisterRoutes` sees the route table.

## Alternatives considered

**Build the engines with `AllowOverwritingRoute: false`.** Rejected. Echo's router would then
refuse a duplicate with an `*echo.AddRouteError` that `Echo.AddRoute` and `Group.AddRoute` return
(only the `Add` wrappers panic on it), but that error names the method and path, never either
registrant's handler or package. Naming both, and reporting every conflict in one boot, needs a
go-bricks-side table of first registrants, which is the tracker; with the tracker deciding before
every `Add`, the router's guard adds nothing. It would also miss the widened case: echo's guard
compares the literal path, so `/users/:id` and `/users/:uid` would still overwrite.

**Refuse only at `Start`, and leave `Add` overwriting.** Rejected. A route registered after
`Start` would still replace a live handler, and the probe-template invariant would hold only
while a check runs, not by construction.

## Consequences

- **Breaking for `server.New` users.** A service that registers one route twice used to boot with
  the later handler serving. Now the first handler keeps the route and `Start` refuses. Migration
  is [migrations.md](migrations.md) `[C69.3]`.
- **A probe can be the first registrant, and then deleting it is no exit.** A module route at
  GET/HEAD `<base>/health` or `<base>/ready` used to take the path over (the probe itself at
  `server.probes.port` 0, the application listener's reservation with the probe listener on); now
  the module handler never runs and `Start` refuses. `server.path.health` equal to
  `server.path.ready`, which nothing validates, used to let readiness replace health; now health
  keeps the path and `Start` refuses with `dispatchReady` as the duplicate. The exits are
  `Server.RegisterReadyHandler` for custom readiness, moving the probe with `server.path.health` /
  `server.path.ready` (two distinct values), or moving the module route. `app` already refused
  both, since both probes were recorded in the tracker.
- **Source-breaking for unkeyed `RouteConflict` literals.** `RouteConflict` gains `FirstPath`, so an
  unkeyed `server.RouteConflict{...}` literal — typically a test fake's `RouteConflicts()` — stops
  compiling until its fields are keyed.
- **Param-name-differing templates are now conflicts under `app` too.** A service that registered
  `/users/:id` and `/users/:uid` for one method used to boot with the second serving; it now fails
  startup.
- **A duplicate registered after `Start` is dropped.** It is visible only through
  `RouteConflicts()`; nothing returns an error, since `Start` already ran.
- **Echo's own `RouteNotFound` catch-alls stay outside the tracker.** `Group.Use` registers them at
  `<prefix>` and `<prefix>/*` with overwrite allowed, so a second `Group("/api", mw)` replaces the
  first group's 404 chain; they serve no GET/HEAD handler and no module handler.
- **A refused duplicate still writes its route-registry descriptor** (`DefaultRouteRegistry`):
  a typed route (its descriptor is written before `addEcho`), a raw route and, at
  `server.probes.port` 0, a probe (through `registerRoute`; only `dispatchReady` at a shared
  health/ready path can be one). Only the probe-listener arm skips it. Startup is refused anyway,
  so the table is never served with it.

## References

- [ADR-036](adr_036_global_middleware.md): the probe exemption keyed on the matched template
- [startup_defaults.md](startup_defaults.md#duplicate-route-detection): behaviour and error shape
- `server/route_conflicts.go` (`DuplicateRouteError`, `routeNodeKey`, `routeConflictTracker.record`),
  `server/route_registrar.go` (`addEcho`, `Add`), `server/server.go` (`Start`,
  `registerProbeRoutes`), `app/lifecycle.go` (`checkRouteConflicts`)
