# GoBricks

Modern building blocks for Go microservices. GoBricks brings together configuration, HTTP, messaging, database, logging and observability primitives that teams need to ship production-grade services fast.

[![CI](https://github.com/gaborage/go-bricks/workflows/CI/badge.svg)](https://github.com/gaborage/go-bricks/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/gaborage/go-bricks)](https://goreportcard.com/report/github.com/gaborage/go-bricks)
[![codecov](https://codecov.io/gh/gaborage/go-bricks/branch/main/graph/badge.svg)](https://codecov.io/gh/gaborage/go-bricks)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=gaborage_go-bricks&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=gaborage_go-bricks)
[![Go Reference](https://pkg.go.dev/badge/github.com/gaborage/go-bricks.svg)](https://pkg.go.dev/github.com/gaborage/go-bricks)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

---

## Table of Contents
1. [Why GoBricks?](#why-gobricks)
2. [Feature Overview](#feature-overview)
3. [Quick Start](#quick-start)
4. [Configuration](#configuration)
5. [Modules and Application Structure](#modules-and-application-structure)
6. [HTTP Server](#http-server)
7. [Messaging](#messaging)
8. [Database](#database)
9. [Observability and Operations](#observability-and-operations)
10. [Examples](#examples)
11. [Contributing](#contributing)
12. [License](#license)

---

## Why GoBricks?
- **Production-ready defaults** for the boring-but-essential pieces (server, logging, configuration, tracing).
- **Composable module system** that keeps HTTP, database, and messaging concerns organized.
- **Mission-critical integrations** (PostgreSQL, Oracle, RabbitMQ, Flyway) with unified ergonomics.
- **Extensible design** that works with modern Go idioms and the wider ecosystem.

---

## Feature Overview
- **Modular architecture** with explicit dependencies and lifecycle hooks.
- **Echo-based HTTP server** with typed handlers, standardized response envelopes, and middleware batteries.
- **AMQP messaging registry** for RabbitMQ that orchestrates exchanges, queues, publishers, and consumers.
- **Configuration loader** built on Koanf that merges defaults, YAML, and environment variables.
- **Database toolkit** with PostgreSQL and Oracle drivers, query builders, and health checks.
- **Flyway migration integration** for schema evolution.
- **Structured logging and observability** with trace propagation, request instrumentation, and health endpoints.

---

## Quick Start

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
  rate_limit: 200

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

See [examples/params/main.go](examples/params/main.go) for a complete walkthrough.

### Overriding via environment
Environment variables override everything using uppercase and underscores:
```
APP_NAME=my-service
DATABASE_HOST=prod-db.company.com
CUSTOM_FEATURE_FLAG=true
```

---

## Modules and Application Structure

### Module interface
```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo)
    RegisterMessaging(registry *messaging.Registry)
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

`Init` is called once to capture dependencies, `RegisterRoutes` attaches HTTP handlers, `RegisterMessaging` declares AMQP infrastructure, and `Shutdown` releases resources.

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
  base_path: "/api/v1"        # All routes automatically prefixed
  health_route: "/health"     # Custom health endpoint path
  ready_route: "/ready"       # Custom readiness endpoint path
```

With this configuration:
- Module route `/users` becomes `/api/v1/users`
- Health endpoint available at `/api/v1/health`
- Ready endpoint available at `/api/v1/ready`

Environment variable overrides:
```bash
SERVER_BASE_PATH="/api/v1"
SERVER_HEALTH_ROUTE="/status"
SERVER_READY_ROUTE="/readiness"
```

---

## Messaging

AMQP support via RabbitMQ:
- Declare exchanges and queues once using the shared registry.
- Register publishers/consumers during module initialization.
- Health / readiness hooks integrate with the main app lifecycle.

```go
func (m *Module) RegisterMessaging(reg *messaging.Registry) {
    reg.DeclareExchange("user.events", "topic")
    reg.RegisterConsumer("user.events", "user.created", handler)
}
```

---

## Database

### PostgreSQL / Oracle support
Configure under `database.*` and use the provided factory to create connections. The query builder handles dialect differences (placeholders, quoting) and health checks feed into the readiness probe.

### Flyway migrations
GoBricks ships with a Flyway integration that runs migrations via CLI while injecting database credentials from configuration.
```go
migrator := migration.NewFlywayMigrator(cfg, log)
_ = migrator.Migrate(ctx, nil)
```

---

## Observability and Operations

- **Structured logging** via Zerolog.
- **Tracing** propagates W3C `traceparent` headers.
- **Metrics** capture HTTP/messaging/database timings.
- **Health endpoints**: `/health` (liveness) and `/ready` (readiness with DB/messaging checks).
- **Graceful shutdown** coordinates servers, consumers, and background workers.

---

## Examples
Explore the [`examples/`](examples/) folder:
- `http/handlers` – typed HTTP handler module
- `http/client` – fluent HTTP client with retries and interceptors
- `oracle` – Oracle insert with reserved column quoting
- `params` – custom configuration namespace demo
- `trace-propagation` – W3C tracing demonstration

---

## Contributing
Issues and pull requests are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for coding standards, tooling, and workflow.

---

## License
MIT © [Contributors](LICENSE)

---

Built with ❤️ for the Go community.
