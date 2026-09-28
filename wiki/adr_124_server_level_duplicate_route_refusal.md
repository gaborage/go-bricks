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

The health/ready probe exemption is keyed on the route template the router matched
(ADR-036, GHSA-h4jw follow-up). That template identifies the probe HANDLER only while no module
can take over a probe path. An overwrite keeps the template and swaps the handler, so the
exemption rested on a check that `app` alone ran.

## Decision

- **The first registration wins for every route registered through the server's registrars and
  for the probe routes.** `routeConflictTracker.record` reports whether the route is new.
  `addEcho`, `RouteRegistrar.Add` and the probe wiring skip the engine `Add` for a duplicate.
  The probes register in `New`, before any module, so a module route on a probe path is always
  the refused duplicate. With the probe listener enabled, the probe arm also skips the probe
  engine's `Add` and its route-registry entry; at `server.probes.port` 0 the probe wiring goes
  through `registerRoute`, which writes the descriptor before reporting the duplicate.
- **`Server.Start` refuses before either bind.** Any recorded conflict fails `Start` with a
  `*server.DuplicateRouteError` (`errors.As` recovers `Conflicts`), which matches
  `server.ErrDuplicateRoute` with `errors.Is`. Its text is the one `app` already printed.
- **`app` keeps the earlier check.** It still fails at registration time, with the same error type,
  so a conflict aborts before `app.Options.PostRegisterRoutes` sees the route table.

## Alternatives considered

**Build the engines with `AllowOverwritingRoute: false`.** Rejected. Echo's router would then
refuse a duplicate with an `*echo.AddRouteError` that `Echo.AddRoute` and `Group.AddRoute` return
(only the `Add` wrappers panic on it), but that error names the method and path, never either
registrant's handler or package. Naming both, and reporting every conflict in one boot, needs a
go-bricks-side table of first registrants, which is the tracker; with the tracker deciding before
every `Add`, the router's guard adds nothing.

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
  `server.path.ready` (two distinct values), or moving the module route.
- **`app` users see no behaviour change.** They already failed startup on a duplicate, the two
  probe cases included, since both probes were already recorded in the tracker; the error now also
  matches `server.ErrDuplicateRoute` and recovers as `*server.DuplicateRouteError`.
- **The key is the literal method + full path.** Templates that differ only in a parameter or
  wildcard name (`/users/:id`, `/users/:uid`) are one route to echo's router but two keys to the
  tracker, so they stay undetected and echo's overwrite still governs them.
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
- `server/route_conflicts.go` (`DuplicateRouteError`, `routeConflictTracker.record`),
  `server/route_registrar.go` (`addEcho`, `Add`), `server/server.go` (`Start`,
  `registerProbeRoutes`), `app/lifecycle.go` (`checkRouteConflicts`)
