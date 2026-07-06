# Design: `GlobalMiddlewareRegisterer` — module-contributed global middleware

**Date:** 2026-07-05
**Status:** Approved design (pre-implementation)
**Scope:** New optional module capability + server seam. Additive, non-breaking (`feat:`). Requires ADR-036.

---

## 1. Problem

Consumer applications need a way to install HTTP middleware that:

- runs **exactly once** per request,
- **cannot be skipped** by an individual route,
- runs **after tenant resolution** but **before handlers**,
- **skips** the framework's `health`/`ready` probe endpoints.

The canonical use case is an **auth gate**, plus API-key / request-signature verification, global audit logging, and maintenance-mode kill-switches.

### Why the existing seams don't cover it

The framework already has a global, un-skippable, run-once chain — `SetupMiddlewares` (`server/middleware.go:25`) registers tenant resolution, rate limiting, recovery, etc. via `e.Use()` on the root Echo. But that chain is **framework-internal**: there is no consumer-facing door into it.

The consumer-reachable seams are all wrong for this:

- `RouteRegistrar.Use(...)` (`server/route_registrar.go:66`) delegates to `echo.Group.Use`, which is **group-scoped** and **order-fragile**: Echo bakes group middleware into a route at `Add` time (`group.go:176`, snapshots `g.middleware`), so it only applies to routes added *after* the `Use()` call, and multiple modules calling it stack duplicate copies.
- `RootGroup()` / `ModuleGroup()` (`server/server.go:146-159`) return **sibling** `echo.Group` wrappers — middleware on one never touches the other's routes.
- Per-route middleware is, by definition, skippable per route.

## 2. Chosen approach (Option B)

A new **optional duck-typed module interface**, detected at startup exactly like `RouteRegisterer` and `MessagingDeclarer`. The framework collects each implementer's middleware after `Init()` and registers it **once** on the **raw root Echo chain** (`s.echo.Use`), wrapping each with a health/ready skipper.

Rationale for Option B over an `app.Options` field or an `app.Use()` method: the middleware is built **after module `Init()`**, so it can capture dependencies a module wires up in `Init()` (keystore verifier, DB/cache handles) — which real auth needs. It also reuses the framework's established "implement an interface, we detect it" idiom rather than inventing a new wiring style.

---

## 3. Public API

### 3.1 New interface — `app/module.go`

Placed beside `RouteRegisterer` (`module.go:32`) and `MessagingDeclarer` (`module.go:40`):

```go
// GlobalMiddlewareRegisterer is an optional module capability. A module that
// implements it contributes middleware that runs once per request on the root
// engine chain — after tenant resolution and every built-in middleware, before
// handlers, and skipping the health/ready probe endpoints. Registration is
// framework-controlled: no individual route can opt out.
//
// The returned middleware is built during module Init(), so it may capture
// dependencies the module wired up there (e.g. a keystore-backed token verifier).
type GlobalMiddlewareRegisterer interface {
    GlobalMiddleware() []server.MiddlewareFunc
}
```

`server.MiddlewareFunc` already exists and is stable (ADR-034, v0.46): `func(c HandlerContext, next func() error) error` (`server/handler.go:83`). **Do not** introduce a second middleware shape.

### 3.2 New server method — `server/global_middleware.go` (new file)

```go
// RegisterGlobalMiddleware appends module-contributed middleware to the root
// engine chain. Each is wrapped with a health/ready probe skipper and lands at
// the innermost slot — after every built-in middleware (tenant resolution, rate
// limiting, recovery, ...) and immediately before the route handler.
//
// Must be called during startup, before Start(). Safe to call once with all
// collected middleware, or repeatedly; ordering follows call order.
func (s *Server) RegisterGlobalMiddleware(mw ...MiddlewareFunc) {
    healthPath := s.buildFullPath(s.healthRoute)
    readyPath  := s.buildFullPath(s.readyRoute)
    for _, m := range mw {
        wrapped := skipProbes(m, healthPath, readyPath)
        s.echo.Use(adaptMiddleware(wrapped, s.cfg)) // RAW ROOT CHAIN — see §5
    }
}

// skipProbes wraps a MiddlewareFunc so it is bypassed for the health/ready
// endpoints, reusing CreateProbeSkipper for byte-identical semantics with the
// tenant middleware (server/tenant_middleware.go:57).
func skipProbes(mw MiddlewareFunc, healthPath, readyPath string) MiddlewareFunc {
    skipper := CreateProbeSkipper(healthPath, readyPath) // exact-path match on r.URL.Path
    return func(c HandlerContext, next func() error) error {
        if skipper(c.Request()) {
            return next()
        }
        return mw(c, next)
    }
}
```

This method **must** live in package `server`: `adaptMiddleware` is unexported (`handler.go:264`), and `s.echo` / `s.healthRoute` / `s.readyRoute` / `s.buildFullPath` / `s.cfg` are all private. `app/` cannot perform the `MiddlewareFunc → echo.MiddlewareFunc` conversion itself.

### 3.3 apidiff verdict — stays `feat:` (non-breaking)

- **Adding `GlobalMiddlewareRegisterer`** is a pure symbol add → apidiff Compatible → CI gate (`.github/workflows/ci-v2.yml:983`) passes without acknowledgement.
- **`app.ServerRunner` (`app/interfaces.go:26`) MUST NOT change.** It is an exported interface (5 methods) that external consumers can implement and inject via `Options.Server` (`app/options.go:17`). Adding a 6th method is an apidiff **INCOMPATIBLE** change → would force `feat!:` + `breaking-approved`.
- **How the wiring avoids it:** `app` invokes the new method through an **optional type assertion**, mirroring the framework's own duck-typing:

  ```go
  if reg, ok := a.server.(interface {
      RegisterGlobalMiddleware(...server.MiddlewareFunc)
  }); ok {
      reg.RegisterGlobalMiddleware(collected...)
  }
  ```

  The default path constructs a concrete `*server.Server` (`app/app.go:143` → `server.New`), so the assertion succeeds in production. `ServerRunner` stays byte-identical.

  **Fail-closed:** if a module registers global middleware but the configured `ServerRunner` does not implement `RegisterGlobalMiddleware`, startup aborts with an error rather than silently dropping the (canonically auth) middleware. A fake needing the capability implements the assertion; one that omits it is valid only when no module contributes global middleware.

---

## 4. Lifecycle & wiring

**Collection point:** `app/lifecycle.go`, in `prepareRuntime()`, immediately before `RegisterRoutes` (`lifecycle.go:63`). By then all module `Init()` has run synchronously inside `ModuleRegistry.Register` (`app/module_registry.go:55`), and the serve loop has not started — the same window `RegisterJobs` (`lifecycle.go:59`) and `RegisterRoutes` (`lifecycle.go:63`) already use.

**New collector on `*ModuleRegistry`** — a drop-in analogue of the `RegisterRoutes` loop (`module_registry.go:141-149`):

```go
func (r *ModuleRegistry) CollectGlobalMiddleware() []server.MiddlewareFunc {
    var mws []server.MiddlewareFunc
    for _, m := range r.modules { // append-ordered slice (module_registry.go:105)
        if g, ok := m.(GlobalMiddlewareRegisterer); ok {
            r.logger.Info().Str("module", m.Name()).Msg("Collecting global middleware")
            mws = append(mws, g.GlobalMiddleware()...)
        }
    }
    return mws
}
```

**Deterministic ordering:** `r.modules` is an append-ordered slice; every optional-interface pass ranges it directly (no map iteration). Global middleware therefore composes in **module-registration order** — the first-registered module's middleware is outermost. Callers control ordering via `RegisterModule` call order. Documented, mirroring `RegisterRoutes`.

**Wiring in `prepareRuntime` (sketch):**

```go
if mws := a.registry.CollectGlobalMiddleware(); len(mws) > 0 {
    if reg, ok := a.server.(interface {
        RegisterGlobalMiddleware(...server.MiddlewareFunc)
    }); ok {
        reg.RegisterGlobalMiddleware(mws...)
    }
}
a.registry.RegisterRoutes(a.server.ModuleGroup())
```

---

## 5. Chain placement & skipper

**Register on the raw root chain (`s.echo.Use`), NOT via `RootGroup()/ModuleGroup().Use()`.**

Echo's *root* `e.Use` is fundamentally different from *group* `Use`:

- **Root:** `Use()` appends to `e.middleware` and recompiles the whole global chain via `buildRouterChains()` (`echo.go:489`). Per request, routing resolves the handler onto `c.handler`, then `e.chain` (the compiled global chain) wraps it (`echo.go:781-782`, `echo.go:400`). **Order of `Use` vs route `Add` is irrelevant** — the global chain applies to every request. *(Adversarially verified: CONFIRMED.)*
- **Group:** order-fragile and group-scoped (see §1). Registering through a group would be order-dependent, would miss health/ready + debug routes (registered on the root engine at `server.go:130-133`), and could miss module routes on timing slips.

**Safety:** Echo warns only against mutating middleware *after the server starts* (`echo.go:392-394`). Registration happens in `prepareRuntime`, before `Start()` — safe.

**Resulting order.** `SetupMiddlewares` runs eagerly in `New()` (`server.go:126`) building the fixed chain: requestID → OTel → requestEnrich → CORS → ipPreGuard → **tenantMiddleware** → logger → Recover → Secure → timeout → bodyLimit → gzip → rateLimit → timing (`middleware.go:31-155`). Appending global middleware places it at the **innermost slot — right before the handler**:

- ✅ **After tenant resolution** — tenant ID is in `context.Context` (`tenant_middleware.go:47`).
- ✅ **Inside `Recover()`** — panics in the middleware are caught.
- ⚠️ **After `rateLimit` / `timeout` / `gzip` / `bodyLimit`** — an unauthenticated request still consumes rate-limit budget before rejection. **Accepted** (decision, 2026-07-05): desirable for cheap flood rejection, and consistent with the "after tenant resolution" requirement. This hook grants only the innermost slot; it is **not** a general "run early" hook.

**404 / 405:** Echo's router returns a non-nil handler even on miss (`notFoundHandler`) and `e.chain` wraps it, so the gate fires **before** the 404/405 response — an unauthenticated request to a bogus path returns 401/403, not 404 (does not leak route existence). Only health/ready are exempt.

**Skip predicate: health + ready ONLY** (decision, 2026-07-05: **gate system endpoints too**). Reuse `CreateProbeSkipper` (`tenant_middleware.go:57`) unchanged. `/_sys/job` (scheduler) and `/debug/*` (debug endpoints) are **gated** by the global middleware; their own CIDR + bearer-token checks still run underneath (defense in depth). The skip predicate is **not** widened beyond health/ready.

---

## 6. Error / short-circuit semantics

An auth middleware short-circuits by returning an `IAPIError` **without calling `next()`**. *(Adversarially verified: CONFIRMED.)*

1. `return server.NewUnauthorizedError("invalid token")` (`errors.go:189`; use `NewForbiddenError` for 403). Empty message defaults appropriately.
2. `adaptMiddleware` returns the error up Echo's chain; the baton `func() error { return next(c) }` is never invoked, so the handler is never entered (`handler.go:264-270`).
3. Echo's `HTTPErrorHandler` = `customErrorHandler` (`server.go:83`) → `classifyError` matches `IAPIError` via `errors.As` and uses it as-is (`server.go:273-276`) → `formatErrorResponse` renders the canonical envelope `{"error":{"code","message"},"meta":{"timestamp","traceId"}}` at `apiErr.HTTPStatus()` (`handler.go:1062`), plus the `traceparent` header.

Result: HTTP 401, `error.code=UNAUTHORIZED`, standard envelope — identical to handler-originated errors. Mirrors the proven `tenant_middleware.go` idiom.

**Author guidance (to document in the wiki):**

- Use `server.NewUnauthorizedError` / `NewForbiddenError`, **never** `fmt.Errorf` — a plain error maps to HTTP 500 (`classifyError` default, `server.go:281`).
- **Return the error and write nothing, OR write and return nil — never both.** Both `customErrorHandler` and `formatErrorResponse` bail if the response is already committed, so a double-write silently skips the envelope.
- **Do NOT copy the `ipPreGuard`/`rateLimit` inline-JSON pattern** (`ratelimit.go:54-71`) — those write a **divergent** body (no `meta`/`traceId`). The `NewUnauthorizedError` return path is the correct, envelope-consistent one.

### "Cannot be skipped" and public-route exemptions

Registration is framework-controlled and lands on the root chain, so **there is no per-route opt-out** — satisfying the core requirement. Consequently, any public-route exemptions (login/token endpoints, public assets) live **inside the auth middleware body** (inspect `c.Request().URL.Path` or the resolved route), **never** as a route-level flag. The only framework-level exemption is the health/ready probes.

---

## 7. Interaction notes & documented limitations

- **Runs before JOSE request decryption — hard boundary.** JOSE inbound decrypt/verify runs *inside* the typed-handler leaf wrapper (`runJOSEInbound`, `handler.go:515,538`), after the entire `e.Use` chain. *(Adversarially verified: CONFIRMED.)* A root-chain global gate therefore sees the body as opaque `application/jose` ciphertext and **cannot authenticate against decrypted plaintext** — it authenticates on headers/token/tenant only. Payload-origin auth for JOSE routes is already handled by JOSE's inner-JWS verify, so this is not a gap for token auth. **Document it up front** so body-dependent authorization is placed at the handler, not the global layer.
- **Tenant is available** — tenant middleware runs earlier in the chain (see §5).
- **Raw-response error-shape divergence (document).** `WithRawResponse` sets its context key *inside* the leaf wrapper (`handler.go:507`), after all middleware. `customErrorHandler` selects the raw formatter only when that key is set (`server.go:253`). A global-middleware rejection returns *before* the leaf runs, so a global auth 401/403 on a raw (Strangler-Fig) route emits the **standard** envelope, not the legacy raw shape. Not new (tenant middleware has the same property), but always-on with a global gate. The auth layer cannot read the route descriptor, so honoring the raw shape is impractical — **accept and document.**
- **JOSE XOR raw** is enforced by a startup panic (`jose.go:221`), so a route is never both — the hazard matrix is per-category, never combined.

---

## 8. ADR / migrations / docs

- **ADR-036** (`ADR-035` is the highest on `main`: `wiki/adr_035_route_template_path_params.md`; counter note at `architecture_decisions.md:507` reads "through ADR-035"). Re-confirm with `ls wiki/adr_*.md` at PR time in case of a concurrent ADR.
  - Create `wiki/adr_036_global_middleware.md`.
  - Add an index entry in `wiki/architecture_decisions.md` after the ADR-035 block.
  - Bump the counter note (`architecture_decisions.md:507`): "…through ADR-035" → "…through ADR-036".
- **Conventional-commit type: `feat:`** (additive interface, `ServerRunner` untouched — apidiff agrees, release-please reads the squash title).
- **migrations.md:** intentionally **not** updated — it is a breaking-change runbook (its version ladder does not track additive hops) and this change needs no upgrade action; documented in ADR-036 + CLAUDE.md instead.
- **Docs (not a new package → no Core Components / File Organization / Key Interfaces churn):**
  - New wiki deep-dive `wiki/global_middleware.md` (ordering, health/ready skip, run-once guarantee, 401 envelope, JOSE/raw limitations, public-route-exemption guidance).
  - `CLAUDE.md`: add a `GlobalMiddlewareRegisterer` stub in the Module System section beside the two existing optional interfaces + a Quick Reference link.
  - `llms.txt`: update if it enumerates module interfaces.
- **Per CLAUDE.md workflow:** run BOTH `/code-review` AND `/security-audit` before pushing — auth middleware is security-sensitive and the health/ready skip is a boundary worth auditing.

---

## 9. Testing plan

Conventions: camelCase test-function names, snake_case table-case names; 1:1 source-to-test files (append, never `*_extra_test.go`); `require.*` only on the test goroutine (post-`ServeHTTP`), `t.Errorf` inside any server closure; fresh `echo.New()` per table case; no alloc/op assertions under the default `-race` run; serve requests sequentially in e2e tests.

**Seam 1 — collection (exactly once), `app` package.** Test `CollectGlobalMiddleware` in `app/module_test.go`, co-located with the sibling `RouteRegisterer` collection tests (`module_test.go:613-650`). Define a `fakeGlobalMWModule` with a call-counter implementing `GlobalMiddlewareRegisterer`; assert counter == 1 and returned slice length; add a "skips modules without the interface" negative test (mirror `TestRegisterRoutesSkipsModulesWithoutRouteRegisterer`, `module_test.go:626`). No echo/server needed.

> **Minor 1:1 deviation (documented):** `module_registry.go` has no dedicated `module_registry_test.go` today; the sibling `RouteRegisterer` tests live in `module_test.go`. Follow the sibling — co-locate in `module_test.go`.

**Seam 2 — runtime behavior, `server` package.** New `server/global_middleware.go` (the `skipProbes` helper + `RegisterGlobalMiddleware`) → 1:1 `server/global_middleware_test.go`:

- **(a) end-to-end run:** `echo.New()` + `SetupMiddlewares` + `RegisterGlobalMiddleware` + a route + `httptest` + `e.ServeHTTP`; assert a header the fake middleware set (model: `TestAdaptMiddlewareChainsThrough`, `handler_test.go:2368`).
- **(b) skip health/ready:** GET `/health` and `/ready` bypass the gate (200) while a normal route is gated (model: `TestCreateProbeSkipperFlatForm`, `handler_test.go:2549`).
- **(c) short-circuit 401:** fake auth returns `NewUnauthorizedError` without `next`; assert `nextCalled == false`, `rec.Code == 401`, JSON `error.code`; wire `e.HTTPErrorHandler = customErrorHandler` (model: `TestAdaptMiddlewareAbortPropagatesError`, `handler_test.go:2387`).
- **(d) order — auth sees resolved tenant:** register the gate after `SetupMiddlewares`; fake auth reads `multitenant.GetTenant(c.RequestContext())`; assert post-`ServeHTTP` it equals the `X-Tenant-ID` header (model: `TestPublicTenantMiddlewareReturnsFlatForm`, `handler_test.go:2525`).
- **(e) runs-once-per-request:** per-instance invocation counter; assert == 1 per request (guards against double-wrapping).
- **(f) regression — applies to routes registered AFTER the gate:** register global middleware, then `Add` a route, then assert the gate ran on it (proves the root-chain, order-independent property).
- **(g) system endpoints are gated:** a route under `/_sys/...` or `/debug/...` is subject to the gate (per the "gate them too" decision).

For cross-package invocation of the flat `MiddlewareFunc`, use `server.NewHandlerContextForTest` + a `next()` bool-flip closure (template: `debug_handlers_test.invokeDebugMiddleware`, `debug_handlers_test.go:20`).

---

## 10. Files touched

| File | Change |
|---|---|
| `app/module.go` | New `GlobalMiddlewareRegisterer` interface |
| `app/module_registry.go` | New `CollectGlobalMiddleware()` (ranges `r.modules`, ~:105/:141) |
| `app/lifecycle.go` | Collect + register via optional type assertion, before `RegisterRoutes` (:63) |
| `app/interfaces.go` | **No change** (`ServerRunner` stays byte-identical) |
| `server/global_middleware.go` | **New** — `RegisterGlobalMiddleware` + `skipProbes` |
| `server/global_middleware_test.go` | **New** — runtime tests (a)–(g) |
| `app/module_test.go` | Collection tests (seam 1) |
| `wiki/adr_036_global_middleware.md` | **New** ADR |
| `wiki/architecture_decisions.md` | Index entry + counter bump (:507) |
| `wiki/global_middleware.md` | **New** deep-dive |
| `wiki/migrations.md` | Additive adopt-only note |
| `CLAUDE.md` | Module System stub + Quick Reference link |
| `llms.txt` | Update if it enumerates module interfaces |

---

## 11. Open risks

1. **Root chain vs group (RESOLVED).** Implementation must call `s.echo.Use`, not `RootGroup()/ModuleGroup().Use()`. Regression test (f) guards it.
2. **ServerRunner apidiff trap.** If the wiring is ever forced onto `app.ServerRunner`, the change flips to `feat!:` + `breaking-approved`. The inline type-assertion approach (§3.3) avoids it. A `ServerRunner` fake lacking the method makes startup **fail closed** when a module contributes global middleware (§3.3), so the gap is surfaced, not silent.
3. **ADR number.** Re-confirm `036` with `ls wiki/adr_*.md` at PR time (concurrent-ADR risk).
4. **Innermost-slot placement (ACCEPTED).** Auth runs after rate-limit/timeout/gzip/bodyLimit; the hook cannot express "run before those." Documented as an auth gate, not a general early hook.
5. **Raw-response error-shape divergence (ACCEPTED/documented).** Global 401/403 on a raw route emits the standard envelope, not the legacy raw shape.
6. **JOSE ordering (ACCEPTED/documented).** Global auth cannot see decrypted bodies; body-dependent authorization belongs at the handler.
7. **Echo serve-time behavior** was reasoned from Echo internals; test (a)/(f) double as empirical confirmation.

## 12. Review-driven changes (PR #643)

The sections above reflect the **shipped** implementation. Two behaviors were hardened during code review:

- **Fail-closed startup** (§3.3, §11): if a module registers global middleware but the server cannot install it, `applyGlobalMiddleware` returns an error and startup aborts — dropping a (canonically auth) gate silently is unsafe.
- **Inline capability assertion**: the app-side check is an inline anonymous interface, not a named `globalMiddlewareRegistrar` (avoids SonarCloud S8196 and a case-collision with the exported `GlobalMiddlewareRegisterer`).

`migrations.md` is intentionally skipped (§8, above).
