# ADR-161: An Unknown Tenant Is a Client Fault

**Status:** Accepted
**Date:** 2026-10-31

> **Amended by [ADR-167](adr_167_inbound_paths_render_from_route_template.md) (#2212):** the
> unknown-tenant WARN's `path` is the masked rendering of the matched template (`maskedPath`:
> `/:tenant/orders` prints `/REDACTED/orders`), not the bare template, the rendering every
> framework inbound sink prints.
>
> **Amended by [ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md) (#2212):** under
> a path-reading resolver the seat does check existence for a path-read tenant: one the store's
> `HasTenant` does not hold is never written, and the typed and raw handler adapters answer this
> ADR's 400 before binding, so the raw adapter (`adaptHandler`) is a third caller of
> `logUnknownTenantRefusal`. Header- and subdomain-won tenants keep this ADR's accessor refusal.

## Context

Under multitenant, the tenant seat (`server/tenant_middleware.go` `tenantMiddlewareEcho`) checks
the resolved tenant's form, through `multitenant.ValidatingResolver`, not its existence. A
malformed tenant answers 400 `BAD_REQUEST` "Invalid tenant" and logs one WARN. A well-formed
tenant the deployment does not serve passes the seat, and the handler runs (#2226):

- **The store's answer had no identity.** For a key absent from the store, the built-in
  `config.TenantStore` `DBConfig`, `CacheConfig` and `BrokerURL` returned a `*config.ConfigError`
  from `NewMultiTenantError`. `ConfigError.Unwrap` returns nil, so no sentinel matched it, and the
  same type also reports a known tenant's missing section, which is a server fault.
- **A raw handler or middleware turned it into a 500.** `server/server.go` `classifyError` found
  no status on it and answered 500 `INTERNAL_ERROR` "An error occurred while processing your
  request", logging `ERROR "unhandled error"`: a caller's mistake paged an operator.
- **A typed handler had no way to say "unknown tenant".** It returns an `IAPIError`, which
  `handlerWrapper.dispatchResponse` renders without `classifyError`, and no `IAPIError` carried a
  cause. It answered whatever it mapped the error to, usually a 500.
- **Nothing defined a dynamic store's answer** for a tenant it does not know.

## Decision

1. **One sentinel: `multitenant.ErrUnknownTenant`** (`"multitenant: unknown tenant"`), a
   standalone `errors.New`. #2166 link 1 redefines it to wrap `ErrTenantResolutionFailed`; every
   check below is `errors.Is`, so it keeps matching. `config` imports `multitenant` for it, and
   `multitenant` imports no `config`.
2. **The built-in store joins it on the absent-key branch only.** `config/tenant_store.go`
   `unknownTenant` returns `fmt.Errorf("%w: %w", multitenant.ErrUnknownTenant, cfgErr)` from
   `DBConfig`, `CacheConfig` and `BrokerURL`, so the error `Is` the sentinel and still `As` the
   `*ConfigError`. A known tenant's missing section (`BrokerURL` with no `messaging.url`,
   `CacheConfig` with the cache not enabled), the control-plane key `""` and named databases are
   unchanged and never take the sentinel.
3. **The answer has the malformed-tenant status and error object:** 400 `BAD_REQUEST` "Invalid
   tenant" (the shared `msgInvalidTenant`). Response headers can differ: the refusal is written
   inside the middleware chain, so it carries the security headers and `Vary` the seat's earlier
   400 does not, which reveals only that the ID matched the public grammar. It also logs one WARN
   in the seat's shape, from `logTenantRejection`:
   `[server.tenant] request rejected: invalid tenant method=... path=... client=... status=400 reason="unknown tenant"`.
   `path` is the matched route template (`matchedRouteTemplate`), not `URL.Path`, because a path
   resolver puts the tenant ID in the URL. The line never carries the tenant ID or the error text,
   whose `ConfigError` names the key. The seat's own reasons (`tenantRejectionReason`) and its
   `URL.Path` are unchanged.
4. **Typed handlers get a door: `server.NewUnknownTenantError(cause error) *UnknownTenantError`.**
   The type embeds `*BaseAPIError` like `BadRequestError`, and `Unwrap` returns `cause`, so
   `errors.Is` the sentinel and `errors.As` `*config.ConfigError` hold on it. **It is
   fail-closed**, deciding at construction from `errors.Is(cause, multitenant.ErrUnknownTenant)`:
   a match answers the 400; any other cause (nil, an outage, a timeout, a `ConfigError` without
   the sentinel) answers 500 `INTERNAL_ERROR` with the production message, whatever `app.debug`
   says. A misused door therefore never turns a server fault into a client fault, and
   `return server.Result[R]{}, server.NewUnknownTenantError(err)` is safe for any accessor error.
5. **One refusal writer for both kinds of handler.** `logUnknownTenantRefusal` writes the WARN
   for a matched cause, and for an unmatched one `classifyError`'s `unhandled error` ERROR line
   (`request_id`, the error type only outside debug). It has two callers, and each refusal logs
   exactly one line:
   - `classifyError`, for raw handlers and middleware. Its order is: deadline (503), then the
     first `IAPIError` in the chain with its own answer (even one that wraps the sentinel),
     logging the refusal line when that error is an `*UnknownTenantError`, then a plain error
     that `Is` the sentinel, wrapped with `NewUnknownTenantError`, then echo and untyped errors.
     The `*UnknownTenantError` check is a type assertion inside the `IAPIError` branch, not its
     own `errors.As`, whose escaping target would add an allocation to every error path
     (ADR-026).
   - `handlerWrapper.dispatchResponse`, for typed routes, when the returned `IAPIError` is an
     `*UnknownTenantError`, before it hands off to the standard, raw-response or JOSE sink. The
     standard sink logs nothing for a 500 `IAPIError`, so without this a misused door would be a
     silent 500.
6. **The route's shape governs, as in [ADR-152](adr_152_body_too_large_one_code.md).** A raw
   handler, a middleware and a standard typed route answer in the standard envelope, with the
   same status and `error` object as the seat. A typed `WithRawResponse` route renders the same
   `code` and `message` in its flat shape. A JOSE route renders them through its own error sink,
   as it does every `IAPIError`.
7. **The dynamic-store contract** (`app.TenantStore` godoc): a store wraps the sentinel only when
   it knows the tenant does not exist. Any other error, such as an outage or a timeout, stays
   unwrapped and is never a 400, so a store that cannot tell never fails open. A raw handler or
   middleware returning it answers 500, or 503 when it wraps `context.DeadlineExceeded`, since
   `classifyError` checks the deadline first; a typed handler returning it through the door
   answers 500.
8. **The seat does no existence check.** It checks form; existence belongs to the store, and the
   first tenant resource the request asks for answers it. An opt-in existence check at the seat
   can come later and return the same sentinel, so the wire answer would not move. (Amended by
   ADR-170: under a path-reading resolver this no longer holds for a path-read tenant, which the
   seat writes only when the store's `HasTenant` holds it; header- and subdomain-won tenants keep
   this decision.)

## Consequences

- The handler still runs until its first per-tenant accessor (`deps.DB(ctx)`, `deps.Cache(ctx)`,
  `deps.Messaging(ctx)`). Work done before it runs for the unknown tenant.
- A route that never touches a per-tenant accessor serves the unknown tenant. Resolution is
  identification, not authorization
  ([multi_tenant_resolvers.md](multi_tenant_resolvers.md#tenant-resolution-is-identification-not-authorization)).
- A typed handler changes only when it adopts the door. One that maps the accessor's error to its
  own 500 keeps that answer.
- The sentinel classifies wherever it sits in the error chain; it is not tied to the request's own
  tenant. A handler that looks up some other tenant and returns that miss raw answers 400 with
  the WARN. Map such an error yourself when it is a server fault.
- A custom `Options.CacheConnector` still dials an unknown tenant: it owns its dial, and
  `FactoryResolver.readCacheSectionForNamespace` still reaches the `*config.ConfigError` and reads
  it as "no section".
- The door's 500 always carries the production message, while a raw route's 500 in a debug
  deployment carries the debug message and details. The door and a raw handler answer
  identically for the 400 everywhere and for the 500 outside `app.debug`.
- `tools/migration` uses `config.TenantStore` as its `DBConfigProvider`, so its unknown-tenant
  error text gains the `multitenant: unknown tenant: ` prefix on its next go-bricks pin bump. Only
  the text moves.
- The access-log line of a refusal is unchanged (#2213).
- **Breaking on the wire.** A client, test, alert or runbook reading the 500 or the ERROR line for
  an unknown tenant now sees a 400 and a WARN. No signature or configuration key moves. See
  [migrations.md](migrations.md) `[C73.21]`.

## Alternatives considered

- **B: an existence check at the seat.** Rejected for this change: the seat would ask the store
  on every request, routes that need no tenant resource included, and on a dynamic store that is
  a lookup, an outage path and a negative-cache design per request. It stays possible as an
  opt-in returning the same sentinel.
- **C: documentation only.** Rejected: the 500 and its ERROR line stay, an operator is still
  paged for a caller's mistake, and a typed handler still cannot say "unknown tenant".

## References

- [ADR-152](adr_152_body_too_large_one_code.md): precedent, one answer per condition with the
  route's shape governing.
- [ADR-140](adr_140_forwarded_host_peer_gate.md): the seat's WARN and its reasons.
- [ADR-127](adr_127_resource_plan_rule.md): the control-plane key `""`.
- [multi_tenant_resolvers.md](multi_tenant_resolvers.md#unknown-tenants): usage, the door and the
  before-the-handler recipe.
- `multitenant/errors.go` (`ErrUnknownTenant`) · `config/tenant_store.go` (`unknownTenant`,
  `DBConfig`, `CacheConfig`, `BrokerURL`) · `server/errors.go` (`UnknownTenantError`,
  `NewUnknownTenantError`) · `server/server.go` (`classifyError`) · `server/handler.go`
  (`handlerWrapper.dispatchResponse`) · `server/tenant_middleware.go` (`logTenantRejection`,
  `logUnknownTenantRefusal`) · `app/interfaces.go` (`TenantStore`).
- gaborage/go-bricks#2226; #2166 redefines the sentinel.
