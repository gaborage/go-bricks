# ADR-169: Framework Routes Skip Tenant Resolution Under a Path-Reading Resolver

**Status:** Accepted
**Date:** 2026-11-03
**Related:** [ADR-036](adr_036_global_middleware.md) (global middleware) · [ADR-039](adr_039_composite_resolver_order.md) (composite resolver order) · [ADR-164](adr_164_tenant_owned_route_family.md) (tenant-owned route family) · [ADR-168](adr_168_framework_route_roots_reserved.md) (framework route roots reserved) · #2212
**Amends:** [ADR-036](adr_036_global_middleware.md) (global middleware runs after tenant resolution), [ADR-039](adr_039_composite_resolver_order.md) (the composite chain's obligations)

## Context

The root tenant seat resolves a tenant on every routed request except the health and ready
probes. A path resolver (`multitenant.resolver.type: path`, or a composite chain with a `path`
member) reads the tenant from a fixed segment of the request path. The framework mounts its own
routes under fixed prefixes that hold no tenant:

- the scheduler's system API, `GET <base>/_sys/job` and `POST <base>/_sys/job/:jobId`;
- the debug group, `<debug.pathprefix>/…` at the engine root (default `/_sys`).

Under a path resolver those routes either fail the tenant grammar (`_sys` is not a tenant ID), so
the framework answers its own endpoints with 400, or take a tenant from a segment such as `job`.
A tenant read from those segments is not one, and the later #2212 link that checks a path-read
tenant against the store (A1b) would answer each one 400.

[ADR-168](adr_168_framework_route_roots_reserved.md) already reserves those prefixes, the
framework route roots, for the framework's own routes under such a resolver: startup refuses any
other package's route or middleware-bearing group there, and refuses a debug prefix that would
cover the module routes.

## Decision

**With `multitenant.enabled` and a resolver that may read the path (`multitenant.MayReadPath`), the tenant seat skips
every matched route template under the framework route roots, as it skips the probes.** The roots
are the ones ADR-168 reserves, `<base>/_sys` always plus `debug.pathprefix` when `debug.enabled`
is true; admission and the seat read one list, so the seat skips exactly what admission reserves.
The skip keys on `c.Path()`, the template the router matched, never on the request URL, and
allocates nothing.

With ADR-168's refusal in place, a template under a root names a framework handler or a
not-found catch-all, so the skip cannot reach a consumer handler. Consumer middleware still runs
there (see Consequences). The seat skips the whole resolver on those templates, so under a
composite chain its header and subdomain members are not read there either.

Under a header or subdomain resolver, or with multitenant off, there is no root and nothing
changes.

## Alternatives considered

- **Exempt exact framework templates instead of roots.** Rejected. Echo keeps the node's template
  on a 405, a HEAD or an OPTIONS request, so a consumer route on the same node with another
  method would inherit the skip; skipping the whole root, which ADR-168 reserves, closes that.
- **Skip by the request path.** Rejected. The request bytes are caller-written; the matched
  template is the framework's.
- **Leave framework routes to the resolver.** Rejected. It turns the framework's own endpoints
  into 400s, or into tenant-scoped requests keyed on `_sys` or `job`.

## Consequences

- **Breaking + silent-behavior**: framework routes under the roots carry no tenant. Module global
  middleware (ADR-036) still runs there, with no tenant in context, and so does a module's `Use`
  on the shared module registrar on the scheduler's routes, whose group is built after it: under
  a composite chain, a header or subdomain tenant that resolved on `<base>/_sys/job` in v0.72.0
  no longer does, so a gate that requires a tenant now refuses those routes and must let them
  through. The scheduler and
  debug handlers need none. The built-in rate limiter keys those requests on the client address;
  a `server.TenantRateLimit` installed as global middleware passes them without charging a bucket.
- Those routes' access lines carry no `tenant` field, where a composite chain's header or subdomain
  tenant or a segment such as `job` filled it in v0.72.0, and a path that failed the grammar there no longer
  logs a `[server.tenant]` WARN.
- Scheduler and debug routes keep their own access control (the CIDR allowlist and the debug IP
  allowlist or bearer token); the skip removes tenant resolution only.
- No allocation moves on the default chain: the skip is a string-prefix walk over at most two
  roots, and it runs only on a path-reading seat.

## References

- `server/framework_roots.go`: `skipFrameworkRoots`
- `server/middleware.go`: `setupIdentityMiddlewares`
- See [migrations.md](migrations.md) `[C73.35]`.
