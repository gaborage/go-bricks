# ADR-160: The Global Rate Limiter Keys on the Client IP Only

**Status:** Accepted
**Date:** 2026-10-31

## Context

`SetupMiddlewares` installs the built-in rate limiter on the root chain from `app.rate.limit`.
It keyed on the context tenant (`multitenant.GetTenant`) when one resolved, and on `RealIP()`
otherwise. It runs after tenant resolution and before every global and route-group middleware,
so the tenant key was whatever the caller wrote in the header, path or `Host` the resolver reads.
That had four effects:

- A caller that rotated tenant values got a fresh bucket for each value, lifting one address from
  `app.rate.limit` to the IP pre-guard's ceiling.
- A caller that named a victim tenant drained that tenant's bucket, unauthenticated, and read its
  `X-RateLimit-Remaining`, since echo's memory store sets `X-RateLimit-Limit`,
  `X-RateLimit-Remaining` and, on a 429, `Retry-After` on every limited response.
- The memory store grew by one entry per distinct value, swept only after 3-6 minutes idle
  (`RateLimitCleanup`).
- The exported `server.RateLimit` wraps the same constructor, so it keyed the same way.

Nothing chose tenant keying on purpose. [ADR-057](adr_057_trusted_proxy_ip_extraction.md) fixed
the IP half of F23 and recorded the tenant-keyed half as open, and
[ADR-140](adr_140_forwarded_host_peer_gate.md) described the tenant bucket as a side effect of
tenant resolution.

## Decision

1. **The built-in limiter keys on `RealIP()` alone, for every request,** whether or not a tenant
   resolved. `app.rate.limit` is a per-client-IP budget. The burst (`app.rate.limit ×
   BurstMultiplier`), `RateLimitCleanup` and `<= 0` disabling the limiter are unchanged. Its
   `X-RateLimit-*` headers describe only the caller's own address bucket.
2. **`server.RateLimit` keeps sharing the built-in's keying,** so it keys on the client IP too.
   After this change no public door keys per tenant with an IP fallback.
3. **`server.TenantRateLimit(requestsPerSecond int) MiddlewareFunc` limits per context tenant,**
   with the same rate, burst, cleanup and 429 bodies. A request with no tenant in context
   (multitenant disabled, or a route outside tenant resolution) passes through without charging
   any bucket and without `X-RateLimit-*` headers; it never falls back to `RealIP()`, because the
   built-in limiter already owns the per-IP budget. `<= 0` makes it a pass-through. The framework
   never installs it.
4. **Placement: the same `RouteRegistrar.Group(prefix, ...)` call as `auth.Middleware`, listed
   after it.** Group middleware runs in the order given, first listed outermost, so authentication
   runs first and an unauthenticated request never reaches the tenant bucket. `auth.Middleware`
   verifies the credential, not the tenant, so the limiter belongs after whatever check ties the
   resolved tenant to the caller. A placement through `GlobalMiddlewareRegisterer`, behind an
   authenticating global middleware, is legal today, but #2280's resolution (an
   [ADR-036](adr_036_global_middleware.md) amendment) refuses global middleware beside a
   tenant-owned route family once #2166 lands, so it is not a documented placement.
5. **Headers describe the innermost limiter that ran.** Both limiters set echo's `X-RateLimit-*`
   headers, so on a route carrying `TenantRateLimit` the tenant bucket's values overwrite the
   address bucket's.

## Consequences

- Every client behind one NAT, partner gateway or load balancer shares one bucket, and so does
  every tenant behind it. Size `app.rate.limit` for the busiest shared address, and attach
  `TenantRateLimit` where tenants need their own budget.
- A correct `server.trustedproxies` (ADR-057) is load-bearing for the limiter. A missing entry
  for a proxy on a public address keys the whole fleet on the proxy's address; an over-broad
  entry lets a caller pick its own key through `X-Forwarded-For`.
- Probe requests on the application listener share the built-in limiter's bucket with all
  traffic from their address, whatever that traffic's tenant, so `server.probes.port`
  ([ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)) matters more.
- Limiter memory is bounded by distinct client IPs, not by caller-chosen strings.
- Root-chain requests of the tenant-owned route families #2166 will mount key on the client IP,
  consistent with this default.
- A deployment that relied on a per-tenant budget from the built-in limiter loses it silently.
  See [migrations.md](migrations.md) `[C73.20]`.

## Alternatives considered

- **Key on the tenant and the IP together.** Rejected: caller-chosen strings still multiply the
  entries per address, and rotation still buys fresh buckets.
- **Bound the tenant key with a `TenantStore` lookup.** Not taken: a well-formed provisioned
  tenant is still caller-named, so the victim drain stays.
- **Install the tenant limiter as global middleware by default.** Rejected: global middleware runs
  before route-group authentication (ADR-036), so it would charge the bucket for requests auth
  later rejects.

## References

- [ADR-036](adr_036_global_middleware.md): global middleware runs before route-group middleware.
- [ADR-057](adr_057_trusted_proxy_ip_extraction.md): client IP derivation; the IP half of F23.
- [ADR-109](adr_109_bearer_credential_verification.md): `auth.Middleware`, attached per route group.
- [ADR-140](adr_140_forwarded_host_peer_gate.md): tenant resolution precedes the limiter.
- [startup_defaults.md](startup_defaults.md#probe-endpoints-and-rate-limiting): limiter defaults and
  placement.
- `server/ratelimit.go` (`rateLimiterEcho`, `rateLimitEcho`, `RateLimit`, `TenantRateLimit`).
- gaborage/go-bricks#2167.
