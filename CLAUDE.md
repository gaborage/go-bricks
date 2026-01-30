# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle/MongoDB), and clean architecture patterns.

**Requirements**:
- **Go 1.25** required
- Docker Desktop or Docker Engine (integration tests only)

## Quick Reference

**Most Common Commands:**
```bash
make check              # Pre-commit: fmt + lint + test
make test               # Unit tests with race detection
make test-integration   # Integration tests (Docker required)
go test -run TestName   # Run specific test
go test -bench=.        # Run benchmarks
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
- CI/CD: Multi-platform (Ubuntu, Windows) × Go 1.25
- Race detection enabled on all platforms

### Breaking Change: Go Naming Conventions (S8179)

GoBricks follows Go's idiomatic naming conventions. Per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/), getter methods should NOT have the `Get` prefix.

**Migration Required:** If upgrading from versions prior to this change, update all call sites:

| Package | Old Method | New Method |
|---------|------------|------------|
| `config.Config` | `GetString()`, `GetInt()`, `GetInt64()`, `GetFloat64()`, `GetBool()` | `String()`, `Int()`, `Int64()`, `Float64()`, `Bool()` |
| `config.Config` | `GetRequiredString()`, `GetRequiredInt()`, `GetRequiredInt64()`, `GetRequiredFloat64()`, `GetRequiredBool()` | `RequiredString()`, `RequiredInt()`, `RequiredInt64()`, `RequiredFloat64()`, `RequiredBool()` |
| `app.ResourceProvider` | `GetDB()`, `GetMessaging()`, `GetCache()` | `DB()`, `Messaging()`, `Cache()` |
| `app.ModuleDeps` | `GetDB`, `GetMessaging`, `GetCache` (fields) | `DB`, `Messaging`, `Cache` (fields) |
| `app.Builder` | `GetError()` | `Error()` |
| `messaging.Manager` | `GetPublisher()` | `Publisher()` |
| `server.Validator` | `GetValidator()` | `Validator()` |
| `validation.TagInfo` | `GetMin()`, `GetMax()`, `GetMinLength()`, `GetMaxLength()`, `GetPattern()`, `GetEnum()`, `GetConstraints()` | `Min()`, `Max()`, `MinLength()`, `MaxLength()`, `Pattern()`, `Enum()`, `AllConstraints()` |
| `migration.FlywayMigrator` | `GetDefaultMigrationConfig()` | `DefaultMigrationConfig()` |
| `config.TenantStore` | `GetTenants()` | `Tenants()` |
| `app.MetadataRegistry` | `GetModules()`, `GetModule()` | `Modules()`, `Module()` |
| `app.App` | `GetMessagingDeclarations()` | `MessagingDeclarations()` |
| `database/mongodb.Builder` | `GetState()`, `GetSkip()`, `GetLimit()`, `GetProjectionFields()`, `GetSortFields()` | `State()`, `SkipValue()`, `LimitValue()`, `ProjectionFields()`, `SortFields()` |
| `database/mongodb.Connection` | `GetDatabase()`, `GetClient()` | `Database()`, `Client()` |
| `database/mongodb.Transaction` | `GetSession()`, `GetDatabase()` | `Session()`, `Database()` |
| `database.Interface` | `GetMigrationTable()` | `MigrationTable()` |
| `database/testing.TestDB` | `GetQueryLog()`, `GetExecLog()` | `QueryLog()`, `ExecLog()` |
| `database/testing.TenantDBMap` | `GetTenantDB()` | `TenantDB()` |
| `messaging.Registry` | `GetDeclarations()` | `Declarations()` |
| `server.RouteRegistry` | `GetRoutes()` | `Routes()` |

**Example Migration:**
```go
// ❌ OLD
host := cfg.GetString("server.host", "0.0.0.0")
port := cfg.GetInt("server.port", 8080)
db, err := deps.GetDB(ctx)
client, err := manager.GetPublisher(ctx, "tenant-1")

// ✅ NEW
host := cfg.String("server.host", "0.0.0.0")
port := cfg.Int("server.port", 8080)
db, err := deps.DB(ctx)
client, err := manager.Publisher(ctx, "tenant-1")
```

## Architecture

### Core Components
- **app/** - Application framework and module system
- **config/** - Configuration management (Koanf: YAML + env vars)
- **database/** - Multi-database interface with query builder
- **cache/** - Redis caching with type-safe CBOR serialization
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

#### Handler Performance: Pointer vs Value Types

The enhanced handler pattern supports both **value** and **pointer** types for requests and responses, allowing you to optimize for performance when handling large payloads.

**When to Use Value Types (Default)**:
- ✅ Small requests/responses (<1KB, ~10-15 simple fields)
- ✅ No large embedded arrays or slices
- ✅ Emphasizes immutability (idiomatic Go)
- ✅ Examples: login credentials, ID lookups, simple CRUD operations

**When to Use Pointer Types**:
- ✅ Large requests/responses (>1KB)
- ✅ File uploads (base64-encoded images, documents)
- ✅ Bulk imports/exports (hundreds or thousands of records)
- ✅ Embedded byte arrays or large slices
- ✅ Performance-critical high-traffic endpoints

**Examples**:

```go
// Small request - use value type (default)
type LoginRequest struct {
    Email    string `json:"email" validate:"email"`
    Password string `json:"password" validate:"required"`
}

func (h *Handler) login(req LoginRequest, ctx server.HandlerContext) (Result[Token], server.IAPIError) {
    token := h.authService.Authenticate(req)
    return server.OK(token), nil
}

// Large request - use pointer type for performance
type FileUploadRequest struct {
    Data     []byte `json:"data"` // Base64-encoded file (could be MB)
    Filename string `json:"filename" validate:"required"`
    MimeType string `json:"mime_type"`
}

func (h *Handler) uploadFile(req *FileUploadRequest, ctx server.HandlerContext) (Result[UploadResponse], server.IAPIError) {
    // Pointer avoids copying large byte slice
    fileID := h.storageService.Store(req.Data, req.Filename)
    return server.Created(UploadResponse{FileID: fileID}), nil
}

// Large response - use pointer type
type BulkExportResponse struct {
    Records []Record `json:"records"` // Thousands of records
    Total   int      `json:"total"`
}

func (h *Handler) exportAll(req ExportRequest, ctx server.HandlerContext) (*BulkExportResponse, server.IAPIError) {
    records := h.recordService.GetAll(ctx)
    return &BulkExportResponse{
        Records: records,
        Total:   len(records),
    }, nil
}

// Mixed: pointer request, value response
func (h *Handler) processBulk(req *BulkRequest, ctx server.HandlerContext) (Summary, server.IAPIError) {
    summary := h.processor.Process(req)
    return summary, nil
}
```

**Performance Impact**:
- **Value types**: Small struct copy overhead (~nanoseconds for <1KB)
- **Pointer types**: Zero copy overhead, just 8-byte pointer
- **Rule of thumb**: Use pointers when struct size >1KB or contains large slices/arrays

**Linter Configuration**:
Configure `govet` to warn on large value copies:
```yaml
# .golangci.yml
linters-settings:
  govet:
    enable:
      - copylocks
      - composites
```

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

#### Named Databases (Single-Tenant Multi-Database)

GoBricks supports accessing multiple databases in single-tenant mode, useful for legacy system migrations where applications need to access both old (e.g., Oracle) and new (e.g., PostgreSQL) databases.

**Configuration:**
```yaml
# Default database (unchanged - backward compatible)
database:
  type: postgres
  host: primary.db.example.com
  port: 5432
  database: main_db

# Named databases (NEW) - supports mixed vendors
databases:
  legacy:
    type: oracle
    host: legacy-oracle.example.com
    port: 1521
    service_name: LEGACYDB
    username: legacy_user
    password: ${LEGACY_DATABASE_PASSWORD}
  analytics:
    type: postgres
    host: analytics.db.example.com
    port: 5432
    database: analytics_db
```

**Module Usage:**
```go
func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getDB = deps.DB              // Default database (unchanged)
    m.getDBByName = deps.DBByName  // Named database access (NEW)
    return nil
}

// Handler with cross-database operations
func (h *Handler) MigrateLegacyData(ctx context.Context) error {
    // Access legacy Oracle database
    legacyDB, err := h.getDBByName(ctx, "legacy")
    if err != nil {
        return err
    }

    // Access default PostgreSQL database
    mainDB, err := h.getDB(ctx)
    if err != nil {
        return err
    }

    // Cross-database operation: Oracle -> PostgreSQL
    oracleQB := builder.NewQueryBuilder(dbtypes.Oracle)
    rows, _ := legacyDB.Query(ctx, oracleQB.Select("*").From("OLD_USERS").Build())
    // ... process and write to mainDB ...
    return nil
}
```

**Key Features:**
- **Mixed vendor support:** Each named database can have a different type (Oracle, PostgreSQL, MongoDB)
- **Backward compatible:** `deps.DB(ctx)` works exactly as before
- **Explicit selection:** `deps.DBByName(ctx, "name")` for clarity
- **Reuses infrastructure:** Same DbManager with LRU, connection pooling, idle cleanup
- **Works with multi-tenant:** Named databases are shared across all tenants

**Testing:**
```go
namedDBs := dbtest.NewNamedDBMap()
namedDBs.ForNameWithVendor("legacy", dbtypes.Oracle).ExpectQuery("SELECT").WillReturnRows(...)
namedDBs.SetDefaultDB(dbtest.NewTestDB(dbtypes.PostgreSQL))

deps := &app.ModuleDeps{
    DB:       namedDBs.AsDBFunc(),
    DBByName: namedDBs.AsDBByNameFunc(),
}
```

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

### Cache Architecture

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management.

**Core Components:**
- **Redis Client**: Atomic operations (Get/Set/GetOrSet/CompareAndSet), connection pooling, health monitoring
- **CacheManager**: Per-tenant cache lifecycle with lazy initialization, LRU eviction, idle cleanup, singleflight
- **CBOR Serialization**: Type-safe encoding with security limits (max 10k array/map elements)
- **TenantStore Integration**: Automatic tenant resolution from context via `deps.GetCache(ctx)`

**Lifecycle Management (CacheManager):**
- **Lazy Initialization**: Cache created on first access per tenant (no upfront connections)
- **LRU Eviction**: Oldest cache evicted when MaxSize exceeded (default: 100 tenants)
- **Idle Cleanup**: Unused caches closed after IdleTTL (default: 15m, checked every 5m)
- **Singleflight**: Prevents duplicate cache creation during concurrent access
- **Lock-Free Close**: Cache close operations don't block Get/Set/Delete operations

**Performance Characteristics:**
- **Latency**: <1ms for Get/Set (localhost), ~2ms for atomic operations (Lua scripts)
- **Throughput**: 100k reads/sec, 80k writes/sec (single Redis instance)
- **CBOR Serialization**: ~83ns/op marshal, ~167ns/op unmarshal (simple structs)
- **Connection Pool**: Default `NumCPU * 2`, configurable via `cache.redis.pool_size`
- **Network Impact**: +0.5-1ms (same datacenter), +50-200ms (cross-region, not recommended)

**Benchmark Results** (Apple M4 Pro, localhost Redis):
| Operation | Performance | Allocations | Notes |
|-----------|-------------|-------------|-------|
| CBOR Marshal (simple) | ~83 ns/op | 96 B/op, 2 allocs | 12M ops/sec |
| CBOR Unmarshal (simple) | ~167 ns/op | 88 B/op, 3 allocs | 6M ops/sec |
| CBOR Marshal (complex) | ~800 ns/op | 400 B/op, 8 allocs | Nested structs, maps, slices |
| CBOR Unmarshal (complex) | ~1200 ns/op | 600 B/op, 15 allocs | Full deserialization |

*Run benchmarks:* `go test -bench=BenchmarkCBOR -benchmem ./cache/`
*Redis benchmarks require:* `docker run -d -p 6379:6379 redis:7-alpine` then `go test -bench=BenchmarkRealRedis -benchmem -tags=integration ./cache/redis/`

**Configuration Example:**
```yaml
cache:
  enabled: true
  type: redis
  manager:
    max_size: 100          # Max tenant cache instances
    idle_ttl: 15m          # Idle timeout per cache
    cleanup_interval: 5m   # Cleanup goroutine frequency
  redis:
    host: localhost
    port: 6379
    password: ${CACHE_REDIS_PASSWORD}  # From environment
    database: 0
    pool_size: 10
```

**Module Setup Pattern:**
```go
type Module struct {
    getCache func(context.Context) (cache.Cache, error)  // Store function, NOT instance
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getCache = deps.GetCache  // Tenant-aware resolution
    return nil
}

func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
    cache, err := s.getCache(ctx)  // Resolves tenant from context
    if err != nil {
        return nil, err
    }

    // Try cache first
    data, err := cache.Get(ctx, fmt.Sprintf("user:%d", id))
    if err == nil {
        return cache.Unmarshal[User](data)
    }

    // Cache miss - query database
    user, err := s.queryDatabase(ctx, id)

    // Store in cache with TTL
    data, _ = cache.Marshal(user)
    cache.Set(ctx, fmt.Sprintf("user:%d", id), data, 5*time.Minute)

    return user, nil
}
```

**Key Operations:**
| Operation | Method | Use Case | Atomicity |
|-----------|--------|----------|-----------|
| Basic read | `Get(ctx, key)` | Query result caching | Single-key |
| Basic write | `Set(ctx, key, value, ttl)` | Store computed result | Single-key |
| Deduplication | `GetOrSet(ctx, key, value, ttl)` | Idempotency keys | Atomic SET NX |
| Distributed lock | `CompareAndSet(ctx, key, expected, new, ttl)` | Job coordination | Lua script CAS |
| Type-safe store | `Marshal(v)` + `Set()` | Struct serialization | CBOR encoding |

**Multi-Tenant Isolation:**
- Each tenant gets separate Redis database (configurable per-tenant)
- Cache instances managed by CacheManager with automatic lifecycle
- Context propagation ensures tenant resolution via `deps.GetCache(ctx)`
- No key collision between tenants (different Redis databases)

**Observability Integration:**
When `observability.enabled: true`, cache operations automatically emit:
- **Traces**: Spans for Get/Set/Delete with `cache.operation`, `cache.key`, `cache.hit` attributes
- **Metrics**: `cache.operation.duration`, `cache.errors.total`, `cache.manager.active_caches`
- **Health**: Automatic integration with `/health` endpoint (Redis PING command)

**For comprehensive examples**, see the Cache Operations section in [llms.txt](llms.txt)

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

**Panic Recovery:** Handler panics are automatically recovered and treated identically to errors:
- Panic recovered with stack trace logging
- Message nacked WITHOUT requeue (consistent with error policy)
- Service continues processing other messages
- Metrics recorded with panic error type
- Other consumers remain unaffected (panic isolation)

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

#### Consumer Concurrency (v0.17+)

**Breaking Change (v0.17.0):** Default worker count changed from 1 to `runtime.NumCPU() * 4` for optimal I/O-bound performance (20-30x throughput improvement).

**Smart Auto-Scaling:**
GoBricks automatically configures `Workers = runtime.NumCPU() * 4` to handle blocking I/O operations (database queries, HTTP calls, file operations). The 4x multiplier ensures CPU utilization while threads wait on I/O.

**Configuration:**
```go
// Auto-scaling (default): Workers = NumCPU * 4, PrefetchCount = Workers * 10
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:     "orders",
    Consumer:  "processor",
    EventType: "order.created",
    Handler:   handler,
}, queue)
// 8-core machine: 32 workers, 320 prefetch

// Explicit sequential (for message ordering)
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:     "ordered.events",
    Consumer:  "sequencer",
    EventType: "event.sequence",
    Workers:   1,  // Sequential processing
    Handler:   handler,
}, queue)

// Custom high concurrency
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:         "batch.processing",
    Consumer:      "batch-worker",
    EventType:     "batch.import",
    Workers:       100,          // Explicit
    PrefetchCount: 500,          // Explicit
    Handler:       handler,
}, queue)
```

**Thread-Safety Requirements:**
- Handlers MUST be thread-safe (no shared mutable state without locks/atomic operations)
- Database pools MUST be sized: `MaxOpenConns >= NumCPU * 4 * NumConsumers`
- External APIs: Add semaphore for rate limit enforcement if needed
- Test with `go test -race` to detect data races

**Resource Safeguards:**
- Workers capped at 200 per consumer (prevents goroutine explosion)
- PrefetchCount capped at 1000 (prevents memory exhaustion)
- Warnings logged when caps are applied

**Performance Impact (8-core machine, 100ms handler):**
| Version | Workers | Throughput | Speedup |
|---------|---------|------------|---------|
| v0.16.x | 1 | 10 msg/sec | Baseline |
| v0.17.0 | 32 | 320 msg/sec | **32x** |

**When to Override Defaults:**
- **Workers=1**: Message ordering required (events must be processed sequentially)
- **Workers>NumCPU*4**: Very slow handlers (>1s per message) or high throughput needs
- **Workers<NumCPU*4**: CPU-bound handlers (rare - most handlers are I/O-bound)

**Observability:**
- Startup logs include `workers` and `prefetch` counts
- Each worker logs with `worker_id` for debugging
- OpenTelemetry metrics track per-consumer throughput

### Observability

**Key Features:** W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, environment-aware batching (500ms dev, 5s prod), environment-aware export timeouts (10s dev, 60s prod)

**Go Runtime Metrics:** Auto-exports memory, goroutines, CPU, scheduler latency, GC config when `observability.enabled: true`. Follows [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/runtime/go-metrics/)

**Export Timeout Configuration:** GoBricks uses environment-aware export timeouts to balance fail-fast feedback (development) with network resilience (production):
- **Development/stdout:** 10s (quick failure detection for debugging)
- **Production:** 60s (accommodates network latency, TLS handshake, batch transmission)
- **Override via YAML:** `observability.trace.export.timeout: "90s"` (applies to traces/metrics/logs)
- **Why 60s?** Real-world production scenarios involve cross-region latency, TLS negotiation, and 512-span batch transmission to remote OTLP endpoints

**Dual-Mode Logging:** `DualModeLogProcessor` routes logs by `log.type`:
- **Action logs** (`log.type="action"`): Always exported at 100% (request summaries)
- **Trace logs** (`log.type="trace"`): ERROR/WARN always exported, INFO/DEBUG sampled by `sampling_rate`
- Configure via `observability.logs.sampling_rate` (0.0-1.0, default 0.0 drops INFO/DEBUG)
- Sampling is deterministic per trace (all logs in same trace sampled together)

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

#### Custom Metrics

GoBricks exposes `MeterProvider` via `ModuleDeps` for creating application-specific metrics. When `observability.enabled: false`, a no-op provider is used with zero overhead.

**Available in ModuleDeps:**
- `deps.MeterProvider` - OpenTelemetry MeterProvider for creating custom instruments

**Helper Functions (observability/metrics.go):**
- `CreateCounter(meter, name, description)` - Monotonically increasing values (requests, errors)
- `CreateHistogram(meter, name, description)` - Distributions (latency, size)
- `CreateUpDownCounter(meter, name, description)` - Values that increase/decrease (connections, queue depth)

**Pattern:**
1. Store `MeterProvider` in module struct
2. Create instruments in `Init()` (one-time, cached)
3. Record values in business logic with attributes

**Quick Example:**
```go
type OrderModule struct {
    meterProvider metric.MeterProvider
    orderCounter  metric.Int64Counter
}

func (m *OrderModule) Init(deps *app.ModuleDeps) error {
    m.meterProvider = deps.MeterProvider
    if m.meterProvider != nil {
        meter := m.meterProvider.Meter("orders")
        m.orderCounter, _ = observability.CreateCounter(meter, "orders.created.total", "Total orders created")
    }
    return nil
}

func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
    // Record metric with attributes
    if s.orderCounter != nil {
        s.orderCounter.Add(ctx, 1,
            metric.WithAttributes(
                attribute.String("order_type", req.Type),
                attribute.String("status", "success"),
            ),
        )
    }
    // ... business logic
}
```

**Metric Types:**

| Type | Use Case | Example |
|------|----------|---------|
| `Int64Counter` | Monotonically increasing counts | Requests, errors, events |
| `Float64Histogram` | Value distributions | Latency (seconds), payload size (bytes) |
| `Int64UpDownCounter` | Values that increase/decrease | Active connections, queue depth |
| `Int64ObservableGauge` | Current state via callback | Memory usage, pool size |

**Best Practices:**
- Pre-create instruments in `Init()` for performance (avoid per-request creation)
- Use semantic naming: `<namespace>.<entity>.<measurement>` (e.g., `orders.processing.duration`)
- Add attributes for dimensions: `status`, `tenant_id`, `operation_type`
- Nil-check instruments when recording (safe when observability disabled)
- Test with no-op provider: `noop.NewMeterProvider()` from `go.opentelemetry.io/otel/metric/noop`

**Real-World Example:** See `scheduler/module.go` for production usage with counter, histogram, and panic tracking.

See [llms.txt](llms.txt) Custom Metrics section for complete code examples including observable gauges and testing patterns.

#### Observability Headers & Authentication

**IMPORTANT:** Headers (API keys, bearer tokens) MUST be configured in YAML files, NOT via environment variables. GoBricks does not support `OBSERVABILITY_*_HEADERS_*` env vars.

**Recommended Approach (Production):**

Create environment-specific config files with headers:

```yaml
# config.production.yaml (DO NOT commit - add to .gitignore)
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  # Traces
  traces:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here

  # Metrics (reuses traces endpoint)
  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here

  # Logs (reuses traces endpoint)
  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here
```

**Security Best Practices:**

1. **Never commit secrets to git:**
   ```bash
   # .gitignore
   config.production.yaml
   config.staging.yaml
   config.*.local.yaml
   ```

2. **Use separate config files per environment:**
   - `config.yaml` - Base config (committed, no secrets)
   - `config.production.yaml` - Production secrets (NOT committed)
   - `config.development.yaml` - Dev/local settings (committed or git-ignored)

3. **Alternative: Read from secret managers**
   ```go
   // In main.go before app.Run()
   apiKey := os.Getenv("OTEL_API_KEY")  // From AWS Secrets Manager, Vault, etc.

   // Override config programmatically
   cfg.Observability.Traces.Headers = map[string]string{
       "api-key": apiKey,
   }
   ```

**Vendor-Specific Examples:**

```yaml
# New Relic
headers:
  api-key: your-license-key

# Honeycomb
headers:
  x-honeycomb-team: your-team-key

# Datadog
headers:
  dd-api-key: your-api-key

# Grafana Cloud
headers:
  authorization: "Basic base64-encoded-credentials"

# Generic Bearer Token
headers:
  authorization: "Bearer your-token"
```

#### New Relic OTLP Integration (Optimized)

GoBricks supports all New Relic OTLP optimizations for bandwidth reduction and performance:

**Complete New Relic Configuration (gRPC - Recommended):**

```yaml
# config.production.yaml
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  # Traces with gRPC (recommended by New Relic)
  trace:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # gRPC port (NO https:// prefix)
    protocol: grpc
    insecure: false  # TLS required for New Relic
    compression: gzip  # ~70% bandwidth reduction
    headers:
      api-key: your-new-relic-license-key-here

  # Metrics with New Relic optimizations
  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    temporality: delta  # New Relic recommendation (lower memory, better performance)
    histogram_aggregation: exponential  # Better precision, ~10x lower memory
    headers:
      api-key: your-new-relic-license-key-here

  # Logs with gRPC
  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    sampling_rate: 0.1  # Export 10% of INFO/DEBUG logs (ERROR/WARN always exported)
    headers:
      api-key: your-new-relic-license-key-here
```

**New Relic HTTP Alternative (Port 4318):**

```yaml
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  trace:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/traces  # HTTP requires https:// + path
    protocol: http
    compression: gzip
    headers:
      api-key: your-license-key

  metrics:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/metrics  # Signal-specific path required
    protocol: http
    compression: gzip
    temporality: delta
    histogram_aggregation: exponential
    headers:
      api-key: your-license-key

  logs:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/logs  # Signal-specific path required
    protocol: http
    compression: gzip
    headers:
      api-key: your-license-key
```

**New Relic Port 443 Alternative:**

New Relic supports both gRPC and HTTP on port 443 (default HTTPS port). This simplifies firewall rules:

```yaml
# gRPC on port 443
trace:
  endpoint: otlp.nr-data.net:443  # Explicit port 443 for gRPC
  protocol: grpc

# HTTP on port 443 (port implicit when using https://)
trace:
  endpoint: https://otlp.nr-data.net/v1/traces  # Port 443 implicit
  protocol: http
```

**New Relic Configuration Options Explained:**

| Option | Values | Default | New Relic Recommendation |
|--------|--------|---------|--------------------------|
| `compression` | `gzip`, `none` | `gzip` | **gzip** (~70% bandwidth reduction) |
| `temporality` | `delta`, `cumulative` | `cumulative` | **delta** (lower memory, better performance) |
| `histogram_aggregation` | `exponential`, `explicit` | `explicit` | **exponential** (better precision, ~10x lower memory) |
| `protocol` | `http`, `grpc` | `http` | **grpc** (lower latency, better performance) |

**New Relic Attribute Limits:**

GoBricks automatically handles New Relic's attribute limits, but be aware of:
- **Maximum attributes per span/metric/log:** 255 attributes
- **Maximum attribute value size:** 4095 bytes
- **Truncation behavior:** Attributes exceeding limits are dropped with warning logs

**Performance Impact:**

| Feature | Bandwidth Savings | Memory Savings | Notes |
|---------|-------------------|----------------|-------|
| gzip compression | ~70% | N/A | CPU overhead ~1-2ms per batch |
| Delta temporality | N/A | ~50% | Resets counters after each export |
| Exponential histograms | ~30% | ~90% | MaxSize=160, MaxScale=20 (auto-configured) |

**Endpoint Format Rules (CRITICAL):**

| Protocol | Endpoint Format | Example | TLS |
|----------|-----------------|---------|-----|
| `grpc` | `host:port` (NO scheme) | `otlp.nr-data.net:4317` | Auto-enabled for 4317 |
| `grpc` (insecure) | `host:port` + `insecure: true` | `localhost:4317` | Disabled |
| `http` | `https://host:port/path` | `https://otlp.nr-data.net:4318/v1/traces` | Enabled |
| `http` (insecure) | `http://host:port/path` | `http://localhost:4318/v1/traces` | Disabled |

**Common Mistakes:**
- ❌ `https://otlp.nr-data.net:4317` with `protocol: grpc` → **ERROR: "too many colons in address"**
- ❌ `otlp.nr-data.net:4318` with `protocol: http` → **ERROR: missing scheme**
- ✅ `otlp.nr-data.net:4317` with `protocol: grpc` → **Correct**
- ✅ `https://otlp.nr-data.net:4318/v1/traces` with `protocol: http` → **Correct**

**Insecure gRPC Example (localhost):**
```yaml
traces:
  endpoint: localhost:4317  # No https://
  protocol: grpc
  insecure: true  # Disable TLS for local testing
```

**Validation:** GoBricks validates endpoint format at startup (fail-fast). Invalid combinations return `ErrInvalidEndpointFormat`.

**Why Not Environment Variables?**

Environment variables like `OBSERVABILITY_TRACES_HEADERS_API_KEY` would require complex parsing logic that conflicts with Koanf's nested key handling. The explicit YAML approach:
- ✅ Aligns with "Explicit > Implicit" manifesto principle
- ✅ Matches industry standard (Docker Compose, Kubernetes ConfigMaps)
- ✅ Simpler implementation, fewer edge cases
- ✅ Users control exact header names (no automatic `_` → `-` conversion)

#### OpenTelemetry Collector (Recommended for Production)

The OpenTelemetry Collector is a **vendor-agnostic proxy** that sits between your application and observability backends. GoBricks supports direct export to vendors (New Relic, Datadog, Honeycomb), but using a collector provides significant production benefits.

**Why Use a Collector?**

| Feature | Direct Export | With OTEL Collector |
|---------|---------------|---------------------|
| **Retry Logic** | Limited (SDK retries 5x) | Advanced retry with exponential backoff |
| **Buffering** | Small in-memory buffers | Large disk-backed queues |
| **Batching** | Fixed batch sizes | Dynamic batching based on backend load |
| **Vendor Lock-In** | Coupled to vendor | Change backends without code changes |
| **Resource Usage** | App memory/CPU for retries | Offloaded to collector |
| **Multi-Backend** | Not supported | Send to multiple backends simultaneously |
| **Data Transformation** | Not supported | Filter, sample, enrich telemetry data |

**Common Deployment Patterns:**

**1. Sidecar Pattern (Kubernetes)**

Each application pod runs a collector sidecar:

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-service
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-service:v1.0.0
        env:
        - name: OBSERVABILITY_TRACE_ENDPOINT
          value: "localhost:4317"  # Collector sidecar
        - name: OBSERVABILITY_TRACE_PROTOCOL
          value: "grpc"
      - name: otel-collector
        image: otel/opentelemetry-collector-contrib:0.96.0
        ports:
        - containerPort: 4317  # gRPC receiver
        - containerPort: 4318  # HTTP receiver
        volumeMounts:
        - name: collector-config
          mountPath: /etc/otelcol
      volumes:
      - name: collector-config
        configMap:
          name: otel-collector-config
```

**Benefits:** Simple network configuration (localhost), isolated per service, automatic scaling with pods

**2. DaemonSet Pattern (Kubernetes)**

One collector per node, shared by all pods on that node:

```yaml
# GoBricks config (points to node-local collector)
observability:
  trace:
    endpoint: ${NODE_IP}:4317  # DaemonSet collector on host network
    protocol: grpc
```

**Benefits:** Lower resource overhead, simplified configuration, node-level batching

**3. Gateway Pattern (High Volume)**

Central collector cluster with load balancing:

```yaml
# GoBricks config (points to load balancer)
observability:
  trace:
    endpoint: otel-gateway.monitoring.svc.cluster.local:4317
    protocol: grpc
    compression: gzip  # Reduce network traffic to gateway
```

**Benefits:** Centralized retry/buffering, horizontal scaling, advanced processing pipelines

**Minimal Collector Configuration (New Relic):**

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:
    timeout: 10s
    send_batch_size: 1024
    send_batch_max_size: 2048

  # Retry with exponential backoff
  retry:
    enabled: true
    initial_interval: 5s
    max_interval: 30s
    max_elapsed_time: 300s  # 5 minutes total retry window

exporters:
  otlp:
    endpoint: otlp.nr-data.net:4317
    compression: gzip
    headers:
      api-key: ${NEW_RELIC_LICENSE_KEY}
    retry_on_failure:
      enabled: true
      initial_interval: 5s
      max_interval: 30s
      max_elapsed_time: 300s

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch, retry]
      exporters: [otlp]
    metrics:
      receivers: [otlp]
      processors: [batch, retry]
      exporters: [otlp]
    logs:
      receivers: [otlp]
      processors: [batch, retry]
      exporters: [otlp]
```

**When to Use Collector:**

| Scenario | Recommendation |
|----------|----------------|
| **Development/Staging** | Direct export (simpler, faster iteration) |
| **Production (low volume)** | Direct export with compression (simplicity > overhead) |
| **Production (high volume)** | Collector (retry logic, buffering critical) |
| **Multi-region** | Collector per region (reduces cross-region latency) |
| **Multi-vendor** | Collector (send to multiple backends without app changes) |
| **Compliance/Filtering** | Collector (PII scrubbing, data sampling) |

**GoBricks Configuration with Collector:**

```yaml
# Application points to collector, collector handles vendor specifics
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  trace:
    endpoint: otel-collector:4317  # Collector endpoint
    protocol: grpc
    insecure: true  # Collector often on same network (no TLS needed)
    compression: gzip  # Reduce app→collector network traffic
    # NO headers needed - collector handles vendor auth

  metrics:
    enabled: true
    endpoint: otel-collector:4317
    protocol: grpc
    compression: gzip
    temporality: delta  # Collector can convert if needed
    histogram_aggregation: exponential

  logs:
    enabled: true
    endpoint: otel-collector:4317
    protocol: grpc
    compression: gzip
```

**Collector Resources:**
- [OpenTelemetry Collector Documentation](https://opentelemetry.io/docs/collector/)
- [Collector Configuration Reference](https://opentelemetry.io/docs/collector/configuration/)
- [Collector Contrib Distributions](https://github.com/open-telemetry/opentelemetry-collector-contrib) (vendor-specific exporters)

## Testing Guidelines

### Test Naming Conventions

**MANDATORY: Use camelCase for ALL test function names**

```go
// ✅ CORRECT - camelCase naming
func TestUserServiceCreateUser(t *testing.T) { }
func TestCacheManagerGetOrCreateCache(t *testing.T) { }
func TestQueryBuilderWithComplexJoins(t *testing.T) { }

// ❌ WRONG - snake_case (NEVER use this)
func TestUserService_CreateUser(t *testing.T) { }
func Test_CacheManager_GetOrCreateCache(t *testing.T) { }
func TestQueryBuilder_with_complex_joins(t *testing.T) { }
```

**Table-Driven Test Naming:**
```go
// ✅ CORRECT
func TestFilterEq(t *testing.T) {
    tests := []struct {
        name     string  // Use snake_case for test case descriptions
        column   string
        value    any
        expected string
    }{
        {name: "simple_equality", column: "id", value: 1, expected: "id = :1"},
        {name: "string_value", column: "name", value: "Alice", expected: "name = :1"},
    }
}

// ❌ WRONG - function name uses underscores
func Test_Filter_Eq(t *testing.T) { }
```

**Rationale:**
- **Consistency:** GoBricks enforces camelCase across the entire codebase
- **Go Idioms:** Test function names are regular Go identifiers (prefer camelCase)
- **Tooling:** Some tools parse test names assuming camelCase convention
- **Legacy Code:** All existing tests use camelCase (>800 test functions)

**Exception:** Test case descriptions in table-driven tests use snake_case for readability (e.g., `name: "with_invalid_credentials"`)

### Testing Strategy
- **Unit tests:** testify, database/testing (database), httptest (server), fake adapters (messaging)
- **Integration tests:** testcontainers (MongoDB), `-tags=integration` flag
- **Race detection:** All tests run with `-race` in CI
- **Coverage target:** 80% (SonarCloud)

### Database Testing

GoBricks provides `database/testing` package for easy database mocking without sqlmock complexity (**73% less boilerplate**).

**Simple Query Test:**
```go
import dbtest "github.com/gaborage/go-bricks/database/testing"

func TestProductService_FindActive(t *testing.T) {
    // Setup (8 lines vs 30+ with sqlmock)
    db := dbtest.NewTestDB(dbtypes.PostgreSQL)
    db.ExpectQuery("SELECT").
        WillReturnRows(
            dbtest.NewRowSet("id", "name").
                AddRow(int64(1), "Widget").
                AddRow(int64(2), "Gadget"),
        )

    deps := &app.ModuleDeps{
        GetDB: func(ctx context.Context) (database.Interface, error) {
            return db, nil
        },
    }

    svc := NewProductService(deps)
    products, err := svc.FindActive(ctx)

    assert.NoError(t, err)
    assert.Len(t, products, 2)
    dbtest.AssertQueryExecuted(t, db, "SELECT")
}
```

**Transaction Testing:**
```go
db := dbtest.NewTestDB(dbtypes.PostgreSQL)
tx := db.ExpectTransaction().
    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)

// Test code that uses transactions
svc.CreateWithItems(ctx, order, items)

dbtest.AssertCommitted(t, tx)
```

**Multi-Tenant Testing:**
```go
tenants := dbtest.NewTenantDBMap()
tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
tenants.ForTenant("globex").ExpectQuery("SELECT").WillReturnRows(...)

deps := &app.ModuleDeps{
    GetDB: tenants.AsGetDBFunc(),  // Resolves tenant from context
}

ctx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(ctx)  // Uses acme's TestDB
```

**Key Features:**
- Fluent expectation API (ExpectQuery/ExpectExec)
- Multi-tenant support via TenantDBMap
- Transaction tracking (commit/rollback assertions)
- Vendor-agnostic RowSet builder
- Partial SQL matching by default (or strict with StrictSQLMatching())

See [database/testing](database/testing/) package and [llms.txt:294](llms.txt:294) for full examples.

### Cache Testing

GoBricks provides `cache/testing` package for easy cache mocking without Redis dependencies (**similar to database/testing pattern**).

**Simple Cache Test:**
```go
import cachetest "github.com/gaborage/go-bricks/cache/testing"

func TestUserServiceCaching(t *testing.T) {
    mockCache := cachetest.NewMockCache()

    deps := &app.ModuleDeps{
        GetCache: func(ctx context.Context) (cache.Cache, error) {
            return mockCache, nil
        },
    }

    svc := NewUserService(deps)
    user, err := svc.GetUser(ctx, 123)

    assert.NoError(t, err)
    cachetest.AssertCacheHit(t, mockCache, "user:123")
}
```

**Configurable Failures:**
```go
mockCache := cachetest.NewMockCache().
    WithGetFailure(cache.ErrConnectionError)

// Service should gracefully degrade
user, err := svc.GetUser(ctx, 123)  // Falls back to database
assert.NoError(t, err)

// Verify cache operation was attempted
cachetest.AssertOperationCount(t, mockCache, "Get", 1)
```

**Multi-Tenant Testing:**
```go
tenantCaches := map[string]*cachetest.MockCache{
    "acme":   cachetest.NewMockCache(),
    "globex": cachetest.NewMockCache(),
}

deps := &app.ModuleDeps{
    GetCache: func(ctx context.Context) (cache.Cache, error) {
        tenantID := multitenant.GetTenant(ctx)
        return tenantCaches[tenantID], nil
    },
}

acmeCtx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(acmeCtx)  // Uses acme's MockCache
```

**Key Features:**
- Fluent configuration API (`WithGetFailure`, `WithDelay`, `WithCloseCallback`)
- Operation tracking (Get/Set/Delete/GetOrSet/CompareAndSet counts)
- 20+ assertion helpers (`AssertCacheHit`, `AssertOperationCount`, `AssertValue`)
- TTL expiration testing (real time-based expiration)
- Multi-tenant isolation support

See [cache/testing](cache/testing/) package for full API documentation and the Cache Testing Utilities section in [llms.txt](llms.txt) for comprehensive examples.

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
| **Oracle** | `:1`, `:2` | Automatic reserved word quoting, service name/SID options, **SEQUENCE support (built-in), UDT registration for custom types** |
| **PostgreSQL** | `$1`, `$2` | pgx driver with optimized connection pooling |
| **MongoDB** | N/A | Document-based with SQL-like interface, aggregation pipeline |

### Connection Pool Defaults

GoBricks applies production-safe connection pool defaults when database is configured:

| Setting | Default | Purpose |
|---------|---------|---------|
| `pool.max.connections` | 25 | Maximum open connections |
| `pool.idle.connections` | 2 | Minimum warm connections |
| `pool.idle.time` | 5m | Close idle connections (prevents stale connections) |
| `pool.lifetime.max` | 30m | Force periodic recycling (DNS, memory hygiene) |
| `pool.keepalive.enabled` | true | TCP keep-alive probes |
| `pool.keepalive.interval` | 60s | Probe interval (below NAT timeouts) |

**Cloud Provider Idle Timeouts:**
| Provider | Component | Timeout |
|----------|-----------|---------|
| AWS | NAT Gateway/ALB | 350s |
| GCP | Cloud NAT | 30s |
| Azure | NAT Gateway | 240s |
| On-prem | Firewalls | 60-300s |

**Override defaults** in `config.yaml`:
```yaml
database:
  pool:
    idle:
      time: 3m          # More aggressive recycling
    lifetime:
      max: 15m          # Shorter lifetime
    keepalive:
      interval: 30s     # More frequent probes
```

### Messaging Reconnection Defaults

GoBricks applies production-safe AMQP reconnection defaults when messaging is configured:

| Setting | Default | Purpose |
|---------|---------|---------|
| `reconnect.delay` | 5s | Initial delay before reconnect attempts |
| `reconnect.reinit_delay` | 2s | Delay between channel re-initialization |
| `reconnect.resend_delay` | 5s | Delay before resending failed messages |
| `reconnect.connection_timeout` | 30s | Timeout for connection establishment |
| `reconnect.max_delay` | 60s | Maximum backoff cap for exponential retry |
| `publisher.max_cached` | 50 | Maximum cached publisher channels |
| `publisher.idle_ttl` | 10m | TTL for idle publisher channels |

**Override defaults** in `config.yaml`:
```yaml
messaging:
  reconnect:
    delay: 10s            # Slower initial reconnect
    max_delay: 120s       # Higher backoff cap
  publisher:
    max_cached: 100       # More cached publishers for high-throughput
    idle_ttl: 30m         # Keep publishers longer
```

### Cache Manager Defaults

GoBricks applies production-safe cache manager defaults when cache is configured:

| Setting | Default | Purpose |
|---------|---------|---------|
| `manager.max_size` | 100 | Maximum tenant cache instances |
| `manager.idle_ttl` | 15m | Close idle cache connections |
| `manager.cleanup_interval` | 5m | Frequency of idle cache cleanup |

**Override defaults** in `config.yaml`:
```yaml
cache:
  manager:
    max_size: 200         # Support more tenants
    idle_ttl: 30m         # Keep caches longer
    cleanup_interval: 10m # Less frequent cleanup
```

### Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization:

| Setting | Default | Purpose |
|---------|---------|---------|
| `startup.timeout` | 10s | Overall startup timeout (also serves as fallback for unset components) |
| `startup.database` | 10s | Database connection establishment |
| `startup.messaging` | 10s | AMQP broker connection |
| `startup.cache` | 5s | Redis connection |
| `startup.observability` | 15s | OTLP endpoint connection (higher for TLS handshake) |

**Fallback Hierarchy:**
1. Explicit component value (e.g., `startup.database: 15s`) → preserved
2. Global timeout (if set): `startup.timeout: 30s` → applied to all unset components
3. Per-component default (shown in table) → used when neither is set

**Example - Global fallback:**
```yaml
app:
  startup:
    timeout: 30s  # All components inherit 30s (database, messaging, cache, observability)
```

**Override defaults** in `config.yaml`:
```yaml
app:
  startup:
    timeout: 30s          # Longer overall timeout
    database: 15s         # More time for slow databases
    observability: 30s    # More time for remote OTLP endpoints
```

### Oracle SEQUENCE Objects (No Configuration Required)

Oracle SEQUENCE objects for ID generation work immediately with standard queries:

```go
// Get next sequence value
var id int64
err := conn.QueryRow(ctx, "SELECT user_seq.NEXTVAL FROM DUAL").Scan(&id)

// Use in INSERT
_, err = conn.Exec(ctx, "INSERT INTO users VALUES (user_seq.NEXTVAL, :1)", name)
```

**No UDT registration needed** - SEQUENCE returns standard NUMBER type.

### Oracle User-Defined Types (Require Registration)

For custom object/collection types created with `CREATE TYPE`, use UDT registration:

**When UDT Registration Required:**
- Bulk insert/update with TABLE OF collections
- Stored procedures with custom object parameters
- Functions returning complex types

**Quick Example:**
```go
type Product struct {
    ID    int64  `udt:"ID"`
    Name  string `udt:"NAME"`
    Price float64 `udt:"PRICE"`
}

oracleConn := conn.(*oracle.Connection)

// Register collection type
err := oracleConn.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})

// Bulk insert
products := []Product{{ID: 1, Name: "Widget", Price: 19.99}, ...}
_, err = conn.Exec(ctx, "BEGIN bulk_insert_products(:1); END;", products)
```

**For comprehensive examples**, see [llms.txt](llms.txt) Oracle SEQUENCE vs UDT section.

**Common Error:** `"call register type before use user defined type"`
**Solution:** Call `RegisterType()` during initialization (does NOT affect SEQUENCE queries).

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
- **Test Matrix:** Ubuntu/Windows × Go 1.25
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

**Connection Pool Issues (ORA-01013, connection reset):**

```bash
# ORA-01013: "user requested cancel of current operation" after idle period
# → This indicates stale connections being used after NAT/firewall timeout
# → GoBricks applies production-safe defaults automatically:
#   - Pool.KeepAlive.Enabled: true (60s probes prevent silent drops)
#   - Pool.Idle.Time: 5m (recycle idle connections before timeout)
#   - Pool.Lifetime.Max: 30m (periodic connection recycling)
# → For custom configuration, ensure keepalive interval < NAT timeout

# Override defaults for aggressive environments (e.g., strict firewall):
database:
  pool:
    keepalive:
      enabled: true
      interval: 30s       # Probe every 30s for strict firewalls
    idle:
      time: 2m            # Close idle after 2 minutes
    lifetime:
      max: 15m            # Recycle all connections every 15 minutes

# For on-premises with no NAT/firewall concerns, opt-out of recycling:
database:
  pool:
    idle:
      time: 0             # 0 = no idle timeout (not recommended for cloud)
    lifetime:
      max: 1h             # Longer lifetime acceptable without NAT
```

**Cache Issues:**

```bash
# "cache not configured" errors
# → Set cache.enabled: true AND cache.redis.host in config
# → OR verify multi-tenant cache config in multitenant.tenants.<tenant_id>.cache

# Connection failures
# → Check Redis server running: redis-cli ping
# → Verify cache.redis.port matches Redis instance (default: 6379)
# → Check firewall rules if Redis on different host

# Multi-tenant cache issues
# → Use deps.GetCache(ctx), NOT deps.Cache (function vs field)
# → Ensure tenant context set: multitenant.SetTenant(ctx, tenantID)
# → Verify tenant has cache.enabled: true in tenant config

# Cache timeout errors
# → Increase operation timeout: ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
# → Check network latency if Redis on different host
# → Verify pool size adequate: cache.redis.pool_size >= NumCPU * 2

# Cache hit/miss issues
# → Check TTL not expired: cache.Set(ctx, key, data, ttl)
# → Verify key consistency across Set/Get operations
# → Check CBOR serialization/deserialization for custom types

# CacheManager eviction issues
# → Increase max_size if seeing unexpected evictions: cache.manager.max_size
# → Increase idle_ttl if caches closing too quickly: cache.manager.idle_ttl
# → Monitor stats: cacheManager.Stats() - check Evictions/IdleCleanups counters
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

# gRPC error: "frame header looked like an HTTP/1.1 header" (New Relic)
# ERROR: rpc error: code = Unavailable desc = connection error: desc = "error reading server preface:
#        http2: failed reading the frame payload: http2: frame too large, note that the frame header
#        looked like an HTTP/1.1 header"
#
# ROOT CAUSE: gRPC client connecting to HTTP endpoint (port mismatch)
#
# SOLUTIONS:
# 1. Using port 4318 with protocol: grpc → WRONG (4318 is HTTP port)
#    FIX: Change endpoint to otlp.nr-data.net:4317 (gRPC port)
#
# 2. Using https:// scheme with gRPC protocol → WRONG (gRPC doesn't accept scheme)
#    WRONG: endpoint: https://otlp.nr-data.net:4317
#    FIX:   endpoint: otlp.nr-data.net:4317 (no https://)
#
# 3. Missing TLS configuration → Check insecure: false (New Relic requires TLS)
#
# CORRECT NEW RELIC GRPC CONFIG:
observability:
  trace:
    endpoint: otlp.nr-data.net:4317  # NO https://, port 4317 for gRPC
    protocol: grpc
    insecure: false  # TLS required
    compression: gzip
    headers:
      api-key: your-license-key

# HTTP endpoint format errors
# → HTTP requires https:// or http:// scheme: https://otlp.nr-data.net:4318/v1/traces
# → gRPC requires NO scheme, just host:port: otlp.nr-data.net:4317
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

# Handler panics crashing service (v0.16+: auto-recovered)
# → Panics are now automatically recovered with stack trace logging
# → Messages nacked without requeue (same as errors)
# → Check ERROR logs for "Panic recovered in message handler" with stack traces
# → Service continues processing other messages (no downtime)

# Diagnostic commands
grep "Starting AMQP consumers" logs/app.log
grep "Multiple consumers registered for same queue" logs/app.log
grep "Panic recovered in message handler" logs/app.log
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
