# ADR-170: A Path-Read Tenant Is Written Only When the Store Holds It

**Status:** Accepted
**Date:** 2026-11-03
**Related:** [ADR-036](adr_036_global_middleware.md) (global middleware, reversed in part here) · [ADR-160](adr_160_rate_limit_keys_on_client_ip.md) (rate limit keys on the client IP) · [ADR-161](adr_161_unknown_tenant_is_a_client_fault.md) (unknown tenant is a client fault) · [ADR-167](adr_167_inbound_paths_render_from_route_template.md) (masked inbound paths) · [ADR-169](adr_169_framework_routes_skip_path_tenant.md) (framework routes skip the path tenant) · #2212

## Context

A path resolver (`multitenant.resolver.type: path`, or a composite chain whose `path` member won)
reads the tenant from a URL segment, which the caller writes. The seat checked only the grammar
`^[a-z0-9-]{1,64}$`, which a 16-digit card number passes, and then wrote the id to the request
context. From there every downstream printer carried it: the access line's `tenant` field, global
and route middleware, the handler, and any consumer line that logs the tenant. The docs claimed a
known-tenant lookup that nothing performed: a well-formed id the store does not hold was refused
only at the first per-tenant accessor (ADR-161), after it had already been printed.

## Decision

1. **The framework seat asks a `multitenant.TenantChecker` about a path-read id.** It resolves
   through `multitenant.Resolve`, which reports whether a path member, or a resolver of unknown
   provenance, produced the id. When it did, the seat writes the id only if
   `TenantChecker.HasTenant` answers true. Header- and subdomain-won ids are never asked and are
   written as before; a resolution failure still answers 400 at the seat.
2. **An unknown path id is never written.** The request carries an id-free marker instead and runs
   on with no tenant: the access logger, the limiter, global middleware, route-group middleware
   (auth) and JOSE inbound verification all see no tenant. The framework handler adapters (typed
   and raw) then answer 400 `BAD_REQUEST` "Invalid tenant" through the unknown-tenant door
   (ADR-161), before binding and before the handler runs. The refusal logs the seat's WARN with
   `reason="unknown tenant"` and the masked path (ADR-167); the access line, the span and
   `http.server.request.duration` record 400. An unauthenticated caller on an auth group gets that
   group's 401 for every id, and an unmatched request keeps its 404/405: only an adapter refuses.
3. **`server.Options.TenantChecker` carries the check, and the App sets it** on the server it
   builds, from its tenant resource source when the source implements `HasTenant` (the built-in
   `*config.TenantStore` does), replacing any value a consumer put in `app.Options.ServerOptions`:
   the resource source is the one authority on which tenants exist. A server passed as
   `app.Options.Server` is used as given and keeps its own checker. A server whose resolver may
   read the path (`multitenant.MayReadPath`) and that has no checker refuses `Start` before
   binding, where it would answer 400 to every path tenant: a hand-built `server.New`, or an App
   whose resource source has no `HasTenant`. One served anyway (its `Echo()` behind another
   listener) still vouches for no path-read id, so it stays fail-closed. With a checker wired,
   `Start` writes one INFO line (`log.type=action`): `[server.tenant] a tenant read from the
   request path is written only when the tenant store holds it (ADR-170) store=<%T>`, plus
   `tenants=<n>` for a `*config.TenantStore`; other servers write none.
4. **The public `server.TenantMiddleware` is unchanged.** It asks no checker and writes every
   resolved id; a later link (A1d) requires the check there.
5. **Reversal of ADR-036.** ADR-036 promised global middleware "the tenant already in context".
   Under a path-reading resolver that no longer holds for a path tenant the store does not hold:
   global middleware runs with no tenant, and the request is refused at the adapter afterwards. A
   global gate that requires a tenant must pass a tenant-less request through and leave the
   refusal to the framework.

The rate limiter is unchanged here: since ADR-160 it keys every request on the client address
before the adapter runs, so a drained client gets the same 429 for a known and an unknown path
tenant, and no response before the adapter differs by tenant existence.

## Alternatives considered

- **Refuse at the seat.** Rejected. The seat runs before route-group middleware, so an
  unauthenticated caller would learn which ids exist from 400-versus-401, and the 400 would skip
  the access line the seat sits outside of.
- **Write the id and let the first accessor refuse (main).** Rejected. The id reaches every
  printer first, and a route that never calls an accessor serves the unknown tenant.
- **Ask with a context-carrying lookup.** Rejected for now. The check is in memory, with no
  context, no I/O and no blocking (a stated Context-First exception); a source backed by a remote
  catalog refreshes its own set out of band and answers from it.

## Consequences

- **Breaking**: under a path-reading resolver, a request whose path tenant the store does not hold
  answers 400 "Invalid tenant" from the adapter, with no tenant in context anywhere, where it ran
  its handler with the tenant written. A path-reading server with no checker, built without the
  App or by an App whose resource source has no `HasTenant`, fails `Start`.
- **Limits.** A consumer `HasTenant` that breaks its contract (panics, blocks or answers from a
  stale set) is a named residual; a panic is caught by the outermost guard (`Panic recovered`, no
  access line). Consumer `SetTenant` values, consumer `Request().URL` reads and edge or WAF logs
  stay outside the framework's sinks.
- **Runbook.**
  - *Identity of a refused caller:* the principal appears only on the span's `enduser.id`, and only
    with `auth.jwt.telemetry.enduserid` on; otherwise correlate on `client.address` and
    `user_agent.original`.
  - *Authenticated unknown-tenant 400s* read as catalog drift or credentialed enumeration.
  - *Store health:* the `[server.tenant]` startup line names the store and, for the built-in
    one, its tenant count; every path tenant answering 400 on one pod means that pod's set is
    empty or stale; `Panic recovered` with no access line means its `HasTenant` panicked.
  - *Collapsed clients:* a prober sharing a load balancer's address shares that address's limiter
    bucket (ADR-160).
  - *`HasTenant` contract for a custom source:* load the set before `app.New`, keep the last good
    set on a failed refresh, never shrink to empty because a refresh failed, never panic, and log
    each refresh's size.
- **Cost (ADR-026).** A known path tenant pays one in-memory lookup and no allocation; every
  existing observed-request row is unchanged. Two 400 rows are pinned: `path_won_unknown_tenant`
  at 133 and `xff_path_won_unknown_tenant` at 142 allocs/op, whose WARN reuses the limiter's
  memoized client address.

## References

- `server/tenant_middleware.go`: `tenantMiddlewareEcho`, `seatTenant`, `seatInputs.holds`,
  `refuseUnknownPathTenant`, `unknownPathTenantKey`
- `server/handler.go`: `adaptHandler`, `handlerWrapper.wrap`
- `server/options.go`: `Options.TenantChecker`
- `server/server.go`: `newServer` (`errNoTenantChecker`), `logPathTenantCheck`
- `app/app.go`: `resolveServer`; `app/bootstrap.go`: `dependencies`
- See [migrations.md](migrations.md) `[C73.36]`.
