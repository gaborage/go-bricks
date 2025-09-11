# GoBricks

[![CI](https://github.com/gaborage/go-bricks/workflows/CI/badge.svg)](https://github.com/gaborage/go-bricks/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/gaborage/go-bricks)](https://goreportcard.com/report/github.com/gaborage/go-bricks)
[![codecov](https://codecov.io/gh/gaborage/go-bricks/branch/main/graph/badge.svg)](https://codecov.io/gh/gaborage/go-bricks)
[![Go Reference](https://pkg.go.dev/badge/github.com/gaborage/go-bricks.svg)](https://pkg.go.dev/github.com/gaborage/go-bricks)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Enterprise-grade building blocks for Go microservices**

GoBricks is an open-source, enterprise-grade Go framework for building robust microservices with modular, reusable components. It provides a complete foundation for developing production-ready applications with built-in support for HTTP servers, AMQP messaging, multi-database connectivity, and clean architecture patterns.

## 🚀 Features

- **🏗️ Modular Architecture**: Component-based design with dependency injection
- **🌐 HTTP Server**: Echo-based server with comprehensive middleware ecosystem
- **🗄️ Multi-Database Support**: PostgreSQL and Oracle with unified interface
- **📨 AMQP Messaging**: RabbitMQ integration with automatic reconnection
- **⚙️ Configuration Management**: Koanf-based config with multiple sources (YAML, env vars)
- **📊 Built-in Observability**: Request tracking, performance metrics, structured logging
- **🔧 Database Migrations**: Flyway integration for schema management
- **🧩 Plugin System**: Easy module registration and lifecycle management
- **🚀 Production Ready**: Used in enterprise environments

## 📦 Installation

```bash
go mod init your-service
go get github.com/gaborage/go-bricks@latest
```

## 🏃 Quick Start

### 1. Create Your Application

```go
// cmd/main.go
package main

import (
    "log"
    "github.com/gaborage/go-bricks/app"
)

func main() {
    framework, err := app.New()
    if err != nil {
        log.Fatal(err)
    }
    
    // Register your modules here
    
    if err := framework.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### 2. Create Configuration

```yaml
# config.yaml
app:
  name: "my-service"
  version: "v1.0.0"
  env: "development"

server:
  port: 8080

database:
  type: "postgresql"
  host: "localhost"
  port: 5432
  database: "mydb"
  username: "postgres"
  password: "password"

log:
  level: "info"
  pretty: true
```

### 3. Create a Module

```go
// internal/modules/example/module.go
package example

import (
    "net/http"
    "github.com/labstack/echo/v4"
    "github.com/gaborage/go-bricks/app"
)

type Module struct {
    deps *app.ModuleDeps
}

func (m *Module) Name() string {
    return "example"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    m.deps = deps
    return nil
}

func (m *Module) RegisterRoutes(e *echo.Echo) error {
    e.GET("/hello", m.hello)
    return nil
}

func (m *Module) Shutdown() error {
    return nil
}

func (m *Module) hello(c echo.Context) error {
    return c.JSON(http.StatusOK, map[string]string{
        "message": "Hello from GoBricks!",
    })
}
```

### 4. Register and Run

```go
// cmd/main.go (updated)
func main() {
    framework, err := app.New()
    if err != nil {
        log.Fatal(err)
    }
    
    // Register modules
    framework.RegisterModule(&example.Module{})
    
    if err := framework.Run(); err != nil {
        log.Fatal(err)
    }
}
```

## 🏗️ Architecture

GoBricks follows a modular architecture where each business domain is implemented as a module:

```
┌─────────────────────────────────────────┐
│               GoBricks App              │
├─────────────────────────────────────────┤
│  Module A  │  Module B  │  Module C     │
├─────────────────────────────────────────┤
│  HTTP Server │ Database │ Messaging     │
├─────────────────────────────────────────┤
│  Logger │ Config │ Migrations │ Utils   │
└─────────────────────────────────────────┘
```

### Module System

Each module implements the `Module` interface:

```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    RegisterRoutes(e *echo.Echo) error
    Shutdown() error
}
```

Modules have access to shared dependencies:

```go
type ModuleDeps struct {
    DB        database.Interface  // Database connection
    Logger    logger.Logger       // Structured logger
    Messaging messaging.Client    // AMQP client
}
```

## 🗄️ Database Support

GoBricks supports multiple databases through a unified interface:

### PostgreSQL
```yaml
database:
  type: "postgresql"
  host: "localhost"
  port: 5432
  database: "myapp"
  username: "postgres"
  password: "password"
  ssl_mode: "disable"
```

### Oracle
```yaml
database:
  type: "oracle"
  host: "localhost"
  port: 1521
  service_name: "ORCL"
  username: "system"
  password: "password"
```

## 📨 Messaging

AMQP messaging with RabbitMQ:

```yaml
messaging:
  broker_url: "amqp://guest:guest@localhost:5672/"
  exchange: "my-service"
  routing_key: "events"
  virtual_host: "/"
```

## ⚙️ Configuration

Configuration management with multiple sources (priority order):

1. **Environment Variables** (highest priority)
2. **Environment-specific YAML** (`config.production.yaml`)
3. **Base YAML** (`config.yaml`)
4. **Default Values** (lowest priority)

### Environment Variables

```bash
export APP_NAME="my-service"
export DATABASE_HOST="prod-db.company.com"
export DATABASE_PASSWORD="secure_password"
export LOG_LEVEL="info"
```

## 📊 Observability

### Structured Logging

```go
deps.Logger.Info().
    Str("user_id", userID).
    Int("count", items).
    Msg("Processing user items")
```

### Request Tracking

Automatic tracking of:
- HTTP request/response metrics
- Database query performance
- AMQP message statistics
- Request correlation IDs

### Health Checks

Built-in endpoints:
- `/health` - Basic health check
- `/ready` - Readiness probe with database connectivity

## 🚀 Production Features

- **Graceful Shutdown**: Coordinated shutdown of all components
- **Connection Pooling**: Optimized database connection management
- **Rate Limiting**: Built-in request rate limiting
- **CORS Support**: Configurable cross-origin resource sharing
- **Request Validation**: Automatic request validation with go-playground/validator
- **Security Headers**: Essential security headers included
- **Recovery Middleware**: Panic recovery with logging

## 📚 Documentation

- [Getting Started Guide](docs/getting-started.md)
- [Configuration Reference](docs/configuration.md)
- [Module Development](docs/modules.md)
- [Database Usage](docs/database.md)
- [Messaging Guide](docs/messaging.md)

## 🛠️ Examples

See the [examples/](examples/) directory for complete examples:

- [Basic API](examples/basic-api/) - Simple REST API
- [Messaging Service](examples/messaging-service/) - AMQP messaging
- [Multi-Database](examples/multi-database/) - PostgreSQL + Oracle

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

GoBricks was extracted from production enterprise applications and battle-tested in real-world microservice architectures.

---

**Built with ❤️ for the Go community**