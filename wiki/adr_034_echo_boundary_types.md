# ADR-034: Echo-Free Boundary Types

**Status:** Accepted
**Date:** 2026-06-30

## Context

The Developer Manifesto's **Vendor Agnosticism** principle says: *"Abstract
high-cost dependencies (databases), embrace low-cost ones (HTTP frameworks)."*
GoBricks deliberately does **not** abstract Echo away — Echo stays the embraced
engine inside `server/` for performance ([ADR-015](adr_015_echo_v5_migration.md)).
But "embrace the engine" was never meant to mean "leak its concrete types onto
every consumer's surface." Before this change, `github.com/labstack/echo/v5`
types appeared directly on the public API that application authors touch, name,
and import — coupling every downstream service to Echo's concrete API and version,
and contradicting the Interface-Segregation and Composition principles already
applied to `database`/`cache`/`messaging` (all of which expose go-bricks types,
never the vendor's).

Six leak classes were verified against HEAD (issue #623):

| # | Leak | Location |
|---|------|----------|
| L1 | `HandlerContext.Echo *echo.Context` exported field | `server/handler.go` |
| L2 | `RouteRegistrar.Add/Group/Use` take `echo.HandlerFunc`/`echo.MiddlewareFunc`; `Add` returns `echo.RouteInfo` | `server/handler.go`, `server/route_registrar.go` |
| L3 | `ServerRunner.Echo() *echo.Echo` + `RegisterReadyHandler(echo.HandlerFunc)` | `app/interfaces.go` |
| L4 | `RegisterDebugEndpoints(*echo.Echo)` + all `app/debug_*.go` handlers `func(*echo.Context) error` + whitelist/auth middleware as `echo.MiddlewareFunc` | `app/debug_*.go` |
| L5 | `scheduler.CIDRMiddleware(...) echo.MiddlewareFunc` (exported) | `scheduler/cidr_middleware.go` |
| L6 (class) | ~11 exported `server.*` middleware constructors returning `echo.MiddlewareFunc`; `SkipperFunc func(*echo.Context) bool`; `EscalateSeverity(*echo.Context, …)` | `server/{cors,ip_preguard,logger,…}.go` |

`ClientIP`/`ParseCIDRs` (`server/clientip.go`) already used stdlib types and were
unchanged.

**Acceptance north-star:** `git grep -l 'labstack/echo'` resolves to `server/`
only (production + tests). `app/`, `scheduler/`, and all their `_test` files
become echo-free, and no exported symbol on the consumer path names an `echo.*`
type. This is a **boundary-type change only** — no router or middleware
reimplementation.

## Decision

Wrap every remaining `echo.*` leak behind go-bricks boundary types while Echo
remains the unchanged engine inside `server/`.

**New boundary types (`server` package):**

```go
// Flat, framework-neutral middleware: run logic, then call next() to continue
// the chain (or return an IAPIError WITHOUT calling next to abort).
type MiddlewareFunc func(c HandlerContext, next func() error) error

// Untyped, echo-free handler for raw routes, the readiness probe, and debug
// endpoints — distinct from the typed HandlerFunc[T,R], and unrelated to
// WithRawResponse/RawResponse (the APIResponse-envelope-bypass mode).
type Handler func(c HandlerContext) error
```

`HandlerContext` drops the exported `Echo` field for an unexported `ectx
*echo.Context` escape hatch (greppable, unnameable cross-package) plus
stdlib-typed accessors: `Request()`, `RequestContext() context.Context`,
`ResponseWriter()`, `Param`, `Query`, `RequestHeader` (renamed from `Header` for
request/response disambiguation), `Get`/`Set`, `SetRequest`,
`SetRequestContext`, `JSON`, `String`, and `EscalateSeverity(level)`. The two
mutators (`SetRequest`/`SetRequestContext`) are **required** for the locked flat
shape to express the canonical context-propagation pattern; without them a
read-only context would strand every consumer auth/tenant middleware.

### The changed contract

```go
type RouteRegistrar interface {
    Add(method, path string, handler Handler, middleware ...MiddlewareFunc) // RouteInfo return dropped (never captured anywhere)
    Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar
    Use(middleware ...MiddlewareFunc)
    FullPath(path string) string
}

type ServerRunner interface {
    Start() error
    Shutdown(ctx context.Context) error
    RootGroup() server.RouteRegistrar   // NEW — root (no basePath) registrar for debug/_sys-style endpoints
    ModuleGroup() server.RouteRegistrar // unchanged (basePath-applied)
    RegisterReadyHandler(handler server.Handler) // retyped from echo.HandlerFunc; nil restores the default
}
```

`scheduler.CIDRMiddleware(...)` returns `server.MiddlewareFunc`. The middleware
constructor class (`CORS`, `RateLimit`, `Timeout`, `Timing`, `LoggerWithConfig`,
`IPPreGuard`, `RequestIDMiddleware`, `RequestEnrich`, `TenantMiddleware`,
`TraceContext`, `PerformanceStats`) returns `server.MiddlewareFunc` — call sites
are unchanged. `SkipperFunc` becomes `func(r *http.Request) bool` (a skip decision
needs only the request); severity escalation is the `HandlerContext.EscalateSeverity(level)`
method (the old package-level `server.EscalateSeverity` is removed). A new exported
`server.NewHandlerContextForTest(w, r, cfg) HandlerContext` lets external-package
tests (`app/`, `scheduler/`) build a context without naming the escape hatch.

### Changed (consumer-visible)

- `HandlerContext.Echo` field → accessors (`RequestContext()`, `Request()`, …).
- Custom middleware signature → flat `MiddlewareFunc` (no nested `func(next) next`).
- `RouteRegistrar.Add` drops its `echo.RouteInfo` return; `Add`/`Group`/`Use` take go-bricks types.
- `ServerRunner.Echo()` **removed**; `RootGroup()` added; `RegisterReadyHandler` takes `server.Handler`.
- `scheduler.CIDRMiddleware`, all framework middleware constructors, `SkipperFunc`, and `EscalateSeverity` return/accept go-bricks types.

### Unchanged

- Typed `HandlerFunc[T, R]` handlers, the `data`/`meta` envelope, `Result`/`ResultWithMeta`, raw-response (Strangler Fig) mode, JOSE middleware.
- `ModuleGroup()`, `RegisterRoutes(hr, r server.RouteRegistrar)`, `HandlerRegistry`.
- The constructor **call sites** (`r.Use(server.CORS(...))`, etc.) — only the return type changed.
- Echo stays the engine inside `server/`: the `HTTPErrorHandler`, validator, `IPExtractor`, OTel wiring, and the internal `*echo.Echo` are untouched.

### Locked decisions

1. **Middleware shape = flat.** `func(c HandlerContext, next func() error) error`, adapted to Echo internally — not Echo's nested `func(next) next`.
2. **Deprecation = big-bang, single release.** Old echo-typed exported methods are **removed**, not `// Deprecated:`. Every consumer (including the demo project) migrates at once.

**Maintainer scope decision (Phase-1 gate):** the 6th leak class (exported
middleware constructors) is resolved by **converting the constructors to flat
public signatures** (Option A) while keeping `SetupMiddlewares` on echo-native
internals. Functionality is preserved and the ADR-026 default path stays
baton-free.

## Consequences

**Positive:**
- No `echo.*` symbol appears in any code an application developer writes, names, or imports; the consumer surface matches the database/cache/messaging precedent.
- Downstream services are decoupled from Echo's concrete API and version — a future Echo bump is a `server/`-internal change.
- The flat middleware shape is simpler than Echo's nested closure and is uniform across custom, framework, scheduler, and debug middleware.
- Security improvement: no `RealIP()` accessor is exposed (it would graduate Echo's spoofable `LegacyIPExtractor` into the blessed path); the one internal consumer (the auth denial log) now uses `server.ClientIP(c.Request(), trustedNets)`, removing the spoofable IP from that log.

**Negative:**
- Breaking change for every consumer that touched the removed surface — mitigated by the compiler (each removed symbol fails the build), the `wiki/migrations.md` section, and the demo-project migration.
- A custom-middleware route pays a bounded **+1 heap-alloc/request/middleware-layer** — the `func() error { return next(c) }` baton, structurally unavoidable under the locked flat shape (see Performance).

### Performance (ADR-026)

The [ADR-026](adr_026_zero_overhead_request_path.md) zero-overhead invariant is
preserved:

- **Typed handler request path: byte-identical.** `RegisterHandler` still builds the `echo.HandlerFunc` wrapper exactly as before and registers it through an **unexported `addEcho` seam** on `routeGroup` (an `io.ReaderFrom`-style optional-interface upgrade) — there is **no** per-request adapter on the typed path. `HandlerContext` is the same 16-byte two-pointer struct; the value-receiver accessors are single-field derefs that inline to zero allocation.
- **Default middleware path: baton-free.** All framework/default middleware is registered echo-native via `e.Use` (`SetupMiddlewares` calls the unexported `xxxEcho()` forms), never through the flat adapter. A test-enforced invariant prevents a future maintainer from silently routing the default chain through `adaptMiddleware`.
- **New cost is confined to middleware routes.** The +1 baton alloc is incurred **only** when a route actually carries a flat middleware (consumer custom middleware, the debug endpoints, `/_sys/job`) — never on the ADR-026-protected default path. `BenchmarkTypedHandlerPath` asserts the typed path's allocs/op is unchanged; `BenchmarkMiddlewareAdapter` records the bounded baton cost.

## Alternatives Considered

- **Keep Echo's nested middleware shape** (`func(next echo.HandlerFunc) echo.HandlerFunc`): rejected — it re-leaks the very `echo.*` types this ADR removes. The flat shape is the locked decision.
- **`// Deprecated:` soft migration** (keep echo-typed methods one release): rejected in favor of big-bang removal — a single, compiler-enforced cutover avoids a long-lived dual surface and is cleaner under a framework where breaking changes are acceptable when documented.
- **Leave the middleware constructors echo-typed** (only fix L1–L5): rejected — retyping `RouteRegistrar.Use` to flat breaks `r.Use(server.CORS(...))` for every constructor still returning `echo.MiddlewareFunc`, with no migration path. Option A (flat constructors) is the only coherent choice.
- **Expose a `RealIP()`/`ClientIP()` accessor on `HandlerContext`:** rejected on security grounds — it makes the spoofable extractor the easy path. Middleware uses `server.ClientIP(c.Request(), …)` directly.
- **A `RouteInfo` wrapper type** for `Add`'s return: rejected (YAGNI) — the return value is never captured anywhere.
- **A `server/testing` subpackage** for the test constructor: rejected — one exported `NewHandlerContextForTest` in the `server` package keeps echo confined to `server/` and avoids a new package's doc-drift cost.

## Related

- [ADR-026](adr_026_zero_overhead_request_path.md) — zero-overhead request path; informs the typed-path-stays-echo-direct seam and the baton budget.
- [ADR-015](adr_015_echo_v5_migration.md) — Echo v4→v5 migration; established `HandlerContext.Echo` / raw echo handlers as the prior boundary (superseded here for the public surface).
- [ADR-013](adr_013_interface_naming_conventions.md) — prior public-interface evolution precedent (rename/refactor over suppression).
- [ADR-001](adr_001_enhanced_handler_system.md) — the enhanced handler system whose `HandlerContext` this ADR makes fully echo-free.
