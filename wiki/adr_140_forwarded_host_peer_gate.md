# ADR-140: `X-Forwarded-Host` Counts Only From a Trusted Peer

**Status:** Accepted
**Date:** 2026-10-03

## Context

With `multitenant.resolver.proxies: true`, `multitenant.SubdomainResolver` read the
tenant host from `X-Forwarded-Host` whatever the immediate peer was. It read only the
first header line, and took that line's first comma-separated entry. Two gaps
followed:

- A trusted proxy that appends to a client-sent `X-Forwarded-Host` handed resolution
  the client's entry.
- The header was believed from a peer that the server's client-IP walk
  ([ADR-057](adr_057_trusted_proxy_ip_extraction.md), [ADR-080](adr_080_client_ip_answers_only_from_observed_hops.md))
  and, since echo v5.4.0, its scheme extraction both ignore.

The resolved tenant is set before any consumer authorization and before the rate
limiter. Per-tenant accessors and the tenant-keyed rate-limit bucket follow it.
[ADR-039](adr_039_composite_resolver_order.md) obligation 1 already required the
ingress to ensure only the trusted proxy can set the header, so this ADR hardens a
posture-based model rather than restoring a broken guarantee.

## Decision

1. **`proxies` stays the explicit opt-in, default `false`.** With it `false`,
   `X-Forwarded-Host` is ignored as before.
2. **With `proxies: true`, the header counts only from a trusted immediate peer.**
   The rule is the one echo's `ExtractSchemeFromHeaders` applies to
   `X-Forwarded-Proto`:
   - loopback, link-local and private addresses;
   - unix-socket peers (an empty `RemoteAddr`, or one starting with `@` or `/`);
   - the `server.trustedproxies` ranges, after the server's existing vetting.

   echo's checker is unexported and its `TrustOption`s are opaque, so the rule is
   rebuilt in `SubdomainResolver` from the same vetted list, and a parity test keeps
   the two aligned. `SubdomainResolver.TrustedProxies` carries the extra ranges.
3. **The tenant host is the last entry.** From a trusted peer it is the last
   comma-separated entry of the last `X-Forwarded-Host` line, trimmed: the one the
   nearest proxy wrote. An empty last entry is no match; the resolver never walks
   left.
4. **An untrusted peer that sends the header fails terminally.** Resolution returns
   `multitenant.ErrUntrustedForwardedHost`; `CompositeResolver` returns it at once
   instead of trying the next sub-resolver, `ValidatingResolver` passes it through, and
   the request gets the existing 400 `Invalid tenant`. Without this, an order such as
   `[subdomain, header]` would silently fall through to a caller-controlled
   `X-Tenant-ID`. The tenant-rejection WARN carries its own reason and never logs the
   header value.
5. **The gate lives in the resolver,** so a hand-built `SubdomainResolver` passed to
   `server.TenantMiddleware` is gated too.

ADR-039 obligation 1 and the ADR-057 echo v5.4.0 amendment are amended to match.

## Alternatives considered

- **Derive `proxies` from `server.trustedproxies`.** Rejected: private ranges are
  trusted with no configuration, so `proxies: false` deployments behind a private load
  balancer would start believing the header.
- **Keep the first entry.** Rejected: behind an appending proxy the first entry is
  the caller's.
- **Fall through to the next sub-resolver on an untrusted peer.** Rejected: a proxy
  missing from `server.trustedproxies` would resolve from a caller-authored source
  without any signal.

## Consequences

- **Breaking with `proxies: true`.** A proxy on a public or `100.64.0.0/10` address
  must be listed in `server.trustedproxies`, or every request it forwards with
  `X-Forwarded-Host` is rejected with 400. Appending chains now resolve from the last
  entry. Tests that send the header through `httptest.NewRequest` (peer `192.0.2.1`,
  public) must set a trusted `RemoteAddr`.
- A direct caller can still choose a tenant with `Host`, with or without `proxies`,
  and a direct caller inside the VPC is a trusted private peer. Network posture and
  ADR-039 obligation 1 remain the control for direct reachability and for a proxy that
  passes the client's header through untouched.
- The forwarded-client-cert identity headers
  ([ADR-043](adr_043_forwarded_client_cert.md)) stay posture-only; this ADR does not
  extend peer gating to them.

## References

- `multitenant/resolver.go` — `SubdomainResolver`, `CompositeResolver`
- `multitenant/errors.go` — `ErrUntrustedForwardedHost`
- `server/middleware.go` — `buildTenantResolver`, `newSubdomainResolver`; `server/server.go` —
  `vetTrustedProxies` (the vetting both the extractors and the resolver use), `trustedProxyOptions`
- gaborage/go-bricks#1918
- See [migrations.md](migrations.md) `[C72.8]`.
