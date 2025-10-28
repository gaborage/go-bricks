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
9. [Messaging](#messaging)
10. [Database](#database)
11. [Multi-Tenant Implementation](#multi-tenant-implementation)
12. [Observability and Operations](#observability-and-operations)
13. [Examples](#examples)
14. [Contributing](#contributing)
15. [License](#license)

---

## Why GoBricks?
- **Production-ready defaults** for the boring-but-essential pieces (server, logging, configuration, tracing).
- **Composable module system** that keeps HTTP, database, and messaging concerns organized.
- **Mission-critical integrations** (PostgreSQL, Oracle, RabbitMQ, Flyway) with unified ergonomics.
- **Modern Go practices** with type safety, performance optimizations, and Go 1.18+ features.
- **Extensible design** that works with modern Go idioms and the wider ecosystem.

---

## Developer Resources

**For Contributors & Framework Developers:**
- **[CLAUDE.md](CLAUDE.md)** - Comprehensive development guide with architecture, commands, testing, and workflows
- **[llms.txt](llms.txt)** - Quick code examples optimized for LLMs and copy-paste development
- **[.specify/memory/constitution.md](.specify/memory/constitution.md)** - Project governance and non-negotiable principles

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
- **Echo-based HTTP server** with typed handlers and standardized response envelopes
- **AMQP messaging** with validate-once, replay-many pattern for multi-tenant isolation
- **Configuration loader** merging defaults, YAML, and environment variables
- **Multi-database support** for PostgreSQL, Oracle, and MongoDB with type-safe query builders
- **Multi-tenant architecture** with complete resource isolation and context propagation
- **Flyway migration integration** for schema evolution
- **Observability** with W3C trace propagation, metrics, and health endpoints
- **Enterprise-grade quality** with comprehensive linting, 80%+ test coverage, and security scanning

---

## Quick Start

### Requirements
- **Go 1.24 or 1.25** (CI tests both versions)
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

    // Register modules here: framework.RegisterModule(newExampleModule())

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
host := cfg.GetString("server.host", "0.0.0.0")
port := cfg.GetInt("server.port", 8080)

// Required values with validation
apiKey, err := cfg.GetRequiredString("custom.api.key")
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
    RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo)
    DeclareMessaging(decls *messaging.Declarations)
    Shutdown() error
}
```

`ModuleDeps` injects shared services:
```go
type ModuleDeps struct {
    DB        database.Interface
    Logger    logger.Logger
    Messaging messaging.Client
    Config    *config.Config
}
```

### Registering a module
```go
func register(framework *app.App) error {
    return framework.RegisterModule(&users.Module{})
}
```

`Init` is called once to capture dependencies, `RegisterRoutes` attaches HTTP handlers, `DeclareMessaging` declares AMQP infrastructure (validated once, replayed per-tenant), and `Shutdown` releases resources. The framework ensures proper lifecycle ordering and error handling across all module hooks.

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

Request structs use tags for binding/validation (`path`, `query`, `header`, `validate`). Responses follow consistent `{data:…, meta:…}` envelope structure.

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

## Messaging

AMQP/RabbitMQ support with validate-once, replay-many declaration pattern:

- **Declarative Infrastructure**: Exchanges, queues, bindings, publishers, and consumers declared as data structures, validated upfront
- **Multi-Tenant Isolation**: Declarations replayed to tenant-specific registries for complete separation
- **Auto-Reconnection**: Exponential backoff for resilient operations
- **Context Propagation**: Tenant IDs and trace information flow automatically through messaging

---

## Database

Unified interface supporting PostgreSQL, Oracle, and MongoDB with vendor-specific optimizations and type-safe query building across all operations.

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

**JoinFilter Methods**: `EqColumn`, `NotEqColumn`, `LtColumn`, `LteColumn`, `GtColumn`, `GteColumn`, `And`, `Or`, `Raw`

All methods automatically handle:
- **Vendor-specific quoting** (Oracle reserved words like `NUMBER`, `DATE`)
- **Placeholder formatting** (`$1` for PostgreSQL, `:1` for Oracle)
- **Type safety** at compile time with refactor-friendly interfaces

### Database Support
- **PostgreSQL**: `$1, $2` placeholders, pgx driver, advanced features
- **Oracle**: `:1, :2` placeholders, reserved word quoting, service name/SID support
- **MongoDB**: Document operations with SQL-like interface, aggregation pipelines

### Features
- Connection pooling and health monitoring
- Flyway integration for schema migrations
- Performance tracking via OpenTelemetry
- Type-safe query builders prevent runtime SQL errors

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

Modules access tenant resources via `ModuleDeps.GetDB(ctx)` and `ModuleDeps.GetMessaging(ctx)`.

### Custom Integration
Implement `app.TenantStore` to integrate with AWS Secrets Manager, HashiCorp Vault, or custom backends.

See [MULTI_TENANT.md](MULTI_TENANT.md) for detailed architecture and [multitenant-aws example](https://github.com/gaborage/go-bricks-demo-project/tree/main/multitenant-aws) for implementation patterns.

---

## Observability and Operations

- **OpenTelemetry first**: native traces, metrics, and dual-mode logs.
- **Structured logging** via Zerolog with OTLP export for action + trace streams.
- **Tracing** propagates W3C `traceparent` headers.
- **Metrics** capture HTTP/messaging/database timings.
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

   - All application logs default to `log.type="trace"` and only WARN+ severities are exported (≈95 % volume reduction).
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
- HTTP handlers (POST, GET, PUT, DELETE)
- Database queries (SELECT, UPDATE, DELETE, JOINs, subqueries)
- Messaging (AMQP declarations, publishers, consumers)
- Multi-tenant configuration
- Observability setup
- Configuration injection

---

## Contributing

Issues and pull requests are welcome!

**Getting Started:**
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Coding standards, tooling, and workflow
- **[CLAUDE.md](CLAUDE.md)** - Comprehensive development guide with architecture details, commands, and testing guidelines
- **[.specify/memory/constitution.md](.specify/memory/constitution.md)** - Project governance and non-negotiable principles (80% test coverage, security-first, etc.)

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
