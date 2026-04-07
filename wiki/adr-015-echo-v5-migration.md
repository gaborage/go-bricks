# ADR-015: Echo v4 to v5 Migration

**Status:** Accepted
**Date:** 2026-04-06

## Context

GoBricks uses Echo v4 as its HTTP framework foundation, deeply integrated across ~92 files spanning the server, app, and scheduler packages. Echo v5.0.0 was released January 18, 2026, with the latest stable release being v5.1.0 (March 31, 2026). The "critical API change window" for v5 closed on March 31, 2026, meaning the API surface is now stable.

Echo v4 will only receive security and bug fixes until December 31, 2026. Continuing on v4 means:
- No new features or improvements
- Growing incompatibility with the Echo ecosystem (middleware, tooling)
- The `otelecho` OpenTelemetry instrumentation package has no v5 support, blocking future OTel upgrades

## Decision

Migrate from `echo/v4 v4.15.1` to `echo/v5 v5.1.0` and replace the OpenTelemetry instrumentation:
- **Remove:** `go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho v0.67.0`
- **Add:** `github.com/labstack/echo-opentelemetry v0.0.2` (first-party Echo OTel middleware)

### IP Extraction Strategy

Echo v5.1.0 changed `RealIP()` to only return `request.RemoteAddr` by default — it no longer reads proxy headers (`X-Forwarded-For`, `X-Real-IP`). To maintain backward compatibility, we configure `echo.LegacyIPExtractor()` on the Echo instance. This preserves existing behavior for rate limiting, IP pre-guard, and request logging.

**Future hardening:** Replace `LegacyIPExtractor()` with trusted-proxy-aware extractors (`ExtractIPFromXFFHeader()` / `ExtractIPFromRealIPHeader()`) in a follow-up change.

## Breaking Changes for Downstream Users

GoBricks' enhanced handler pattern (`server.GET/POST/etc.` with `HandlerFunc[T, R]`) abstracts most Echo internals. The migration impact on application code is limited to:

| Change | Old | New | Impact |
|--------|-----|-----|--------|
| `HandlerContext.Echo` field | `echo.Context` | `*echo.Context` | Update code accessing `ctx.Echo` methods |
| `RouteRegistrar.Add()` return | `*echo.Route` | `echo.RouteInfo` | Update code capturing route return values |
| `echo.HandlerFunc` signature | `func(echo.Context) error` | `func(*echo.Context) error` | Update raw Echo handlers (e.g., `RegisterReadyHandler`) |
| `echo.MiddlewareFunc` signature | Changes accordingly | | Update custom middleware |
| Import path | `echo/v4` | `echo/v5` | Update all imports |

**Low impact for most users:** Application code using the enhanced handler pattern only needs to update the import path and any direct `ctx.Echo` field accesses. The `HandlerFunc[T, R]` signature, `IAPIError`, and `Result[T]` types are unchanged.

## Key API Changes

### `echo.Context`: Interface to Struct Pointer
The most pervasive change. `echo.Context` changes from an interface to a concrete struct, and all handler/middleware signatures use `*echo.Context`.

### `HTTPErrorHandler`: Parameter Order Reversed
```go
// v4
e.HTTPErrorHandler = func(err error, c echo.Context) { ... }

// v5
e.HTTPErrorHandler = func(c *echo.Context, err error) { ... }
```

### `echo.HTTPError.Message`: `interface{}` to `string`
No longer requires type switching. `echo.NewHTTPError()` takes `(int, string)` instead of variadic `(int, ...interface{})`.

### `c.Response()`: `*echo.Response` to `http.ResponseWriter`
Use `echo.UnwrapResponse(c.Response())` to access `.Committed` and `.Status` fields.

### Server Lifecycle: `StartServer()`/`Shutdown()` Removed
Replaced by `echo.StartConfig` with context-based graceful shutdown.

### `NewContext`: Standalone Function
Changed from `e.NewContext(req, rec)` method to `echo.NewContext(req, rec, e)` standalone function.

### OpenTelemetry: `otelecho` to `echo-opentelemetry`
The community `otelecho` package does not support Echo v5. The first-party `echo-opentelemetry` package provides equivalent functionality.

## Consequences

### Positive
- Access to Echo v5 features: generic parameter extraction, type-safe context store, improved routing
- First-party OpenTelemetry support from the Echo team
- Better type safety with concrete struct pointer (compile-time errors instead of runtime)
- Alignment with the Echo ecosystem going forward

### Negative
- Breaking change for downstream users (import path + `HandlerContext.Echo` type)
- `echo-opentelemetry` is v0.0.2 (young package), though it's maintained by the Echo team
- `LegacyIPExtractor()` is a compatibility shim — should be replaced with proper trusted proxy configuration

### Neutral
- `Echo.Logger` changed from custom interface to `*slog.Logger` — GoBricks uses zerolog, minimal impact
- New v5 features (generic params, `ContextGet[T]`) available but not adopted in this migration to keep the diff focused
