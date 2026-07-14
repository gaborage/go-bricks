# Multi-Tenant Resolvers

> Where tenant identifiers come from on an inbound HTTP request, and how the framework picks one. Settings live under `multitenant.resolver` in your YAML config; the framework wires the active resolver into a request-scoped middleware before module routes run, so handlers and database/cache/messaging accessors see the tenant on every call.

## Resolver types

The `multitenant.resolver.type` field selects one of four strategies:

| Type | Tenant source | Typical shape |
|---|---|---|
| `header` | Request header (default `X-Tenant-ID`, configurable) | `X-Tenant-ID: acme` |
| `subdomain` | Hostname with `.<RootDomain>` suffix | `acme.api.example.com` |
| `path` | Path segment (1-indexed) | `/itsp/acme/lifecycle/...` |
| `composite` | Tries sub-resolvers in the **required** `order`; first non-empty wins | Mix of above |

All resolvers funnel through the same interface (`multitenant.TenantResolver`) and surface the result via `multitenant.SetTenant(ctx, id)` on success. Failure returns `multitenant.ErrTenantResolutionFailed`, which the server middleware maps to a 4xx error response.

## Validation

Every resolved tenant ID is checked against a regex (default `^[a-z0-9-]{1,64}$`) before it lands in context. Reject the request rather than silently masking malformed IDs — tenant strings are used downstream as DB schema names, cache keys, and span attributes, so a strict allowlist closes a class of injection attacks. The composite resolver advances to the next sub-resolver if the validation fails (same as a missing tenant).

**This is input sanitization, not access control.** The regex — and the subsequent known-tenant lookup — reject *garbage*, not *cross-tenant access*. A forged value naming a real tenant passes both checks and resolves fine. Resolution answers "which tenant does this request claim to be for"; it never checks whether the caller is entitled to that tenant. See [Tenant resolution is identification, not authorization](#tenant-resolution-is-identification-not-authorization).

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
    order: [subdomain, path, header]   # REQUIRED for composite — no default
    header: X-Tenant-ID
    domain: api.example.com            # required when `order` contains `subdomain`
    proxies: true
    path:
      segment: 2                       # required (> 0) when `order` contains `path`
      prefix: /itsp
```

Tries each sub-resolver named in `order` until one returns a non-empty, valid tenant ID; first match wins.

**`multitenant.resolver.order` is required for `type: composite`.** There is no implicit default — a composite config without it **fails validation at startup** and the application does not boot. Env form is a comma-separated list: `MULTITENANT_RESOLVER_ORDER=subdomain,path,header`.

- Valid entries: `header`, `subdomain`, `path`. Unknown entries and duplicates are rejected; `order` set on any non-composite `type` is rejected outright rather than silently ignored.
- A sub-resolver named in the order must be configured: `path` requires `path.segment > 0`; `subdomain` requires a real `domain` (a `domain` of `"."` is rejected). A composite whose order omits `subdomain` needs no `domain` at all. `header` needs no configuration — it defaults to `X-Tenant-ID`.
- `config.DefaultResolverOrder()` returns `[subdomain, path, header]`. That is the **recommended** order, not a default that gets applied for you.

**Which order do you need?**

| Your edge | Order to pin |
|---|---|
| A trusted gateway authenticates the caller and **owns `X-Tenant-ID`** (strips the inbound header, sets its own) | `[header, subdomain, path]` — otherwise a caller-controlled `Host`/path outranks the gateway's assertion |
| Per-tenant DNS (each tenant has its own hostname) | `[subdomain, path, header]` — the recommended order |
| **Path-scoped contracts only**, no per-tenant DNS | `[path, header]` — omit `subdomain` and you need no `domain` at all. Listing `subdomain` without per-tenant DNS just forces you to invent a `domain` the resolver will never match |
| No legacy header clients left | Drop `header` from the order entirely (e.g. `[subdomain, path]`). The strongest option: a request matching no source then fails closed instead of falling through to the header |

See [ADR-039](adr_039_composite_resolver_order.md) for why the framework refuses to pick for you.

### Tenant resolution is identification, not authorization

**All three tenant sources are written by the caller.** The framework chooses *which caller-supplied field it reads*; it cannot make any of them trustworthy:

- **Header** — `X-Tenant-ID` is a request header. Any caller can set it to anything.
- **Path** — the URL path is authored entirely by the caller. With `segment: 2` and no prefix gate, a request to `/api/v1/orders` resolves the tenant `v1`.
- **Subdomain** — `SubdomainResolver` reads `req.Host`, and `Host` is *itself a request header*. It is constrained only if your ingress pins it (per-tenant DNS/vhost/SNI, or a `Host` allowlist) — something the framework cannot verify.

The resolver extracts an identifier and puts it in `context.Context`. It performs **no entitlement check**. Whatever the order, your deployment must authorize the resolved tenant against the authenticated principal — e.g. a [global middleware](global_middleware.md) comparing `multitenant.Tenant(ctx)` against the tenant claim in the verified JWT.

> **Deployment obligations (normative).** A composite deployment that does not meet these does not have tenant isolation, whatever order it declares. Each is scoped to the order you actually declare — an obligation about a sub-resolver you did not list does not apply to you.
>
> 1. If `subdomain` participates in the order, the ingress **must** validate `Host` against the tenant's own DNS name. On a permissive wildcard vhost, a caller can send `Host: other-tenant.api.example.com` and the subdomain resolver reads `other-tenant`. With `proxies: true`, the ingress **must** also ensure only the trusted proxy can set `X-Forwarded-Host`.
> 2. If `path` participates in the order, the tenant segment **must** be authorized against the authenticated principal. **The path segment is not an authorization boundary and this ordering does not make it one.**
> 3. If `header` participates in the order, the gateway **must** strip or overwrite inbound `X-Tenant-ID`.
> 4. Conversely, if your gateway **owns** `X-Tenant-ID`, you **must** pin a header-first order — otherwise a caller-controlled `Host`/path outranks it. This one is unconditional: it is a statement about your edge, not about your order.
> 5. If `header` sits **behind** another source in the order, the edge **must** force every tenant-scoped request to carry a resolvable subdomain/path — otherwise the fall-through below silently reinstates header trust. An order that omits `header` is not exposed to this: an unmatched request simply fails closed.

### The fall-through bypass

First-match precedence is defeated by making the higher-precedence resolver **fail**, not by beating it. With `order: [subdomain, path, header]`, `domain: api.example.com`, and `path.prefix: /itsp`:

> A caller hits the **apex host** `api.example.com` (no tenant label → subdomain returns no match) on a path **outside** `/itsp` (→ path returns no match) while sending `X-Tenant-ID: b`. Both higher-precedence sources decline, and resolution falls through to the header anyway.

**Ordering only removes the header override for requests where a higher-precedence source actually matches.** If the edge lets tenant-scoped requests through on the apex host or on unprefixed paths, header trust is reinstated via fall-through. Force every tenant-scoped request to carry a resolvable subdomain/path — or drop `header` from the order entirely.

### What ordering *does* buy you

The correct reaction to "the path is caller-controlled" is **add the authorization check**, not **rip out the path resolver**.

- For a deployment whose edge **does** pin `Host` per tenant, putting `subdomain` ahead of the header is the difference between real isolation and none. The old hardcoded header-first order was actively throwing that edge control away.
- An explicit order keeps the resolved tenant in agreement with what the rest of the stack sees — the matched route, the access log, the segment an authorization middleware would check — instead of letting an invisible header silently desynchronize "the tenant this request appears to be for" from "the tenant whose database `deps.DB(ctx)` returns".

The typical use case is migrating from header-based clients to path-based clients gradually: keep header support for legacy callers, add `path` for new ones, no downtime — with `order` making the precedence between them explicit for the overlap window.

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
