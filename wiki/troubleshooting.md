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

# Linting errors (force a cold run if results look stale)
LINT_CLEAN=1 make lint
```

### Alloc guards fail after a toolchain change

The ADR-026 alloc guards (`make test-alloc`, run inside `make check`) are
tripwire guards (CONTEXT.md § Testing). A Go toolchain change can shift the
measurement uniformly (go1.26.6 → go1.27.0 was +3 on every request
path, #1177); the fix is to re-measure under the new toolchain and re-pin
the baseline constants in the bump's own PR. Locally, remember `GOTOOLCHAIN=auto`
lets a newer installed Go outrank the go.mod pin, so a guard that fails only
on your machine usually means your toolchain, not the code.

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
    gvenzl/oracle-free:23.26.2-slim
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
# → Provide a COMPLETE database section: type + host + port + username + a target
#   (database, or for Oracle oracle.service.name / oracle.service.sid). A
#   connectionstring still needs a type. A partial one now fails startup
#   (see ADR-003, ADR-047)

# /ready returns 200 with "database": "not_configured"
# → Expected for a service with NO database: block. If the service DOES need one,
#   its config never reached the process — implement app.DatabaseRequirer so this
#   aborts startup instead of going green (see ADR-047)

# /ready returns 200 with "database": "per_tenant"
# → Multi-tenant, and the fixed "" key does not resolve — no tenant database has
#   ever been probed, so /ready carries no database signal for this deployment.
#   A multi-tenant service that DOES configure a root block (a shared-ledger
#   control plane) is still probed and still 503s when that database is down

# Startup fails: config_invalid: database.type '' is not supported
# → A PARTIAL database: block. Any one of type/host/port/database/username/
#   password/connectionstring/oracle.service.name/oracle.service.sid marks the
#   section as intended, and an intended section must be complete (type + host +
#   port + username + a target). Complete it, or remove the block entirely
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
# → Verify pool size adequate: cache.redis.poolsize >= NumCPU * 2

# CacheManager eviction issues
# → Increase maxsize if seeing unexpected evictions: cache.manager.maxsize
# → Increase idlettl if caches closing too quickly: cache.manager.idlettl
# → Monitor stats: cacheManager.Stats() — check Evictions/IdleCleanups counters
```

## Observability Issues

```bash
# "cannot use OTLP logs with pretty=true"
# → Set log.pretty: false when observability.logs.enabled: true
# → Or omit log.pretty entirely and rely on log.output.format: auto (default)
#   which picks JSON automatically when OTLP logs are active.

# Local logs are not colored as expected
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

```text
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
      api-key: ${NEW_RELIC_API_KEY}  # rendered before startup — see observability_headers_auth.md
```

**HTTP endpoint format:**

- HTTP requires `https://` or `http://` scheme: `https://otlp.nr-data.net:4318/v1/traces`
- gRPC requires NO scheme, just `host:port`: `otlp.nr-data.net:4317`

## CI/CD Issues

```bash
# Windows-specific path failures
# → Check for /tmp vs D:\temp in test assertions
# → See: migration/multi_tenant_test.go, tools/migration/internal/commands/migrate_test.go for Windows path-handling patterns

# Coverage below 80%
# → Run: make test-coverage
# → Check SonarCloud quality gate requirements

# CodeQL job "Analyze (javascript-typescript)" fails: exit 32,
# "CodeQL detected code written in JavaScript/TypeScript but could not process any of it"
# → The only .js files in this repo are Claude Code workflow scripts
#   (.claude/workflows/*.js) in the Workflow-tool dialect — NOT valid
#   standalone JavaScript (top-level return) and never will be.
#   See .claude/workflows/README.md for the June 2026 incident record.
# → Fix lives in TWO places:
#   1. GitHub settings: CodeQL default setup analyzes an explicit language
#      list ["actions","go"] (set 2026-07-20). Inspect (read-only):
#      gh api repos/gaborage/go-bricks/code-scanning/default-setup
#   2. In-repo: .gitattributes marks the scripts linguist-detectable=false,
#      intended to keep language detection from re-classifying the repo as
#      containing JavaScript after a default-setup reset.
# → Do NOT re-add javascript-typescript as a remedy for THIS failure, and do
#   NOT edit the workflow scripts' syntax to appease the scanner. Re-enabling
#   it is only sensible once real, parseable JS/TS actually enters the repo
#   — see .claude/workflows/README.md for that case.

# Repo language bar / Languages API still shows "JavaScript"
# → Expected for a while after the .gitattributes change lands; GitHub
#   recomputes language stats asynchronously, so it may take one or more
#   pushes to the default branch to clear.
#   Check: gh api repos/gaborage/go-bricks/languages   # JavaScript key drops out
```

## Multi-Tenant Issues

```bash
# "tenant ID not found in context"
# → Use deps.DB(ctx) (function-based, resolves tenant from context)
# → Ensure tenant resolver configured in multitenant.resolver

# Messaging registry initialization errors
# → Fatal Init error: "messaging declarations were registered ... but
#   messaging is not configured; set messaging.broker.url"
# → Verify messaging.broker.url set for each tenant
# → See ADR-014 for the MessagingDeclarer pattern details
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
grep "Starting message consumers" logs/app.log
grep "Multiple consumers registered for same queue" logs/app.log
grep "Panic recovered in message handler" logs/app.log
```

### Decode errors read differently on a Go 1.27 build

`encoding/json` is backed by the v2 decoder from Go 1.27. It keeps v1 *acceptance*
semantics — duplicate object keys are still accepted (a struct destination merges
the two objects field by field, a map destination keeps the last value), invalid
UTF-8 is still replaced with U+FFFD, so a body that decoded before still decodes.
`encoding/json/v2` rejects duplicate names unless a caller opts back in. What does change is the error detail
a rejected body produces, so never match on a decode error's string.

`json.UnmarshalTypeError.Field` is the case that matters here: it carries the
offending **input** map key for a map destination (`limits.<key>`), and the inner
key alone for a field with its own `UnmarshalJSON`, where Go 1.26 reported the
destination field. That is why the framework's decode summary drops the field
path entirely for a payload type from which a map, an interface (including
`any`), or a `json.Unmarshaler` or `encoding.TextUnmarshaler` is reachable —
`time.Time` included — and reports only the wanted type and byte offset:

```text
messaging: decode failed for event "LimitsUpdated": json: type mismatch (want int, offset 47)
```

Which toolchain applies is **yours**, not the framework's: a service built on Go
1.26 keeps the 1.26 error text even against a GoBricks release built and tested
on 1.27.

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
# → Set outbox.autocreatetable: false and create table manually
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
# → Check for duplicate route paths — startup fails with "duplicate route
#   registration (N conflict(s))" naming both registrants (Echo itself
#   silently overwrites, see wiki/startup_defaults.md#duplicate-route-detection)
# → Ensure request struct has proper validation tags
```
