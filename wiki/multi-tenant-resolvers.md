# Multi-Tenant Resolvers

> Where tenant identifiers come from on an inbound HTTP request, and how the framework picks one. Settings live under `multitenant.resolver` in your YAML config; the framework wires the active resolver into a request-scoped middleware before module routes run, so handlers and database/cache/messaging accessors see the tenant on every call.

## Resolver types

The `multitenant.resolver.type` field selects one of four strategies:

| Type | Tenant source | Typical shape |
|---|---|---|
| `header` | Request header (default `X-Tenant-ID`, configurable) | `X-Tenant-ID: acme` |
| `subdomain` | Hostname with `.<RootDomain>` suffix | `acme.api.example.com` |
| `path` | Path segment (1-indexed) | `/itsp/acme/lifecycle/...` |
| `composite` | Tries header → subdomain → path in order; first non-empty wins | Mix of above |

All resolvers funnel through the same interface (`multitenant.TenantResolver`) and surface the result via `multitenant.SetTenant(ctx, id)` on success. Failure returns `multitenant.ErrTenantResolutionFailed`, which the server middleware maps to a 4xx error response.

## Validation

Every resolved tenant ID is checked against a regex (default `^[a-z0-9-]{1,64}$`) before it lands in context. Reject the request rather than silently masking malformed IDs — tenant strings are used downstream as DB schema names, cache keys, and span attributes, so a strict allowlist closes a class of injection attacks. The composite resolver advances to the next sub-resolver if the validation fails (same as a missing tenant).

## Header resolver

```yaml
multitenant:
  enabled: true
  resolver:
    type: header
    header: X-Tenant-ID   # optional; default is X-Tenant-ID
```

The simplest and most common form for internal APIs where clients can set headers freely. Whitespace is trimmed; empty values fail.

## Subdomain resolver

```yaml
multitenant:
  enabled: true
  resolver:
    type: subdomain
    domain: api.example.com   # leading dot optional
    proxies: true             # trust X-Forwarded-Host
```

Extracts the part before `.<domain>` from the request `Host`. Use `proxies: true` only behind a trusted reverse proxy that sets `X-Forwarded-Host`; otherwise an attacker can forge it. Ports are stripped from the host; IPv6 literals are preserved correctly.

## Path resolver

```yaml
multitenant:
  enabled: true
  resolver:
    type: path
    path:
      segment: 2          # 1-indexed; segment 1 = first part after leading slash
      prefix: /itsp        # optional gate; only paths under here are eligible
```

Use this when downstream clients route by URL path rather than header or hostname. The NovoPayment tokenization services (ITSP, TRTSP, 3DS, Click-to-Pay, Backoffice) are the canonical examples — their public contracts are `/itsp/{clientID}/lifecycle/...`, `/trtsp/{clientID}/provisioning/...`, etc.

**Segment indexing is 1-indexed**: segment 1 is the first non-empty path part after the leading `/`. For `/itsp/acme/lifecycle`, segment 2 yields `acme`.

**Prefix gate** is optional. When set, requests whose path doesn't match `Prefix` exactly or start with `Prefix/` return `ErrTenantResolutionFailed` immediately — useful when a service hosts non-tenanted routes (e.g. `/health`, `/ready`, `/admin`) alongside the tenant-scoped ones. Without a prefix, the resolver always attempts extraction at the configured segment.

**Trailing slashes are tolerated**: `/itsp/acme/` resolves identically to `/itsp/acme`.

**Empty segments are rejected outright**: paths with intermediate empty parts like `/itsp//foo` or a leading double-slash (`//foo`) fail at any segment index — these are malformed inputs that must not produce a tenant ID regardless of which slot is configured.

**Query strings and fragments are ignored**: they're isolated from `URL.Path` by the standard library before the resolver sees the request.

### Why path-segment over Echo's `c.Param("tenantID")`?

Route params would only resolve **inside** a route group like `r.Group("/itsp/:tenantID", ...)`. That means framework middleware that needs the tenant before route matching (rate limiting, request logging, OTel span attributes, tenant-scoped DB resolution) wouldn't have it. The path resolver extracts at the URL level, before routing, so the entire framework's tenant-aware machinery has the value from the first middleware onward.

## Composite resolver

```yaml
multitenant:
  enabled: true
  resolver:
    type: composite
    header: X-Tenant-ID         # tried first
    domain: api.example.com     # tried second
    proxies: true
    path:
      segment: 2                # tried third
      prefix: /itsp
```

Tries each configured sub-resolver in order until one returns a non-empty, valid tenant ID. Order is fixed: **header → subdomain → path**. A sub-resolver is included only when its config is present (e.g., `path:` is included only when `path.segment > 0`).

The typical use case is migrating from header-based clients to path-based clients gradually: keep header support for legacy callers, add `path` for new ones, no downtime.

## Implementation reference

Resolver implementations live in [`multitenant/resolver.go`](../multitenant/resolver.go). All four implement the same interface:

```go
type TenantResolver interface {
    ResolveTenant(ctx context.Context, req *http.Request) (string, error)
}
```

The factory that wires `ResolverConfig` to a concrete resolver instance is `buildTenantResolver` in [`server/middleware.go`](../server/middleware.go). The composite resolver wraps each sub-resolver and returns the first non-empty, validation-passing result.

## Programmatic use

If you need to call a resolver directly (in tests, or as a building block in custom middleware):

```go
import "github.com/gaborage/go-bricks/multitenant"

resolver := &multitenant.PathResolver{Segment: 2, Prefix: "/itsp"}
tenantID, err := resolver.ResolveTenant(ctx, req)
if err != nil {
    // Failure cases: nil request, missing/empty segment, prefix mismatch.
    return err
}
ctx = multitenant.SetTenant(ctx, tenantID)
```

For the multi-tenant resource resolution that consumes the tenant ID downstream (`deps.DB(ctx)`, `deps.Cache(ctx)`, etc.), see the per-subsystem wiki pages.
