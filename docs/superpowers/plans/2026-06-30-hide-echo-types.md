# Hide echo.* Types Behind go-bricks Abstractions — Implementation Plan

**Goal:** Wrap every `github.com/labstack/echo/v5` leak behind go-bricks types so no `echo.*` symbol appears in code an application developer writes, names, or imports, while Echo stays the unchanged engine inside `server/`.

**Architecture:** Boundary-type change only. New flat `server.MiddlewareFunc` + untyped `server.Handler` + accessor-based `HandlerContext`; the framework adapts go-bricks↔echo at the `server/` boundary. The typed `server.GET/POST` hot path stays echo-direct via an unexported `addEcho` seam (ADR-026 preserved). All framework middleware stays echo-native inside `SetupMiddlewares`; public middleware constructors expose flat wrappers. `app/` and `scheduler/` (incl. tests) become echo-free.

**Tech Stack:** Go 1.26, Echo v5, golangci-lint v2.12.2 (GOWORK=off), zerolog. Verification: `make check` (fmt+lint+test+vuln), `make test-coverage`, `make sec`.

**Spec:** `docs/superpowers/specs/2026-06-30-hide-echo-types-design.md`

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| server/handler.go | Boundary types, HandlerContext, RouteRegistrar+seam, adapters, test ctor | Modify (foundation) |
| server/route_registrar.go | routeGroup adapts go-bricks↔echo.Group | Modify |
| server/server.go | drop Echo(); RootGroup(); RegisterReadyHandler retype | Modify |
| server/middleware.go | call echo-native internals; flat SkipperFunc seam | Modify |
| server/{cors,ip_preguard,logger,performance_stats,ratelimit,request_enrich,request_utils,timeout,timing,trace_context}.go | unexported xxxEcho() + public flat constructor | Modify |
| server/tenant_middleware.go | flat SkipperFunc; CreateProbeSkipper; TenantMiddleware | Modify |
| server/logger_context.go | EscalateSeverity(HandlerContext); method form | Modify |
| app/interfaces.go | ServerRunner: drop Echo(), add RootGroup(), retype RegisterReadyHandler | Modify |
| app/lifecycle.go | RootGroup() call; readyCheck(server.HandlerContext) | Modify |
| app/debug_handlers.go, debug_goroutines.go, debug_health.go | echo-free debug behind RouteRegistrar | Modify |
| scheduler/cidr_middleware.go | flat CIDRMiddleware; go-bricks errors | Modify |
| server/*_test.go, app/*_test.go, scheduler/*_test.go | new signatures, benchmarks, invariant, echo-free | Modify |
| wiki/adr_034_echo_boundary_types.md | ADR | Create |
| wiki/architecture_decisions.md, migrations.md, handler_patterns.md, context_deadlines.md, multi_tenant_migration.md, TESTING.md, README.md, llms.txt, CLAUDE.md | docs | Modify |

**Dependency order:** Task 1 (foundation) is sequential and blocks all others. Tasks 2–7 edit disjoint file sets and run in parallel. Task 8 (tests) runs after 2–7. Task 9 (docs) is fully independent and runs anytime. Task 10 (verify) is last.

---

## Task 1: Foundation — server/handler.go (LEAD, sequential, blocks all)

**Files:** Modify `server/handler.go`

- [ ] **Step 1: Define boundary types.** Add `type MiddlewareFunc func(c HandlerContext, next func() error) error` and `type Handler func(c HandlerContext) error` with godoc (Handler explicitly distinguished from `WithRawResponse`/`RawResponse`).
- [ ] **Step 2: Rewrite HandlerContext.** Replace the `Echo *echo.Context` field with unexported `ectx *echo.Context`; keep `Config`. Add accessors: `Request`, `RequestContext`, `ResponseWriter`, `Param`, `Query`, `RequestHeader`, `Get`, `Set`, `SetRequest`, `SetRequestContext`, `JSON`, `String`, `EscalateSeverity` (delegates to `getRequestLogContext(c.ectx)`), and unexported `echoContext()`. Add `newHandlerContext(c *echo.Context, cfg *config.Config) HandlerContext`.
- [ ] **Step 3: Update construction site.** handler.go:328 `HandlerContext{Echo:c, Config:hw.responder.cfg}` → `newHandlerContext(c, hw.responder.cfg)`.
- [ ] **Step 4: Retype RouteRegistrar + add seam.** `Add(method, path string, handler Handler, middleware ...MiddlewareFunc)` (drop RouteInfo return); `Group`/`Use` take `...MiddlewareFunc`. Add unexported `type echoRegistrar interface { addEcho(method, path string, h echo.HandlerFunc) }`.
- [ ] **Step 5: RegisterHandler uses the seam.** Replace `r.Add(method, path, wrappedHandler)` with the `echoRegistrar` type-assert fast path + `Handler` fallback (spec §3).
- [ ] **Step 6: Add adapters.** `adaptHandler(h Handler, cfg *config.Config) echo.HandlerFunc`, `adaptMiddleware(m MiddlewareFunc, cfg *config.Config) echo.MiddlewareFunc`, `fromEchoMiddleware(em echo.MiddlewareFunc, cfg *config.Config) MiddlewareFunc`.
- [ ] **Step 7: Add test ctor.** `func NewHandlerContextForTest(w http.ResponseWriter, r *http.Request, cfg *config.Config) HandlerContext` (`echo.New().NewContext(r,w)`), with test-support godoc.

Run: `GOWORK=off go build ./server/...` — Expected: server package compiles (callers in same package may still reference old API until their tasks land; build the package in isolation first, then `go vet ./server/` after Task 4).

## Task 2 (Stream A): server/route_registrar.go

**Files:** Modify `server/route_registrar.go`

- [ ] **Step 1:** Add `cfg *config.Config` to `routeGroup`; thread through `newRouteGroup(group, prefix, cfg)` and `Group`.
- [ ] **Step 2:** Implement `addEcho(method, path string, h echo.HandlerFunc)` = today's `rg.group.Add(method, rg.relativePath(path), h)` (echo-direct).
- [ ] **Step 3:** Public `Add(method, path string, handler Handler, middleware ...MiddlewareFunc)` → adapt handler + middleware via `adaptHandler`/`adaptMiddleware`, call `rg.group.Add(...)`, discard return. `Use`/`Group` adapt middleware.

Run: `GOWORK=off go build ./server/` — Expected: compiles after Task 1.

## Task 3 (Stream B): server/server.go + app/interfaces.go

**Files:** Modify `server/server.go`, `app/interfaces.go`

- [ ] **Step 1 (server.go):** Remove public `Echo()`. Add `RootGroup() RouteRegistrar` → `newRouteGroup(s.echo.Group(""), "", s.cfg)`. Update `ModuleGroup()` to pass `s.cfg`.
- [ ] **Step 2 (server.go):** Retype `RegisterReadyHandler(handler Handler)`; nil → internal `s.readyCheck`, else `adaptHandler(handler, s.cfg)`. Change :128 to `s.RegisterReadyHandler(nil)`.
- [ ] **Step 3 (interfaces.go):** `ServerRunner`: drop `Echo()`, add `RootGroup() server.RouteRegistrar`, retype `RegisterReadyHandler(handler server.Handler)`. Drop the echo import.

Run: `GOWORK=off go build ./server/ ./app/` — Expected: app may still fail until Task 5; build server/ clean.

## Task 4 (Stream): middleware-constructor class

**Files:** Modify `server/{cors,ip_preguard,logger,performance_stats,ratelimit,request_enrich,request_utils,timeout,timing,trace_context}.go`, `server/tenant_middleware.go`, `server/logger_context.go`, `server/middleware.go`

- [ ] **Step 1:** For each constructor, rename the existing body to unexported `xxxEcho(...) echo.MiddlewareFunc`; add public `Xxx(...) MiddlewareFunc { return fromEchoMiddleware(xxxEcho(...), nil) }`.
- [ ] **Step 2:** `SkipperFunc` → `func(r *http.Request) bool`; `CreateProbeSkipper` returns flat; `TenantMiddleware` public flat + `tenantMiddlewareEcho` internal.
- [ ] **Step 3:** `SetupMiddlewares` calls the `xxxEcho()` forms via `e.Use(...)`; adapt the flat probe skipper at echo `Skipper` configs (`func(c *echo.Context) bool { return probeSkipper(newHandlerContext(c, cfg)) }`).
- [ ] **Step 4:** `HandlerContext.EscalateSeverity(level zerolog.Level)` method using `getRequestLogContext(c.echoContext())` internally (method-only — no package-level function).

Run: `GOWORK=off go vet ./server/` — Expected: server/ fully compiles + vets.

## Task 5 (Stream C): app/ debug + readyCheck

**Files:** Modify `app/debug_handlers.go`, `app/debug_goroutines.go`, `app/debug_health.go`, `app/lifecycle.go`

- [ ] **Step 1:** `RegisterDebugEndpoints(r server.RouteRegistrar)`: `g := r.Group(prefix)`; parse `trustedNets` once; `g.Use(d.ipWhitelistMiddleware())`; `g.Use(d.authMiddleware(trustedNets))` when token set; `g.Add(http.MethodGet, "/goroutines", d.handleGoroutines)` etc.
- [ ] **Step 2:** `ipWhitelistMiddleware`/`authMiddleware(trustedNets)` return `server.MiddlewareFunc`; denials use `server.NewForbiddenError`/`NewUnauthorizedError`; auth denial log uses `server.ClientIP(c.Request(), trustedNets)`.
- [ ] **Step 3:** All handlers → `func(d *DebugHandlers) handleX(c server.HandlerContext) error` using `c.JSON`/`c.String`/`c.Query`/`c.RequestContext`/`c.ResponseWriter`; the goroutine dump failure path → `server.NewInternalServerError`.
- [ ] **Step 4:** lifecycle.go:113 → `RegisterDebugEndpoints(a.server.RootGroup())`; readyCheck → `func(a *App) readyCheck(c server.HandlerContext) error`. Drop echo import from all four files.

Run: `GOWORK=off go build ./app/` — Expected: compiles after Tasks 1,3.

## Task 6 (Stream): scheduler/cidr_middleware.go

**Files:** Modify `scheduler/cidr_middleware.go`

- [ ] **Step 1:** `CIDRMiddleware(...) server.MiddlewareFunc` (flat); `createIPCheckHandler` adapts; denials use `server.NewForbiddenError`; IP via `server.ClientIP(c.Request(), trustedNets)`. Drop echo import.

Run: `GOWORK=off go build ./scheduler/` — Expected: compiles after Task 1.

## Task 7 (Stream): scheduler verify glue — none beyond Task 6 (module.go:161 unchanged)

## Task 8: Tests (after 1–7)

**Files:** `server/handler_test.go`, `server/route_registrar_test.go`, `server/server_test.go`, `server/jose_test.go`, `app/app_test.go`, `app/debug_*_test.go`, `scheduler/api_handlers_test.go`, `scheduler/cidr_middleware_test.go`

- [ ] **Step 1:** Update `noopRouteRegistrar`, `fakeRegistrar`, `mockServer`, `stubServerRunner` to new signatures; drop echo from app tests.
- [ ] **Step 2:** scheduler/api_handlers_test.go: drop `Echo: nil` lines.
- [ ] **Step 3:** Migrate debug/app/scheduler context construction to `server.NewHandlerContextForTest`; middleware tests call the flat form `mw(ctx, func() error {...})`.
- [ ] **Step 4:** Add `BenchmarkTypedHandlerPath` (allocs/op invariant), `BenchmarkMiddlewareAdapter`, and the echo-native-default-chain invariant test.
- [ ] **Step 5:** Add tests for the full accessor set, `SetRequestContext` propagation, `Handler` adapter round-trip, ready path nil-restore, `NewHandlerContextForTest`, `EscalateSeverity`.

Run: `GOWORK=off go test ./server/... ./app/... ./scheduler/...` — Expected: all pass, `-race` clean.

## Task 9 (Stream D, docs — independent)

**Files:** Create `wiki/adr_034_echo_boundary_types.md`; modify `wiki/architecture_decisions.md`, `wiki/migrations.md`, `wiki/handler_patterns.md`, `wiki/context_deadlines.md`, `wiki/multi_tenant_migration.md`, `TESTING.md`, `README.md`, `llms.txt`, `CLAUDE.md`

- [ ] **Step 1:** ADR-034 (house format) + index entry + range-note bump.
- [ ] **Step 2:** migrations.md top section with Before/After per leak + middleware-class + baton-cost note.
- [ ] **Step 3:** Migrate every consumer-path doc site to `ctx.RequestContext()` / `server.Handler` / `RootGroup()` / flat middleware / `NewHandlerContextForTest`. Annotate historical ADRs with "superseded by ADR-034".

## Task 10: Verify

- [ ] `make check` green; `make sec` (touched debug auth); `make test-coverage`.
- [ ] Leak gates: `git grep -l 'labstack/echo'` → `server/` only; `git grep -nE '^func [A-Z].*echo\.(MiddlewareFunc|Context|HandlerFunc)' -- server/*.go ':!server/*_test.go'` → empty; doc gate → only historical ADRs.
- [ ] Phase-3 adversarial reviewers (conventions, leak-hunter, behavior-equivalence, test-adequacy); reconcile; re-run `make check`.

---

## Self-Review

**Spec coverage** — every spec section maps to a task:
- §1 boundary types → Task 1 ✓; §2 HandlerContext → Task 1 ✓; §3 RouteRegistrar+seam → Tasks 1,2 ✓; §4 ServerRunner → Task 3 ✓; §5 app echo-free → Task 5 ✓; §6 scheduler → Task 6 ✓; §7 middleware class → Task 4 ✓; §8 test ctor → Task 1 ✓; §9 perf → Task 8 benchmarks ✓; Testing → Task 8 ✓; Docs → Task 9 ✓; gates → Task 10 ✓.

**Panel resolutions covered:** benchmarks+invariant (Task 8) ✓; SetRequest/SetRequestContext (Task 1) ✓; constructor class Option A (Task 4) ✓; `Handler` rename + `RequestHeader` (Task 1) ✓; RealIP dropped + ClientIP denial log (Tasks 1,5) ✓; doc expansion + doc gate (Tasks 9,10) ✓.

**Placeholder scan** — no TBD/TODO; every step has a concrete action and run/expected.
