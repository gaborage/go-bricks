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
- **🌐 HTTP Server**: Echo-based server with standardized envelopes and middleware stack
- **🗄️ Multi-Database Support**: PostgreSQL and Oracle drivers with unified interface and health checks
- **📨 Messaging Registry**: RabbitMQ client with infrastructure declaration and consumer lifecycle management
- **⚙️ Configuration Management**: Koanf-based loader that merges defaults, YAML, and environment variables
- **📊 Built-in Observability**: Request tracking, trace propagation, performance metrics, structured logging
- **🔧 Database Migrations**: Flyway compatible workflow for schema management
- **🧩 Pluggable Modules**: Register modules with HTTP routes and messaging hooks in seconds
- **🚀 Production Ready**: Graceful shutdown, rate limiting, and battle-tested defaults

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
  rate_limit: 200

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

messaging:
  broker_url: "amqp://guest:guest@localhost:5672/" # optional
```

`app.New()` loads defaults, `config.yaml`, `config.<env>.yaml`, and environment variables (highest priority) via the Koanf-powered loader.

### 3. Create a Module

```go
// internal/modules/example/module.go
package example

import (
    "github.com/labstack/echo/v4"

    "github.com/gaborage/go-bricks/app"
    "github.com/gaborage/go-bricks/messaging"
    "github.com/gaborage/go-bricks/server"
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

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo) {
    server.GET(hr, e, "/hello", m.hello)
}

func (m *Module) RegisterMessaging(registry *messaging.Registry) {
    // Declare exchanges/queues or register publishers & consumers here when needed
}

func (m *Module) Shutdown() error {
    return nil
}

// Enhanced handler signature: focuses on business logic
type HelloReq struct {
    Name string `query:"name" validate:"required"`
}

type HelloResp struct {
    Message string `json:"message"`
}

func (m *Module) hello(req HelloReq, _ server.HandlerContext) (HelloResp, server.IAPIError) {
    return HelloResp{Message: "Hello " + req.Name}, nil
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
    if err := framework.RegisterModule(&example.Module{}); err != nil {
        log.Fatal(err)
    }
    
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
    RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo)
    RegisterMessaging(registry *messaging.Registry)
    Shutdown() error
}
```

`RegisterMessaging` receives a shared `messaging.Registry` that can declare exchanges, queues, bindings, publishers, and consumers before the server starts.

Modules have access to shared dependencies:

```go
type ModuleDeps struct {
    DB        database.Interface  // Database connection
    Logger    logger.Logger       // Structured logger
    Messaging messaging.Client    // AMQP client
    Config    *config.Config      // Loaded configuration
}

```

`Config` gives modules read-only access to the fully merged application settings so that feature flags or rate limits can be honored without custom loading logic.

### Enhanced Handlers: Result and Envelope

Handlers use a type-safe signature and return either a data type or a Result wrapper to control status codes and headers:

```go
// Create returns 201 Created
func (m *Module) createUser(req CreateReq, _ server.HandlerContext) (server.Result[User], server.IAPIError) {
    user := User{ /* ... */ }
    return server.Created(user), nil
}

// Update returns 202 Accepted
func (m *Module) updateUser(req UpdateReq, _ server.HandlerContext) (server.Result[User], server.IAPIError) {
    updated := User{ /* ... */ }
    return server.Accepted(updated), nil
}

// Delete returns 204 No Content (no body)
func (m *Module) deleteUser(req DeleteReq, _ server.HandlerContext) (server.NoContentResult, server.IAPIError) {
    return server.NoContent(), nil
}
```

All responses are wrapped in a standard envelope on success:

```json
{
  "data": { /* your payload */ },
  "meta": {
    "timestamp": "2025-01-12T12:34:56Z",
    "traceId": "uuid-or-request-id"
  }
}
```

Errors are returned in a consistent envelope with codes and optional details (details in dev):

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "User not found",
    "details": { /* dev only */ }
  },
  "meta": {
    "timestamp": "2025-01-12T12:34:56Z",
    "traceId": "uuid-or-request-id"
  }
}
```

### Migration: echo.Context to Enhanced Handlers

Minimal steps to migrate an endpoint from raw Echo to the enhanced pattern:

1) Change Module interface and route registration

Before:
```go
func (m *Module) RegisterRoutes(e *echo.Echo) error {
    e.GET("/users/:id", m.getUser)
    return nil
}

func (m *Module) getUser(c echo.Context) error {
    id := c.Param("id")
    // ...parse, validate, fetch
    return c.JSON(http.StatusOK, user)
}
```

After:
```go
type GetUserReq struct {
    ID int `param:"id" validate:"min=1"`
}

type UserResp struct { /* fields */ }

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo) {
    server.GET(hr, e, "/users/:id", m.getUser)
}

func (m *Module) getUser(req GetUserReq, _ server.HandlerContext) (UserResp, server.IAPIError) {
    // ...business logic only
    // on missing: return UserResp{}, server.NewNotFoundError("User")
    return UserResp{/* ... */}, nil
}
```

2) Use struct tags for binding and validation
- `param:"id"`, `query:"q"`, `header:"Authorization"`, and JSON body tags.
- Add `validate:"..."` using validator/v10 rules.

3) Set custom statuses with Result helpers when needed
- 201: `return server.Created(resp), nil`
- 202: `return server.Accepted(resp), nil`
- 204: `return server.NoContent(), nil` (no body)

Notes
- The framework auto-wraps responses into `{ data, meta }` and errors into `{ error, meta }`.
- Existing Echo middleware remains compatible; tracing uses `X-Request-ID` (generated if missing).

Common Pitfalls
- Ensure Echo’s validator is set. Using `app.New()` wires `server.NewValidator()` automatically. If you build Echo manually, set `e.Validator = server.NewValidator()`.
- 204 responses must not include a body. Use `server.NoContent()`.
- To set headers (e.g., `Location` on 201), return a `server.Result[T]{ Data: t, Status: http.StatusCreated, Headers: http.Header{"Location": {"/users/123"}} }`.
- Binding precedence: JSON body → path params → query params → headers. Later sources overwrite earlier values.
- `[]string` binding: use repeated query params (`?names=a&names=b`) or comma-separated headers (`X-Items: a, b`).
- Error details appear only in development; production hides internal details for 5xx.
- Handlers get `server.HandlerContext` with optional `Echo` access for advanced cases; most handlers won’t need it.

Old → New Mapping (quick reference)
- `c.Param("id")` → `ID int \\`param:"id"\\`` in request struct
- `c.QueryParam("q")` → `Q string \\`query:"q"\\``
- Repeated query values → `Tags []string \\`query:"tags"\\`` with `?tags=a&tags=b`
- Header read → `Auth string \\`header:"Authorization"\\``
- Manual bind/validate → Framework binds and calls `c.Validate()` for you (add `validate:"..."` tags)
- `return c.JSON(200, data)` → `return data, nil`
- `return c.JSON(201, data)` → `return server.Created(data), nil`
- `return c.NoContent(204)` → `return server.NoContent(), nil`
- Errors: `echo.NewHTTPError(404)` → `server.NewNotFoundError("Resource")`; `400` → `server.NewBadRequestError("...")`; `409` → `server.NewConflictError("...")`
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

### Oracle Tips

- Reserved identifiers: Oracle reserves many keywords like NUMBER. If your table has a column named number, you must quote it in SQL (e.g., "NUMBER" or "number", matching how it was created). GoBricks' query builder helps with this.
- Placeholders: Oracle uses `:1`, `:2`, ... (or named binds like `:id`), not `$1`.

Best practice: avoid reserved identifiers in schema. Prefer descriptive names like `account_number` instead of `number`. If you need to rename:

```sql
-- Use the exact quoted identifier if it was created quoted
ALTER TABLE accounts RENAME COLUMN "NUMBER" TO account_number;
```

Example using the query builder to safely insert into a table with a reserved column:

```go
import (
    brdb "github.com/gaborage/go-bricks/database"
    "github.com/Masterminds/squirrel"
)

qb := brdb.NewQueryBuilder(brdb.Oracle)

// Will quote the reserved column name on Oracle automatically
insert := qb.
    InsertWithColumns("accounts", "id", "name", "number", "balance", "created_at").
    Values(1, "John", "12345", 100.50, squirrel.Expr(qb.BuildCurrentTimestamp()))

sql, args, _ := insert.ToSql()
// sql: INSERT INTO accounts (id,name,"number",balance,created_at) VALUES (:1,:2,:3,:4,:5)
// args: [1 "John" "12345" 100.5]
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
- W3C `traceparent` propagation for outbound calls

### Health Checks

Built-in endpoints:
- `/health` - Basic health check
- `/ready` - Readiness probe with database stats and messaging readiness

## 🚀 Production Features

- **Graceful Shutdown**: Coordinated shutdown of all components
- **Connection Pooling**: Optimized database connection management
- **Rate Limiting**: Built-in request rate limiting
- **CORS Support**: Configurable cross-origin resource sharing
- **Request Validation**: Automatic request validation with go-playground/validator
- **Security Headers**: Essential security headers included
- **Recovery Middleware**: Panic recovery with logging

## 📚 Documentation

- `wiki/architecture_decisions.md` – Design notes and rationales for the building blocks
- `CONTRIBUTING.md` – Project workflow, coding standards, and test expectations
- Inline GoDoc comments across packages (`app`, `server`, `messaging`, `database`, `trace`)
- Rich examples under `examples/` demonstrating enhanced handlers, HTTP client usage, params, Oracle quirks, and trace propagation

## 🛠️ Examples

See the [examples/](examples/) directory for complete examples:

- [Enhanced HTTP Handlers](examples/enhanced-handlers) - Full module showcasing typed handlers
- [HTTP Client](examples/http/main.go) - Fluent client with builder, interceptors, retries
- [Oracle Insert With Reserved Column](examples/oracle/main.go) - Safe quoting and `:n` binds
- [Params usage example](examples/params/main.go) - Definition and usage of custom params
- [Trace Propagation](examples/trace-propagation) - Demonstrates W3C traceparent support

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

GoBricks was extracted from production enterprise applications and battle-tested in real-world microservice architectures.

---

**Built with ❤️ for the Go community**
## 🌐 HTTP Client

GoBricks includes a small HTTP client with a fluent builder, request/response interceptors, default headers and basic auth, plus a retry mechanism with exponential backoff and jitter.

- Create a client with defaults:

```go
package main

import (
    "context"

    brhttp "github.com/gaborage/go-bricks/http"
    "github.com/gaborage/go-bricks/logger"
)

func main() {
    log := logger.New("info", false)
    client := brhttp.NewClient(log)

    ctx := context.Background()
    resp, err := client.Get(ctx, &brhttp.Request{URL: "https://example.com"})
    if err != nil {
        log.Error().Err(err).Msg("request failed")
        return
    }
    log.Info().Int("status", resp.StatusCode).Msg("ok")
}
```

- Configure via builder, including retries and interceptors:

```go
package main

import (
    "context"
    nethttp "net/http"
    "time"

    brhttp "github.com/gaborage/go-bricks/http"
    "github.com/gaborage/go-bricks/logger"
)

func main() {
    log := logger.New("info", false)
    client := brhttp.NewBuilder(log).
        WithTimeout(5 * time.Second).
        WithDefaultHeader("Accept", "application/json").
        WithBasicAuth("user", "pass").
        WithRequestInterceptor(func(ctx context.Context, r *nethttp.Request) error {
            r.Header.Set("X-Correlation-ID", "demo-123")
            return nil
        }).
        WithResponseInterceptor(func(ctx context.Context, r *nethttp.Request, resp *nethttp.Response) error {
            if resp.StatusCode >= 500 {
                log.Warn().Int("status", resp.StatusCode).Msg("server error")
            }
            return nil
        }).
        // Retries: exponential backoff with full jitter
        // delay per attempt = baseDelay * 2^attempt, jittered in [0, delay), capped at 30s
        WithRetries(3, 200*time.Millisecond).
        Build()

    ctx := context.Background()
    req := &brhttp.Request{URL: "https://example.com/api"}
    resp, err := client.Get(ctx, req)
    if err != nil {
        log.Error().Err(err).Msg("request failed")
        return
    }
    log.Info().Int("status", resp.StatusCode).Msg("ok")
}
```

Retry/backoff details:
- Retries on: transport/network errors, timeouts, and 5xx responses.
- No retries on 4xx.
- Backoff: exponential (base × 2^attempt), full jitter, max 30s.
- Interceptor errors are not retried and are returned immediately.
