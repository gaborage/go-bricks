# Global Middleware

Module-contributed HTTP middleware that runs once per request across every route — the
framework-level seam for cross-cutting concerns like authentication, API-key or
request-signature verification, global audit logging, and maintenance-mode gates.

See [ADR-036](adr_036_global_middleware.md) for the design rationale.

## The interface

A module opts in by implementing `GlobalMiddlewareRegisterer` (alongside the optional
`RouteRegisterer` / `MessagingDeclarer` capabilities):

```go
type GlobalMiddlewareRegisterer interface {
    GlobalMiddleware() []server.MiddlewareFunc
}
```

The framework detects implementers at startup, collects their middleware **after every
module `Init()`**, and registers it once on the root request chain. Because the middleware
is built after `Init()`, it can capture dependencies the module wired up there — a
keystore-backed token verifier, a DB/cache handle, resolved config.

## Guarantees

| Property | Behavior |
|---|---|
| Runs once per request | Registered on the root echo chain (`e.Use`), compiled once, applied to every request. |
| Cannot be skipped per-route | Registration is framework-controlled; no route-level opt-out exists. |
| After tenant resolution | Lands after the built-in chain, so the tenant is already in `context.Context`. |
| Skips health/ready | The framework wraps each with the health/ready probe skipper. |
| Deterministic ordering | Across modules, middleware composes in module-registration order. |

## Writing an auth gate

```go
func (m *AuthModule) Init(deps *app.ModuleDeps) error {
    m.verifier = jwt.NewVerifier(deps.Config, m.keystore) // built here, captured below
    return nil
}

func (m *AuthModule) GlobalMiddleware() []server.MiddlewareFunc {
    return []server.MiddlewareFunc{
        func(c server.HandlerContext, next func() error) error {
            if isPublicPath(c.Request().URL.Path) { // your own exemptions (see below)
                return next()
            }
            if !m.verifier.Valid(c.Request().Header.Get("Authorization")) {
                return server.NewUnauthorizedError("invalid token")
            }
            return next()
        },
    }
}
```

Short-circuit by returning an `IAPIError` (`server.NewUnauthorizedError` /
`NewForbiddenError`) **without** calling `next()`. The framework renders the standard
`{error:{code,message}, meta:{timestamp,traceId}}` envelope at the mapped status. Never
return a plain `fmt.Errorf` — it maps to HTTP 500. Return the error *or* write a response,
never both.

### Public-route exemptions

Because no route can opt out, exemptions (login/token endpoints, public assets) live **inside
the middleware body** — inspect `c.Request().URL.Path` or `c.RouteTemplate()`. The only
framework-level exemption is the health/ready probes.

## Placement in the chain

Global middleware lands at the **innermost slot** of the root chain:

```
requestID → tenant resolution → logger → Recover → timeout → rate-limit → [GLOBAL MIDDLEWARE] → handler
```

Consequences:

- Runs **inside `Recover`** — panics are caught.
- Runs **after rate-limiting** — an unauthenticated request still consumes rate-limit budget
  before being rejected (intended: cheap flood rejection). This is an auth-gate hook, not a
  general "run early" hook.
- Fires **before** the 404/405 response, so unauthenticated requests to unknown paths get 401
  (does not leak route existence).
- `/_sys` (scheduler) and `/debug` endpoints are **gated** (their own CIDR/bearer checks still
  apply underneath); only health/ready are exempt.

## Limitations

- **JOSE routes**: request decryption/verification runs in the typed-handler leaf, *after* the
  middleware chain. A global gate therefore authenticates on headers/token/tenant, never the
  decrypted body. Put body-dependent authorization in the handler.
- **Raw-response (Strangler-Fig) routes**: a global rejection emits the standard envelope, not
  the legacy raw shape (the raw-mode marker is set in the leaf, after middleware).

## Fail-closed startup

If a module registers global middleware but the configured server cannot install it (a custom
`ServerRunner` that is not `*server.Server`), startup **aborts** rather than silently serving
unguarded traffic — a mis-wired security gate should refuse to start.
