# ADR-174: `PathResolver` Reads the Path the Router Matches

**Status:** Accepted
**Date:** 2026-11-08
**Related:** [ADR-039](adr_039_composite_resolver_order.md) (composite resolver order) · [ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md) (a path-read tenant is written only when the store holds it)

## Context

`multitenant.PathResolver` reads the tenant from a 1-indexed path segment. It split the decoded
`req.URL.Path`, while echo's router, under the framework's `RouterConfig` (echo's defaults), matches
`req.URL.RawPath` whenever net/url keeps one and binds path parameter values as sent, still
escaped (`UnescapePathParamValues` is false). The two read different paths whenever a request
arrives percent-encoded.

An encoded slash in an earlier segment therefore shifted the resolver's segment index but not the
router's. `GET /a%2Fb/acme/orders` against the route `/:p/:tenant/orders`, with `Segment: 2`,
bound the route parameter `tenant=acme` while the resolver read `b` and the framework wrote `b`
to the request context: the handler's parameter and the per-tenant resources it reached
(`deps.DB(ctx)`, the cache, messaging) named different tenants. The resolver also trimmed
whitespace the router keeps, so `/%20acme` bound ` acme` and resolved `acme`.

## Decision

1. **`PathResolver.ResolveTenant` splits `cmp.Or(req.URL.RawPath, req.URL.Path)`**, the router's own
   selection: `RawPath` when net/url keeps one, `Path` otherwise. The resolver and the router count
   segments over the same string, so the tenant and a route parameter at the same position always
   name the same segment.
2. **The segment is returned as sent, still escaped**, as the router binds parameter values. For
   `/a%2Fb/acme/x`, segment 2 is `acme`, and segment 1 is `a%2Fb`.
3. **`PathResolver.Prefix` is matched against the same path**, so an encoded spelling of the prefix
   no longer matches it.
4. **No signature or configuration key moves.** The change reaches every `PathResolver` user: the
   framework tenant seat (`multitenant.resolver.type: path`, or `composite` with a `path` member),
   `server.TenantMiddleware` built with a `PathResolver`, and direct callers.

`TestPathResolverAgreesWithRouterParam` pins the resolver against the value echo's router binds
for the same segment.

## Alternatives considered

- **Decode each `RawPath` segment after splitting.** Rejected. It fixes the segment index but
  returns a decoded value where the router binds the escaped one, so `/%61cme/...` would put `acme`
  in context and `%61cme` in the route parameter: the two would still differ.
- **Split `url.URL.EscapedPath()`.** Rejected, for the reasons `server/probe_skip.go`'s probe
  skipper records for the same choice: it re-encodes `Path` when `RawPath` is unset, so it can
  differ from what the client sent, and it ignores a `RawPath` that fails its own validity check
  although the router still matches on that `RawPath`.
- **Split `RawPath` and unescape only the selected segment.** Rejected. The route parameter stays
  escaped, so the tenant in context and the parameter would diverge again for any encoded tenant
  segment.

## Consequences

- **Breaking**: a tenant segment that arrives percent-encoded (`/%61cme/orders`) now resolves to
  `%61cme`, and a space-padded one (`/%20acme/orders`) to ` acme`, where both resolved `acme`.
  The framework seat's tenant-ID grammar `^[a-z0-9-]{1,64}$` treats either as any malformed
  tenant: a bare `path` resolver answers 400, and a composite chain falls through to its next
  member, as it already did for a malformed segment. A caller that runs the resolver without that
  check gets an id no tenant store holds. An encoded `PathResolver.Prefix` (`/%69tsp/...` against
  `/itsp`) no longer matches, so the resolver fails as it does outside its prefix. No
  grammar-valid tenant id needs any byte escaped or padded, so a client that sends the tenant
  segment and the prefix unencoded sees no change.
- **ADR-170's existence check does not cover this case.** `HasTenant` asks only whether the store
  holds the resolved id; when both the id the resolver read and the id the route parameter named
  are tenants the store holds, the check passes either way. Only reading the same segment as the
  router keeps the two equal.
- The tenant seat, the route parameter and `HandlerContext.Param` now agree on every request the
  router matches, encoded or not.
