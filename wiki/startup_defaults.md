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

## Messaging Pre-Warm Readiness Wait

In single-tenant mode, startup pre-warms the messaging publisher and then waits for it to report `IsReady()`, bounded by `messaging.reconnect.readytimeout` (default 5s — the same key and budget as the per-publish readiness pre-flight; see [context_deadlines.md](context_deadlines.md)). A publisher that isn't ready in time logs a WARN and startup continues — the wait never fails startup; the publish-time pre-flight still absorbs a slow first publish. The wait (`ConnectionPreWarmer.awaitPublisherReady`) is context-aware and reports a distinct cancellation outcome when its `ctx` is canceled, rather than mislabeling it as a readiness timeout — but that path only fires for callers that pass a cancelable context. On the framework's own boot path (`app/lifecycle.go`'s `prepareRuntime`), pre-warm runs with `context.Background()` and the OS signal handler is installed later (`waitForShutdownOrServerError`, after `prepareRuntime` returns), so a shutdown signal received during pre-warm does **not** abort the wait — it runs to ready-or-`readytimeout` regardless.

**Operator guidance:** because the HTTP listener starts only after pre-warm completes, raising `messaging.reconnect.readytimeout` directly stretches the pre-listen boot window whenever the broker is unreachable — size Kubernetes `startupProbe`/`livenessProbe` initial-delay and failure-threshold settings (or any other external "is it up yet" check) to comfortably exceed the configured `readytimeout`, not just the steady-state startup time.
