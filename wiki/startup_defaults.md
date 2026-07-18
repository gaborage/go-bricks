# Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization, with a documented fallback hierarchy that lets you override per-component or set a single global cap.

## Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization:

| Setting | Default | Purpose |
|---------|---------|---------|
| `app.startup.timeout` | 10s | Overall startup timeout (also serves as fallback for unset components) |
| `app.startup.database` | 10s | Database connection establishment |
| `app.startup.messaging` | 10s | AMQP broker connection |
| `app.startup.cache` | 5s | Redis connection |
| `app.startup.observability` | 15s | OTLP endpoint connection (higher for TLS handshake) |

**Fallback Hierarchy:**
1. Explicit component value (e.g., `app.startup.database: 15s`) → preserved
2. Global timeout (if set): `app.startup.timeout: 30s` → applied to all unset components
3. Per-component default (shown in table) → used when neither is set

**Example - Global fallback:**
```yaml
app:
  startup:
    timeout: 30s  # All components inherit 30s (database, messaging, cache, observability)
```

**Override defaults** in `config.yaml`:
```yaml
app:
  startup:
    timeout: 30s          # Longer overall timeout
    database: 15s         # More time for slow databases
    observability: 30s    # More time for remote OTLP endpoints
```

## Server Request Body Limit

`server.bodylimit` (int64 bytes; env `SERVER_BODYLIMIT`) caps the accepted HTTP request body size, rejecting an over-cap request with `413 Request Entity Too Large`. A request with a known `Content-Length` above the cap is rejected up front, before the handler runs; a chunked / unknown-length body is bounded by a limited reader instead, so the 413 surfaces when the read crosses the cap while the handler consumes the body:

| Setting | Default | Purpose |
|---------|---------|---------|
| `server.bodylimit` | 10 MB (10485760 bytes) | Maximum accepted HTTP request body size |

Raise it for endpoints that accept large uploads or bulk imports, or lower it to tighten the boundary:

```yaml
server:
  bodylimit: 26214400   # 25 MB — allow larger uploads
```

## Messaging Pre-Warm Readiness Wait

In single-tenant mode, startup pre-warms the messaging publisher and then waits for it to report `IsReady()`, bounded by `messaging.reconnect.readytimeout` (default 5s — the same key and budget as the per-publish readiness pre-flight; see [context_deadlines.md](context_deadlines.md)). A publisher that isn't ready in time logs a WARN and startup continues — the wait never fails startup; the publish-time pre-flight still absorbs a slow first publish. The wait (`ConnectionPreWarmer.awaitPublisherReady`) is context-aware and reports a distinct cancellation outcome when its `ctx` is canceled, rather than mislabeling it as a readiness timeout — but that path only fires for callers that pass a cancelable context. On the framework's own boot path (`app/lifecycle.go`'s `prepareRuntime`), pre-warm runs with `context.Background()` and the OS signal handler is installed later (`waitForShutdownOrServerError`, after `prepareRuntime` returns), so a shutdown signal received during pre-warm does **not** abort the wait — it runs to ready-or-`readytimeout` regardless.

**Operator guidance:** because the HTTP listener starts only after pre-warm completes, raising `messaging.reconnect.readytimeout` directly stretches the pre-listen boot window whenever the broker is unreachable — size Kubernetes `startupProbe`/`livenessProbe` initial-delay and failure-threshold settings (or any other external "is it up yet" check) to comfortably exceed the configured `readytimeout`, not just the steady-state startup time.

## Startup Route Logging

Set `server.logroutes` (bool; env `SERVER_LOGROUTES`) to emit one `Info` line per registered HTTP route at startup:

```text
Route registered  module=events method=POST path=/v1/events
```

It is a **tri-state** flag: an explicit `server.logroutes` value always wins; when the key is absent it defaults to `app.env` being development (on in `dev`/`development`/`local`, off in `prod`/`staging` per ADR-022). So routes are visible at first `go run` while production stays silent — an N-route service pays **zero** extra boot lines in prod unless an operator opts in. Turn it on in production for a smoke-check with `server.logroutes: true`; silence a dev boot with `server.logroutes: false`.

Attribution is by **registration order** (`module.Name()`), covering both typed (`server.GET/POST`) and raw (`RouteRegistrar.Add`) routes — `RouteDescriptor.ModuleName` is empty for every route, so the module is derived from the registration span, not the descriptor field. Routes registered before the module loop (debug / `_sys`) are attributed to `framework`. Note: `health`/`ready` are registered directly on the HTTP engine (not the route registry) and are therefore **not** included.
