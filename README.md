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
2. [Feature Overview](#feature-overview)
3. [Quick Start](#quick-start)
4. [Configuration](#configuration)
5. [Error Handling and Diagnostics](#error-handling-and-diagnostics)
6. [Modules and Application Structure](#modules-and-application-structure)
7. [HTTP Server](#http-server)
8. [Messaging](#messaging)
9. [Database](#database)
10. [Multi-Tenant Implementation](#multi-tenant-implementation)
11. [Observability and Operations](#observability-and-operations)
12. [Examples](#examples)
13. [Contributing](#contributing)
14. [License](#license)

---

## Why GoBricks?
- **Production-ready defaults** for the boring-but-essential pieces (server, logging, configuration, tracing).
- **Composable module system** that keeps HTTP, database, and messaging concerns organized.
- **Mission-critical integrations** (PostgreSQL, Oracle, RabbitMQ, Flyway) with unified ergonomics.
- **Modern Go practices** with type safety, performance optimizations, and Go 1.18+ features.
- **Extensible design** that works with modern Go idioms and the wider ecosystem.

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
- **Go 1.25+** (required for modern type aliases, generics, and slices package)
- Modern Go toolchain with module support

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

Unified interface supporting PostgreSQL, Oracle, and MongoDB with vendor-specific optimizations.

### Type-Safe Query Builder
```go
// Type-safe WHERE methods prevent SQL errors
query := qb.Select("id", "number").
    From("accounts").
    WhereEq("number", value)  // Auto-quotes Oracle reserved words
```

**Available Methods**: `WhereEq`, `WhereNotEq`, `WhereLt/Lte/Gt/Gte`, `WhereIn/NotIn`, `WhereLike`, `WhereNull/NotNull`, `WhereBetween`, `WhereRaw`

### Database Support
- **PostgreSQL**: `$1, $2` placeholders, pgx driver, advanced features
- **Oracle**: `:1, :2` placeholders, reserved word quoting, service name/SID support
- **MongoDB**: Document operations with SQL-like interface, aggregation pipelines

### Features
- Connection pooling and health monitoring
- Flyway integration for schema migrations
- Performance tracking via OpenTelemetry

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

- **Structured logging** via Zerolog.
- **Tracing** propagates W3C `traceparent` headers.
- **Metrics** capture HTTP/messaging/database timings.
- **Health endpoints**: `/health` (liveness) and `/ready` (readiness with DB/messaging checks).
- **Graceful shutdown** coordinates servers, consumers, and background workers.

---

## Examples
Explore the [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) repository:
- `http/handlers` – typed HTTP handler module
- `http/client` – fluent HTTP client with retries and interceptors
- `oracle` – Oracle insert with reserved column quoting
- `config-injection` – custom configuration namespace demo
- `trace-propagation` – W3C tracing demonstration
- `openapi-demo` – OpenAPI specification generation
- `multitenant-aws` – multi-tenant app with AWS Secrets Manager

---

## Contributing
Issues and pull requests are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for coding standards, tooling, and workflow.

---

## License
MIT © [Contributors](LICENSE)

---

Built with ❤️ for the Go community.
