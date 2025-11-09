# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle/MongoDB), and clean architecture patterns.

**Requirements**:
- **Go 1.24 or 1.25** (CI tests both versions)
- Docker Desktop or Docker Engine (integration tests only)

## Quick Reference

**Most Common Commands:**
```bash
make check              # Pre-commit: fmt + lint + test
make test               # Unit tests with race detection
make test-integration   # Integration tests (Docker required)
go test -run TestName   # Run specific test
```

**Key Files:**
- [CLAUDE.md](CLAUDE.md) - This development guide
- [.specify/memory/constitution.md](.specify/memory/constitution.md) - Project governance
- [wiki/architecture_decisions.md](wiki/architecture_decisions.md) - ADRs for breaking changes
- [llms.txt](llms.txt) - Quick code examples for LLMs
- [.golangci.yml](.golangci.yml) - Linting configuration

**External Resources:**
- [Demo Project](https://github.com/gaborage/go-bricks-demo-project) - Complete examples
- [SonarCloud](https://sonarcloud.io/project/overview?id=gaborage_go-bricks) - Code quality metrics

## Table of Contents

**Getting Started:**
- [Quick Reference](#quick-reference) - Commands, files, resources
- [Documentation Hierarchy](#documentation-hierarchy) - How to navigate the docs
- [Developer Manifesto](#developer-manifesto-mandatory) - Core principles
- [Development Commands](#development-commands) - Build, test, lint

**Architecture:**
- [Core Components](#core-components) - Module system, configuration
- [Database Architecture](#database-architecture) - Query builder, vendors
- [Messaging Architecture](#messaging-architecture) - AMQP patterns
- [Observability](#observability) - Tracing, metrics, logging

**Development:**
- [Testing Guidelines](#testing-guidelines) - Unit, integration, coverage
- [Development Workflow](#development-workflow) - Pre-commit, CI/CD
- [Troubleshooting](#troubleshooting) - Common issues and solutions

**Reference:**
- [Key Interfaces](#key-interfaces) - Database, messaging, observability
- [File Organization](#file-organization) - Project structure
- [Dependencies](#dependencies) - External packages

## Documentation Hierarchy

**For Quick Tasks** (Code-first developers):
1. [llms.txt](llms.txt) - Copy-paste code snippets (30-second answers)
2. This file (CLAUDE.md) - Architecture and commands (5-minute orientation)

**For Deep Understanding** (Architecture exploration):
1. [.specify/memory/constitution.md](.specify/memory/constitution.md) - Non-negotiable principles
2. [wiki/architecture_decisions.md](wiki/architecture_decisions.md) - ADRs for breaking changes
3. [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) - Working examples

**For End Users** (Application developers):
1. [README.md](README.md) - Public-facing overview and quick start
2. [MULTI_TENANT.md](MULTI_TENANT.md) - Multi-tenant patterns
3. [Go Reference](https://pkg.go.dev/github.com/gaborage/go-bricks) - API documentation

## Developer Manifesto (MANDATORY)

> **Note:** The full governance framework is defined in [.specify/memory/constitution.md](.specify/memory/constitution.md). This section provides an overview of core principles that guide all development.

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
Abstractions for vendor differences (databases, cloud providers) are justified. Test utilities only if actively used. Breaking changes acceptable for safety/correctness (see ADRs).

### Framework vs. Application Development

**GoBricks Framework (this codebase):** 80% coverage (SonarCloud enforced), race detection, multi-platform CI, production-grade stability. Breaking changes acceptable when justified (documented in ADRs).

**Applications Built with GoBricks:** 60-70% coverage on core business logic, focus on happy paths + critical errors, always test database/HTTP/messaging, defer exotic edge cases, iterate on requirements

### Engineering Principles (Go & Architecture Mindset)
- **Observability:** OpenTelemetry standards, W3C traceparent propagation across HTTP/messaging
- **12-Factor App:** Environment variables for config, stateless design, explicit dependencies
- **Error Handling:** Idiomatic Go errors (`fmt.Errorf`, `errors.Is/As`), structured errors at API boundaries
- **Context Propagation:** No global variables for tenant IDs or trace IDs—always thread context through calls
- **Automation:** Makefile/Taskfile for common tasks, multi-platform CI/CD pipelines
- **Documentation:** Just enough for others to understand quickly, examples over exhaustive docs

**"Build it simple, build it strong, and refactor when it matters."**

## Development Commands

```bash
# Build and test
go build ./...                  # Build all packages
go test ./...                   # Run all tests
go test -run TestName ./package # Run specific test

# Pre-commit checks
make check                      # Fast: framework only (fmt + lint + test)
make check-all                  # Comprehensive: framework + tool (catches breaking changes)

# Testing
make test                       # Unit tests with race detection
make test-integration           # Integration tests (Docker required)
make test-all                   # Unit + integration tests
make test-coverage              # Coverage report

# OpenAPI tool
make build-tool                 # Build CLI binary
make check-tool                 # Full tool validation
make test-tool                  # Tool tests only
make clean-tool                 # Clean artifacts

# Other
make build                      # Build project
make lint                       # Run golangci-lint
```

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

#### Breaking Change: Struct-Based Column Extraction (v0.15.0+)

**Problem:** Raw string column names bypass Oracle identifier quoting and lack type safety:
```go
// ❌ OLD (pre-v0.15.0) - raw strings, no auto-quoting, no refactor safety
query := qb.Select("id", "number").From("accounts").Where("number = ?", value)
```

**Solution:** Use struct-based column extraction with type-safe methods:
```go
// ✅ NEW (v0.15.0+) - struct-based with auto-quoting and compile-time safety
type Account struct {
    ID     int64  `db:"id"`
    Number string `db:"number"`  // Oracle reserved word - auto-quoted
}

cols := qb.Columns(&Account{})
f := qb.Filter()

query := qb.Select(cols.Fields("ID", "Number")...).
    From("accounts").
    Where(f.Eq(cols.Col("Number"), value))
// Oracle: SELECT "ID", "NUMBER" FROM accounts WHERE "NUMBER" = :1
```

**Type-Safe Methods:** `f.Eq`, `f.NotEq`, `f.Lt/Lte/Gt/Gte`, `f.In/NotIn`, `f.Like`, `f.Null/NotNull`, `f.Between`

**Escape Hatch:** `WhereRaw(condition, args...)` - user must manually quote Oracle reserved words

#### Table Aliases

The query builder supports table aliases with struct-based columns using the `As()` method for type safety and auto-quoting:

```go
type User struct {
    ID     int64  `db:"id"`
    Name   string `db:"name"`
    Status string `db:"status"`
}

type Profile struct {
    UserID int64  `db:"user_id"`
    Bio    string `db:"bio"`
}

qb := builder.NewQueryBuilder(dbtypes.Oracle)
jf := qb.JoinFilter()
f := qb.Filter()

userCols := qb.Columns(&User{})
profileCols := qb.Columns(&Profile{})

// Create aliased instances using As()
u := userCols.As("u")
p := profileCols.As("p")

query := qb.Select(u.Col("ID"), u.Col("Name"), p.Col("Bio")).
    From(dbtypes.Table("users").As("u")).
    LeftJoinOn(dbtypes.Table("profiles").As("p"),
        jf.EqColumn(u.Col("ID"), p.Col("UserID"))).
    Where(f.Eq(u.Col("Status"), "active"))
// Oracle: SELECT u."ID", u."NAME", p."BIO" FROM users u LEFT JOIN profiles p ON u."ID" = p."USER_ID" WHERE u."STATUS" = :1
```

**Benefits:** Struct-based (DRY principle), type-safe (compile-time field validation), auto-quoting (Oracle reserved words), refactor-friendly (rename struct fields → compiler catches all references), immutable (As() returns new instance)

**Mixed JOIN Conditions (v2.2+):**

JoinFilter supports both column-to-column comparisons and column-to-value filters, eliminating the need for `Raw()` in common cases:

```go
jf := qb.JoinFilter()
f := qb.Filter()

// Type-safe mixed conditions (replaces Raw() for common patterns)
query := qb.Select("*").
    From(dbtypes.Table("orders").As("o")).
    JoinOn(dbtypes.Table("customers").As("c"), jf.And(
        jf.EqColumn("c.id", "o.customer_id"),       // Column-to-column
        jf.Eq("c.status", "active"),                 // Column-to-value with placeholder
        jf.In("c.tier", []string{"gold", "platinum"}), // IN clause
    )).
    JoinOn(dbtypes.Table("products").As("p"), jf.And(
        jf.EqColumn("p.id", "o.product_id"),
        jf.Eq("p.price", qb.Expr("TO_NUMBER(o.max_price)")), // Expression support
    )).
    Where(f.Eq("o.status", "pending"))

// SQL (Oracle):
// SELECT * FROM orders o
// JOIN customers c ON (c.id = o.customer_id AND c.status = :1 AND c.tier IN (:2,:3))
// JOIN products p ON (p.id = o.product_id AND p.price = TO_NUMBER(o.max_price))
// WHERE o.status = :4
```

**Available Methods:** `Eq`, `NotEq`, `Lt/Lte/Gt/Gte`, `In/NotIn`, `Between`, `Like`, `Null/NotNull`. See [llms.txt](llms.txt) for examples.

**Expression Support:**
All comparison methods accept `qb.Expr()` for complex SQL expressions without placeholders:

```go
jf.Eq("emp.col1", qb.Expr("TO_NUMBER(o.field1)"))   // emp.col1 = TO_NUMBER(o.field1)
jf.Eq("seb.id", qb.Expr("LPAD(emp.col2, 10, '0')")) // seb.id = LPAD(emp.col2, 10, '0')
jf.Between("age", qb.Expr("18"), qb.Expr("65"))     // age >= 18 AND age <= 65
```

**Raw() Escape Hatch:**
Use `jf.Raw()` only for conditions that type-safe methods cannot express (e.g., spatial functions, exotic operators).

#### Subquery Support

Supports subqueries in WHERE clauses with `EXISTS`, `NOT EXISTS`, and `IN` patterns. Type-safe with vendor-specific placeholder handling.

**Example:**
```go
type Review struct {
    ProductID int64 `db:"product_id"`
    Rating    int   `db:"rating"`
}

type Product struct {
    ID   int64  `db:"id"`
    Name string `db:"name"`
}

reviewCols := qb.Columns(&Review{})
productCols := qb.Columns(&Product{})

jf := qb.JoinFilter()
f := qb.Filter()

p := productCols.As("p")

subquery := qb.Select("1").From("reviews").
    Where(jf.And(
        jf.EqColumn("reviews."+reviewCols.Col("ProductID"), p.Col("ID")),
        f.Eq(reviewCols.Col("Rating"), 5),
    ))

query := qb.Select(p.Col("Name")).
    From(dbtypes.Table("products").As("p")).
    Where(f.Exists(subquery))
// Oracle: SELECT p."NAME" FROM products p WHERE EXISTS (SELECT 1 FROM reviews WHERE reviews."PRODUCT_ID" = p."ID" AND "RATING" = :1)
```

**Methods:** `f.Exists(subquery)`, `f.NotExists(subquery)`, `f.InSubquery(column, subquery)`. Supports correlated subqueries and nested subqueries.

**For comprehensive examples**, see [database/internal/builder/query_builder_test.go](database/internal/builder/query_builder_test.go)

#### SELECT Expressions (v2.1+)

Supports raw SQL expressions in SELECT, GROUP BY, and ORDER BY for aggregations, functions, and calculations.

**Example:**
```go
type Product struct {
    Category string  `db:"category"`
    Price    float64 `db:"price"`
}

cols := qb.Columns(&Product{})

query := qb.Select(
    cols.Col("Category"),
    qb.Expr("COUNT(*)", "product_count"),
    qb.Expr("AVG(price)", "avg_price"),
).From("products").GroupBy(cols.Col("Category"))
// Oracle: SELECT "CATEGORY", COUNT(*) AS product_count, AVG(price) AS avg_price FROM products GROUP BY "CATEGORY"
```

**⚠️ SECURITY WARNING:** Raw SQL expressions are NOT escaped. Never interpolate user input:
```go
qb.Expr("COUNT(*)", "total")  // ✅ SAFE
qb.Expr(fmt.Sprintf("UPPER(%s)", userInput))  // ❌ SQL INJECTION!
```
Use WHERE with placeholders for dynamic values: `qb.Select("*").From("users").Where(f.Eq(userColumn, userValue))`

**Common Use Cases:** Aggregations, string/date functions, window functions. See [llms.txt](llms.txt) for more examples and [ADR-005](wiki/adr-005-type-safe-where-clauses.md) for security guidelines

#### Struct-Based Column Extraction (v0.15.0+)

GoBricks eliminates column repetition through struct-based column management using `db:"column_name"` tags.

**Key Benefits:**
- **DRY Principle**: Define columns once in struct tags, reference by field name
- **Type Safety**: Compile-time field name validation (panics on typos)
- **Vendor-Aware**: Automatic Oracle reserved word quoting
- **Zero Overhead**: One-time reflection (~0.6µs), cached forever (~26ns access)
- **Refactor-Friendly**: Rename struct fields → compiler catches all query references

**Quick Example:**

```go
type User struct {
    ID    int64  `db:"id"`
    Name  string `db:"name"`
    Level int    `db:"level"`  // Oracle reserved word - auto-quoted
}

// Extract column metadata (cached per vendor)
cols := qb.Columns(&User{})

// SELECT operations
query := qb.Select(cols.All()...).From("users")
query := qb.Select(cols.Fields("ID", "Name")...).From("users")

// WHERE with auto-quoting
query := qb.Select(cols.All()...).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT "ID", "NAME", "LEVEL" FROM users WHERE "LEVEL" = :1

// UPDATE operations
qb.Update("users").
    Set(cols.Col("Name"), "Jane").
    Where(f.Eq(cols.Col("ID"), 123))
```

**Common Pattern (Service-Level Caching):**
```go
type ProductService struct {
    qb   *builder.QueryBuilder
    cols dbtypes.ColumnMetadata
}

func NewProductService(db database.Interface) *ProductService {
    qb := builder.NewQueryBuilder(db.DatabaseType())
    return &ProductService{
        qb:   qb,
        cols: qb.Columns(&Product{}), // Cached forever
    }
}
```

**Performance:** First use ~0.6µs (reflection), cached access ~26ns (map lookup), thread-safe via `sync.Map`

**Vendor Quoting:** Oracle auto-quotes reserved words (`"NUMBER"`, `"LEVEL"`), PostgreSQL/MongoDB no quoting

**For detailed examples** (INSERT, JOINs, complex queries), see [llms.txt](llms.txt) and [ADR-007](wiki/adr-007-struct-based-columns.md)

### Messaging Architecture
AMQP-based messaging with **validate-once, replay-many** pattern:
- Declarations validated upfront, replayed per-tenant for isolation
- Automatic reconnection with exponential backoff
- Context propagation for tenant IDs and tracing

#### Helper Functions for Simplified Declarations

GoBricks provides production-safe defaults to reduce AMQP boilerplate (~50+ lines → ~15 lines):

**Concise Declaration Pattern:**
```go
exchange := decls.DeclareTopicExchange("issuance.events")
queue := decls.DeclareQueue("issuance.events.queue")
decls.DeclareBinding(queue.Name, exchange.Name, "issuance.*")

decls.DeclarePublisher(&messaging.PublisherOptions{
    Exchange: exchange.Name, RoutingKey: "issuance.created",
    EventType: "CreateBatchIssuanceRequest",
}, nil)

decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue: queue.Name, EventType: "CreateBatchIssuanceRequest",
    Handler: amqp.NewHandler(m.logger),
}, nil)
```

**Production-Safe Defaults:**
- Exchanges: `Durable: true`, `AutoDelete: false`, `Type: "topic"`
- Queues: `Durable: true`, `AutoDelete: false`, `Exclusive: false`
- Publishers: `Mandatory: false`, `Immediate: false`
- Consumers: `AutoAck: false`, `Exclusive: false`, `NoLocal: false`

**Key Helpers:** `DeclareTopicExchange()`, `DeclareQueue()`, `DeclareBinding()`, `DeclarePublisher()`, `DeclareConsumer()`

**For verbose before/after comparison**, see [messaging/declarations.go](messaging/declarations.go)

#### Consumer Registration Best Practices

**CRITICAL: Deduplication Rules**

GoBricks enforces **strict deduplication** to prevent message duplication bugs. Each unique `queue + consumer_tag + event_type` combination must be registered exactly once:

```go
func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
    decls.DeclareConsumer(&messaging.ConsumerOptions{
        Queue:     "events.queue",
        Consumer:  "discover-pending",
        EventType: "discover-pending-events",
        Handler:   m.discoverHandler.Handle,
    }, nil)

    decls.DeclareConsumer(&messaging.ConsumerOptions{
        Queue:     "events.queue",
        Consumer:  "process-batch",  // Different consumer tag - OK
        EventType: "process-batch-events",
        Handler:   m.processHandler.Handle,
    }, nil)
}
```

**Common Mistakes:**
- Registering consumers in loops or conditional blocks (creates duplicates)
- Calling `app.RegisterModule()` multiple times for the same module
- Module registration errors are unrecoverable - MUST use `log.Fatal(err)` to handle

See [Troubleshooting](#troubleshooting) section for diagnosing duplicate consumer/module errors

#### Message Error Handling

**IMPORTANT:** GoBricks uses a **no-retry policy** for failed messages to prevent infinite retry loops.

**Behavior:** All handler errors → Message nacked WITHOUT requeue (message dropped). Prevents poison messages from blocking queues. Rich ERROR logs + OpenTelemetry metrics track all failures.

**Error Handling Pattern:**
```go
func (h *Handler) Handle(ctx context.Context, delivery *amqp.Delivery) error {
    var order Order

    // Validation errors → message dropped (no retry)
    if err := json.Unmarshal(delivery.Body, &order); err != nil {
        return fmt.Errorf("invalid message format: %w", err)
    }

    // Business logic errors → message dropped (no retry)
    if err := h.orderService.Process(ctx, order); err != nil {
        return fmt.Errorf("processing failed: %w", err)
    }

    return nil // Success → message ACKed
}
```

**Observability:** ERROR logs include `message_id`, `queue`, `event_type`, `correlation_id`, `error`. OpenTelemetry metrics track operation duration with `error.type` attribute.

**Best Practices:** Thorough handler testing, monitor ERROR logs with alerts, use trace IDs for manual replay. Dead-letter queue support planned for future releases.

**Breaking Change (v2.X):** Previous behavior auto-requeued errors (infinite retry risk). New behavior drops failed messages with rich logging. Review handler error handling and set up monitoring

### Observability

**Key Features:** W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging (action logs 100% sampling, trace logs WARN+ only), environment-aware batching (500ms dev, 5s prod)

**Go Runtime Metrics:** Auto-exports memory, goroutines, CPU, scheduler latency, GC config when `observability.enabled: true`. Follows [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/runtime/go-metrics/)

**Dual-Mode Logging:** `DualModeLogProcessor` routes logs by `log.type` - action logs (100% sampling) vs trace logs (WARN+ only, ~95% reduction)

**Request Logging:** HTTP requests track severity escalation via `requestLogContext`. Automatic escalation from status codes (4xx→WARN, 5xx→ERROR). Explicit: `server.EscalateSeverity(c, zerolog.WarnLevel)`. Configure `observability.logs.slow_request_threshold` for slow request detection.

**Testing:** Use `observability/testing` package:
```go
tp := obtest.NewTestTraceProvider()
spans := tp.Exporter.GetSpans()
obtest.AssertSpanName(t, &spans[0], "operation")
obtest.AssertLogTypeExists(t, tp.LogExporter, "action")
```

**Debug Mode:** Set `GOBRICKS_DEBUG=true` for `[OBSERVABILITY]` logs (provider init, exporter setup, span lifecycle)

**Common Issues:** Spans not appearing (check `observability.enabled`, wait for batch timeout), logs not exported (verify `observability.logs.enabled`, set `logger.pretty: false`), pretty mode conflict (fails fast at startup). See [Troubleshooting](#troubleshooting) for details

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

**Demo Project:** [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) - Comprehensive examples for config injection, OpenAPI, tracing, Oracle, multi-tenant AWS

**Documentation:**
- Architecture Decisions: `wiki/architecture_decisions.md`, Quick Examples: `llms.txt`
- Governance: `.specify/memory/constitution.md`, Task Planning: `.claude/tasks/archive/`

## Database-Specific Notes

| Database | Placeholders | Key Features |
|----------|--------------|--------------|
| **Oracle** | `:1`, `:2` | Automatic reserved word quoting, service name/SID options |
| **PostgreSQL** | `$1`, `$2` | pgx driver with optimized connection pooling |
| **MongoDB** | N/A | Document-based with SQL-like interface, aggregation pipeline |

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

## Troubleshooting

### Common Issues

**Build/Test Failures:**

```bash
# "cannot find package" errors
go mod tidy && go mod download

# "Docker not running" during integration tests
make docker-check  # Check Docker status
docker info        # Verify Docker daemon

# Race condition failures
go test -race -run TestSpecificFailing ./package

# Linting errors
golangci-lint cache clean
golangci-lint run
```

**Database Issues:**

```bash
# Oracle: ORA-00936 "missing expression"
# → Use type-safe WHERE methods (.WhereEq) instead of .WhereRaw() for auto-quoting

# PostgreSQL: "syntax error at or near $1"
# → Check placeholder numbering (PostgreSQL: $1,$2; Oracle: :1,:2)

# "database not configured" errors
# → Set database.type, database.host OR database.connection_string (see [ADR-003](wiki/adr-003-database-by-intent.md))
```

**Observability Issues:**

```bash
# "cannot use OTLP logs with pretty=true"
# → Set logger.pretty: false when observability.logs.enabled: true

# Spans not appearing in collector
# → Check observability.enabled: true, wait for batch timeout (500ms dev, 5s prod), or set trace.endpoint: stdout

# Missing trace_id in logs
# → Use logger.WithContext(ctx).Info(), verify provider initialized before logger enhancement

# Noisy [OBSERVABILITY] debug logs
# → Unset GOBRICKS_DEBUG environment variable
```

**CI/CD Issues:**

```bash
# Tool tests failing after framework changes
make check-all  # Run comprehensive validation (framework + tool)

# Windows-specific path failures
# → Check for /tmp vs D:\temp in test assertions
# → See: observability/provider_test.go for retry patterns

# Coverage below 80%
# → Run: make test-coverage
# → Check SonarCloud quality gate requirements
```

**Multi-Tenant Issues:**

```bash
# "tenant ID not found in context"
# → Use deps.GetDB(ctx), not deps.DB (function-based access)
# → Ensure tenant resolver configured in multitenant.resolver

# Messaging registry initialization errors
# → Check logs for "messaging not configured" warnings
# → Verify messaging.broker.url set for each tenant
# → See [ADR-004](wiki/adr-004-lazy-messaging-registry.md) for lazy registry creation details
```

**Messaging Issues:**

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

# Diagnostic commands
grep "Starting AMQP consumers" logs/app.log
grep "Multiple consumers registered for same queue" logs/app.log
```

**Module Registration Issues:**

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

## SpecKit Workflow (Feature Development)

GoBricks uses SpecKit for structured feature development with built-in slash commands:

### Available Commands
- `/speckit.specify` - Create feature specification from natural language description
- `/speckit.plan` - Generate implementation plan with design artifacts
- `/speckit.tasks` - Generate actionable, dependency-ordered task list
- `/speckit.implement` - Execute implementation plan by processing tasks
- `/speckit.clarify` - Identify underspecified areas and ask clarification questions
- `/speckit.checklist` - Generate custom checklist for current feature
- `/speckit.analyze` - Cross-artifact consistency and quality analysis
- `/speckit.constitution` - Update project constitution and sync templates

### Typical Workflow
1. `/speckit.specify` - Start with feature requirements
2. `/speckit.clarify` - Resolve ambiguities (if needed)
3. `/speckit.plan` - Generate detailed implementation plan
4. `/speckit.tasks` - Break down into actionable tasks
5. `/speckit.implement` - Execute tasks with tracking
6. `/speckit.analyze` - Verify consistency across artifacts

### Constitution Reference
The project constitution (`.specify/memory/constitution.md`) defines non-negotiable governance:
- Explicit > Implicit (no magic configuration)
- Type Safety > Dynamic Hacks (breaking changes acceptable for safety)
- Test-First Development (80% coverage enforced)
- Security First (mandatory input validation)
- Observability as First-Class Citizen (OpenTelemetry standards)
- Performance Standards (minimal framework overhead)
- User Experience Consistency (predictable APIs)

See [.specify/memory/constitution.md](.specify/memory/constitution.md) for full governance framework.

### When to Use Each Tool

| Tool | Primary Use Case |
|------|------------------|
| `make check` | Daily development, pre-commit checks (fast feedback on framework code) |
| `make check-all` | Before PRs that modify public interfaces (server, database, config, observability) |
| `make test-integration` | Testing database/messaging vendor differences (requires Docker) |
| SpecKit commands | Planning multi-step features (>3 tasks) with structured task breakdown |
| `go test -run TestName` | Debugging specific failing tests, fast iteration on single test cases |
| SonarCloud | Coverage metrics (80% target), quality gate validation before releases |

## File Organization
- **internal/** - Private packages
- **tools/** - Development tooling (OpenAPI generator)
- **wiki/** - Architecture documentation (see [ADR-006](wiki/adr-006-otlp-log-export.md) for dual-mode logging)
- **.claude/tasks/** - Development task planning
- **.specify/** - SpecKit feature development workflow
  - **memory/constitution.md** - Project governance framework
  - **templates/** - Spec, plan, task, and checklist templates
  - **scripts/bash/** - Automation scripts for SpecKit commands
- **observability/testing/** - Test utilities for spans, metrics, and logs
- **observability/dual_processor.go** - Dual-mode log routing implementation
- **server/logger_context.go** - Request log context tracking
- **llms.txt** - Quick reference examples for LLM code generation
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
