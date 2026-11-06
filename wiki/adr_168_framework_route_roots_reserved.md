# ADR-168: Framework Route Roots Are Reserved Under a Path-Reading Resolver

**Status:** Accepted
**Date:** 2026-11-04
**Related:** [ADR-155](adr_155_reserved_engine_methods_refused.md) (route admission) · [ADR-164](adr_164_tenant_owned_route_family.md) (tenant-owned route family) · #2212

## Context

A path resolver (`multitenant.resolver.type: path`, or a composite chain with a `path` member)
reads the tenant from a fixed segment of the request path. The framework mounts its own routes
under fixed prefixes that hold no tenant: the scheduler's system API under `<base>/_sys`, and
the debug group under `debug.pathprefix` at the engine root (default `/_sys`). The next #2212
link (ADR-169) lets the tenant seat skip every template under those prefixes. That is safe only
if nothing but framework code is registered there, so the prefixes are reserved first.

Route admission named a registrant by its caller frame. Frame 3 is the module only on the
`GET`/`POST` helper path: a direct `RegisterHandler` call from a module's `RegisterRoutes`
resolves to `app` (the `ModuleRegistry` frame), and `server.GET` through a registrar embedding
`RouteRegistrar` resolves to `server` through the typed fallback's `Add`. Both are framework
packages, so a caller frame cannot tell a consumer route from a framework one.

## Decision

**Under a resolver that may read the path (`multitenant.MayReadPath`), the framework route roots
are reserved.** The roots are `<base>/_sys` (`internal/pathutil.SystemPrefix`, the one spelling
the scheduler, the config default and the server share) always, plus `debug.pathprefix` when
`debug.enabled` is true.

- **Refusal through route admission.** A route, a group carrying middleware, or a
  `TenantOwnedGroup` family's prefix (its steps are middleware, so it is refused with no routes),
  at or below a root is a conflict with the exported reason `RouteConflictReasonFrameworkRoot`
  unless its package is `scheduler` or `app` (the debug group), or it is one of the server's own
  probes: a `GET` or `HEAD` the server registers at the health or ready path. Start fails
  with the `*DuplicateRouteError` (`errors.Is(err, server.ErrDuplicateRoute)`), whose line names
  the route and the root, and a registration after Start panics, as a tenant-owned trespass does
  (ADR-164). A refused family's steps are not mounted. A middleware-bearing group is refused because its middleware would run on the
  framework routes below it. Middleware above a root is not judged: a module's `Use` on the
  shared `ModuleGroup` registrar reaches every route registered through it afterwards, the
  scheduler's included, and global middleware runs on every route.
- **A route's package is its handler function's.** `RegisterHandler` and `RouteRegistrar.Add`
  read the package that declares the handler (`runtime.FuncForPC` on the function value): a method
  value's receiver package, or a closure's enclosing function's. The `GET`/`POST` helpers, a
  direct `RegisterHandler` call and a raw `Add` give that answer. The typed fallback for a
  registrar that is not the framework's adapts the handler in a `server` closure, so admission
  sees `server` there, and `server` is not an admitted package. `RegisterHandler` sets
  `RouteDescriptor.Package` before the `RouteOption`s run, so an option reads it, and stamps it
  again after the options have run, unconditionally and under every resolver, so no option forges or clears
  it. A group, and a family, keeps its caller frame: it has no handler.
- **A debug prefix that covers the module routes is refused.** Under the same resolver, with
  `debug.enabled`, a `debug.pathprefix` of `""` or `/`, or one at or above `server.path.base`,
  would reserve every module route. `server.New` records an error naming `debug.pathprefix` and
  `server.path.base`, and Start returns it before binding. The error states what is admitted: a
  non-root path below the engine root that neither equals nor covers a non-empty
  `server.path.base`; with the base at the engine root, only `""` and `/` fail.

Under a header or subdomain resolver, or with multitenant off, no root is reserved.

## Alternatives considered

- **Keep the caller frame.** Rejected: see Context; it misattributes two doors to framework
  packages.
- **Key ownership on a marker the framework packages pass.** Rejected. Any exported marker is a
  door consumers can call; the handler function is not something a consumer can relabel.
- **Drop a root-level debug prefix silently.** Rejected. It booted with the debug routes still
  under tenant resolution, and a prefix above the base failed only once a module route existed,
  naming that route rather than the key.

## Consequences

- **Breaking**: under a path-reading resolver, a consumer route, middleware-bearing group or
  tenant-owned family at or below `<base>/_sys` or an enabled `debug.pathprefix` fails startup, whichever door
  registered it; and an enabled `debug.pathprefix` of `""`, `/` or at or above
  `server.path.base` fails startup.
- Under every resolver, `RouteDescriptor.Package` (the route table, the `PostRegisterRoutes`
  hook, conflict lines) names the handler's declaring package, where it named the registering
  caller's, and a `RouteOption` that wrote it no longer takes effect.
- A typed route registered through a consumer registrar is described twice in the route table:
  the fallback's descriptor names the handler's package, and the one `Add` records names `server`
  (the fallback's adapter), as its admission record and conflict line do.
- No request-path cost: admission runs at registration.

## References

- `internal/pathutil/pathutil.go`: `SystemPrefix`
- `server/framework_roots.go`: `frameworkRoots`, `frameworkRootPackages`
- `server/route_conflicts.go`: `refuseFrameworkRootLocked`, `refuseFamilyUnderFrameworkRoot`,
  `isProbe`, `RouteConflictReasonFrameworkRoot`
- `server/tenant_family.go`: `TenantOwnedGroup`
- `server/handler.go`: `RegisterHandler`, `handlerPackage`
- See [migrations.md](migrations.md) `[C73.34]`.
