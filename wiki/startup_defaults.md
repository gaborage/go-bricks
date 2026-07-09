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

In single-tenant mode, startup pre-warms the messaging publisher and then waits for it to report `IsReady()`, bounded by `messaging.reconnect.readytimeout` (default 5s — the same key and budget as the per-publish readiness pre-flight; see [context_deadlines.md](context_deadlines.md)). A publisher that isn't ready in time logs a WARN and startup continues — the wait never fails startup; the publish-time pre-flight still absorbs a slow first publish. Cancellation of the startup context (shutdown signal) aborts the wait immediately and is reported as a cancellation, not as a readiness timeout.
