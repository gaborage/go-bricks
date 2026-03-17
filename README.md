# GoBricks

Modern building blocks for Go microservices. GoBricks brings together configuration, HTTP, messaging, database, logging and observability primitives that teams need to ship production-grade services fast.

[![CI](https://github.com/gaborage/go-bricks/workflows/CI/badge.svg)](https://github.com/gaborage/go-bricks/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/gaborage/go-bricks)](https://goreportcard.com/report/github.com/gaborage/go-bricks)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=gaborage_go-bricks&metric=coverage)](https://sonarcloud.io/summary/new_code?id=gaborage_go-bricks)
[![Reliability Rating](https://sonarcloud.io/api/project_badges/measure?project=gaborage_go-bricks&metric=reliability_rating)](https://sonarcloud.io/summary/new_code?id=gaborage_go-bricks)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=gaborage_go-bricks&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=gaborage_go-bricks)
[![Maintainability Rating](https://sonarcloud.io/api/project_badges/measure?project=gaborage_go-bricks&metric=sqale_rating)](https://sonarcloud.io/summary/new_code?id=gaborage_go-bricks)
[![Go Reference](https://pkg.go.dev/badge/github.com/gaborage/go-bricks.svg)](https://pkg.go.dev/github.com/gaborage/go-bricks)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

---

## Table of Contents
1. [Why GoBricks?](#why-gobricks)
2. [Developer Resources](#developer-resources)
3. [Feature Overview](#feature-overview)
4. [Quick Start](#quick-start)
5. [Configuration](#configuration)
6. [Error Handling and Diagnostics](#error-handling-and-diagnostics)
7. [Modules and Application Structure](#modules-and-application-structure)
8. [HTTP Server](#http-server)
9. [HTTP Client](#http-client)
10. [Messaging](#messaging)
11. [Database](#database)
12. [Cache](#cache)
13. [Scheduler and Outbox](#scheduler-and-outbox)
14. [KeyStore](#keystore)
15. [Multi-Tenant Implementation](#multi-tenant-implementation)
16. [Observability and Operations](#observability-and-operations)
17. [Examples](#examples)
18. [Contributing](#contributing)
19. [License](#license)

---

## Why GoBricks?
- **Production-ready defaults** for the boring-but-essential pieces (server, logging, configuration, tracing).
- **Composable module system** that keeps HTTP, database, and messaging concerns organized.
- **Mission-critical integrations** (PostgreSQL, Oracle, RabbitMQ, Flyway) with unified ergonomics.
- **Modern Go practices** with type safety, performance optimizations, and Go 1.25 features.
- **Extensible design** that works with modern Go idioms and the wider ecosystem.

---

## Developer Resources

**For Contributors & Framework Developers:**
- **[CLAUDE.md](CLAUDE.md)** - Comprehensive development guide with architecture, commands, testing, and workflows
- **[llms.txt](llms.txt)** - Quick code examples optimized for LLMs and copy-paste development
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Coding standards, tooling, and contribution workflow

**For Application Developers:**
- **[go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project)** - Complete working examples and patterns
- **[MULTI_TENANT.md](MULTI_TENANT.md)** - Multi-tenant architecture and implementation guide
- **[Go Reference](https://pkg.go.dev/github.com/gaborage/go-bricks)** - Complete API documentation

**Quick Commands:**
```bash
make check              # Pre-commit: fmt + lint + test
make test-integration   # Integration tests (requires Docker)
go test -run TestName   # Run specific test
```

---

## Feature Overview
- **Modular architecture** with explicit dependencies and lifecycle hooks
- **Echo-based HTTP server** with typed handlers, standardized response envelopes, and raw response mode for legacy API migration
- **Production-ready HTTP client** with retries, exponential backoff, W3C trace propagation, and interceptor chains
- **AMQP messaging** with validate-once, replay-many pattern, auto-scaling consumer concurrency, and panic recovery
- **Configuration loader** merging defaults, YAML, and environment variables with struct-based injection (`config:` tags)
- **Multi-database support** for PostgreSQL and Oracle with struct-based column extraction and type-safe query builders
- **Named databases** for accessing multiple databases (including mixed vendors) in single-tenant mode
- **Redis cache integration** with type-safe serialization, multi-tenant isolation, and automatic lifecycle management
- **Transactional outbox** for reliable at-least-once event publishing (dual-write problem solved)
- **Job scheduler** with gocron, overlapping prevention, panic recovery, and system APIs
- **Named RSA key pair management** from DER files or base64 environment variables
- **Multi-tenant architecture** with complete resource isolation and context propagation
- **Flyway migration integration** for schema evolution
- **Observability** with W3C trace propagation, custom metrics, dual-mode logs, and health endpoints
- **Enterprise-grade quality** with comprehensive linting, 80%+ test coverage, and security scanning

---

## Quick Start

### Requirements
- **Go 1.25** required
- Modern Go toolchain with module support
- Docker Desktop or Docker Engine (integration tests only)

### Install
```bash
go mod init your-service
go get github.com/gaborage/go-bricks@latest
```

### Bootstrap an application
```go
// cmd/main.go
package main

import (
    "log"

    "github.com/gaborage/go-bricks/app"
    "github.com/gaborage/go-bricks/config"
)

func main() {
    cfg, err := config.Load()
    if err != nil {
        log.Fatal(err)
    }

    framework, err := app.NewWithConfig(cfg, nil)
    if err != nil {
        log.Fatal(err)
    }

    // Register modules here
    if err := framework.RegisterModule(&users.Module{}); err != nil {
        log.Fatal(err)
    }

    if err := framework.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### Configuration file
```yaml
app:
  name: "my-service"
  version: "v1.0.0"
  env: "development"
  rate:
    limit: 200

server:
  port: 8080

database:
  type: postgresql
  host: localhost
  port: 5432
  database: mydb
  username: postgres
  password: password

cache:
  enabled: true
  type: redis
  redis:
    host: localhost
    port: 6379
    database: 0
    pool_size: 10

log:
  level: info
  pretty: true
```

GoBricks loads defaults → `config.yaml` → `config.<env>.yaml` → environment variables. `app.env` controls the environment suffix and defaults to `development`.

---

## Configuration

GoBricks uses Koanf for configuration management with layered loading: defaults → `config.yaml` → `config.<env>.yaml` → environment variables.

### Access Patterns
```go
cfg, _ := config.Load()

// Simple values with defaults
host := cfg.String("server.host", "0.0.0.0")
port := cfg.Int("server.port", 8080)

// Required values with validation
apiKey, err := cfg.RequiredString("custom.api.key")
if err != nil {
    return fmt.Errorf("missing api key: %w", err)
}

// Structured configuration under custom.*
var custom struct {
    FeatureFlag bool   `koanf:"feature.flag"`
    Endpoint    string `koanf:"api.endpoint"`
}
_ = cfg.Unmarshal("custom", &custom)
```

### Config Injection

Inject configuration directly into structs using `config:` tags with automatic validation:

```go
type ServiceConfig struct {
    APIKey   string        `config:"custom.api.key" required:"true"`
    Timeout  time.Duration `config:"custom.api.timeout" default:"30s"`
    Retries  int           `config:"custom.api.retries" default:"3"`
}

func (m *Module) Init(deps *ModuleDeps) error {
    var cfg ServiceConfig
    if err := deps.Config.InjectInto(&cfg); err != nil {
        return err // Fails fast if required keys are missing
    }
    m.service = NewService(cfg)
    return nil
}
```

**Supported tags:** `config:"key.path"` (required), `required:"true"` (fail if missing), `default:"value"` (fallback).
**Supported types:** string, int, int64, float64, bool, time.Duration.

### Environment Variables
Environment variables use uppercase with underscores and automatically map to dot notation:
```bash
DATABASE_HOST=prod-db.company.com   # maps to database.host
DATABASE_SERVICE_NAME=FREEPDB1      # maps to database.service.name
CUSTOM_API_TIMEOUT=30s              # maps to custom.api.timeout
```

See the [config-injection example](https://github.com/gaborage/go-bricks-demo-project/tree/main/config-injection) for advanced patterns.

---

## Error Handling and Diagnostics

Clear, actionable error messages for configuration and startup failures.

### Error Format
```text
config_<category>: <field> <message> <action>
```

**Categories**: `missing` (required field), `invalid` (bad value), `not_configured` (optional feature), `connection` (resource failure)

### Examples
```text
config_missing: database.host required set DATABASE_HOST env var or add database.host to config.yaml

config_invalid: database.port invalid port; must be between 1 and 65535 must be one of: 1-65535

config_not_configured: messaging.broker.url (optional) to enable: set MESSAGING_BROKER_URL env var or add messaging.broker.url to config.yaml
```

### Features
- Dual paths: shows both env var and YAML path
- Semantic distinction: differentiates missing, invalid, and optional
- Multi-tenant context: includes tenant ID when applicable
- No error chains: single clear message per issue

---

## Modules and Application Structure

### Module interface
```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    Shutdown() error
}

// Optional — implement to register HTTP routes (detected via duck-typing at startup)
type RouteRegisterer interface {
    RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
}

// Optional — implement to declare AMQP exchanges, queues, and consumers
type MessagingDeclarer interface {
    DeclareMessaging(decls *messaging.Declarations)
}
```

`ModuleDeps` injects shared services into every module:
```go
type ModuleDeps struct {
    Logger        logger.Logger                                          // Structured logging
    Config        *config.Config                                         // Configuration access
    Tracer        trace.Tracer                                           // Distributed tracing (no-op if disabled)
    MeterProvider metric.MeterProvider                                   // Custom metrics (no-op if disabled)
    Scheduler     JobRegistrar                                           // Job scheduling (nil if no scheduler module)
    Outbox        OutboxPublisher                                        // Transactional event publishing (nil if disabled)
    KeyStore      KeyStore                                               // Named RSA key pairs (nil if not configured)
    DB            func(ctx context.Context) (database.Interface, error)  // Tenant-aware database
    DBByName      func(ctx context.Context, name string) (database.Interface, error) // Named databases
    Messaging     func(ctx context.Context) (messaging.AMQPClient, error) // Tenant-aware messaging
    Cache         func(ctx context.Context) (cache.Cache, error)         // Tenant-aware cache
}
```

### Registering a module
```go
func register(framework *app.App) error {
    return framework.RegisterModule(&users.Module{})
}
```

`Init` is called once to capture dependencies and `Shutdown` releases resources. Route registration and messaging are opt-in: if your module implements `RouteRegisterer`, the framework calls `RegisterRoutes` to attach HTTP handlers; if it implements `MessagingDeclarer`, `DeclareMessaging` is called to declare AMQP infrastructure (validated once, replayed per-tenant). Modules that don't need HTTP or AMQP simply omit the interface. The framework ensures proper lifecycle ordering and error handling across all module hooks.

---

## HTTP Server

Echo v4-based server with typed handlers, standardized response envelopes, and comprehensive middleware (logging, recovery, rate limiting, CORS).

### Typed Handlers
```go
func (h *Handler) createUser(req CreateReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
    user := h.svc.Create(req)
    return server.Created(user), nil
}
```

Request structs use tags for binding/validation (`path`, `query`, `header`, `validate`). Responses follow consistent `{data:…, meta:…}` envelope structure. Use `server.WithRawResponse()` to bypass the envelope for legacy API migration (Strangler Fig pattern).

### Routing Configuration
```yaml
server:
  base:
    path: "/api/v1"    # All routes prefixed
  health:
    route: "/health"   # Liveness endpoint
  ready:
    route: "/ready"    # Readiness endpoint
```

Override with environment variables: `SERVER_BASE_PATH`, `SERVER_HEALTH_ROUTE`, `SERVER_READY_ROUTE`.

---

## HTTP Client

Production-ready HTTP client with built-in observability and resilience.

```go
client := httpclient.NewBuilder(logger).
    WithTimeout(10 * time.Second).
    WithRetries(3, 500 * time.Millisecond).
    WithDefaultHeader("Accept", "application/json").
    WithW3CTrace(true).
    Build()

resp, err := client.Get(ctx, &httpclient.Request{
    URL: "https://api.example.com/users",
})
```

**Features:** Builder pattern, W3C trace propagation, exponential backoff with full jitter, request/response interceptor chains, structured logging. Methods: `Get`, `Post`, `Put`, `Patch`, `Delete`, `Do`.

See [llms.txt](llms.txt) for more examples and [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) for working demos.

---

## Messaging

AMQP/RabbitMQ support with validate-once, replay-many declaration pattern:

- **Declarative Infrastructure**: Exchanges, queues, bindings, publishers, and consumers declared as data structures, validated upfront
- **Multi-Tenant Isolation**: Declarations replayed to tenant-specific registries for complete separation
- **Auto-Reconnection**: Exponential backoff for resilient operations
- **Context Propagation**: Tenant IDs and trace information flow automatically through messaging
- **Consumer Concurrency**: Auto-scaling workers (`NumCPU * 4`) with configurable overrides and resource safeguards

---

## Database

Unified interface supporting PostgreSQL and Oracle with vendor-specific optimizations and type-safe query building across all operations.

### Type-Safe Filter API

GoBricks provides a composable Filter API that works across SELECT, UPDATE, DELETE, and JOIN operations:

```go
// Build reusable filters
f := qb.Filter()

// SELECT with filters
users := qb.Select("id", "name", "email").
    From("users").
    Where(f.Eq("status", "active")).
    Where(f.Gt("created_at", startDate))

// UPDATE with filters
qb.Update("users").
    Set("status", "inactive").
    Where(f.Lt("last_login", cutoffDate))

// DELETE with filters
qb.Delete("users").
    Where(f.Eq("status", "deleted")).
    Where(f.Lt("deleted_at", retentionDate))

// JOIN with type-safe column comparisons
jf := qb.JoinFilter()
query := qb.Select("u.name", "p.bio").
    From("users u").
    LeftJoinOn("profiles p", jf.EqColumn("u.id", "p.user_id")).
    Where(f.NotNull("p.bio"))
```

**Filter Methods**: `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `In`, `NotIn`, `Like`, `Null`, `NotNull`, `Between`, `And`, `Or`, `Not`, `Raw`

**JoinFilter Methods**: `EqColumn`, `NotEqColumn`, `LtColumn`, `LteColumn`, `GtColumn`, `GteColumn`, `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `In`, `NotIn`, `Between`, `Like`, `Null`, `NotNull`, `And`, `Or`, `Raw`

All methods automatically handle:
- **Vendor-specific quoting** (Oracle reserved words like `NUMBER`, `DATE`)
- **Placeholder formatting** (`$1` for PostgreSQL, `:1` for Oracle)
- **Type safety** at compile time with refactor-friendly interfaces

### Struct-Based Column Extraction

Eliminate column repetition using `db:"column_name"` struct tags. Columns are extracted once via reflection and cached forever (~26ns access):

```go
type User struct {
    ID    int64  `db:"id"`
    Name  string `db:"name"`
    Level int    `db:"level"` // Oracle reserved word — auto-quoted
}

cols := qb.Columns(&User{})
f := qb.Filter()

query := qb.Select(cols.Fields("ID", "Name")...).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT "ID", "NAME" FROM users WHERE "LEVEL" = :1
// PostgreSQL: SELECT id, name FROM users WHERE level = $1
```

**Benefits:** DRY (define columns once), type-safe (panics on typos), vendor-aware (auto-quotes Oracle reserved words), refactor-friendly, zero overhead after first use. See [CLAUDE.md](CLAUDE.md) for table aliases, INSERT patterns, and advanced examples.

### Database Support
- **PostgreSQL**: `$1, $2` placeholders, pgx driver, advanced features
- **Oracle**: `:1, :2` placeholders, reserved word quoting, service name/SID support, SEQUENCE objects, UDT registration

### Features
- Connection pooling with production-safe defaults and health monitoring
- Named databases for multi-database single-tenant setups (`deps.DBByName(ctx, "legacy")`)
- Flyway integration for schema migrations
- Performance tracking via OpenTelemetry
- Type-safe query builders prevent runtime SQL errors

---

## Cache

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management through the CacheManager.

### Quick Example

```go
import (
    "context"
    "time"

    "github.com/gaborage/go-bricks/cache"
)

type UserService struct {
    getCache func(context.Context) (cache.Cache, error)
}

func (s *UserService) GetUser(ctx context.Context, id int64) (*User, error) {
    // GetCache resolves tenant from context automatically
    cache, err := s.getCache(ctx)
    if err != nil {
        return nil, err
    }

    cacheKey := fmt.Sprintf("user:%d", id)

    // Try cache first
    data, err := cache.Get(ctx, cacheKey)
    if err == nil {
        return cache.Unmarshal[User](data)
    }

    // Cache miss - query database
    user, err := s.queryDatabase(ctx, id)
    if err != nil {
        return nil, err
    }

    // Store in cache with TTL
    data, _ = cache.Marshal(user)
    cache.Set(ctx, cacheKey, data, 5*time.Minute)

    return user, nil
}
```

### Key Features
- **Type-Safe Serialization**: CBOR encoding with compile-time type safety
- **Multi-Tenant Isolation**: Automatic tenant context resolution
- **Lifecycle Management**: Lazy initialization, LRU eviction (max 100 tenants), idle cleanup (15m default)
- **Atomic Operations**: `GetOrSet` for deduplication, `CompareAndSet` for distributed locking
- **Performance**: <1ms latency for Get/Set, 100k ops/sec throughput
- **Observability**: OpenTelemetry spans, metrics, and health checks

### Operations

| Operation | Method | Use Case |
|-----------|--------|----------|
| **Basic read** | `Get(ctx, key)` | Query result caching |
| **Basic write** | `Set(ctx, key, value, ttl)` | Store computed data with TTL |
| **Deduplication** | `GetOrSet(ctx, key, value, ttl)` | Idempotency keys, atomic SET NX |
| **Distributed lock** | `CompareAndSet(ctx, key, expected, new, ttl)` | Cross-pod job coordination |
| **Type-safe store** | `Marshal(v)` + `Set()` | Struct serialization (CBOR) |
| **Type-safe retrieve** | `Get()` + `Unmarshal[T](data)` | Struct deserialization |

**For comprehensive examples**, see the Cache Operations section in [llms.txt](llms.txt).

### Testing with Mock Cache

GoBricks provides `cache/testing` package for unit tests without Redis dependencies:

```go
import cachetest "github.com/gaborage/go-bricks/cache/testing"

func TestMyService(t *testing.T) {
    mockCache := cachetest.NewMockCache()

    // Configure mock behavior
    mockCache.WithGetFailure(cache.ErrNotFound)

    // Inject into service
    deps := &app.ModuleDeps{
        Cache: func(ctx context.Context) (cache.Cache, error) {
            return mockCache, nil
        },
    }
    svc := NewService(deps)

    // Run tests
    result, err := svc.GetUser(ctx, 123)

    // Assert cache operations
    cachetest.AssertOperationCount(t, mockCache, "Get", 1)
}
```

**Features:** Fluent configuration API, operation tracking, 20+ assertion helpers, TTL expiration testing, multi-tenant support.

**See also:** [database/testing](https://pkg.go.dev/github.com/gaborage/go-bricks/database/testing) for similar patterns.

---

## Scheduler and Outbox

### Job Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention (mutex per job), automatic panic recovery with stack trace logging, and system APIs (`GET /_sys/jobs`, `POST /_sys/job/:jobId`).

```go
func (m *Module) Init(deps *app.ModuleDeps) error {
    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
}

// Implement the Executor interface
type CleanupJob struct{}
func (j *CleanupJob) Execute(ctx scheduler.JobContext) error {
    // ctx provides: JobID(), TriggerType(), Logger(), DB(), Messaging(), Config()
    return nil
}
```

**Schedule types:** `Every(duration)`, `Cron(expression)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`.

### Transactional Outbox

Solves the dual-write problem: events are written to an outbox table in the **same database transaction** as business data, then reliably delivered to the message broker by a background relay. Delivery guarantee: **at-least-once** (consumers must be idempotent).

```go
func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderReq) error {
    db, _ := s.getDB(ctx)
    tx, _ := db.Begin(ctx)
    defer tx.Rollback(ctx)

    // 1. Write business data
    _, err := tx.Exec(ctx, "INSERT INTO orders (id, customer_id) VALUES ($1, $2)",
        req.ID, req.CustomerID)
    if err != nil { return err }

    // 2. Write event to outbox (SAME transaction — atomic!)
    payload, _ := json.Marshal(OrderCreatedEvent{OrderID: req.ID})
    _, err = s.outbox.Publish(ctx, tx, &app.OutboxEvent{
        EventType:   "order.created",
        AggregateID: fmt.Sprintf("order-%d", req.ID),
        Payload:     payload,
        Exchange:    "order.events",
    })
    if err != nil { return err }

    return tx.Commit(ctx) // Event GUARANTEED to reach the broker eventually
}
```

The relay job polls for pending events every `poll_interval` (default 5s), publishes them to AMQP with an `x-outbox-event-id` header for deduplication, and a cleanup job removes published events after `retention_period` (default 72h). See [CLAUDE.md](CLAUDE.md) for full configuration options.

---

## KeyStore

Named RSA key pair management for encryption and signing. Keys are loaded at startup from DER files or base64-encoded environment variables.

```go
func (m *Module) Init(deps *app.ModuleDeps) error {
    // Sign a JWT
    privateKey, err := deps.KeyStore.PrivateKey("signing")
    if err != nil { return err }

    // Verify a signature
    publicKey, err := deps.KeyStore.PublicKey("signing")
    if err != nil { return err }

    return nil
}
```

**Configuration:**
```yaml
keystore:
  keys:
    signing:
      private_key_file: /secrets/signing.der     # DER file path
      public_key_file: /secrets/signing_pub.der
    encryption:
      private_key: ${ENCRYPTION_PRIVATE_KEY}      # Base64-encoded DER
      public_key: ${ENCRYPTION_PUBLIC_KEY}
```

See [keystore/](keystore/) package for full API documentation.

---

## Multi-Tenant Implementation

Complete resource isolation with per-tenant database and messaging connections.

### Key Features
- **Tenant Resolution**: Headers, subdomains, or custom strategies with validation
- **Resource Isolation**: Separate database/messaging connections per tenant
- **Context Propagation**: Tenant ID flows automatically through all operations
- **Declaration Replay**: Messaging infrastructure validated once, replayed per-tenant

### Quick Setup
```yaml
multitenant:
  enabled: true
  resolver:
    type: "header"
    header: "X-Tenant-ID"
  tenants:
    tenant1:
      database: { ... }
      messaging: { url: "..." }
```

Modules access tenant resources via `deps.DB(ctx)` and `deps.Messaging(ctx)`.

### Custom Integration
Implement `app.TenantStore` to integrate with AWS Secrets Manager, HashiCorp Vault, or custom backends.

See [MULTI_TENANT.md](MULTI_TENANT.md) for detailed architecture and [multitenant-aws example](https://github.com/gaborage/go-bricks-demo-project/tree/main/multitenant-aws) for implementation patterns.

---

## Observability and Operations

- **OpenTelemetry first**: native traces, metrics, and dual-mode logs.
- **Structured logging** via Zerolog with OTLP export for action + trace streams.
- **Tracing** propagates W3C `traceparent` headers.
- **Metrics** capture HTTP/messaging/database timings plus custom application metrics via `deps.MeterProvider`.
- **Health endpoints**: `/health` (liveness) and `/ready` (readiness with DB/messaging checks).
- **Graceful shutdown** coordinates servers, consumers, and background workers.

### Configuring OpenTelemetry (Traces + Dual-Mode Logs)

1. **Enable observability in configuration**

   ```yaml
   observability:
     enabled: true
     service:
       name: checkout-api
       version: 1.2.3
       environment: production
     trace:
       enabled: true
       endpoint: otel-collector:4317
       protocol: grpc        # http for OTLP/HTTP
       insecure: true        # disable TLS when talking to a collector inside the VPC
       export:
         timeout: 5s
     logs:
       enabled: true
       endpoint: otel-collector:4317
       protocol: grpc
       insecure: true
       slow_request_threshold: 750ms   # requests slower than this become WARN result_code
       export:
         timeout: 5s
       batch:
         timeout: 3s
       max:
         queue:
           size: 2048
         batch:
           size: 512
   ```

   - All application logs default to `log.type="trace"` and only WARN+ severities are exported (≈95 % volume reduction).
   - Middleware writes synthesized request summaries with `log.type="action"` for every healthy request; they keep INFO-level retention.

2. **Initialize the OpenTelemetry provider and hook the logger**

   ```go
   import (
       "github.com/gaborage/go-bricks/logger"
       "github.com/gaborage/go-bricks/observability"
       "github.com/gaborage/go-bricks/server"
   )

   func wireLogging(cfg *config.Config, e *echo.Echo) (observability.Provider, logger.Logger, error) {
       obsProvider, err := observability.NewProvider(&cfg.Observability)
       if err != nil {
           return nil, nil, err
       }

       appLogger := logger.New(cfg.Log.Level, cfg.Log.Pretty).
           WithOTelProvider(obsProvider)

       e.Use(server.LoggerWithConfig(appLogger, server.LoggerConfig{
           HealthPath:           "/health",
           ReadyPath:            "/ready",
           SlowRequestThreshold: cfg.Observability.Logs.SlowRequestThreshold,
       }))

       return obsProvider, appLogger, nil
   }
   ```

   - The bridge automatically enriches Zerolog output with `trace_id`, `span_id`, and `log.type`.
   - `server.LoggerWithConfig` emits action logs and registers the WARN/ERROR hook so requests with elevated severity stay in the trace stream.

3. **Route the two log classes in your backend**

   - **Grafana Loki**: `logql` query `{log.type="action"}` for request summaries, `{log.type="trace", trace_id="abc"}` for correlated traces.
   - **Datadog**: create indexes `@log.type:action` (long retention) and `@log.type:trace` (short retention) to control costs.

4. **Local development tip**: set `observability.trace.endpoint=stdout` and `observability.logs.endpoint=stdout` to pretty-print spans and logs without running a collector.

---

## Examples

### Complete Working Examples
Explore the [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) repository:
- `http/handlers` – typed HTTP handler module
- `http/client` – fluent HTTP client with retries and interceptors
- `oracle` – Oracle insert with reserved column quoting
- `config-injection` – custom configuration namespace demo
- `trace-propagation` – W3C tracing demonstration
- `openapi-demo` – OpenAPI specification generation
- `multitenant-aws` – multi-tenant app with AWS Secrets Manager

### Quick Code Reference
See **[llms.txt](llms.txt)** for copy-paste-ready code snippets covering:
- Module system patterns
- HTTP handlers (POST, GET, PUT, DELETE) and raw response mode
- HTTP client with retries, interceptors, and W3C traces
- Database queries (SELECT, UPDATE, DELETE, JOINs, subqueries)
- Struct-based column extraction and named databases
- Messaging (AMQP declarations, publishers, consumers)
- Transactional outbox and job scheduler
- Configuration injection with struct tags
- Custom errors and middleware
- KeyStore for RSA key management
- Multi-tenant configuration
- Observability setup and custom metrics

---

## Contributing

Issues and pull requests are welcome!

**Getting Started:**
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Coding standards, tooling, and workflow
- **[CLAUDE.md](CLAUDE.md)** - Comprehensive development guide with architecture details, commands, and testing guidelines

**Quick Setup:**
```bash
go test ./...              # Run unit tests
make check                 # Pre-commit checks (fmt, lint, test)
make test-integration      # Integration tests (Docker required)
```

---

## License
MIT © [Contributors](LICENSE)

---

Built with ❤️ for the Go community.
