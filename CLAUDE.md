# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle/MongoDB), and clean architecture patterns.

**Requirements**:
- **Go 1.24 or 1.25** (CI tests both versions)
- Docker Desktop or Docker Engine (integration tests only)

## Developer Manifesto (MANDATORY)

### Framework Philosophy
GoBricks is a **production-grade framework for building MVPs fast**. It provides enterprise-quality tooling (validation, observability, tracing, type safety) while enabling rapid development velocity. The framework itself maintains high quality standards so applications built with it can move quickly with confidence.

### Core Principles
- **Explicit > Implicit** → Code must be clear. No hidden defaults, no magic configuration.
- **Type Safety > Dynamic Hacks** → Refactor-friendly code. Breaking changes prioritized for compile-time safety.
- **Deterministic > Dynamic Flow** → Predictable, testable logic. Same inputs always produce same outputs.
- **Composition > Inheritance** → Flexible, simple structures. Use interfaces and embedding over inheritance.
- **Robustness** → Handle errors idiomatically, wrap once at boundaries. No silent failures.
- **Patterns, not Over-Design** → Use them only when they solve real problems. Justify abstractions.
- **Security First** → Input validation mandatory, secrets from env/vault, audit `WhereRaw()` usage.
- **Context-First Design** → Always pass `context.Context` as first parameter for tracing, cancellation, deadlines.
- **Interface Segregation** → Small, focused interfaces for testability (e.g., `Client` vs `AMQPClient`).
- **Vendor Agnosticism** → Abstract high-cost dependencies (databases), embrace low-cost ones (HTTP frameworks).

#### Detailed Security Guidelines
- Input validation is **mandatory** at all boundaries (HTTP, messaging, database)
- `WhereRaw()` requires annotation: `// SECURITY: Manual SQL review completed - identifier quoting verified`
- Secrets from environment variables or secret managers (AWS Secrets Manager, HashiCorp Vault)
- No hardcoded credentials, no secrets in logs or error messages
- Audit logging for sensitive operations (access control, data modifications)

### Practices & Patterns
- **SOLID** → Apply when it simplifies, don't force it.
- **Fail Fast** → Module `Init()` errors are fatal. Validation errors crash at startup, never degrade silently.
- **DRY** → Don't repeat yourself (but avoid premature abstractions).
- **CQS** → Separate commands vs. queries where it adds clarity.
- **KISS** → Keep it simple, complexity must earn its place.
- **YAGNI** → Don't build what isn't needed *today*.

#### YAGNI Exceptions
- Abstractions for **vendor differences** (databases, cloud providers) are justified
- Test utilities justified **only if actively used** (measure utility function calls)
- Breaking changes acceptable when justified for safety/correctness (see ADRs)

### Framework vs. Application Development

**GoBricks Framework (this codebase):**
- **Coverage Target:** 80% (SonarCloud enforced)
- **Testing Rigor:** Race detection, multi-platform CI (Ubuntu/Windows × Go 1.24/1.25), comprehensive linting
- **Quality Bar:** Production-grade stability—users depend on framework reliability
- **Breaking Changes:** Acceptable when justified for safety/correctness (documented in ADRs)

**Applications Built with GoBricks:**
- **Coverage Target:** 60-70% on core business logic
- **Testing Focus:** Happy paths + critical error scenarios
- **Always Test:** Database queries, HTTP handlers, messaging consumers
- **Defer:** Exotic configuration combinations, rare edge cases
- **Iterate:** Expect some code to be throwaway/refactored as requirements evolve

### Engineering Principles (Go & Architecture Mindset)
- **Observability:** OpenTelemetry standards, W3C traceparent propagation across HTTP/messaging
- **12-Factor App:** Environment variables for config, stateless design, explicit dependencies
- **Error Handling:** Idiomatic Go errors (`fmt.Errorf`, `errors.Is/As`), structured errors at API boundaries
- **Context Propagation:** No global variables for tenant IDs or trace IDs—always thread context through calls
- **Automation:** Makefile/Taskfile for common tasks, multi-platform CI/CD pipelines
- **Documentation:** Just enough for others to understand quickly, examples over exhaustive docs

**"Build it simple, build it strong, and refactor when it matters."**

## Development Commands

### Essential Commands
```bash
# Build and test
go build ./...
go test ./...
go test -run TestSpecificFunction ./package

# Pre-commit checks
make check                      # Fast: framework only (fmt + lint + test)
make check-all                  # Comprehensive: framework + tool (catches breaking changes)

# Coverage
make test-coverage              # Unit tests
make test-integration           # Integration tests (requires Docker)
make test-all                   # All tests

# OpenAPI tool
make build-tool                 # Build CLI binary
make check-tool                 # Full tool validation
cd tools/openapi && make check  # Alternative: run from tool directory
```

### Make Targets

**Framework Targets:**
- `make build` - Build project
- `make test` - Unit tests with race detection
- `make test-integration` - Integration tests (Docker required)
- `make test-all` - Unit + integration tests
- `make lint` - Run golangci-lint
- `make check` - Pre-commit checks (fmt, lint, test)

**Tool Integration Targets:**
- `make build-tool` - Build OpenAPI CLI binary
- `make test-tool` - Run tool tests only
- `make check-tool` - Full tool validation (fmt, lint, test, validate-cli)
- `make clean-tool` - Clean tool build artifacts
- `make check-all` - Run all checks (framework + tool)

### Code Quality
- Linting: `.golangci.yml` with staticcheck, gosec, gocritic
- SonarCloud: Project `gaborage_go-bricks`, 80% coverage target
- CI/CD: Multi-platform (Ubuntu, Windows) × Go (1.24, 1.25)
- Race detection enabled on all platforms

## Architecture

### Core Components
- **app/** - Application framework and module system
- **config/** - Configuration management (Koanf: YAML + env vars)
- **database/** - Multi-database interface with query builder
- **logger/** - Structured logging (zerolog)
- **messaging/** - AMQP client for RabbitMQ
- **server/** - Echo-based HTTP server
- **migration/** - Flyway integration
- **observability/** - OpenTelemetry tracing and metrics

### Module System
Modules implement this interface:
```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo)
    DeclareMessaging(decls *messaging.Declarations)
    Shutdown() error
}

type ModuleDeps struct {
    DB        database.Interface
    Logger    logger.Logger
    Messaging messaging.Client
    Config    *config.Config
}
```

### Configuration Injection
Service-specific configuration with automatic validation:

```go
type ServiceConfig struct {
    APIKey   string        `config:"custom.api.key" required:"true"`
    Timeout  time.Duration `config:"custom.api.timeout" default:"30s"`
    Retries  int           `config:"custom.api.retries" default:"3"`
}

func (m *Module) Init(deps *ModuleDeps) error {
    var cfg ServiceConfig
    if err := deps.Config.InjectInto(&cfg); err != nil {
        return err
    }
    m.service = NewService(cfg)
    return nil
}
```

**Struct Tags:**
- `config:"key.path"` - Configuration key (required)
- `required:"true"` - Validation fails if missing
- `default:"value"` - Default if not set

**Supported Types:** string, int, int64, float64, bool, time.Duration

**Configuration Priority:** Environment variables > `config.<env>.yaml` > `config.yaml` > defaults

### Enhanced Handler Pattern
Type-safe handlers eliminate boilerplate:

```go
func (h *Handler) createUser(req CreateReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
    user := h.svc.Create(req)
    return server.Created(user), nil
}

type CreateReq struct {
    Name  string `json:"name" validate:"required"`
    Email string `json:"email" validate:"email"`
}

server.POST(handlerRegistry, echo, "/users", h.createUser)
```

Benefits: automatic binding/validation, standardized response envelopes, type safety

### Database Architecture
Unified `database.Interface` supporting PostgreSQL, Oracle, MongoDB with:
- Query builder with vendor-specific SQL generation
- Type-safe WHERE clause methods (prevents Oracle reserved word errors)
- Performance tracking via OpenTelemetry
- Connection pooling and health monitoring

**Package Structure:**
- `database/types/` - Core interfaces
- `database/internal/tracking/` - Performance metrics
- `database/internal/builder/` - Query builder implementations

#### Breaking Change: Type-Safe WHERE Clauses (v2.0)

**Problem:** Raw string WHERE clauses bypass Oracle identifier quoting:
```go
// ❌ OLD - fails with Oracle reserved words
query := qb.Select("id", "number").From("accounts").Where("number = ?", value)
```

**Solution:** Use type-safe methods:
```go
// ✅ NEW - automatic quoting
query := qb.Select("id", "number").From("accounts").WhereEq("number", value)
```

**Type-Safe Methods:** `WhereEq`, `WhereNotEq`, `WhereLt/Lte/Gt/Gte`, `WhereIn/NotIn`, `WhereLike`, `WhereNull/NotNull`, `WhereBetween`

**Escape Hatch:** `WhereRaw(condition, args...)` - user must manually quote Oracle reserved words

### Messaging Architecture
AMQP-based messaging with **validate-once, replay-many** pattern:
- Declarations validated upfront, replayed per-tenant for isolation
- Automatic reconnection with exponential backoff
- Context propagation for tenant IDs and tracing

### Observability

**Key Features:**
- W3C traceparent propagation across HTTP/messaging
- OpenTelemetry metrics: database operations, HTTP requests, AMQP, **Go runtime metrics**
- Health endpoints: `/health` (liveness), `/ready` (readiness)
- Dual-mode logging: action logs (100% sampling) + trace logs (WARN+ only)
- Environment-aware batching: 500ms (dev), 5s (prod)

**Go Runtime Metrics (Automatic):**
When observability is enabled, GoBricks automatically exports Go runtime metrics via OTLP:
- **Memory:** `go.memory.used`, `go.memory.limit`, `go.memory.allocated`, `go.memory.allocations`, `go.memory.gc.goal`
- **Goroutines:** `go.goroutine.count` (live goroutines)
- **CPU:** `go.processor.limit` (GOMAXPROCS)
- **Scheduler:** `go.schedule.duration` (goroutine scheduler latency histogram)
- **Configuration:** `go.config.gogc` (GOGC setting)

These metrics follow [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/runtime/go-metrics/) and are collected using the official `go.opentelemetry.io/contrib/instrumentation/runtime` package. No additional configuration required—metrics automatically export when `observability.enabled: true`.

**Dual-Mode Logging Architecture:**
The framework uses `DualModeLogProcessor` to route logs based on `log.type` attribute:
- **Action logs** (`log.type="action"`): Request summaries with 100% sampling, all severities
- **Trace logs** (`log.type="trace"`): Application debug logs filtered to WARN+ only (~95% volume reduction)

**Request Logging Lifecycle:**
Each HTTP request tracks severity escalation via `requestLogContext` in Echo's context:
```go
// Automatic escalation from HTTP status (4xx→WARN, 5xx→ERROR)
// Explicit escalation in application code:
if rateLimiter.Exceeded() {
    server.EscalateSeverity(c, zerolog.WarnLevel)
}
```

**Slow Request Detection:**
Configure `observability.logs.slow_request_threshold` (e.g., "750ms") to automatically escalate slow requests to WARN level, ensuring they appear in action logs.

**Testing Observability:**
Use `observability/testing` package for span/metric/log assertions:

```go
tp := obtest.NewTestTraceProvider()
defer tp.Shutdown(context.Background())

// Test spans
spans := tp.Exporter.GetSpans()
obtest.AssertSpanName(t, &spans[0], "operation")
obtest.AssertSpanAttribute(t, &spans[0], "key", "value")

// Test dual-mode logging
obtest.AssertLogTypeExists(t, tp.LogExporter, "action")
obtest.AssertLogMinSeverity(t, tp.LogExporter, "trace", log.SeverityWarn)
```

**Debug Logging:**
Enable internal observability debug logs by setting `GOBRICKS_DEBUG=true` or `GOBRICKS_DEBUG=1`:
```bash
GOBRICKS_DEBUG=true go run main.go
```
This shows detailed `[OBSERVABILITY]` logs for provider initialization, exporter setup, and span lifecycle tracking. Useful for troubleshooting observability configuration issues.

**Common Issues:**
- *Spans not appearing:* Check `observability.enabled: true`, wait for batch timeout (500ms dev, 5s prod)
- *Logs not exported:* Verify `observability.logs.enabled: true` and logger uses JSON mode (`logger.pretty: false`)
- *Pretty mode conflict:* Cannot use OTLP logs with `logger.pretty: true` (fails fast at startup)
- *Debug spans:* Use `environment: development` and `trace.endpoint: stdout`
- *Noisy debug logs:* If seeing `[OBSERVABILITY]` logs, ensure `GOBRICKS_DEBUG` is not set (default: disabled)

## Testing Guidelines

### Testing Strategy
- **Unit tests:** testify, sqlmock (database), httptest (server), fake adapters (messaging)
- **Integration tests:** testcontainers (MongoDB), `-tags=integration` flag
- **Race detection:** All tests run with `-race` in CI
- **Coverage target:** 80% (SonarCloud)

### Integration Testing with Testcontainers

**Prerequisites:** Docker Desktop or Docker Engine running

**Run Integration Tests:**
```bash
make test-integration           # All integration tests
make test-coverage-integration  # With coverage
go test -v -tags=integration ./database/mongodb/...
```

**Build Tag Isolation:** Integration tests use `//go:build integration` - testcontainers dependencies only compiled with `-tags=integration`

**Writing Integration Tests:**
```go
//go:build integration

func TestFeature(t *testing.T) {
    conn, ctx := setupTestContainer(t)      // Starts container
    defer cleanupTestCollection(t, conn, ctx, "test_coll")

    // Test with real database
    coll := conn.Collection("test_coll")
    _, err := coll.InsertOne(ctx, doc, nil)
    assert.NoError(t, err)
}
```

**CI/CD:** Integration tests run only on Ubuntu (Docker requirement), unit tests on all platforms

## Examples and Resources

### Demo Project
Comprehensive examples: [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project)
- **config-injection/** - Configuration patterns
- **openapi-demo/** - OpenAPI generation
- **trace-propagation/** - W3C tracing
- **oracle/** - Oracle database patterns
- **multitenant-aws/** - Multi-tenant with AWS Secrets Manager

### Documentation
- **Architecture Decisions:** `wiki/architecture_decisions.md`
- **Task Planning:** `.claude/tasks/archive/`

## Database-Specific Notes

### Oracle
- Uses `:1`, `:2` placeholders (not `$1`, `$2`)
- Automatic identifier quoting for reserved words
- Service name vs SID connection options

### PostgreSQL
- Standard `$1`, `$2` placeholders
- Optimized connection pooling with pgx driver

### MongoDB
- Document-based operations with SQL-like interface
- Aggregation pipeline support

## OpenAPI Tool

```bash
cd tools/openapi
make install                    # Install CLI tool
go-bricks-openapi generate -project . -output docs/openapi.yaml
go-bricks-openapi doctor        # Check compatibility
make demo                       # Test on example service
```

Features: Static analysis-based spec generation, automatic route discovery, typed request/response models

## Development Workflow

### Pre-commit Workflow
```bash
# Daily development (fast feedback)
make check        # Framework only: fmt, lint, test with race detection

# Before committing framework API changes (comprehensive validation)
make check-all    # Framework + tool: catches breaking changes in tool

# Tool-only development
cd tools/openapi && make check    # Validates tool against current framework
```

**When to use `check-all`:**
- Modifying public interfaces (server, database, config, observability)
- Changing struct tags or validation logic
- Refactoring shared types or error handling
- Before creating PRs that touch framework APIs

### CI Workflow Testing
The unified CI workflow (`ci-v2.yml`) intelligently runs only necessary jobs:
- **Framework changes only:** Skips tool test jobs (saves ~8-10 minutes)
- **Tool changes only:** Skips framework test/integration jobs (saves ~15-20 minutes)
- **Both components:** Runs all jobs
- **Path detection:** Automatic via `dorny/paths-filter@v3` action

### Branch Model
- Main branch: `main` (stable releases)
- Feature branches: `feature/*`

### CI/CD Pipeline
- **Unified CI (ci-v2.yml):** Single workflow with intelligent path-based job execution
  - Uses `dorny/paths-filter@v3` to detect framework vs tool changes
  - Framework jobs run only when framework code changes (excludes `tools/**`)
  - Tool jobs run only when `tools/openapi/**` changes
  - Shared jobs (lint, security) run independently for each component
  - Eliminates race conditions from parallel workflow execution
- **Legacy Workflows:** `ci.yml` and `openapi-tool.yml` (deprecated, use ci-v2.yml)
- **Test Matrix:** Ubuntu/Windows × Go 1.24/1.25
- **Coverage:** Merged unit + integration coverage → SonarCloud

### Windows-Specific Testing
- Known path differences (`/tmp` vs `D:\temp`)
- Intelligent retry logic for Windows-specific patterns
- Race detection enabled

## File Organization
- **internal/** - Private packages
- **tools/** - Development tooling (OpenAPI generator)
- **wiki/** - Architecture documentation (see ADR-006 for dual-mode logging)
- **.claude/tasks/** - Development task planning
- **observability/testing/** - Test utilities for spans, metrics, and logs
- **observability/dual_processor.go** - Dual-mode log routing implementation
- **server/logger_context.go** - Request log context tracking
- Tests alongside source files (`*_test.go`)

## Key Interfaces

### Database Interface
```go
type Interface interface {
    Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
    Begin(ctx context.Context) (types.Tx, error)
    Health(ctx context.Context) error
    DatabaseType() string
}
```

### Messaging Client
```go
type Client interface {
    Publish(ctx context.Context, destination string, data []byte) error
    Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error)
    IsReady() bool
}
```

### Observability Provider
```go
type Provider interface {
    TracerProvider() *sdktrace.TracerProvider
    MeterProvider() *sdkmetric.MeterProvider
    LoggerProvider() *sdklog.LoggerProvider
    ShouldDisableStdout() bool
    Shutdown(ctx context.Context) error
}
```

## Dependencies
- **Echo v4** - HTTP framework
- **zerolog** - Structured logging
- **pgx/v5** - PostgreSQL driver
- **go-ora/v2** - Oracle driver
- **Squirrel** - SQL query builder
- **Koanf v2** - Configuration management
- **amqp091-go** - RabbitMQ client
- **validator/v10** - Request validation
- **testify** - Testing framework
- **testcontainers-go** - Integration testing
