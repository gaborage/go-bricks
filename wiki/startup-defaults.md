# Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization, with a documented fallback hierarchy that lets you override per-component or set a single global cap.

## Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization:

| Setting | Default | Purpose |
|---------|---------|---------|
| `startup.timeout` | 10s | Overall startup timeout (also serves as fallback for unset components) |
| `startup.database` | 10s | Database connection establishment |
| `startup.messaging` | 10s | AMQP broker connection |
| `startup.cache` | 5s | Redis connection |
| `startup.observability` | 15s | OTLP endpoint connection (higher for TLS handshake) |

**Fallback Hierarchy:**
1. Explicit component value (e.g., `startup.database: 15s`) → preserved
2. Global timeout (if set): `startup.timeout: 30s` → applied to all unset components
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
