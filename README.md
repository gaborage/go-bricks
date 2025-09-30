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
   - [Migration Guide](#configuration-migration-guide)
5. [Modules and Application Structure](#modules-and-application-structure)
6. [HTTP Server](#http-server)
7. [Messaging](#messaging)
8. [Database](#database)
9. [Multi-Tenant Implementation](#multi-tenant-implementation)
10. [Observability and Operations](#observability-and-operations)
11. [Examples](#examples)
12. [Contributing](#contributing)
13. [License](#license)

---

## Why GoBricks?
- **Production-ready defaults** for the boring-but-essential pieces (server, logging, configuration, tracing).
- **Composable module system** that keeps HTTP, database, and messaging concerns organized.
- **Mission-critical integrations** (PostgreSQL, Oracle, RabbitMQ, Flyway) with unified ergonomics.
- **Modern Go practices** with type safety, performance optimizations, and Go 1.18+ features.
- **Extensible design** that works with modern Go idioms and the wider ecosystem.

---

## Feature Overview
- **Modular architecture** with explicit dependencies and lifecycle hooks.
- **Echo-based HTTP server** with typed handlers, standardized response envelopes, and comprehensive middleware.
- **Advanced AMQP messaging** with validate-once, replay-many declaration pattern for multi-tenant isolation.
- **Configuration loader** built on Koanf that merges defaults, YAML, and environment variables with service-specific injection.
- **Multi-database support** with PostgreSQL, Oracle, and MongoDB drivers, type-safe query builders, and automatic identifier quoting.
- **Type-safe query building** with vendor-specific optimizations and Oracle reserved word handling.
- **Multi-tenant architecture** with complete resource isolation, context-driven tenant resolution, and declaration replay.
- **Flyway migration integration** for schema evolution across all database vendors.
- **Structured logging and observability** with W3C trace propagation, request instrumentation, and health endpoints.
- **Performance optimized** with modern Go type system, efficient connection pooling, and lazy resource initialization.
- **Enterprise-grade code quality** with comprehensive linting, 80%+ test coverage, and security scanning.

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

### Accessing values
Only the essential accessors are exposed:
```go
cfg, _ := config.Load()

host := cfg.GetString("server.host", "0.0.0.0")
port := cfg.GetInt("server.port", 8080)
pretty := cfg.GetBool("log.pretty")
```

### Required values with validation
```go
apiKey, err := cfg.GetRequiredString("custom.api.key")
if err != nil {
    return fmt.Errorf("missing api key: %w", err)
}
```

### Custom namespace
Custom application parameters live under `custom.*`:
```go
enabled := cfg.GetBool("custom.feature.flag")
maxItems := cfg.GetInt("custom.max.items", 100)

// Convert string durations manually
rawTimeout := cfg.GetString("custom.service.timeout")
timeout, err := time.ParseDuration(rawTimeout)

// Unmarshal structured custom config
var custom struct {
    FeatureFlag bool `koanf:"feature.flag"`
    Service struct {
        Endpoint string `koanf:"endpoint"`
        Timeout  string `koanf:"timeout"`
    } `koanf:"service"`
}
_ = cfg.Unmarshal("custom", &custom)
```

See the [configuration injection example](https://github.com/gaborage/go-bricks-demo-project/tree/main/config-injection) for a complete walkthrough.

### Configuration Migration Guide

**Breaking Change Notice**: GoBricks now uses dot notation for nested configuration structures. If you're upgrading from a previous version, update your YAML configuration files:

#### Before (Old Format):
```yaml
app:
  rate_limit: 200

server:
  base_path: "/api/v1"
  health_route: "/health"
  ready_route: "/ready"

database:
  service_name: "FREEPDB1"  # Oracle service name
```

#### After (New Format):
```yaml
app:
  rate:
    limit: 200

server:
  base:
    path: "/api/v1"
  health:
    route: "/health"
  ready:
    route: "/ready"

database:
  service:
    name: "FREEPDB1"  # Oracle service name
```

**Environment variables remain the same** - no changes needed:
- `APP_RATE_LIMIT=200` still works and maps to `app.rate.limit`
- `DATABASE_SERVICE_NAME=FREEPDB1` still works and maps to `database.service.name`

**Code changes** - Update field access in Go code:
```go
// Current (recommended)
rateLimit := cfg.App.Rate.Limit
serviceName := cfg.Database.Service.Name

// Oracle database connection configuration:
// Use exactly one of: Service.Name, SID, or Database
oracleServiceName := cfg.Database.Service.Name  // For Oracle service
oracleSID := cfg.Database.SID                   // For Oracle SID
oracleDB := cfg.Database.Database               // For database name
```

### Overriding via environment
Environment variables override everything using uppercase and underscores. The framework automatically converts underscore notation to dot notation for nested configuration:
```bash
# App configuration
APP_NAME=my-service
APP_RATE_LIMIT=200             # Maps to app.rate.limit

# Database configuration
DATABASE_TYPE=postgresql
DATABASE_HOST=prod-db.company.com
DATABASE_PORT=5432

# Oracle-specific configuration
DATABASE_SERVICE_NAME=FREEPDB1  # Maps to database.service.name
DATABASE_SID=ORCL

# Custom configuration
CUSTOM_FEATURE_FLAG=true       # Maps to custom.feature.flag
CUSTOM_API_TIMEOUT=30s         # Maps to custom.api.timeout
```

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

- Based on Echo v4 with middleware stack (logging, recovery, rate limiting, CORS).
- **Configurable base paths and health routes** for flexible deployment scenarios.
- Request binding/validation: define request structs with tags (`path`, `query`, `header`, `validate`).
- Response envelope ensures consistent `{data:…, meta:…}` payloads.
- Typed handler signatures simplify status codes:

```go
func (h *Handler) createUser(req CreateReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
    user := h.svc.Create(req)
    return server.Created(user), nil
}
```

### Flexible Routing Configuration

GoBricks supports configurable base paths and health endpoints for different deployment scenarios:

```yaml
server:
  base:
    path: "/api/v1"        # All routes automatically prefixed
  health:
    route: "/health"       # Custom health endpoint path
  ready:
    route: "/ready"        # Custom readiness endpoint path
```

With this configuration:
- Module route `/users` becomes `/api/v1/users`
- Health endpoint available at `/api/v1/health`
- Ready endpoint available at `/api/v1/ready`

Environment variable overrides:
```bash
SERVER_BASE_PATH="/api/v1"      # Maps to server.base.path
SERVER_HEALTH_ROUTE="/status"   # Maps to server.health.route
SERVER_READY_ROUTE="/readiness" # Maps to server.ready.route
```

---

## Messaging

AMQP support via RabbitMQ with a sophisticated declaration and registry system:

### Declaration Architecture
- **Validate-Once, Replay-Many Pattern**: Messaging infrastructure is declared once by modules and validated upfront, then replayed to multiple per-tenant registries for complete isolation.
- **Declarative Infrastructure**: Exchanges, queues, bindings, publishers, and consumers are declared as pure data structures before any connections are established.
- **Deep Cloning**: Declarations support deep cloning to create isolated copies for each tenant, preventing shared mutable state.
- **Built-in Validation**: The framework validates that all references between declarations are valid (e.g., bindings reference existing queues and exchanges).

### Key Features
- **Multi-Tenant Ready**: Declarations can be replayed to tenant-specific registries, enabling complete messaging isolation per tenant.
- **Type-Safe Declaration Types**: Separate declaration types for exchanges, queues, bindings, publishers, and consumers with comprehensive configuration options.
- **Automatic Reconnection**: Built-in reconnection logic with exponential backoff for resilient messaging operations.
- **Health Integration**: Messaging health checks integrate with the application lifecycle for readiness probes.
- **Context Propagation**: Tenant IDs and trace information automatically flow through messaging operations.

---

## Database

### Multi-Database Support
GoBricks provides a unified database interface supporting PostgreSQL, Oracle, and MongoDB with vendor-specific optimizations:

- **Unified Interface**: Single `database.Interface` abstraction works across all supported databases
- **Query Builder**: Fluent, type-safe query building with automatic vendor-specific SQL generation
- **Connection Management**: Built-in connection pooling, health monitoring, and performance tracking
- **Migration Support**: Flyway integration for schema evolution with database credential injection

### Type-Safe Query Builder

The framework provides an enhanced query builder with type-safe WHERE clause methods that prevent SQL errors, especially with Oracle reserved words:

**Type-Safe WHERE Methods**:
- `WhereEq(column, value)` - Equality comparison with automatic identifier quoting
- `WhereNotEq(column, value)` - Not equal comparison
- `WhereLt/WhereLte/WhereGt/WhereGte(column, value)` - Comparison operators
- `WhereIn/WhereNotIn(column, values)` - IN and NOT IN clauses
- `WhereLike(column, pattern)` - LIKE pattern matching
- `WhereNull/WhereNotNull(column)` - NULL checks
- `WhereBetween(column, min, max)` - Range comparisons
- `WhereRaw(condition, args...)` - Escape hatch for complex conditions (manual quoting required)

These methods ensure Oracle reserved words like "number", "level", and "size" are automatically quoted, preventing runtime SQL errors.

### Vendor-Specific Features

**PostgreSQL**:
- Standard `$1, $2` placeholder format
- Full support for advanced PostgreSQL features
- Optimized connection pooling with pgx driver

**Oracle**:
- Automatic `:1, :2` placeholder conversion
- Reserved word identifier quoting
- Service name and SID connection support

**MongoDB**:
- Document-based operations with SQL-like interface
- Aggregation pipeline support
- Unified interface for consistency

### Flyway Migrations
GoBricks ships with a Flyway integration that runs migrations via CLI while injecting database credentials from configuration, supporting all database vendors.

---

## Multi-Tenant Implementation

Go-Bricks provides comprehensive multi-tenant support through tenant-specific database and messaging resolution with complete resource isolation.

### Architecture Overview

Go-Bricks implements multi-tenancy through several key components:

1. **Tenant Resolution**: Extract tenant ID from requests using headers, subdomains, composite strategies, or custom logic with built-in validation support.
2. **Resource Isolation**: Each tenant gets isolated database and messaging connections with complete separation.
3. **Context Propagation**: Tenant ID flows through request context automatically across all framework components.
4. **Function-Based Injection**: Database and messaging resources are resolved via functions in `ModuleDeps`, enabling lazy per-tenant connection management.
5. **Declaration Replay**: Messaging infrastructure declarations are validated once and replayed to per-tenant registries, ensuring isolated messaging topologies.

### Configuration Setup

Configure multi-tenancy in your `config.yaml`:

```yaml
multitenant:
  enabled: true
  resolver:
    type: "header"           # or "subdomain", "composite"
    header: "X-Tenant-ID"   # header name for tenant resolution
    domain: "api.example.com" # root domain for subdomain resolution
    proxies: true           # trust X-Forwarded-Host headers
  limits:
    tenants: 100           # maximum number of tenants
  tenants:
    tenant1:
      database:
        type: "postgresql"
        host: "tenant1-db.example.com"
        port: 5432
        database: "tenant1_db"
        username: "tenant1_user"
        password: "secure_pass_1"
      messaging:
        url: "amqp://tenant1:pass@tenant1-rabbitmq.example.com:5672/"
    tenant2:
      database:
        type: "postgresql"
        host: "tenant2-db.example.com"
        port: 5432
        database: "tenant2_db"
        username: "tenant2_user"
        password: "secure_pass_2"
      messaging:
        url: "amqp://tenant2:pass@tenant2-rabbitmq.example.com:5672/"
```

### Custom Tenant Store Implementation

Implement the `app.TenantStore` interface to integrate with external systems like AWS Secrets Manager, HashiCorp Vault, or custom databases. The interface requires implementing two methods:

- `DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)` - Returns database configuration for a specific tenant
- `AMQPURL(ctx context.Context, key string) (string, error)` - Returns AMQP connection URL for a specific tenant

The framework handles connection pooling, caching, and lifecycle management automatically once the store implementation returns the configuration.

### Tenant Resolution Strategies

The framework provides multiple built-in tenant resolution strategies:

- **HeaderResolver**: Extracts tenant ID from HTTP headers (e.g., `X-Tenant-ID`)
- **SubdomainResolver**: Derives tenant ID from request host subdomains with proxy support
- **CompositeResolver**: Tries multiple resolvers sequentially until one succeeds, with optional regex validation
- **ValidatingResolver**: Wraps any resolver with tenant ID format validation using regex patterns

All resolvers support context propagation and integrate seamlessly with the middleware layer.

### Multi-Tenant Module Implementation

Modules access tenant-specific resources through function-based injection in `ModuleDeps`:

- `GetDB(ctx context.Context)` - Returns database connection for the tenant in the context
- `GetMessaging(ctx context.Context)` - Returns messaging client for the tenant in the context

The framework automatically resolves the tenant ID from the request context and provides the appropriate isolated resources. Messaging declarations made in `DeclareMessaging()` are validated once at startup and replayed to each tenant's registry, ensuring complete infrastructure isolation.

### Benefits of Go-Bricks Multi-Tenancy

1. **Complete Isolation**: Each tenant gets isolated database and messaging resources with separate connection pools
2. **Context-Driven**: Tenant ID flows automatically through request context across all framework components
3. **Extensible**: Custom resource stores can integrate with any external system (AWS, HashiCorp, custom databases)
4. **Type-Safe**: Compile-time guarantees for tenant resolution functions and resource access
5. **Performance**: Built-in caching and connection pooling for tenant resources with lazy initialization
6. **Security**: Automatic tenant validation, configurable ID constraints via regex, and isolated infrastructure
7. **Declaration Replay**: Messaging infrastructure is validated once and replayed per-tenant, preventing configuration drift

This architecture enables scalable multi-tenant applications while maintaining strict isolation between tenants and providing flexibility for custom resource management strategies. See the [multitenant-aws example](https://github.com/gaborage/go-bricks-demo-project/tree/main/multitenant-aws) for a complete implementation.

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
