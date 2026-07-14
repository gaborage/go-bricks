# ADR-039: Default Composite Tenant Resolver Order to Subdomain → Path → Header

**Status:** Accepted
**Date:** 2026-07-14

## Context

The composite tenant resolver (`multitenant.resolver.type: composite`) tries a
list of sub-resolvers in order and returns the first non-empty, regex-valid
match (`multitenant.CompositeResolver.ResolveTenant`, unchanged by this ADR).
`server/middleware.go`'s `buildTenantResolver` hardcoded that order to
**header → subdomain → path**.

The `X-Tenant-ID` header is trivially client-controlled — any caller can set
it to any value. Subdomain and path are network-bound: they're fixed by the
request's `Host` or URL, which the deployment's routing/DNS/reverse-proxy
layer controls, not the calling client. With header tried first, a request
already scoped to tenant A by subdomain or path could override that scoping
just by adding `X-Tenant-ID: b` — the header match wins before subdomain or
path are even consulted. Because the resolved tenant ID directly selects the
per-tenant database, cache, and message broker, this is not a cosmetic
ordering choice: any deployment that enables `composite` to support
subdomain- or path-based tenancy was silently downgraded to **header-trust**,
and a caller already scoped to one tenant's subdomain/path could read or
write another tenant's data by adding a header. There is no per-source
binding tying a subdomain or path to an allowed tenant, so the first-match
resolver order *is* the isolation boundary.

## Options Considered

### Option A: Flip the default order + add a configurable `order` escape hatch (Chosen)

Default the composite order to subdomain → path → header (network-bound
before client-controlled), and add an optional `multitenant.resolver.order`
config key so an operator who has a specific reason to prefer header-first
(e.g., a trusted API gateway strips and re-sets the header, so it is
effectively network-bound in that deployment) can opt back in explicitly.

**Chosen because:** it fixes the vulnerable default for every deployment that
didn't set `order` explicitly, while not removing the header sub-resolver or
forcing every composite user into a single fixed shape. The opt-in is a
`[]string` of `header`/`subdomain`/`path` entries, validated and defaulted in
`config.Validate` — an unrecognized entry, a duplicate, or `order` set on a
non-composite type are all rejected outright at startup rather than silently
ignored, matching the framework's Explicit-over-Implicit and Fail-Fast
posture.

### Option B: Flip the order with no configurability (Rejected)

Hardcode subdomain → path → header with no escape hatch.

**Rejected because:** it removes a legitimate configuration a gateway-fronted
deployment might need (header-first when the header is itself
network-bound), and the framework's precedent (`CORS_ORIGINS`,
`resolver.path.prefix`, etc.) is to make security-relevant defaults safe
while still letting an operator deliberately opt into a different posture.

### Option C: Leave the order fixed, document the risk (Rejected)

Keep header → subdomain → path, add a wiki warning about header trust.

**Rejected because:** a documentation-only fix does not change the default
behavior of any already-deployed or newly-deployed composite resolver — the
same class of failure this framework has repeatedly closed by flipping
defaults rather than relying on operators reading docs (see ADR-038 for the
same reasoning applied to CORS).

## Decision

`server/middleware.go`'s composite case now builds sub-resolvers in the
order given by `multitenant.resolver.order`, defaulting to
`config.DefaultResolverOrder()` — `[subdomain, path, header]` — when unset.
`multitenant.CompositeResolver`'s first-match semantics
(`multitenant/resolver.go`) are unchanged: it still returns the first
non-empty, regex-valid result from the ordered resolver list.

- **Validated + defaulted config path** (`config.Validate` →
  `validateMultitenantResolver`): an empty `Order` on a `composite` resolver
  is defaulted to `DefaultResolverOrder()`; a non-empty `Order` is validated
  — every entry must be `header`, `subdomain`, or `path` (no duplicates,
  rejected with `multitenant.resolver.order`), and `Order` is rejected
  outright on any non-composite type rather than silently ignored.
- **Defense in depth at the builder** (`server/middleware.go`): a config that
  never went through `config.Validate()` — e.g. constructed directly in a
  test, or by an embedding application that builds `*config.Config` by hand —
  can reach `buildTenantResolver` with an empty or garbage `Order`. A naive
  loop over an empty order would append zero sub-resolvers, and
  `buildCompositeTenantResolver` returns `nil` for an empty list — silently
  disabling tenant resolution (fail-open) rather than falling back to a safe
  default. `compositeResolverOrder` normalizes an empty or fully-unrecognized
  order to `config.DefaultResolverOrder()` before the builder consumes it, so
  this failure mode is closed independent of whether the config was
  validated.
- The default order is defined once, in `config.DefaultResolverOrder()`
  (a function, not a package-level slice — returns a fresh slice each call so
  callers can't mutate a shared default), and referenced from `server` rather
  than duplicated.

## Consequences

### Positive

- Closes the header-trust downgrade: a request scoped to a tenant by
  subdomain or path can no longer have that scoping overridden by a
  client-supplied header, for every composite deployment that didn't
  explicitly opt into a different order.
- The opt-in (`multitenant.resolver.order`) preserves the legitimate
  gateway-fronted use case without weakening the default for everyone else.
- Two independent layers (validated-config default, builder-level
  normalization) close the fail-open gap for both validated and
  hand-constructed configs — a single missed validation call site can't
  silently disable tenant isolation.

### Negative

- **Breaking change.** Any deployment relying on the old
  header-first-wins-on-conflict behavior of `composite` without setting
  `order` will observe a different resolution result for a request carrying
  both a valid subdomain/path tenant and a conflicting header — the
  subdomain/path value now wins. Deployments that need the old behavior must
  set `multitenant.resolver.order: [header, subdomain, path]` explicitly.
  Tracked as migrations atom **C50.4**.

### Neutral

- `multitenant.CompositeResolver`'s first-match algorithm and the individual
  sub-resolver implementations (`multitenant/resolver.go`) are unchanged —
  this ADR only changes which order `server/middleware.go` feeds them in.

## Migration Impact

**Breaking change:** a `composite` resolver deployment that does not set
`multitenant.resolver.order` now resolves subdomain/path before header
instead of header before subdomain/path. A request carrying a conflicting
header no longer overrides a subdomain/path match. Deployments that
deliberately need header-first resolution (e.g., a trusted gateway owns the
header) must set `multitenant.resolver.order: [header, subdomain, path]`
explicitly. See [wiki/migrations.md](migrations.md) atom **C50.4** for the
detect/gate/apply/verify runbook entry.

## Related ADRs

- [ADR-038: Require Explicit Opt-In for Dev-Permissive CORS](adr_038_cors_dev_wildcard_opt_in.md) —
  same pattern applied to a different subsystem: flip a security-relevant
  default to fail-safe, require explicit opt-in for the previous permissive
  behavior.
