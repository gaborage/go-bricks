# Troubleshooting

## Build/Test Failures

```bash
# "cannot find package" errors
go mod tidy && go mod download

# "Docker not running" during integration tests
make docker-check
docker info

# Race condition failures
go test -race -run TestSpecificFailing ./package

# Linting errors
golangci-lint cache clean
golangci-lint run
```

### Faster local Oracle integration runs (`GO_BRICKS_ORACLE_CONTAINER`)

Since ADR-020, the Oracle integration suite provisions one container per `go test` invocation (~18.5s cold-start) and runs every test in an isolated schema. For tight local iteration where you re-run the suite many times in a row, a long-lived developer container saves the 18.5s on each invocation.

**Pattern** — point a local developer container at a stable port (e.g. 1521) and let a thin wrapper script export the connection details so the test binary reads them via `os.Getenv` and skips the container start:

```bash
# 1) Start a long-lived container once
docker run -d --name gobricks-oracle-dev \
    -p 1521:1521 \
    -e ORACLE_PASSWORD=testpass \
    -e APP_USER=testuser \
    -e APP_USER_PASSWORD=testpass \
    gvenzl/oracle-free:23-slim
docker logs -f gobricks-oracle-dev | grep -m1 "DATABASE IS READY TO USE!"

# 2) Wrap go test in a script that exports the existing endpoint
export GO_BRICKS_ORACLE_CONTAINER="oracle://system:testpass@localhost:1521/FREEPDB1"
go test -tags=integration ./database/oracle/...
```

Currently this is a documented developer convenience: TestMain always spins up a container regardless of `GO_BRICKS_ORACLE_CONTAINER`. If you want to plumb it through your own fork, override TestMain in `database/oracle/integration_main_test.go` to check the env var before calling `containers.StartOracleContainerForTestMain` and construct a synthetic `OracleContainer` against the existing endpoint instead. The per-test `NewSchema` helper works against any reachable container regardless of how it was provisioned — DROP USER ... CASCADE keeps the long-lived container clean between runs.

## Database Issues

```bash
# Oracle: ORA-00936 "missing expression"
# → Use type-safe filter methods (f.Eq, f.Lt, f.In, etc.) instead of f.Raw() for auto-quoting

# PostgreSQL: "syntax error at or near $1"
# → Check placeholder numbering (PostgreSQL: $1,$2; Oracle: :1,:2)

# "database not configured" errors
# → Set database.type, database.host OR database.connection_string (see ADR-003)
```

## Connection Pool Issues (ORA-01013, connection reset)

```bash
# ORA-01013: "user requested cancel of current operation" after idle period
# → Stale connections being used after NAT/firewall timeout
# → GoBricks applies production-safe defaults automatically:
#   - Pool.KeepAlive.Enabled: true (60s probes prevent silent drops)
#   - Pool.Idle.Time: 5m (recycle idle connections before timeout)
#   - Pool.Lifetime.Max: 30m (periodic connection recycling)
# → For custom configuration, ensure keepalive interval < NAT timeout
```

**Override defaults for aggressive environments (e.g., strict firewall):**
```yaml
database:
  pool:
    keepalive:
      enabled: true
      interval: 30s       # Probe every 30s for strict firewalls
    idle:
      time: 2m            # Close idle after 2 minutes
    lifetime:
      max: 15m            # Recycle all connections every 15 minutes
```

**On-premises with no NAT/firewall concerns, opt-out of recycling:**
```yaml
database:
  pool:
    idle:
      time: 0             # 0 = no idle timeout (not recommended for cloud)
    lifetime:
      max: 1h
```

## Cache Issues

```bash
# "cache not configured" errors
# → Set cache.enabled: true AND cache.redis.host in config
# → OR verify multi-tenant cache config in multitenant.tenants.<tenant_id>.cache

# Connection failures
# → Check Redis server running: redis-cli ping
# → Verify cache.redis.port matches Redis instance (default: 6379)
# → Check firewall rules if Redis on different host

# Multi-tenant cache issues
# → Use deps.Cache(ctx) (function-based, resolves tenant from context)
# → Ensure tenant context set: multitenant.SetTenant(ctx, tenantID)

# Cache timeout errors
# → Increase operation timeout: ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
# → Check network latency if Redis on different host
# → Verify pool size adequate: cache.redis.pool_size >= NumCPU * 2

# CacheManager eviction issues
# → Increase max_size if seeing unexpected evictions: cache.manager.max_size
# → Increase idle_ttl if caches closing too quickly: cache.manager.idle_ttl
# → Monitor stats: cacheManager.Stats() — check Evictions/IdleCleanups counters
```

## Observability Issues

```bash
# "cannot use OTLP logs with pretty=true"
# → Set logger.pretty: false when observability.logs.enabled: true
# → Or omit logger.pretty entirely and rely on log.output.format: auto (default)
#   which picks JSON automatically when OTLP logs are active.

# Local logs are uncolored
# → Default is log.output.format: auto, which colors stdout only when it's a TTY
#   AND OTLP logs aren't active. Force colors via log.output.format: console
#   (incompatible with observability.logs.enabled: true).

# Spans not appearing in collector
# → Check observability.enabled: true
# → Wait for batch timeout (500ms dev, 5s prod)
# → Or set trace.endpoint: stdout

# Missing trace_id in logs
# → Use logger.WithContext(ctx).Info()
# → Verify provider initialized before logger enhancement

# Noisy [OBSERVABILITY] debug logs
# → Unset GOBRICKS_DEBUG environment variable
```

### gRPC error: "frame header looked like an HTTP/1.1 header" (New Relic)

```
ERROR: rpc error: code = Unavailable desc = connection error: desc = "error reading server preface:
       http2: failed reading the frame payload: http2: frame too large, note that the frame header
       looked like an HTTP/1.1 header"
```

**Root cause:** gRPC client connecting to HTTP endpoint (port mismatch).

**Solutions:**
1. Using port 4318 with `protocol: grpc` → WRONG (4318 is HTTP port). Change endpoint to `otlp.nr-data.net:4317` (gRPC port).
2. Using `https://` scheme with gRPC protocol → WRONG (gRPC doesn't accept scheme). Use `otlp.nr-data.net:4317` (no `https://`).
3. Missing TLS configuration → Check `insecure: false` (New Relic requires TLS).

**Correct New Relic gRPC config:**
```yaml
observability:
  trace:
    endpoint: otlp.nr-data.net:4317  # NO https://, port 4317 for gRPC
    protocol: grpc
    insecure: false  # TLS required
    compression: gzip
    headers:
      api-key: your-license-key
```

**HTTP endpoint format:**
- HTTP requires `https://` or `http://` scheme: `https://otlp.nr-data.net:4318/v1/traces`
- gRPC requires NO scheme, just `host:port`: `otlp.nr-data.net:4317`

## CI/CD Issues

```bash
# Tool tests failing after framework changes
make check-all

# Windows-specific path failures
# → Check for /tmp vs D:\temp in test assertions
# → See: observability/provider_test.go for retry patterns

# Coverage below 80%
# → Run: make test-coverage
# → Check SonarCloud quality gate requirements
```

## Multi-Tenant Issues

```bash
# "tenant ID not found in context"
# → Use deps.DB(ctx) (function-based, resolves tenant from context)
# → Ensure tenant resolver configured in multitenant.resolver

# Messaging registry initialization errors
# → Check logs for "messaging not configured" warnings
# → Verify messaging.broker.url set for each tenant
# → See ADR-004 for lazy registry creation details
```

## Messaging Issues

```bash
# "duplicate consumer declaration detected"
# → Review module's DeclareMessaging() for loops or conditional duplicates
# → Each queue+consumer+event_type must be registered exactly once

# "duplicate module 'X' detected"
# → Ensure app.RegisterModule() called exactly once per module in main.go
# → MUST use log.Fatal(err) to handle module registration errors

# "attempt to replay different declarations for key"
# → Declaration hash mismatch indicates configuration drift
# → Review DeclareMessaging() for conditional logic or environment-specific declarations

# Handler panics crashing service (v0.16+: auto-recovered)
# → Panics are now automatically recovered with stack trace logging
# → Messages nacked without requeue (same as errors)
# → Check ERROR logs for "Panic recovered in message handler" with stack traces
# → Service continues processing other messages (no downtime)
```

**Diagnostic commands:**
```bash
grep "Starting AMQP consumers" logs/app.log
grep "Multiple consumers registered for same queue" logs/app.log
grep "Panic recovered in message handler" logs/app.log
```

## Outbox Issues

```bash
# "outbox not configured" or deps.Outbox is nil
# → Register outbox module BEFORE your application modules (its Init wires
#   OutboxPublisher into deps.Outbox; downstream modules see nil if it runs later)
# → Set outbox.enabled: true in config

# Events stuck in "pending" status
# → Check scheduler is running: GET /_sys/job (should list outbox-relay)
# → Check messaging is connected: verify messaging.broker.url
# → Manual trigger: POST /_sys/job/outbox-relay

# Duplicate events received by consumers
# → Expected behavior (at-least-once delivery)
# → Use x-outbox-event-id header for idempotency in consumer handlers

# Table creation fails
# → Set outbox.auto_create_table: false and create table manually
# → DDL provided in outbox/store_postgres.go and outbox/store_oracle.go
```

## Module Registration Issues

```bash
# "module X failed to initialize"
# → Check Init() error logs for specific dependency failures
# → Verify all required config keys present (Config.InjectInto validation)
# → Ensure database/messaging configured if module requires them

# Handler registration panics
# → Verify HandlerRegistry passed to RegisterRoutes()
# → Check for duplicate route paths (Echo will panic)
# → Ensure request struct has proper validation tags
```
