# Breaking Change Migrations

Historical migration tables for upgrading existing GoBricks-based applications. Greenfield work can ignore this file — the new APIs are the only ones documented in CLAUDE.md.

## Echo-Free Boundary Types (ADR-034)

Per [ADR-034](adr_034_echo_boundary_types.md) (issue #623), every `github.com/labstack/echo/v5`
type was removed from the public surface. Echo stays the engine inside `server/`, but **no
`echo.*` symbol appears in any code an application developer writes, names, or imports**. This
is a **big-bang** breaking change — the old echo-typed methods are removed (not
`// Deprecated:`), so every consumer migrates at once and the compiler flags each removed symbol.

After upgrading, no `github.com/labstack/echo/v5` import should remain in your application **source**
— though it stays an **indirect** dependency in `go.mod` (and survives `go mod tidy`), because
`server/` still uses Echo as its engine internally. Run `git grep -nE 'ctx\.Echo|hctx\.Echo|echo\.(HandlerFunc|MiddlewareFunc|Context|Echo)|runner\.Echo\(\)|server\.EscalateSeverity'`
across your service to find every call site.

### (a) Custom middleware: Echo nested closure → flat `server.MiddlewareFunc`

The middleware shape is now flat: run your logic, then call `next()` to continue the chain (or
return an `IAPIError` **without** calling `next()` to abort). Propagate context with
`c.SetRequestContext(...)` instead of `c.SetRequest(c.Request().WithContext(...))`.

**Before:**

```go
func Auth(next echo.HandlerFunc) echo.HandlerFunc {
    return func(c *echo.Context) error {
        token := c.Request().Header.Get("Authorization")
        if token == "" {
            return server.NewUnauthorizedError("missing authorization header")
        }
        ctx := withUser(c.Request().Context(), token)
        c.SetRequest(c.Request().WithContext(ctx)) // context propagation
        return next(c)
    }
}
```

**After:**

```go
func Auth() server.MiddlewareFunc {
    return func(c server.HandlerContext, next func() error) error {
        token := c.RequestHeader("Authorization")
        if token == "" {
            return server.NewUnauthorizedError("missing authorization header")
        }
        c.SetRequestContext(withUser(c.RequestContext(), token)) // context propagation
        return next()
    }
}
```

A no-argument middleware can also be a plain function whose signature *is* `server.MiddlewareFunc`
(`func(c server.HandlerContext, next func() error) error`); register it directly with
`r.Use(MyMiddleware)` / `r.Group("/admin", MyMiddleware)`.

### (b) Request access: `ctx.Echo.Request().Context()` → `ctx.RequestContext()`

`HandlerContext.Echo` is removed. Use the typed accessors: `RequestContext()` for the request
`context.Context`, `Request()` / `ResponseWriter()` for the stdlib values, `Param` / `Query` /
`RequestHeader` / `Get` / `Set` for the rest.

**Before:**

```go
func (h *Handler) getUser(req GetReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
    reqCtx := ctx.Echo.Request().Context()
    user, err := h.svc.Find(reqCtx, req.ID)
    // ...
}
```

**After:**

```go
func (h *Handler) getUser(req GetReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
    reqCtx := ctx.RequestContext()
    user, err := h.svc.Find(reqCtx, req.ID)
    // ...
}
```

### (c) `ServerRunner.Echo()` removed → use `ModuleGroup()` / `RootGroup()`

The raw `*echo.Echo` is no longer exposed. Register routes and middleware through a
`server.RouteRegistrar`: `ModuleGroup()` (basePath-applied, for application routes) or the new
`RootGroup()` (no basePath, for `_sys`/debug-style endpoints).

**Before:**

```go
e := runner.Echo()
e.Use(server.LoggerWithConfig(appLogger, cfg))
e.GET("/_sys/ping", pingHandler)
```

**After:**

```go
root := runner.RootGroup()
root.Use(server.LoggerWithConfig(appLogger, cfg)) // constructor now returns server.MiddlewareFunc
root.Add(http.MethodGet, "/_sys/ping", pingHandler) // pingHandler is a server.Handler
```

### (d) `RegisterReadyHandler`: raw echo handler → `server.Handler`

**Before:**

```go
runner.RegisterReadyHandler(func(c *echo.Context) error {
    return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
})
```

**After:**

```go
runner.RegisterReadyHandler(func(c server.HandlerContext) error {
    return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
})
// Passing nil restores the framework's built-in readiness check.
```

### (e) `scheduler.CIDRMiddleware` returns `server.MiddlewareFunc`

The exported scheduler middleware moved to the flat shape. The call site is unchanged
(`Use` now takes `server.MiddlewareFunc`); only direct type references need updating.

**Before:**

```go
var mw echo.MiddlewareFunc = scheduler.CIDRMiddleware(allowed, logger)
sysGroup.Use(mw)
```

**After:**

```go
var mw server.MiddlewareFunc = scheduler.CIDRMiddleware(allowed, logger)
sysGroup.Use(mw)
```

### (f) Framework middleware constructors return `server.MiddlewareFunc` (call unchanged)

`server.CORS`, `RateLimit`, `Timeout`, `Timing`, `LoggerWithConfig`, `IPPreGuard`,
`RequestIDMiddleware`, `RequestEnrich`, `TenantMiddleware`, `TraceContext`, and
`PerformanceStats` now return `server.MiddlewareFunc` instead of `echo.MiddlewareFunc`. **Call
sites are unchanged** — only update any explicit variable types.

**Before:**

```go
var cors echo.MiddlewareFunc = server.CORS(exposeResponseTime, env)
r.Use(server.RateLimit(rps), cors)
```

**After:**

```go
var cors server.MiddlewareFunc = server.CORS(exposeResponseTime, env)
r.Use(server.RateLimit(rps), cors) // call sites identical
```

### (g) `SkipperFunc` takes `*http.Request`; `EscalateSeverity` is a `HandlerContext` method

`SkipperFunc` is now `func(r *http.Request) bool` (a skip decision needs only the request),
and severity escalation is the `HandlerContext.EscalateSeverity(level)` method — the old
package-level `server.EscalateSeverity(c, level)` is removed.

**Before:**

```go
func rateLimit(next echo.HandlerFunc) echo.HandlerFunc {
    return func(c *echo.Context) error {
        if exceeded(c) {
            server.EscalateSeverity(c, zerolog.WarnLevel)
            return c.JSON(429, errBody)
        }
        return next(c)
    }
}
```

**After:**

```go
func rateLimit(c server.HandlerContext, next func() error) error {
    if exceeded(c) {
        c.EscalateSeverity(zerolog.WarnLevel)
        return c.JSON(429, errBody)
    }
    return next()
}
```

### (h) Tests: build the context via `server.NewHandlerContextForTest`

Tests that built a context with `echo.New().NewContext(...)` now use the exported helper, which
returns a `server.HandlerContext` backed by a real (server-internal) echo context.

**Before:**

```go
e := echo.New()
c := e.NewContext(req, rec)
err := handler.GetUser(c) // raw echo handler
```

**After:**

```go
ctx := server.NewHandlerContextForTest(rec, req, cfg)
err := handler.GetUser(ctx) // handler is now a server.Handler
```

### Performance note

Custom-middleware routes pay a bounded **+1 heap-alloc per request per middleware-layer** — the
`func() error { return next(c) }` baton that bridges the flat shape to Echo's chain, which is
structurally unavoidable under the locked flat signature. This is incurred **only** on routes that
carry a flat middleware (your custom middleware, the debug endpoints, `/_sys/job`). The **framework
default middleware chain is unaffected**: it is registered echo-native via `e.Use` and the typed
handler hot path stays echo-direct, so the [ADR-026](adr_026_zero_overhead_request_path.md)
zero-overhead default path is preserved byte-for-byte.

### Behavior notes (debug/system endpoints only)

Converting the framework's own debug and `/_sys` denial paths from `echo.NewHTTPError(...)` to
`server.New*Error(...)` — which `classifyError` returns as-is (an `IAPIError`) rather than
mapping/sanitizing as it does an `*echo.HTTPError` — carries three small, intended deltas. None
touch the typed-handler envelope, `Result`/`ResultWithMeta`, raw-response mode, or JOSE, and all
affect only the IP-whitelisted, auth-gated admin endpoints:

- **Debug auth denial log (security improvement).** The invalid-token denial now logs the
  trusted-proxy-aware `server.ClientIP(...)` instead of echo's spoofable `c.RealIP()`. The
  IP-whitelist-before-auth ordering, constant-time token compare, and CIDR localhost-only fallback
  are unchanged; the 403/401 response bodies stay byte-identical in production.
- **Goroutine-dump 500 message.** When a goroutine dump fails, the production response message is
  now the verbatim, non-sensitive constant `"Failed to get goroutine dump"` rather than the generic
  sanitized string — 500-sanitization applies to *untyped* errors, and a deliberate
  `NewInternalServerError` with a safe constant follows the same convention as every other framework
  typed error. Status (500) and code (`INTERNAL_ERROR`) are unchanged.
- **Dev-mode denial details.** In development only, converted middleware denials now surface
  `details.stackTrace` (when stack capture is on) instead of the old `details.error` echo string.
- **Debug trusted-proxies WARN.** An invalid `debug.trustedproxies` CIDR entry now logs its startup
  WARN whenever debug endpoints are enabled (the trusted-proxy set is parsed once and shared by the
  IP-whitelist and auth middleware), rather than only when an IP allowlist was also configured.

## Bounded Publish Retries + Status-Driven Outbox Dead-Lettering (ADR-033)

Per [ADR-033](adr_033_outbox_retry_count_status_parking.md), the outbox relay now advances an
event's `retry_count` on **every** failed delivery (including a full broker outage), the messaging
publish loop is now bounded, and outbox parking is driven by `status = 'failed'` rather than by
`retry_count`. **No DB schema migration is required** — the `status` column and `'failed'` value
already exist. What changes for callers:

| Area | Before | After |
|------|--------|-------|
| `messaging.PublishToExchange` / `Publish` | retried a failing publish **forever** (returned only on cancel/shutdown/ACK) | returns `ErrPublishRetriesExhausted` after `messaging.reconnect.maxpublishattempts` (default **5**), wrapping the cause (`ErrPublishNacked` / `ErrPublishConfirmTimeout` / raw error) |
| Error identity on cancel/deadline | bare `context.Canceled` / `DeadlineExceeded` | same errors, now **wrapped** with the last attempt cause — match with `errors.Is`, not `==` |
| `outbox.Store.FetchPending` | `FetchPending(ctx, db, batchSize, maxRetries)` | `FetchPending(ctx, db, batchSize)` — status-gated, `maxRetries` parameter removed |
| `outbox.Store` | — | new method `MarkDeadLettered(ctx, db, eventID, errMsg)` |
| `outbox.maxretries` semantics | skipped events whose `retry_count` exceeded it (left silently `pending`) | bounds **poison only** (undecodable headers) → dead-letters to `status = 'failed'`; ALL broker-side failures — broker down, **NACK**, confirmation timeout, missing exchange — are connectivity and **never park** (retry indefinitely; monitor `retry_count` growth) |
| Relay during a broker outage | early-returned, `retry_count` frozen; job logged an error | advances `retry_count` per record per cycle, then returns a job error (only when there IS pending work) so the scheduler-level failure signal is preserved |

**New config keys (additive, safe defaults):** `messaging.reconnect.maxpublishattempts` (default 5)
and `outbox.publishtimeout` (default 60s). **`publishtimeout` must be ≥
`messaging.reconnect.connectiontimeout`** — the outbox module now **fails to start** otherwise.

**⚠️ Upgrade hazard — re-delivery of previously-abandoned events.** Before ADR-033 the relay fetch
filtered on `retry_count < maxretries`, so any event that exhausted its retries was *silently
soft-parked* (left `status='pending'`, never fetched again). The new `FetchPending` is status-gated
only, so on the first post-upgrade relay cycle **every such soft-parked row becomes eligible again and
is re-published in a burst.** This is technically correct un-sticking (those events were never
delivered and the outbox is at-least-once), but it can be a surprising surge. **Before upgrading,**
inspect `SELECT count(*) FROM <outbox> WHERE status='pending' AND retry_count >= <maxretries>` and
either let them re-deliver (ensure consumers are idempotent — they must be anyway) or delete the rows
you intend to abandon.

**Action required only if you:** call `messaging.PublishToExchange` / `Publish` directly and relied on
infinite blocking (now returns an error — handle it; the durable path is the outbox, which retries next
cycle); assert publish errors with `==` (switch to `errors.Is`); implement a custom `outbox.Store`
(update `FetchPending`'s signature and add `MarkDeadLettered`); or set `outbox.publishtimeout` below
`messaging.reconnect.connectiontimeout` (now rejected at startup). Applications using the outbox via
`deps.Outbox` need no code changes. Note `failed` rows accumulate (`DeletePublished` purges only
`published`) — monitor and prune them.

## Resource Managers Return a `ReleaseFunc` (ADR-032)

Per [ADR-032](adr_032_lease_refcount_tenant_handles.md), the three per-tenant resource managers now
reference-count their cached handles so an evicted handle is not closed while a caller is still using
it (the M3 eviction-while-in-use race, issue #606). `Get()` / `Publisher()` therefore return a third
value — a `ReleaseFunc` the caller invokes (typically deferred) when finished with the handle for the
current unit of work.

**This affects only direct callers of the raw managers** (`database.DbManager`, `cache.CacheManager`,
`messaging.Manager`). **Application code is unchanged:** `deps.DB(ctx)`, `deps.Cache(ctx)`,
`deps.Messaging(ctx)`, `deps.DBByName(ctx, name)`, and the `ResourceProvider` interface keep their
existing two-value `(handle, error)` signatures — the framework registers and releases the lease for
you at each request / message / job boundary.

**Before:**

```go
conn, err := dbManager.Get(ctx, tenantID)
client, err := messagingManager.Publisher(ctx, tenantID)
inst, err := cacheManager.Get(ctx, tenantID)
```

**After:**

```go
conn, release, err := dbManager.Get(ctx, tenantID)
if err != nil {
    return err
}
defer release() // return the lease when done with this unit of work

client, release, err := messagingManager.Publisher(ctx, tenantID)
// ... defer release()

inst, release, err := cacheManager.Get(ctx, tenantID)
// ... defer release()
```

The `Manager` interface in `cache/types.go` changed its `Get` signature to match
(`Get(ctx, key) (Cache, ReleaseFunc, error)`). On error the returned `ReleaseFunc` is `nil` — check
`err` first. Releasing is idempotent; the handle itself is a shared, long-lived pool — `release()`
does **not** close it, it only signals this borrower is done.

## Query Builder Validates Direct-String Identifiers (ADR-031)

Per [ADR-031](adr_031_query_builder_identifier_validation.md), the string identifier arguments of `SelectQueryBuilder.From`, the JOIN family (`JoinOn`/`LeftJoinOn`/`RightJoinOn`/`InnerJoinOn`/`CrossJoinOn`), `OrderBy`, `GroupBy`, `UpdateQueryBuilder.Set`/`SetMap`, and `DeleteQueryBuilder.OrderBy` are now validated against a safe identifier grammar on **all vendors** before interpolation (previously only Oracle quoted them; PostgreSQL interpolated verbatim — the M9 SQL-injection vector). Values outside the grammar are surfaced as a `ToSQL()` error (the methods never panic on bad identifier content).

**Before:**

```go
// Silently interpolated on PostgreSQL — executable injection:
qb.Select("*").From("users").OrderBy("name; DROP TABLE users--")
// → SELECT * FROM users ORDER BY name; DROP TABLE users--

// SQL functions accepted as plain strings:
qb.Select("*").From("orders").OrderBy("COUNT(*) DESC")
```

**After:**

```go
// Injection rejected — ToSQL() returns an error, no SQL emitted:
_, _, err := qb.Select("*").From("users").OrderBy("name; DROP TABLE users--").ToSQL()
// err != nil

// Function expressions must go through the Expr() escape hatch:
qb.Select("*").From("orders").OrderBy(qb.MustExpr("COUNT(*) DESC"))
```

**What still works unchanged:** bare/qualified column and table names, inline table aliases (`"users u"`), the `Table().As()` helper (in `From` and every JOIN), framework-quoted Oracle reserved words (`cols.Col("Level")`), and `ORDER BY`/`GROUP BY` with a trailing `ASC`/`DESC` (and optional `NULLS FIRST`/`LAST`) direction. Pass user **values** through the parameterized Filter API (`f.Eq`, …) — those were never affected.

## `PoolKeepAliveConfig.Enabled` Is Now `*bool` (ADR-030)

Per [ADR-030](adr_030_keepalive_enabled_optional.md), `config.PoolKeepAliveConfig.Enabled` changed from `bool` to `*bool` so that an explicit `database.pool.keepalive.enabled: false` is honored even when `interval` is left at its zero default. Previously a zero `interval` flipped `Enabled` back to `true`, silently overriding the opt-out (M5).

**Before:**

```go
PoolKeepAliveConfig{Enabled: true}
PoolKeepAliveConfig{Enabled: false}
if cfg.Pool.KeepAlive.Enabled { ... }
```

**After:**

```go
PoolKeepAliveConfig{Enabled: boolPtr(true)}   // or observability.BoolPtr(true)
PoolKeepAliveConfig{Enabled: boolPtr(false)}
PoolKeepAliveConfig{}                          // nil → defaults to enabled at validation
if cfg.Pool.KeepAlive.IsEnabled() { ... }      // nil-safe; nil is treated as disabled
```

**Action:**
- Code that constructs `PoolKeepAliveConfig` directly must pass a `*bool` for `Enabled` (a `nil` is treated as "unset" and defaulted to `true` during config validation).
- Code that reads `Enabled` as a `bool` must switch to the nil-safe `IsEnabled()` accessor. After validation `Enabled` is always non-nil, but `IsEnabled()` is the safe reader for structs built without validation (e.g. tests).
- **YAML/env config is unchanged:** `database.pool.keepalive.enabled: true|false` still binds. The behavioral fix is that `enabled: false` with `interval` unset now actually disables keep-alive (previously it was re-enabled). Configs that omit `enabled` still default to `true`.

## Environment Variables Are Now Ingested Only Under Known Config Prefixes (M4)

Environment-variable config loading now ignores any variable whose first dotted segment is not a recognized top-level config section (`app`, `server`, `database`, `databases`, `cache`, `log`, `messaging`, `multitenant`, `debug`, `source`, `scheduler`, `outbox`, `inbox`, `keystore`, `observability`, `custom`). Previously **every** process environment variable was merged, so a bare variable matching a section name (`DEBUG=1`, `CACHE=…`, `DATABASE=…`) — or a Kubernetes Docker-link variable like `SERVER_PORT=tcp://10.96.0.1:80` — replaced that section's map with a scalar and crashed startup with `expected a map, got string`. A defensive merge guard also drops any remaining scalar that would overwrite an existing config map.

**Action:**
- Configure the framework only through fully-qualified, sub-keyed variables (e.g. `CACHE_ENABLED=true`, `CACHE_REDIS_HOST=…`, `DATABASE_HOST=…`) — these are unaffected.
- Application-specific settings must live under the `CUSTOM_` prefix (`custom.*`); other unprefixed variables are no longer read into config (and no longer crash startup).
- No action is needed for deployments that already use sub-keyed variables or YAML; this change only removes the ability of unrelated/bare env vars to clobber a config section.

## Graceful Shutdown Now Stops Inbound Work First (ADR-029)

Per [ADR-029](adr_029_graceful_shutdown_order.md), `App.Shutdown` reordered its phases. **Before:** modules were torn down first, while the HTTP server was still serving and AMQP consumers were still delivering — so in-flight handlers could run against already-shut-down modules (panics/errors during the shutdown window). **After:** `server → consumers → modules → observability → manager cleanup loops → closers`.

**No code or config change is required** — this is an internal behavioral correction. Two things to be aware of:

- If a module's `Shutdown()` implicitly relied on the HTTP server still serving or on consumers still delivering, it will now see the corrected order (server drained and consumers stopped before module teardown). This was the buggy case the reorder fixes.
- `messaging.Manager` gains an additive `StopConsumers()` method (quiesce consumers without closing connections); existing code is unaffected.

## PostgreSQL `BuildUpsert` Now Binds Update Values (ADR-028)

Per [ADR-028](adr_028_pg_upsert_binds_update_values.md), `QueryBuilder.BuildUpsert` on PostgreSQL now binds the `updateColumns` values as parameters instead of emitting `col = EXCLUDED.col`.

**Before:** `ON CONFLICT (...) DO UPDATE SET "col" = EXCLUDED."col"` — the on-conflict update silently reused the *insert* value and ignored the `updateColumns` values entirely (diverging from Oracle's MERGE).

**After:** `ON CONFLICT (...) DO UPDATE SET "col" = $N` — the caller's update value is bound (Oracle parity).

**Action:**
- If you assert on the exact generated SQL, update expectations (`EXCLUDED.col` → `$N`).
- If you relied on the old behavior of updating to the *inserted* value, pass the same value in both `insertColumns` and `updateColumns` (the result is then identical).
- Calls that passed differing update values (or update columns absent from the insert set) were silently wrong before and now apply the intended value — no action needed beyond verifying the corrected behavior is what you want.

## Outbox/Inbox Multi-Tenant Relay & Cleanup

The outbox relay, outbox-cleanup, and inbox-cleanup jobs run from the scheduler's tenant-less context, so in multi-tenant mode they could not resolve any tenant's database — outbox events accumulated per tenant and were **never delivered**, and inbox ledgers were never pruned. These jobs now **fan out across the configured static tenants** (`multitenant.tenants`), resolving each tenant's database independently, so multi-tenant outbox delivery and cleanup now work.

For **dynamic** tenant sources (`source.type: dynamic`), the tenant set is not enumerable at job-registration time, so the framework now **fails fast** rather than silently never relaying:

- **Outbox** + `multitenant.enabled` + `source.type: dynamic` → module **Init fails** (the relay is the whole point of the outbox).
- **Inbox** + `multitenant.enabled` + `source.type: dynamic` → **`RegisterJobs` fails** when the cleanup job would register (i.e. when the scheduler is present). `ProcessOnce` is unaffected and still works — run without the scheduler to use the inbox without retention cleanup.

**Action:** a dynamic-multi-tenant deployment with outbox/inbox cleanup enabled now fails at startup instead of silently losing events. Use static `multitenant.tenants` config, or (for inbox) drop the scheduler to keep `ProcessOnce` without cleanup.

## `database.tls.cert/key/ca` Now Wired Into the Drivers (ADR-027)

Per [ADR-027](adr_027_database_tls_material.md), the `database.tls.cert`, `database.tls.key`, and `database.tls.ca` fields — previously advertised but never consumed — are now honored.

**PostgreSQL:** the DSN now includes `sslrootcert` (ca), `sslcert` (cert), and `sslkey` (key) when set, so pgx authenticates the server and presents a client certificate for mTLS. A config that set `mode: require` + `ca:` was previously **encrypted but unauthenticated** (MITM-able); it now performs CA verification.

**Action (PostgreSQL):** if you set `database.tls.ca` (or `cert`/`key`), the connection now verifies the server certificate against that CA. **A wrong, missing, or mismatched CA — or a server cert that doesn't match — will now make the connection fail** where it previously succeeded unauthenticated. Confirm the CA path and server certificate before upgrading. Configs without `cert/key/ca` are unaffected.

**Oracle:** TLS via tcps/wallet is not implemented, so `database.tls.cert/key/ca` are now **rejected at startup validation** (rather than silently ignored). `database.tls.mode` alone still passes (no-op).

**Action (Oracle):** remove `database.tls.cert`, `database.tls.key`, and `database.tls.ca` from Oracle configs — they never did anything and now fail validation.

## Migration `Config.DryRun` Is Now Honored

`Config.DryRun` ("Only validate, do not execute") was documented and stamped into the `migration.applied` audit event, but **never consumed** — a `DryRun=true` `Migrate`/`MigrateAll` ran a real migration (mutating the schema across the whole tenant fleet) while the audit falsely recorded `dry_run=true`.

`DryRun=true` now downgrades a `migrate` to the Flyway `validate` verb, so no schema is mutated. Because `validate` is not an application, it emits no `migration.applied` audit event (per ADR-019) — so the `migration.dry_run` attribute is now always `false` on emitted events, accurately asserting "this was a real application."

**Action:** if any pipeline set `DryRun=true` and relied on it actually applying migrations (contrary to the field's documentation), it now only validates. Remove `DryRun` (or set it `false`) for runs that must apply schema changes.

## `APP_ENV` Now Selects the `config.<env>.yaml` Overlay

Previously the environment-specific YAML overlay suffix was read from koanf **before** the environment provider loaded, so the `APP_ENV` environment variable could not select it — the suffix always came from `config.yaml`/defaults (typically `development`). A 12-factor deployment that set `APP_ENV=production` and shipped `config.production.yaml` silently ran the development overlay (or none), even though `cfg.App.Env` still ended up `production`.

`APP_ENV` now drives overlay selection, matching the documented precedence (env vars > `config.<env>.yaml` > `config.yaml` > defaults).

**Action:** if you set `APP_ENV` and ship a `config.<env>.yaml` that was previously being ignored, that overlay is now loaded — review those files to confirm their values are correct for the environment. A malformed `APP_ENV` (not matching `^[a-z][a-z0-9-]{0,31}$`) is now rejected at startup with a clear `app.env` error instead of being interpolated into a config filename.

## Request-Path Zero-Overhead Changes (ADR-026)

A set of perf changes that make the default-config request path allocate less. Several carry consumer-visible surface:

### DB span/metric emission gated on `observability.enabled`

Per-operation database OpenTelemetry spans and metrics are now emitted only when observability is enabled (the global `otel.Tracer`/`otel.Meter` return non-nil no-ops, so without an explicit gate the framework built and discarded span/metric attributes on every query with observability off). The app bootstrap sets the gate automatically from `observability.enabled`. **Consumers that use the `database` package directly — without the framework's `app` bootstrap — must call `database.SetObservabilityEnabled(true)` to emit DB spans/metrics**; otherwise DB telemetry is silently suppressed even when a global OTel provider is registered.

### `logger.LogEvent` gained `Enabled() bool`

The interface now has `Enabled() bool` so callers can skip building expensive fields when an event would be dropped (the request action-log short-circuits at disabled levels). The framework's `LogEventAdapter` implements it (delegating to zerolog's nil-safe `Event.Enabled()`). **Any external type implementing `logger.LogEvent`** (custom adapters, test doubles) must add the method:

```go
func (e *YourEvent) Enabled() bool { return true } // or delegate to the underlying event
```

This mirrors prior interface-evolution entries (S8179/S8196) — the fix is to add the method, never to abandon the interface.

### `server.SetupMiddlewares` signature

`SetupMiddlewares` now takes an explicit `observabilityEnabled bool` immediately after `cfg`:

```go
// before
SetupMiddlewares(e, log, cfg, healthPath, readyPath)
// after
SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)
```

The OTel HTTP middleware is registered only when the flag is true (zero per-request span/metric overhead when observability is off). Apps using the framework's normal `app`/server bootstrap are unaffected; only direct callers of `SetupMiddlewares` need to pass the flag.

### `server.gzip.minlength` default

`server.gzip.minlength` now defaults to **1024** bytes (previously gzip compressed everything). Responses smaller than this are sent uncompressed, avoiding gzip header overhead on tiny JSON. Set `server.gzip.minlength: 0` to restore always-compress behavior.

### `X-Response-Time` header is now opt-in (`server.responsetime.enabled`)

The diagnostic `X-Response-Time` response header (per-request processing time) is **no longer emitted by default**. The `Timing` middleware that set it on every response is now gated behind `server.responsetime.enabled` (default **false**). Each per-response `Header().Set` allocates a `[]string` in `net/textproto.MIMEHeader.Set` — measured at ~2.4% of total allocations on the default read workload — and OTel provides richer latency telemetry. `X-Request-ID` and `traceparent` are unaffected. To restore the header, set:

```yaml
server:
  responsetime:
    enabled: true   # or SERVER_RESPONSETIME_ENABLED=true
```

When disabled, CORS also stops advertising `X-Response-Time` in `Access-Control-Expose-Headers`, keeping the CORS contract aligned with what is on the wire. Direct callers of `server.CORS(...)` get a new leading `exposeResponseTime bool` parameter (`CORS(cfg.Server.ResponseTime.Enabled, cfg.App.Env)`); apps using the normal server bootstrap are unaffected.

## Connection Pool Idle Default — Tracks Max (ADR-025)

Per [ADR-025](adr_025_pool_idle_tracks_max.md), the default for `database.pool.idle.connections` changed from a fixed **2** to **tracking `database.pool.max.connections`** (default 25). The old default kept only 2 idle connections against a max of 25, so under sustained load the pool continuously opened and closed physical connections (TCP+TLS+auth) — measured as a 91% p95 latency reduction (16.25 → 1.46 ms) and connection-establishment errors dropping from 8.15% to 0% once idle tracked max.

**No code or config change is required.** This is a default-only change with `apiImpact: none`. But be aware of three behavioral consequences if you relied on the old default:

| Area | Before | After |
|---|---|---|
| Idle connections held (idle unset) | up to 2 | up to `pool.max.connections` (default 25), still reaped after `pool.idle.time` (5m) |
| Multi-tenant idle ceiling | 2 × tenants | up to ~12.5× higher per tenant — verify your PostgreSQL `max_connections` / Oracle session budget |
| Pool idle metrics | OTEL idle gauges & `Stats()` `max_idle_connections`/`configured_idle_connections` reported `2` | now report the effective max — update dashboards/alerts keyed to `idle == 2` |

**To keep the old behavior**, set the value explicitly — an explicit `database.pool.idle.connections` always wins over the default:

```yaml
database:
  pool:
    idle:
      connections: 2   # opt back into the old conservative fixed idle cap of 2
```

The effective `pool_max_connections` / `pool_idle_connections` / `pool_max_lifetime` / `pool_idle_time` are now logged at `Info` on every successful connection, so you can confirm what the pool actually uses.

## Config Keys — Flat-Smushed Rename (ADR-024)

Per [ADR-024](adr_024_config_key_flatsmush.md), 21 snake_case config keys were renamed to the framework's underscore-free flat-smushed convention so they become settable via environment variables (the env loader maps `_`→`.`, koanf's nesting delimiter, so underscored leaf keys were silently unreachable from env). Update both your YAML and any environment variables. Go field names are unchanged.

| Old key (YAML) | New key (YAML) | Old env var (broken) | New env var |
|---|---|---|---|
| `cache.manager.max_size` | `cache.manager.maxsize` | `CACHE_MANAGER_MAX_SIZE` | `CACHE_MANAGER_MAXSIZE` |
| `cache.manager.idle_ttl` | `cache.manager.idlettl` | `CACHE_MANAGER_IDLE_TTL` | `CACHE_MANAGER_IDLETTL` |
| `cache.manager.cleanup_interval` | `cache.manager.cleanupinterval` | `CACHE_MANAGER_CLEANUP_INTERVAL` | `CACHE_MANAGER_CLEANUPINTERVAL` |
| `log.sensitive_fields` | `log.sensitivefields` | `LOG_SENSITIVE_FIELDS` | `LOG_SENSITIVEFIELDS` |
| `messaging.reconnect.reinit_delay` | `messaging.reconnect.reinitdelay` | `MESSAGING_RECONNECT_REINIT_DELAY` | `MESSAGING_RECONNECT_REINITDELAY` |
| `messaging.reconnect.resend_delay` | `messaging.reconnect.resenddelay` | `MESSAGING_RECONNECT_RESEND_DELAY` | `MESSAGING_RECONNECT_RESENDDELAY` |
| `messaging.reconnect.connection_timeout` | `messaging.reconnect.connectiontimeout` | `MESSAGING_RECONNECT_CONNECTION_TIMEOUT` | `MESSAGING_RECONNECT_CONNECTIONTIMEOUT` |
| `messaging.reconnect.max_delay` | `messaging.reconnect.maxdelay` | `MESSAGING_RECONNECT_MAX_DELAY` | `MESSAGING_RECONNECT_MAXDELAY` |
| `messaging.publisher.max_cached` | `messaging.publisher.maxcached` | `MESSAGING_PUBLISHER_MAX_CACHED` | `MESSAGING_PUBLISHER_MAXCACHED` |
| `messaging.publisher.idle_ttl` | `messaging.publisher.idlettl` | `MESSAGING_PUBLISHER_IDLE_TTL` | `MESSAGING_PUBLISHER_IDLETTL` |
| `outbox.table_name` | `outbox.tablename` | `OUTBOX_TABLE_NAME` | `OUTBOX_TABLENAME` |
| `outbox.auto_create_table` | `outbox.autocreatetable` | `OUTBOX_AUTO_CREATE_TABLE` | `OUTBOX_AUTOCREATETABLE` |
| `outbox.default_exchange` | `outbox.defaultexchange` | `OUTBOX_DEFAULT_EXCHANGE` | `OUTBOX_DEFAULTEXCHANGE` |
| `outbox.poll_interval` | `outbox.pollinterval` | `OUTBOX_POLL_INTERVAL` | `OUTBOX_POLLINTERVAL` |
| `outbox.batch_size` | `outbox.batchsize` | `OUTBOX_BATCH_SIZE` | `OUTBOX_BATCHSIZE` |
| `outbox.max_retries` | `outbox.maxretries` | `OUTBOX_MAX_RETRIES` | `OUTBOX_MAXRETRIES` |
| `outbox.retention_period` | `outbox.retentionperiod` | `OUTBOX_RETENTION_PERIOD` | `OUTBOX_RETENTIONPERIOD` |
| `inbox.table_name` | `inbox.tablename` | `INBOX_TABLE_NAME` | `INBOX_TABLENAME` |
| `inbox.auto_create_table` | `inbox.autocreatetable` | `INBOX_AUTO_CREATE_TABLE` | `INBOX_AUTOCREATETABLE` |
| `inbox.retention_period` | `inbox.retentionperiod` | `INBOX_RETENTION_PERIOD` | `INBOX_RETENTIONPERIOD` |
| `keystore.secret_min_length` | `keystore.secretminlength` | `KEYSTORE_SECRET_MIN_LENGTH` | `KEYSTORE_SECRETMINLENGTH` |

> The "old env var" column never worked (that is the bug ADR-024 fixes); it is shown only to help locate occurrences in existing deployment manifests.

## Observability Config Keys — Flat-Smushed Rename (#554)

ADR-024 audited only the `koanf`-tagged keys in `config/types.go`. The `observability` config tree (`observability/config.go`) is tagged with `mapstructure` and loaded via a separate `config.Config.Unmarshal("observability", …)` path that binds by koanf tag or the case-insensitive Go field name and **never honors the `mapstructure` tag**. Four compound-word keys there carried underscores and so bound from neither YAML (the underscored key matched no field name) **nor** env (the loader maps `_`→`.`). [Issue #554](https://github.com/gaborage/go-bricks/issues/554) flat-smushed them to the same convention. Go field names are unchanged.

| Old key (YAML, broken) | New key (YAML) | Old env var (broken) | New env var |
|---|---|---|---|
| `observability.metrics.histogram_aggregation` | `observability.metrics.histogramaggregation` | `OBSERVABILITY_METRICS_HISTOGRAM_AGGREGATION` | `OBSERVABILITY_METRICS_HISTOGRAMAGGREGATION` |
| `observability.logs.disable_stdout` | `observability.logs.disablestdout` | `OBSERVABILITY_LOGS_DISABLE_STDOUT` | `OBSERVABILITY_LOGS_DISABLESTDOUT` |
| `observability.logs.slow_request_threshold` | `observability.logs.slowrequestthreshold` | `OBSERVABILITY_LOGS_SLOW_REQUEST_THRESHOLD` | `OBSERVABILITY_LOGS_SLOWREQUESTTHRESHOLD` |
| `observability.logs.sampling_rate` | `observability.logs.samplingrate` | `OBSERVABILITY_LOGS_SAMPLING_RATE` | `OBSERVABILITY_LOGS_SAMPLINGRATE` |

> Unlike the ADR-024 keys (which still bound from YAML and broke only from env), these four never bound from YAML either — a service setting `observability.logs.sampling_rate` silently got the framework default. The recurrence guard now also walks `mapstructure` tags (`config.TestConfigKoanfTagsHaveNoUnderscore`) and a sibling `observability.TestObservabilityConfigTagsHaveNoUnderscore` covers the observability tree.

## Go Naming Conventions (S8179) — Getter Methods

Per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/), getter methods should NOT have the `Get` prefix.

| Package | Old Method | New Method |
|---------|------------|------------|
| `config.Config` | `GetString()`, `GetInt()`, `GetInt64()`, `GetFloat64()`, `GetBool()` | `String()`, `Int()`, `Int64()`, `Float64()`, `Bool()` |
| `config.Config` | `GetRequiredString()`, `GetRequiredInt()`, `GetRequiredInt64()`, `GetRequiredFloat64()`, `GetRequiredBool()` | `RequiredString()`, `RequiredInt()`, `RequiredInt64()`, `RequiredFloat64()`, `RequiredBool()` |
| `app.ResourceProvider` | `GetDB()`, `GetMessaging()`, `GetCache()` | `DB()`, `Messaging()`, `Cache()` |
| `app.ModuleDeps` | `GetDB`, `GetMessaging`, `GetCache` (fields) | `DB`, `Messaging`, `Cache` (fields) |
| `app.Builder` | `GetError()` | `Error()` |
| `messaging.Manager` | `GetPublisher()` | `Publisher()` |
| `server.Validator` | `GetValidator()` | `Validator()` |
| `migration.FlywayMigrator` | `GetDefaultMigrationConfig()` | `DefaultMigrationConfig()` |
| `config.TenantStore` | `GetTenants()` | `Tenants()` |
| `app.MetadataRegistry` | `GetModules()`, `GetModule()` | `Modules()`, `Module()` |
| `app.App` | `GetMessagingDeclarations()` | `MessagingDeclarations()` |
| `database.Interface` | `GetMigrationTable()` | `MigrationTable()` |
| `database/testing.TestDB` | `GetQueryLog()`, `GetExecLog()` | `QueryLog()`, `ExecLog()` |
| `database/testing.TenantDBMap` | `GetTenantDB()` | `TenantDB()` |
| `server.RouteRegistry` | `GetRoutes()` | `Routes()` |

**Example:**
```go
// OLD
host := cfg.GetString("server.host", "0.0.0.0")
db, err := deps.GetDB(ctx)

// NEW
host := cfg.String("server.host", "0.0.0.0")
db, err := deps.DB(ctx)
```

## Interface Naming Conventions (S8196)

Per [SonarCloud rule S8196](https://rules.sonarsource.com/go/RSPEC-8196/) and [ADR-013](adr_013_interface_naming_conventions.md).

| Package | Old Interface | New Interface |
|---------|---------------|---------------|
| `scheduler` | `Job` | `Executor` |
| `app` | `HealthProbe` | `Prober` |
| `database` | `TenantStore` | `DBConfigProvider` |
| `messaging` | `TenantMessagingResourceSource` | `BrokerURLProvider` |
| `server` | `ResultLike` | `ResultMetaProvider` |
| `cache` | `TenantCacheResourceSource` | `ConfigProvider` |

## Standardized `ToSQL()` Across Query Builders (S8179)

Per [ADR-017](adr_017_insert_query_builder.md), `qb.Insert*` constructors return `types.InsertQueryBuilder` (a go-bricks-owned interface) instead of `squirrel.InsertBuilder` directly. The render method is renamed from `ToSql()` to `ToSQL()` — matching `Select`/`Update`/`Delete`.

| Constructor | Old return | New return | Render method |
|---|---|---|---|
| `qb.Insert(table)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertWithColumns(table, cols...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertStruct(table, instance)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertFields(table, instance, fields...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |

**Example:**
```go
// OLD
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSql()

// NEW
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSQL()
```

The new interface preserves all common chaining methods (`Columns`, `Values`, `SetMap`, `Options`, `Prefix`, `Suffix`, `Select`). For specialized squirrel-only methods (e.g., `RunWith`, `PlaceholderFormat`), keep the rendered SQL via `ToSQL()` and execute with `db.Exec(ctx, sql, args...)`.

## Scheduler Default Timezone → UTC (ADR-023)

Previously the scheduler ran jobs in the host's local time (`time.Local`). It now
defaults to **UTC**. Deployments that relied on host-local job times must set
`scheduler.timezone: "-"` to preserve the old behavior, or set an explicit IANA
zone.

```yaml
scheduler:
  timezone: "-"   # preserve pre-upgrade host-local behavior
```
