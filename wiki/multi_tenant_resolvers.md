# Multi-Tenant Resolvers

> Where tenant identifiers come from on an inbound HTTP request, and how the framework picks one. Settings live under `multitenant.resolver` in your YAML config; the framework wires the active resolver into a request-scoped middleware before module routes run, so handlers and database/cache/messaging accessors see the tenant on every call.

## Resolver types

The `multitenant.resolver.type` field selects one of four strategies:

| Type | Tenant source | Typical shape |
| --- | --- | --- |
| `header` | Request header (default `X-Tenant-ID`, configurable) | `X-Tenant-ID: acme` |
| `subdomain` | Hostname with `.<RootDomain>` suffix | `acme.api.example.com` |
| `path` | Path segment (1-indexed) | `/itsp/acme/lifecycle/...` |
| `composite` | Tries sub-resolvers in the **required** `order`; first non-empty wins | Mix of above |

All resolvers funnel through the same interface (`multitenant.TenantResolver`) and surface the result via `multitenant.SetTenant(ctx, id)` on success. Failure returns `multitenant.ErrTenantResolutionFailed`, which the server middleware maps to a 4xx error response.

## Validation

Every tenant ID a framework-built resolver returns is checked against the tenant-ID grammar
`^[a-z0-9-]{1,64}$` (`multitenant.ValidateTenantID`) before it lands in context. A caller-built
resolver passed to `server.TenantMiddleware` is not checked: its non-empty result goes into
context as returned, so validate it with `multitenant.ValidateTenantID` inside the resolver. The grammar is fixed, not
configurable ([ADR-162](adr_162_tenant_id_grammar_is_fixed.md)). Reject the request rather than
silently masking malformed IDs: a tenant ID shares key spaces with other identifiers, and the
grammar is what keeps them apart. It keeps a tenant named `named:x` out of the named-database keys
(`databases.x`), keeps a `:` from reaching into another tenant's cache keys (the cache key
delimiter, [ADR-117](adr_117_cache_key_namespace_and_cluster_mode.md)), and is the fleet-wide wire
contract of the `x-tenant-id` message stamp, checked when a message is published and again when a
`messaging.tenancy: shared` consumer reads it (a per-tenant consumer ignores the stamp). No schema or SQL identifier is built from a tenant ID. The composite resolver advances to the next
sub-resolver if the validation fails (same as a missing tenant).

The same grammar applies to the keys of a STATIC `multitenant.tenants` map, checked at startup
rather than per request. The section-name check runs first, so a tenant section is reachable by
environment variable ([ADR-090](adr_090_env_reachable_section_names.md)); then
`multitenant.ValidateTenantID` runs, so the 64-byte bound applies at load and a longer key fails
startup. A hyphenated ID is legal but settable only where the runtime permits `-` in a variable
name (Docker and Kubernetes yes, POSIX `export` no).
A DYNAMIC source's IDs are not checked at startup — they arrive at request time and this resolver
rule is their gate.

**This is input sanitization, not access control.** The regex — and, for a path-read tenant, the store's `HasTenant` check — reject *garbage*, not *cross-tenant access*. A forged value naming a real tenant passes both checks and resolves fine. Resolution answers "which tenant does this request claim to be for"; it never checks whether the caller is entitled to that tenant. See [Tenant resolution is identification, not authorization](#tenant-resolution-is-identification-not-authorization).

## Unknown tenants

**A path-read tenant is written only when the store holds it** ([ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md)). When a `path` resolver produced the id (a bare `path` resolver, or the `path` member of a composite chain), the seat asks the App's tenant resource source (`multitenant.TenantChecker`, which `*config.TenantStore` implements) before writing it. An id the store does not hold is never written: the request runs on with no tenant through global middleware, route-group middleware and JOSE inbound verification, and the typed or raw handler adapter then answers 400 `BAD_REQUEST` "Invalid tenant" before binding, with one WARN reading `reason="unknown tenant"` and the masked path. An unauthenticated caller on an auth group gets the group's 401 for every id, and an unmatched request keeps its 404/405. The App hands the server it builds its resource source's check (`server.Options.TenantChecker`), replacing any value in `app.Options.ServerOptions`; a server passed as `app.Options.Server` keeps its own; a server with no checker, or a custom `app.Options.ResourceSource` without `HasTenant`, refuses every path tenant.

Operating it:

- **A custom resource source's `HasTenant`** must load its set before `app.New`, keep the last good set on a failed refresh, never shrink to empty, never panic, and log each refresh's size. It is in memory: no context, no I/O, no blocking.
- **Every path tenant answering 400 on one pod** means that pod's set is empty or stale; `Panic recovered` with no access line means its `HasTenant` panicked.
- **Authenticated unknown-tenant 400s** read as catalog drift or credentialed enumeration. The principal appears only on the span's `enduser.id`, with `auth.jwt.telemetry.enduserid` on; otherwise correlate on `client.address` and `user_agent.original`.

Header- and subdomain-won tenants are checked for form only, so a well-formed tenant the deployment does not serve reaches the handler. The first per-tenant accessor the request calls (`deps.DB(ctx)`, `deps.Cache(ctx)`, `deps.Messaging(ctx)`) refuses it: for a key absent from `multitenant.tenants`, the built-in store returns an error that `errors.Is` `multitenant.ErrUnknownTenant`. The answer is the malformed-tenant one, 400 `BAD_REQUEST` "Invalid tenant", plus one WARN with `reason="unknown tenant"` whose `path` is the masked route template (`/REDACTED/orders`), never the tenant ID ([ADR-161](adr_161_unknown_tenant_is_a_client_fault.md), [ADR-167](adr_167_inbound_paths_render_from_route_template.md)). A known tenant missing a section, the control-plane key `""` and named databases still answer 500, as server faults.

- **Raw handlers and middleware** get it automatically: return the accessor's error, bare or wrapped with `%w`.
- **Typed handlers** return it through `server.NewUnknownTenantError`. The door is fail-closed: a cause that is not the sentinel (nil, an outage, a timeout) answers 500, so it is safe for any accessor error. A `WithRawResponse` route renders the same `code` and `message` in its flat shape, and a JOSE route through its own error sink.

```go
db, err := h.getDB(ctx.RequestContext())
if err != nil {
	return server.Result[Order]{}, server.NewUnknownTenantError(err) // 400 unknown tenant, else 500
}
```

To keep the handler's own 500 wording for every other error, guard first:

```go
switch {
case errors.Is(err, multitenant.ErrUnknownTenant):
	return server.Result[Order]{}, server.NewUnknownTenantError(err)
case err != nil:
	return server.Result[Order]{}, server.NewInternalServerError("orders unavailable")
}
```

**A dynamic store** (`app.Options.ResourceSource`) wraps `multitenant.ErrUnknownTenant` only when it knows the tenant does not exist, as in `fmt.Errorf("%w: no catalog entry", multitenant.ErrUnknownTenant)`. Any other error, such as an outage or a timeout, stays unwrapped and is never a 400, so a store that cannot tell never fails open: a raw handler or middleware returning it answers 500, or 503 when it wraps `context.DeadlineExceeded`, and a typed handler returning it through `server.NewUnknownTenantError` answers 500.

**A custom `app.Options.CacheConnector` still dials an unknown tenant:** it owns its dial, and the framework reads the store's error for that key as "no cache section". Refuse unknown tenants in the connector, or before the handler as below.

**Refusing before the handler (header- and subdomain-won tenants).** A path-read tenant the store does not hold never reaches this middleware with a tenant (above). For a header or subdomain tenant, the handler runs until its first accessor, and a route that never calls one serves the unknown tenant. To refuse earlier, or in your own shape, attach a group middleware: it runs after the seat, so a tenant the seat wrote is in context. It reads the tenant catalog and writes its own bytes:

```go
func knownTenant(deps *app.ModuleDeps) server.MiddlewareFunc {
	return func(c server.HandlerContext, next func() error) error {
		ctx := c.RequestContext()
		tenantID, ok := multitenant.GetTenant(ctx) // set by the seat, which runs first
		if !ok {
			return next() // none written: a path tenant the store does not hold, refused 400 by the handler adapter
		}
		catalog, err := deps.DBByName(ctx, "catalog")
		if err != nil {
			return err // 500
		}
		var n int
		// $1 is PostgreSQL's placeholder; an Oracle catalog takes :1.
		if err = catalog.QueryRow(ctx, "SELECT COUNT(*) FROM tenants WHERE id = $1", tenantID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "unknown tenant"})
		}
		return next()
	}
}

orders := r.Group("/orders", knownTenant(m.deps)) // m.deps saved in Init
```

Returning `multitenant.ErrUnknownTenant` instead of writing the body gives the framework's 400 and WARN.

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
    proxies: true             # read X-Forwarded-Host from a trusted peer
```

Extracts the part before `.<domain>` from the request `Host`. Ports are stripped from the host; IPv6 literals are preserved correctly. Matching is ASCII case-insensitive (RFC 4343): ASCII letters in the host and in `domain` fold to lowercase first, so `ACME.api.example.com` resolves tenant `acme`. Only ASCII letters fold: a non-ASCII byte in the tenant label, such as U+212A KELVIN SIGN, is kept as is, and the tenant-ID grammar check the server wraps every resolver in refuses it. `SubdomainResolver` itself does not validate its result, so a caller that uses it directly checks the label with `multitenant.ValidateTenantID`.

With `proxies: true` the resolver reads `X-Forwarded-Host` instead, but only when the immediate peer is trusted by the rule the server applies to `X-Forwarded-For` and `X-Forwarded-Proto`: loopback, link-local, private and unix-socket peers, plus the vetted `server.trustedproxies` ranges ([ADR-140](adr_140_forwarded_host_peer_gate.md)). It takes the last comma-separated entry of the last header line, the one the nearest proxy wrote; an empty last entry is no match. When any other peer sends the header, the request is rejected with 400 `Invalid tenant` (a composite order stops there), and the WARN reason reads `X-Forwarded-Host from a peer outside server.trustedproxies` without logging the header. A proxy on a public or `100.64.0.0/10` address therefore needs its CIDRs in `server.trustedproxies`, the same entry the client IP and HSTS already need. A request without the header resolves from `Host`, and `proxies: false` (the default) ignores the header.

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

Use this when downstream clients route by URL path rather than header or hostname. Payment-tokenization APIs are the canonical example — public contracts shaped like `/itsp/{clientID}/lifecycle/...`, `/trtsp/{clientID}/provisioning/...`.

**Segment indexing is 1-indexed**: segment 1 is the first non-empty path part after the leading `/`. For `/itsp/acme/lifecycle`, segment 2 yields `acme`.

**Prefix gate** is optional. When set, requests whose path doesn't match `Prefix` exactly or start with `Prefix/` return `ErrTenantResolutionFailed` immediately — useful when a service hosts non-tenanted routes (e.g. `/health`, `/ready`, `/admin`) alongside the tenant-scoped ones. Without a prefix, the resolver always attempts extraction at the configured segment.

**Trailing slashes are tolerated**: `/itsp/acme/` resolves identically to `/itsp/acme`.

**Empty segments are rejected outright**: paths with intermediate empty parts like `/itsp//foo` or a leading double-slash (`//foo`) fail at any segment index — these are malformed inputs that must not produce a tenant ID regardless of which slot is configured.

**Query strings and fragments are ignored**: they're isolated from `URL.Path` by the standard library before the resolver sees the request.

### Why path-segment over Echo's `c.Param("tenantID")`?

A route parameter such as `:tenantID` in `/itsp/:tenantID` is matched by echo's router on any route, grouped or not, but it never reaches a resolver. Echo v5 matches the route first and the tenant middleware runs on the root `e.Use` chain after it, before group and route middleware, but a resolver receives only the `*http.Request`: echo's router keeps a matched `:tenantID` on its own context and does not copy it onto the request (`SetPathValue`). The path resolver reads the segment from `URL.Path` instead, so middleware and handlers that run after the tenant middleware (request logging, group and route middleware, tenant-scoped DB resolution) have the value. Middleware ahead of it on the root chain, such as the OTel middleware and the IP pre-guard, does not.

The deliberate exception is a tenant-owned route family (`server.TenantOwnedGroup`, [ADR-164](adr_164_tenant_owned_route_family.md)). Root resolution skips its routes, and the family resolves its own tenant inside its own pipeline, after its own authentication, so a route param or a verified claim can name the tenant there. On a family route, the root chain and the family's `Authenticate` and `Tenant` steps run without a tenant (the built-in rate limiter, for one, keys every request on the client IP, ADR-160). Everything after the framework publishes the tenant has it: group and route middleware, binding, JOSE, the handler and `deps.DB(ctx)`. Global middleware cannot be installed beside a family ([global_middleware.md](global_middleware.md#fail-closed-startup)).

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
- `config.DefaultResolverOrder()` returns `[subdomain, path, header]`. That is the **recommended** order — it is never applied to a *validated* config (validation fails instead). It is used as a last-resort fallback only for a hand-built `ResolverConfig` that bypassed `config.Validate()` entirely (an embedding app or a test), so such a config can't silently end up with zero sub-resolvers and disable tenant resolution.

**Which order do you need?**

| Your edge | Order to pin |
| --- | --- |
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

The resolver extracts an identifier and puts it in `context.Context`. It performs **no entitlement check**. Whatever the order, your deployment must authorize the resolved tenant against the authenticated principal — e.g. a middleware attached in the same `RouteRegistrar.Group` call as `auth.Middleware` and listed after it, that reads `tenantID, ok := multitenant.GetTenant(ctx.RequestContext())`, rejects the request when `ok` is false, and compares `tenantID` with the tenant claim of the verified `auth.Principal` (`auth.PrincipalFromContext`). A [global middleware](global_middleware.md) runs before every group middleware, so it sees no verified principal unless it verifies the credential itself. A tenant-owned route family (`server.TenantOwnedGroup`, [ADR-164](adr_164_tenant_owned_route_family.md)) can instead take its tenant from the verified credential in its `Tenant` step, for example `auth.TenantClaim("tid")`, so there is no caller-written value to compare.

The same holds for a per-tenant rate limit. The framework's global limiter keys on the client IP, never on the resolved tenant ([ADR-160](adr_160_rate_limit_keys_on_client_ip.md)). A per-tenant budget is `server.TenantRateLimit`, attached in the same `RouteRegistrar.Group` call as `auth.Middleware`, listed after it, and after whatever check ties the resolved tenant to the caller; ahead of that check, any caller can drain a tenant it names. On a tenant-owned route family, add it with the family's `Use` before registering the family's routes, since `Use` reaches only routes registered after the call; it then runs after the tenant is published. In the family's `Authenticate` steps no tenant exists yet, so it would charge nothing.

> **Deployment obligations (normative).** A composite deployment that does not meet these does not have tenant isolation, whatever order it declares. Each is scoped to the order you actually declare — an obligation about a sub-resolver you did not list does not apply to you.
>
> 1. If `subdomain` participates in the order, the ingress **must** validate `Host` against the tenant's own DNS name. On a permissive wildcard vhost, a caller can send `Host: other-tenant.api.example.com` and the subdomain resolver reads `other-tenant`. With `proxies: true`, the proxy **must** be a trusted peer (default ranges or `server.trustedproxies`) and **must** overwrite or append `X-Forwarded-Host`; the resolver reads its last entry and rejects the header from any other peer (ADR-140). A proxy that passes the client's header through untouched is indistinguishable from the client, so the ingress **must** still ensure only the trusted proxy sets it.
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

### Path provenance

`multitenant.Resolve(ctx, resolver, req)` returns the error `resolver.ResolveTenant` returns and, when that error is nil, its id (on error the id is empty), plus `fromPath`: true when a `*PathResolver` produced the id, or a resolver whose provenance is unknown. Provenance is judged on the concrete framework types alone, so a consumer type, even one embedding `*HeaderResolver`, counts as unknown (fail closed). `multitenant.MayReadPath(resolver)` is the static check: false only when every member able to produce an id is a header or subdomain resolver; a resolver tree that reaches itself answers true (fail closed).

When `multitenant.enabled` is true and `MayReadPath` is true for the configured resolver, the root tenant seat skips the framework's own routes, as it skips the probes: everything under `<server.path.base>/_sys` (the scheduler's job API) and, only when `debug.enabled` is also true, everything under `debug.pathprefix`. Otherwise nothing is skipped. Those routes carry no tenant, a composite chain's header or subdomain tenant included ([ADR-169](adr_169_framework_routes_skip_path_tenant.md)). Startup refuses a consumer route, middleware-bearing group or `server.TenantOwnedGroup` family at or below either root, and, under those same conditions, an enabled `debug.pathprefix` that is empty or `/` (the engine root) or at or above `server.path.base`: that prefix is not skipped, Start fails naming both keys ([ADR-168](adr_168_framework_route_roots_reserved.md)).

`multitenant.TenantChecker` (`HasTenant(tenantID string) bool`) is the in-memory existence check that `*config.TenantStore` implements. The framework seat asks it about every id `Resolve` reports `fromPath` ([Unknown tenants](#unknown-tenants), [ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md)); the public `server.TenantMiddleware` does not ask it yet. Its godoc carries the contract every implementation keeps. `tenantID` may be an unadmitted request value, so never log it, echo it, put it in an error, send it outbound, pass it to a `deps.*` call, or use it as a cache, store or metric key. Answer false for `""` and for any non-tenant key, `config.NamedDatabasePrefix` keys included. Be safe for concurrent use and hold the complete set before `app.New`. On a failed refresh, keep the last good set rather than shrink to empty. Never panic. The answer is existence, not authorization.

For the multi-tenant resource resolution that consumes the tenant ID downstream (`deps.DB(ctx)`, `deps.Cache(ctx)`, etc.), see the per-subsystem wiki pages.
