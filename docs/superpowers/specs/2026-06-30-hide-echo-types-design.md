# Hide echo.* Types Behind go-bricks Abstractions — Design

**Date:** 2026-06-30
**Issue:** [#623](https://github.com/gaborage/go-bricks/issues/623) — server: hide echo.* types behind go-bricks abstractions in the public API
**Breaking:** yes → ADR-034 + `wiki/migrations.md` section + `feat!:` PR title (release-please reads the squash title).
**Area:** `server`, `app`, `scheduler`
**Status:** Design — pending maintainer approval (Phase-1 gate)
**Related precedent:** [ADR-026](../../../wiki/adr_026_zero_overhead_request_path.md) (zero-overhead request path), [ADR-015](../../../wiki/adr_015_echo_v5_migration.md) (echo v5), [ADR-013](../../../wiki/adr_013_*.md) (interface-naming rename precedent), [ADR-001](../../../wiki/adr_001_*.md) (handler system)

---

## Problem

The Developer Manifesto's **Vendor Agnosticism** principle says: *"Abstract high-cost dependencies (databases), embrace low-cost ones (HTTP frameworks)."* GoBricks deliberately does **not** abstract Echo away for performance — Echo stays the embraced engine. But "embrace it" was never meant to mean "leak its concrete types into every consumer module." Today `github.com/labstack/echo/v5` types appear directly on the public surface application authors touch, coupling every downstream service to Echo's concrete API/version and contradicting the Interface-Segregation and Composition principles applied to database/cache/messaging (all of which expose go-bricks types, never the vendor's).

This change wraps every remaining `echo.*` leak behind go-bricks types so **no `echo.*` symbol appears in any code an application developer writes, names, or imports**, while Echo remains the unchanged engine **inside `server/`**. This is a boundary-type change only — no router/middleware reimplementation.

**Acceptance north-star:** `git grep -l 'labstack/echo' -- '*.go'` resolves to `server/` only (server production + server tests + one in-tree server test-support file may keep echo). `app/`, `scheduler/`, and **all their `_test` files** become echo-free, and no exported symbol on the consumer path names an `echo.*` type.

---

## Locked Decisions (NOT re-litigated; the panel challenged their implementation only)

1. **Middleware shape = flat** `type MiddlewareFunc func(c HandlerContext, next func() error) error`. The framework adapts flat → Echo internally. NOT Echo's nested `func(next) next`.
2. **Deprecation = big-bang, single release.** Old echo-typed exported methods are **removed**, not `// Deprecated:`. Every consumer (incl. the demo project) migrates at once. ADR-034 + migrations.md are mandatory.

**Maintainer scope decision (Phase-1 gate, 2026-06-30):** the 6th-leak class (exported middleware constructors) is handled by **converting them to flat public signatures** while keeping `SetupMiddlewares` on echo-native internals (Option A). Functionality preserved; ADR-026 default path stays baton-free.

---

## The Leaks (verified against HEAD)

| # | Leak | Location |
|---|------|----------|
| L1 | `HandlerContext.Echo *echo.Context` exported field | server/handler.go:79 (constructed :328) |
| L2 | `RouteRegistrar` Add/Group/Use take `echo.HandlerFunc`/`echo.MiddlewareFunc`; Add returns `echo.RouteInfo` | server/handler.go:1050-1055; impl server/route_registrar.go |
| L3 | `ServerRunner.Echo() *echo.Echo` + `RegisterReadyHandler(echo.HandlerFunc)` | app/interfaces.go:31,33 |
| L4 | `RegisterDebugEndpoints(*echo.Echo)` + all app/debug_*.go handlers `func(*echo.Context) error` + ipWhitelist/auth middleware `echo.MiddlewareFunc` | app/debug_handlers.go, debug_goroutines.go, debug_health.go |
| L5 | `scheduler.CIDRMiddleware(...) echo.MiddlewareFunc` (exported) | scheduler/cidr_middleware.go:24 |
| **L6 (class)** | ~11 exported `server.*` middleware constructors returning `echo.MiddlewareFunc`; `SkipperFunc func(*echo.Context) bool`; `EscalateSeverity(*echo.Context, …)` | server/{cors,ip_preguard,logger,performance_stats,ratelimit,request_enrich,request_utils,tenant_middleware,timeout,timing,trace_context,logger_context}.go |

`ClientIP`/`ParseCIDRs` (server/clientip.go) already use stdlib types — **already echo-free, no change.**

---

## Adversarial Panel — Objections & Resolutions (auditable trail)

Six critics each found their single strongest objection. Verdicts: 4 blockers, 1 major, 1 hold-with-required-deliverables.

| Lens | Verdict | Objection | Resolution baked into this design |
|------|---------|-----------|-----------------------------------|
| Performance hawk | **HOLDS** | Typed path provably unchanged, but perf story unproven and the consumer-middleware baton (`func() error { return next(c) }`, a structurally unavoidable +1 heap-alloc/req/middleware-layer under the locked flat shape) was under-stated. | Ship `BenchmarkTypedHandlerPath` (assert identical allocs/op via `testing.AllocsPerRun`) + `BenchmarkMiddlewareAdapter` (record the bounded +1 alloc). Add a **test-enforced invariant**: framework/default middleware is registered echo-native via `e.Use`, never through the flat adapter. Document the bounded cost in migrations.md. |
| API-ergonomics | **BLOCKER** | The locked flat shape can't express the canonical context-propagation pattern (`c.SetRequest(c.Request().WithContext(ctx))`) — read-only accessors strand any consumer auth/tenant middleware; `Set/Get` (echo store) ≠ `context.Context`. `EscalateSeverity` becomes uncallable. | Add `SetRequest(*http.Request)` and `SetRequestContext(context.Context)` to `HandlerContext`. Retype `EscalateSeverity` to take `HandlerContext`. |
| Blast-radius | **BLOCKER** | 6th leak **class**: retyping `RouteRegistrar.Use` to flat breaks `r.Use(server.CORS(...))` for every constructor still returning `echo.MiddlewareFunc`; no migration path. | Convert all constructors + `SkipperFunc` to flat (maintainer Option A); add every symbol to the migration table; add a constructor-signature grep gate. |
| Conventions purist | **BLOCKER** | `RawHandlerFunc` collides with the established "Raw = APIResponse-envelope-bypass" vocabulary (RawResponse / Strangler Fig) — actively misleading, permanent under big-bang. (`echoRegistrar`/`addEcho` seam, `ectx`/`echoContext()`, `RootGroup()`/`ModuleGroup()`, `Get(key)` all blessed.) | Rename the untyped handler type to **`Handler`** (`type Handler func(c HandlerContext) error`, mirrors echo's own `HandlerFunc func(c Context) error`), with godoc distinguishing it from `WithRawResponse`. Rename `Header(name)` → `RequestHeader(name)` for request/response disambiguation. |
| Security reviewer | **MAJOR** | Exposing `RealIP()` graduates echo's spoofable `LegacyIPExtractor` into a blessed accessor with no safe counterpart — makes the spoofable path the easy path for consumer access-control middleware. (All abort/ordering/constant-time/CIDR/denial-body properties hold byte-for-byte.) | **Drop `RealIP()`.** Fix the sole consumer (auth denial log) to use `server.ClientIP(c.Request(), trustedNets)` — thread `trustedNets` into `authMiddleware` (also removes the spoofable IP from that log: a strict improvement). No IP accessor on `HandlerContext`; middleware uses `server.ClientIP(c.Request(), …)` directly. |
| Sixth-leak hunter | **BLOCKER** | Code is complete (no 7th code leak), but the doc deliverable was materially incomplete — `context_deadlines.md`, `multi_tenant_migration.md`, `TESTING.md`, `README.md`, a 2nd `handler_patterns.md` site all teach the deleted API. | Expand doc scope to all consumer-path sites; add a **doc grep-gate** (`git grep -nE 'ctx\.Echo\|hctx\.Echo\|\*echo\.Echo\|echo\.New' -- '*.md' '*.txt'` returns only historical ADRs). |

---

## Detailed Design

### 1. New boundary types (`server` package)

```go
// MiddlewareFunc is the flat, framework-neutral middleware signature. A middleware
// runs logic, then calls next() to invoke the rest of the chain (or returns an
// IAPIError WITHOUT calling next to abort). The framework adapts it to Echo internally.
type MiddlewareFunc func(c HandlerContext, next func() error) error

// Handler is the untyped, echo-free handler signature for raw routes, the readiness
// probe, and debug endpoints — distinct from the typed HandlerFunc[T,R]. It writes its
// own response (c.JSON / c.String) and is NOT related to WithRawResponse/RawResponse
// (the APIResponse-envelope-bypass mode); the two "raw" concepts share no machinery.
type Handler func(c HandlerContext) error
```

### 2. `HandlerContext`: drop the exported `Echo` field; add accessors + unexported escape hatch

```go
type HandlerContext struct {
    Config *config.Config // kept — a go-bricks type, not a leak
    ectx   *echo.Context  // unexported escape hatch; application code cannot name it
}

// Read accessors — stdlib types are the framework-neutral currency.
func (c HandlerContext) Request() *http.Request
func (c HandlerContext) RequestContext() context.Context     // convenience for the dominant ctx.Echo.Request().Context() pattern
func (c HandlerContext) ResponseWriter() http.ResponseWriter
func (c HandlerContext) Param(name string) string
func (c HandlerContext) Query(name string) string
func (c HandlerContext) RequestHeader(name string) string    // request header get (disambiguated name)
func (c HandlerContext) Get(key string) any
// Mutators (REQUIRED for the locked flat middleware shape to be usable):
func (c HandlerContext) Set(key string, val any)
func (c HandlerContext) SetRequest(r *http.Request)
func (c HandlerContext) SetRequestContext(ctx context.Context) // = SetRequest(Request().WithContext(ctx))
// Response writers (for Handler / ready / debug):
func (c HandlerContext) JSON(code int, v any) error
func (c HandlerContext) String(code int, s string) error
// Severity escalation (replaces the echo-typed package helper on this surface):
func (c HandlerContext) EscalateSeverity(level zerolog.Level)
// Unexported escape hatch for server/ framework code only — greppable, unnameable cross-package:
func (c HandlerContext) echoContext() *echo.Context
```

**No `RealIP()`** (security). Construction is `newHandlerContext(c *echo.Context, cfg *config.Config) HandlerContext` (server-internal). handler.go:328 changes `HandlerContext{Echo:c, Config:cfg}` → `newHandlerContext(c, hw.responder.cfg)` — same 16-byte two-pointer struct, identical cost. Accessor methods are value-receiver single-field-deref → inline, zero alloc.

### 3. `RouteRegistrar`: echo-free public interface + unexported echo-direct seam (ADR-026)

```go
type RouteRegistrar interface {
    Add(method, path string, handler Handler, middleware ...MiddlewareFunc) // RouteInfo return DROPPED (never captured anywhere)
    Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar
    Use(middleware ...MiddlewareFunc)
    FullPath(path string) string
}

// Unexported optional-interface seam (only routeGroup implements it — standard io.ReaderFrom-style upgrade).
type echoRegistrar interface { addEcho(method, path string, h echo.HandlerFunc) }
```

The **typed hot path stays echo-direct** — `RegisterHandler` builds the `echo.HandlerFunc` wrapper exactly as today and registers it via the seam:

```go
if er, ok := r.(echoRegistrar); ok {
    er.addEcho(method, path, wrappedHandler)                                   // zero per-request adapter — ADR-026 preserved
} else {
    r.Add(method, path, func(c HandlerContext) error { return wrappedHandler(c.echoContext()) }) // fallback for test fakes
}
```

`routeGroup` gains a `cfg *config.Config` field (to populate `HandlerContext.Config` in adapters), implements `addEcho` (echo-direct, identical to today's `Add`), and its public `Add`/`Use`/`Group` adapt go-bricks↔echo via:

```go
func adaptHandler(h Handler, cfg *config.Config) echo.HandlerFunc {
    return func(c *echo.Context) error { return h(newHandlerContext(c, cfg)) }
}
func adaptMiddleware(m MiddlewareFunc, cfg *config.Config) echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c *echo.Context) error {
            return m(newHandlerContext(c, cfg), func() error { return next(c) }) // baton — only on middleware routes, never the default path
        }
    }
}
```

### 4. `ServerRunner` (app): drop `Echo()`; add `RootGroup()`; retype `RegisterReadyHandler`

```go
type ServerRunner interface {
    Start() error
    Shutdown(ctx context.Context) error
    RootGroup() server.RouteRegistrar             // root (no basePath) registrar for debug/_sys-style endpoints
    ModuleGroup() server.RouteRegistrar           // unchanged (basePath-applied)
    RegisterReadyHandler(handler server.Handler)
}
```

`server.Server`: drop public `Echo()` (keep unexported `s.echo`); add `RootGroup()` → `newRouteGroup(s.echo.Group(""), "", s.cfg)`; `RegisterReadyHandler(server.Handler)` adapts `Handler`→`echo.HandlerFunc` (nil restores the internal `s.readyCheck` default). server.go:128 `s.RegisterReadyHandler(s.readyCheck)` → `s.RegisterReadyHandler(nil)`. Internal `c.Echo()` (server.go:299) and engine config (HTTPErrorHandler, Validator, IPExtractor, OTel) stay echo, all inside `server/` — the legitimate escape hatch.

### 5. `app/` becomes echo-free

- lifecycle.go: `RegisterDebugEndpoints(a.server.RootGroup())`; `readyCheck` → `func(a *App) readyCheck(c server.HandlerContext) error` (uses `c.RequestContext()`, `c.JSON`).
- debug_handlers.go: `RegisterDebugEndpoints(r server.RouteRegistrar)` → `g := r.Group(prefix); g.Use(d.ipWhitelistMiddleware()); if token != "" { g.Use(d.authMiddleware(trustedNets)) }; g.Add(http.MethodGet, "/goroutines", d.handleGoroutines); …`. `ipWhitelistMiddleware`/`authMiddleware` return `server.MiddlewareFunc` (flat). Denials use `server.NewForbiddenError`/`NewUnauthorizedError`. **Auth denial log uses `server.ClientIP(c.Request(), trustedNets)`** (security fix — `trustedNets` threaded in from `RegisterDebugEndpoints`, parsed once).
- debug_goroutines.go / debug_health.go: handlers become `func(d *DebugHandlers) handleX(c server.HandlerContext) error`, using `c.JSON`/`c.String`/`c.Query`/`c.RequestContext`/`c.ResponseWriter`. handleGoroutinesText sets `c.ResponseWriter().Header().Set("Content-Type", "text/plain; charset=UTF-8")`; the dump-failure path uses `server.NewInternalServerError(...)`.

### 6. `scheduler/` becomes echo-free

`CIDRMiddleware(...) server.MiddlewareFunc` (flat); denials use `server.NewForbiddenError(...)`; IP via `server.ClientIP(c.Request(), trustedNets)`. scheduler/module.go:161 `sysGroup.Use(cidrMiddleware)` is unchanged (`Use` now takes `server.MiddlewareFunc`).

### 7. The middleware-constructor class (L6) — Option A

For each exported constructor (`CORS`, `IPPreGuard`, `LoggerWithConfig`, `PerformanceStats`, `RateLimit`, `RequestEnrich`, `RequestIDMiddleware`, `TenantMiddleware`, `Timeout`, `Timing`, `TraceContext`): keep the current echo-native logic as an **unexported `xxxEcho() echo.MiddlewareFunc`**, and make the **public constructor return `MiddlewareFunc`** via a single shared inverse adapter:

```go
func fromEchoMiddleware(em echo.MiddlewareFunc, cfg *config.Config) MiddlewareFunc {
    return func(c HandlerContext, next func() error) error {
        h := em(func(*echo.Context) error { return next() })
        return h(c.echoContext())
    }
}
// public: func CORS(expose bool, env ...string) MiddlewareFunc { return fromEchoMiddleware(corsEcho(expose, env...), nil) }
```

`SetupMiddlewares` calls the unexported `xxxEcho()` forms directly through `e.Use(...)` → **the default request path stays echo-native, zero baton** (the ADR-026 invariant, test-enforced). `SkipperFunc` becomes `func(r *http.Request) bool`; `CreateProbeSkipper` returns the flat form; internal echo `Skipper` configs adapt at the `SetupMiddlewares` seam. Severity escalation is the `HandlerContext.EscalateSeverity(level)` method. _(SkipperFunc→`*http.Request` and method-only EscalateSeverity were refined in Phase-3 code review.)_

### 8. Test support (exported, server package)

```go
// NewHandlerContextForTest builds a HandlerContext backed by a real echo context for
// external-package tests (app/, scheduler/) that cannot name the unexported escape hatch.
func NewHandlerContextForTest(w http.ResponseWriter, r *http.Request, cfg *config.Config) HandlerContext
```

Lives in the `server` package (regular `.go` file) — keeps echo confined to `server/`, avoids a new `server/testing` package and its CLAUDE.md doc-drift cost. (Rejected alternative: `server/testing` subpackage.)

### 9. Performance story (ADR-026)

- **Typed handler request path: byte-identical.** `RegisterHandler`→`addEcho`→`rg.group.Add(echo.HandlerFunc)`. handler.go:328 builds `HandlerContext` exactly as today. Accessors inline.
- **Default middleware path: baton-free.** All framework middleware registered echo-native via `e.Use` (test-enforced invariant).
- **New cost** = the `func() error { return next(c) }` baton, a structurally-unavoidable +1 heap-alloc/req/middleware-layer under the locked flat shape — incurred **only on middleware routes** (debug, `/_sys/job`, consumer custom middleware), never the ADR-026-protected default path. Quantified by `BenchmarkMiddlewareAdapter` and recorded in migrations.md.

---

## Testing

Mirror ADR-016/ADR-026 rigor; camelCase test names, snake_case table cases; `-race` in CI; 1:1 source-to-test.

1. **`BenchmarkTypedHandlerPath`** (server/handler_test.go): `testing.AllocsPerRun` asserts allocs/op of the typed `server.GET` request path is unchanged vs. baseline — locks in `newHandlerContext` + accessors + the `addEcho` seam being free.
2. **`BenchmarkMiddlewareAdapter`** (server/route_registrar_test.go): documents the +1 alloc/req/middleware-layer of the flat adapter vs echo-native.
3. **Echo-native default-chain invariant** test: assert `SetupMiddlewares` registers no flat-adapted middleware (e.g. count `e.Use` registrations are echo-native), so a future maintainer can't silently route the default path through `adaptMiddleware`.
4. **Adapter round-trips**: flat MiddlewareFunc ↔ echo (abort-without-next, next-calls-through, error propagation to customErrorHandler); Handler ↔ echo.
5. **Full accessor set** on HandlerContext incl. `SetRequestContext` propagation visible to a downstream handler; `JSON`/`String` output; `EscalateSeverity`.
6. **Ready path**: `RegisterReadyHandler(server.Handler)` + nil-restores-default.
7. **Debug endpoints behind the abstraction**: registration via `RootGroup()`, IP-whitelist gate, bearer-auth (constant-time, ordering ipWhitelist-before-auth), denial bodies — byte-for-byte; the auth denial log now carries the trusted-proxy-aware IP.
8. **Byte-equivalence**: existing golden/snapshot + envelope/raw-mode/`Result`/`ResultWithMeta`/JOSE tests pass **unmodified**.
9. **Test fakes** (`noopRouteRegistrar`, `fakeRegistrar`) updated to the new signatures, echo-free.
10. **`NewHandlerContextForTest`** has its own test; debug/scheduler/app tests migrate to it.
11. **Leak gates** (CI + acceptance): `git grep -l 'labstack/echo' -- '*.go'` → `server/` only; `git grep -nE '^func [A-Z].*echo\.(MiddlewareFunc|Context|HandlerFunc)' -- server/*.go ':!server/*_test.go'` → empty; **doc gate** `git grep -nE 'ctx\.Echo|hctx\.Echo|\*echo\.Echo|echo\.New' -- '*.md' '*.txt'` → only historical ADRs.

---

## Documentation & ADR

- **ADR-034** `wiki/adr_034_echo_boundary_types.md` + index entry in `wiki/architecture_decisions.md` + bump the "ADR-001 through ADR-NNN" range note (and MEMORY/CLAUDE notes as needed).
- **`wiki/migrations.md`**: new top section with Before/After for HandlerContext.Echo, custom middleware (flat shape), `ServerRunner.Echo()`, `RegisterReadyHandler`, `CIDRMiddleware`, the middleware-constructor class, `EscalateSeverity`, `SkipperFunc`; note the bounded middleware-route baton cost.
- **Consumer-path docs migrated** (sixth-leak-hunter findings): `wiki/handler_patterns.md` (lines 110 & 143), `wiki/context_deadlines.md` (31, 85), `wiki/multi_tenant_migration.md` (75), `TESTING.md` (~289, 328-334 → `NewHandlerContextForTest`), `README.md` (822 `wireLogging(*echo.Echo)`, 835 `e.Use(server.LoggerWithConfig(...))`), `llms.txt` (~21 `ctx.Echo.*` sites + echo-middleware examples 877-963 / 4402-4419 → flat), `CLAUDE.md` handler/middleware examples.
- Historical ADRs (`adr_001`, `adr_015`) keep their echo references; annotate any that teach the now-removed accessor with a "superseded by ADR-034" note so the doc gate can allowlist them.

---

## Breaking Change & Migration (highlights — full table in migrations.md)

```go
// Custom middleware — BEFORE (Echo nested closure)
func Auth(next echo.HandlerFunc) echo.HandlerFunc {
    return func(c *echo.Context) error { /* ... */ ; return next(c) }
}
// AFTER (flat go-bricks)
func Auth() server.MiddlewareFunc {
    return func(c server.HandlerContext, next func() error) error {
        ctx := withUser(c.RequestContext())
        c.SetRequestContext(ctx)
        return next()
    }
}

// Request access — BEFORE: ctx.Echo.Request().Context()   AFTER: ctx.RequestContext()
// Ready handler  — BEFORE: func(*echo.Context) error       AFTER: server.Handler
// Engine access  — BEFORE: runner.Echo()                   AFTER: removed (use ModuleGroup()/RootGroup())
// Framework MW   — BEFORE: server.CORS(...) echo.MiddlewareFunc  AFTER: server.CORS(...) server.MiddlewareFunc (call unchanged)
```

---

## Non-Goals (YAGNI)

- No `RouteInfo` wrapper type (return value never captured anywhere).
- No `RealIP()`/`ClientIP()` accessor on HandlerContext (security; middleware uses `server.ClientIP(c.Request(), …)` directly).
- No `server/testing` subpackage (one exported test constructor instead).
- No router/middleware reimplementation; no change to typed handlers, the data/meta envelope, `Result`/`ResultWithMeta`, raw-response mode, or JOSE.

---

## Files Touched

| File | Change |
|------|--------|
| server/handler.go | `MiddlewareFunc`, `Handler` types; HandlerContext accessors + `ectx`/`echoContext()`; `RouteRegistrar` retype + `echoRegistrar` seam; `newHandlerContext`; `adaptHandler`/`adaptMiddleware`/`fromEchoMiddleware`; `NewHandlerContextForTest`; :328 construction |
| server/route_registrar.go | `routeGroup` gains `cfg`; `addEcho`; flat-typed public Add/Use/Group |
| server/server.go | drop `Echo()`; add `RootGroup()`; retype `RegisterReadyHandler`; :128 |
| server/{cors,ip_preguard,logger,performance_stats,ratelimit,request_enrich,request_utils,tenant_middleware,timeout,timing,trace_context}.go | unexported `xxxEcho()` + public flat constructor |
| server/middleware.go | call `xxxEcho()` internals; flat `SkipperFunc` seam |
| server/tenant_middleware.go | `SkipperFunc func(*http.Request) bool`; `CreateProbeSkipper` flat |
| server/logger_context.go | `EscalateSeverity(HandlerContext, …)` + `HandlerContext.EscalateSeverity` |
| app/interfaces.go | `ServerRunner` drop `Echo()`, add `RootGroup()`, retype `RegisterReadyHandler` |
| app/lifecycle.go | `RootGroup()` call; `readyCheck(server.HandlerContext)` |
| app/debug_handlers.go, debug_goroutines.go, debug_health.go | `RegisterDebugEndpoints(RouteRegistrar)`; flat middleware; `server.Handler` handlers; go-bricks errors; ClientIP denial log |
| scheduler/cidr_middleware.go | flat `CIDRMiddleware`; go-bricks errors |
| tests (server/*_test.go, app/*_test.go, scheduler/*_test.go) | new signatures, `NewHandlerContextForTest`, benchmarks, invariant test, echo-free in app/scheduler |
| wiki/adr_034_*.md, architecture_decisions.md, migrations.md, handler_patterns.md, context_deadlines.md, multi_tenant_migration.md, TESTING.md, README.md, llms.txt, CLAUDE.md | docs |
