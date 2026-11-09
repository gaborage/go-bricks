# ADR-172: Every Tenant Door Asks the Tenant Existence Check

**Status:** Accepted
**Date:** 2026-11-08
**Related:** [ADR-160](adr_160_rate_limit_keys_on_client_ip.md) (rate limit keys on the client IP) · [ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md) (a path-read tenant is written only when the store holds it) · [ADR-171](adr_171_app_refuses_tenant_wiring_store_cannot_vouch_for.md) (path-tenancy wiring refusals, `vetExistence`) · #2212

## Context

Since ADR-170 the framework tenant seat writes a tenant read from the request path only when a
`multitenant.TenantChecker` holds it. The other doors that write a tenant to the request context
kept the older contract. The public `server.TenantMiddleware(resolver, skipper)`, which a consumer
attaches to a route group, wrote any id its resolver returned. A tenant-owned family's `Tenant`
step published any id that passed the grammar. Both doors, the seat included, also overwrote a
tenant an earlier door had already written. So the doors to one request context disagreed.

## Decision

1. **`server.TenantMiddleware(resolver, skipper, exists)`.** The public door takes a third
   parameter, `exists multitenant.TenantChecker`, and checks a path-read id exactly as the
   framework seat does (ADR-170): it writes the id only when `exists.HasTenant` answers true.
   Otherwise no tenant is written, route-group middleware runs with no tenant, and the typed or
   raw handler adapter answers 400 `BAD_REQUEST` "Invalid tenant" with the `reason="unknown
   tenant"` WARN, logged through the adapter's own logger.
   Header- and subdomain-won ids are written as before.
2. **Nil only where the path cannot be read.** `exists` may be nil only when
   `multitenant.MayReadPath(resolver)` is false: the framework header and subdomain resolvers,
   alone, validated or composed. For any other resolver, a `*multitenant.PathResolver` or a type
   the `multitenant` package does not define included, a nil `exists` panics at construction.
3. **The check is probed at construction.** A non-nil `exists` goes through `vetExistence`
   (ADR-171): a check whose `HasTenant("")` panics or answers true, or that is a nil pointer,
   panics at construction. An empty non-nil `*config.TenantStore` is accepted, because a
   consumer may fill it with `AddTenant` after wiring; it holds no tenant until then.
4. **Every door validates the grammar and refuses reserved-prefix ids.** The public door wraps
   a resolver that is not already a `*multitenant.ValidatingResolver` in one with the default
   tenant-ID pattern, as the framework seat wraps its own, so an id outside the grammar answers
   400 at the door; a `ValidatingResolver` keeps the pattern it was given. Whatever resolver
   produced it, an id carrying `config.NamedDatabasePrefix` is never written: the request is
   marked like an unheld path tenant and the handler adapter answers 400. The check is asked only
   about plain tenant keys.
5. **A conflicting resolver refuses.** In the framework seat and the public door, when a tenant is
   already in the request context and the resolved id differs, from any resolver (a header-won
   one included), nothing is written or overwritten. The request is marked as for an unknown
   path tenant, and the handler adapter answers 400 "Invalid tenant". The same id passes. A path
   id the check refuses beside a seated tenant answers 400 from the adapter as before, and the
   seated tenant is never overwritten.
6. **Tenant-owned families verify their tenant.** A `TenantPolicy` `Tenant` step's id is published
   only when the Server's check (`server.Options.TenantChecker`, which the App sets from its
   resource source) holds it. The check runs after the grammar check and is asked only about
   plain tenant keys. A grammar-valid id the check does not hold is a 400 client fault answered
   by the policy's `Deny` (`DenyBodyless` answers a bodyless 400), and the handler never runs;
   an id that fails the grammar stays the framework 500 it was, without `Deny`. `TenantPolicy.ResolveForTest`
   runs no existence check.
7. **`TenantOwnedGroup` needs the check.** It panics at the call when the Server has no tenant
   existence check, after the framework-root refusal and before the prefix claim, with a message
   naming the remedy: build the Server through the App, whose resource source implements
   `multitenant.TenantChecker`, or pass `Options.TenantChecker`. It also probes that check with
   `vetExistence` and panics when it cannot answer.
8. **Rate limiting is unchanged.** `server.RateLimit` and the framework limiter key on the client
   address (ADR-160), so a drained client gets the same 429 for a held and an unheld path tenant.

**Why the adapter answers, not the door.** The seat and the public door run before route-group
middleware such as `auth.Middleware`. A 400 at the door, beside an auth group's 401 for every
other id, would let an unauthenticated caller tell held ids from unheld ones. Leaving the refusal
to the handler adapter keeps ADR-170's property that no response before the adapter differs by
tenant existence. A tenant-owned family answers through its own policy's `Deny`, which runs after
its own authentication step.

## Alternatives considered

- **A second constructor, or an option on the two-argument form.** Rejected. Explicit > Implicit:
  a compile break makes every caller choose a check, where an opt-in leaves the existing calls on
  the older contract.
- **Accept nil for every resolver and refuse every path id.** Rejected. Fail Fast: a door that
  silently answers 400 to every tenant is a wiring error better reported at construction.
- **Let the later door win on a conflict.** Rejected. Which tenant a request runs under would then
  depend on middleware order, not on the request.

## Consequences

- **Breaking.** Every `server.TenantMiddleware` call stops compiling until it passes a third
  argument. Nil compiles, so a resolver that may read the path then panics at construction: the
  compiler does not catch it. Through the public door a path tenant the check does not hold
  answers 400 where it was written and its handler ran.
- **Breaking.** `TenantOwnedGroup` panics on a Server with no check: a hand-built `server.New` or
  `NewWithOptions` under multitenant, header or subdomain tenancy included, needs
  `server.Options{TenantChecker: …}`; an App whose custom `app.Options.ResourceSource` has no
  `HasTenant(string) bool` needs one; a server passed as `app.Options.Server` keeps its own
  checker, so it must carry one. A family `Tenant` step's id the check does not hold answers 400.
- **Breaking.** A request whose doors resolve two different tenants answers 400 from the adapter,
  where the later door overwrote the earlier one.
- **Breaking.** `TenantMiddleware` wraps a hand-built resolver that is not a
  `*multitenant.ValidatingResolver` in the default tenant-ID pattern, so an id outside
  `^[a-z0-9-]{1,64}$` answers 400 at the door where it was written; a reserved-prefix id answers
  400 from the adapter on every door.
- **Single-tenant services** using the public door with a path resolver pass their own check:
  `config.NewTenantStore(cfg)` holds no tenant when `multitenant.enabled` is false.

## References

- `server/tenant_middleware.go`: `TenantMiddleware`, `seatTenant`, `seatInputs.holds`
- `server/middleware.go`: `seatInputs`
- `server/tenant_family.go`, `server/tenant_policy.go`: `TenantOwnedGroup`, the `Tenant` step
- See [migrations.md](migrations.md) `[C73.39]`.
