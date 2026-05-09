# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle), and clean architecture patterns.

**Requirements:**
- Go 1.25 required
- Docker Desktop or Docker Engine (integration tests only)

## Workflow Rules

- Always run `make check` (or `make check-all` for API changes) before committing and pushing. Never commit or push without a passing build.
- When fixing lint/build errors, run `make check` after each fix cycle rather than assuming the fix is correct. Common issues: import ordering, trailing newlines, type narrowing errors.
- **Before pushing code, run the `/simplify` command** to catch reuse, quality, and efficiency issues earlier than CodeRabbit / SonarCloud would. Apply the findings, then push. This avoids review-cycle ping-pong.
- After completing code changes, commit and push automatically (if build passes) without waiting for the user to ask.

## Git Rules

- Always confirm the current Git branch before committing or pushing. Never push directly to `main` unless explicitly instructed.

## PR Review Workflow

- For PR review fix sessions: read ALL review comments first, implement all fixes, run `make check`, then push once — not incrementally.

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
- [CLAUDE.md](CLAUDE.md) — This development guide
- [llms.txt](llms.txt) — Quick code examples for LLMs
- [.golangci.yml](.golangci.yml) — Linting configuration

**Wiki (deep dives — read on demand):**
- Architecture: [database.md](wiki/database.md) · [cache.md](wiki/cache.md) · [messaging.md](wiki/messaging.md) · [outbox.md](wiki/outbox.md) · [scheduler.md](wiki/scheduler.md) · [httpclient.md](wiki/httpclient.md) · [jose.md](wiki/jose.md) · [observability.md](wiki/observability.md)
- Patterns: [handler-patterns.md](wiki/handler-patterns.md) · [context-deadlines.md](wiki/context-deadlines.md) · [testing.md](wiki/testing.md)
- Reference: [troubleshooting.md](wiki/troubleshooting.md) · [migrations.md](wiki/migrations.md) (breaking changes) · [startup-defaults.md](wiki/startup-defaults.md)
- ADRs: [wiki/architecture_decisions.md](wiki/architecture_decisions.md), files `wiki/adr-NNN-*.md`
- Vendor docs: [observability-headers-auth.md](wiki/observability-headers-auth.md) · [new-relic-otlp.md](wiki/new-relic-otlp.md) · [otel-collector.md](wiki/otel-collector.md)

**External Resources:**
- [Demo Project](https://github.com/gaborage/go-bricks-demo-project) — Complete examples
- [SonarCloud](https://sonarcloud.io/project/overview?id=gaborage_go-bricks) — Code quality metrics
- [GitHub Issues](https://github.com/gaborage/go-bricks/issues?q=is%3Aopen%20label%3Akind%2Ffeature) — Technical backlog. Titles use `<area>: <description>` (lowercase); labels combine `area/<package>` with `kind/<type>` or top-level `bug`/`documentation`.

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
- **Security First** → Input validation mandatory, secrets from env/vault, audit raw-SQL escape hatches (see Security Guidelines below).
- **Context-First Design** → Always pass `context.Context` as first parameter for tracing, cancellation, deadlines. See [wiki/context-deadlines.md](wiki/context-deadlines.md).
- **Interface Segregation** → Small, focused interfaces for testability (e.g., `Client` vs `AMQPClient`).
- **Vendor Agnosticism** → Abstract high-cost dependencies (databases), embrace low-cost ones (HTTP frameworks).

### Security Guidelines
- Input validation is **mandatory** at all boundaries (HTTP, messaging, database).
- Raw-SQL escape hatches (`f.Raw()` and `jf.Raw()`) require an inline `// SECURITY: Manual SQL review completed - <what was verified>` annotation at every call site. The annotation is a forcing function for review and makes call sites grep-discoverable (`git grep -E 'f\.Raw\(|jf\.Raw\('`). The rationale should name the specific property checked: identifier quoting for vendor reserved words, parameterization of value sides, absence of user-input concatenation, etc.
- Secrets from environment variables or secret managers (AWS Secrets Manager, HashiCorp Vault).
- No hardcoded credentials, no secrets in logs or error messages.
- Audit logging for sensitive operations (access control, data modifications).

### Practices & Patterns
- **SOLID** → Apply when it simplifies, don't force it.
- **Fail Fast** → Module `Init()` errors are fatal. Validation errors crash at startup, never degrade silently.
- **DRY** → Don't repeat yourself (but avoid premature abstractions).
- **CQS** → Separate commands vs. queries where it adds clarity.
- **KISS** → Keep it simple, complexity must earn its place.
- **YAGNI** → Don't build what isn't needed *today*.

### Framework vs. Application Development

**GoBricks Framework (this codebase):** 80% coverage (SonarCloud enforced), race detection, multi-platform CI, production-grade stability. Breaking changes acceptable when justified (documented in ADRs).

**Applications Built with GoBricks:** 60-70% coverage on core business logic, focus on happy paths + critical errors, always test database/HTTP/messaging, defer exotic edge cases, iterate on requirements.

### Engineering Principles
- **Observability:** OpenTelemetry standards, W3C traceparent propagation across HTTP/messaging.
- **12-Factor App:** Environment variables for config, stateless design, explicit dependencies.
- **Error Handling:** Idiomatic Go errors (`fmt.Errorf`, `errors.Is/As`), structured errors at API boundaries.
- **Context Propagation:** No global variables for tenant IDs or trace IDs — always thread context through calls.
- **Automation:** Makefile/Taskfile for common tasks, multi-platform CI/CD pipelines.
- **Documentation:** Just enough for others to understand quickly, examples over exhaustive docs.

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
- Linting: `.golangci.yml` with staticcheck, gosec, gocritic.
- SonarCloud: Project `gaborage_go-bricks`, 80% coverage target.
- CI/CD: Multi-platform (Ubuntu, Windows) × Go 1.25.
- Race detection enabled on all platforms.

## Architecture

### Core Components
- **app/** — Application framework and module system
- **config/** — Configuration management (Koanf: YAML + env vars)
- **database/** — Multi-database interface with query builder
- **cache/** — Redis caching with type-safe CBOR serialization
- **httpclient/** — HTTP client with retries, W3C trace propagation, and interceptors
- **logger/** — Structured logging (zerolog)
- **messaging/** — AMQP client for RabbitMQ
- **scheduler/** — gocron-based job scheduling with observability and CIDR-restricted APIs
- **server/** — Echo-based HTTP server
- **migration/** — Flyway integration
- **observability/** — OpenTelemetry tracing and metrics
- **outbox/** — Transactional outbox for reliable event publishing (at-least-once delivery)
- **keystore/** — Named RSA key pair management from DER files or base64 env vars
- **jose/** — Nested JWE-of-JWS protection on HTTP request and response bodies

### Module System

Modules implement this core interface. Route registration and messaging are opt-in via duck-typing: if your module implements `RouteRegisterer` or `MessagingDeclarer`, the framework detects this at startup and calls the corresponding method automatically.

```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    Shutdown() error
}

// Optional: register HTTP routes during startup.
type RouteRegisterer interface {
    RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
}

// Optional: declare AMQP exchanges, queues, bindings, publishers, and consumers.
// Declarations are validated once at startup and replayed per-tenant for isolation.
type MessagingDeclarer interface {
    DeclareMessaging(decls *messaging.Declarations)
}

// Simplified — see app/module.go for the full struct (~12 fields including
// Scheduler, Outbox, Tracer, MeterProvider, DBByName, etc.)
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

**Struct Tags:** `config:"key.path"` (required), `required:"true"`, `default:"value"`.
**Supported Types:** string, int, int64, float64, bool, time.Duration.
**Configuration Priority:** Environment variables > `config.<env>.yaml` > `config.yaml` > defaults.

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

Benefits: automatic binding/validation, standardized response envelopes, type safety.

For pointer-vs-value request/response trade-offs (file uploads, bulk exports) and **Raw Response Mode** for Strangler Fig migrations (legacy-shape JSON without the `data`/`meta` envelope), see [wiki/handler-patterns.md](wiki/handler-patterns.md).

### Database Architecture

Unified `database.Interface` supporting PostgreSQL and Oracle with vendor-specific SQL generation, type-safe WHERE clauses, performance tracking, and connection pooling.

| Database | Placeholders | Notes |
|----------|--------------|-------|
| **Oracle** | `:1`, `:2` | Auto reserved word quoting, SEQUENCE built-in, UDT registration for custom types |
| **PostgreSQL** | `$1`, `$2` | pgx driver with optimized connection pooling |

**Type-Safe Query Building (use this pattern by default):**

```go
type User struct {
    ID    int64  `db:"id"`
    Name  string `db:"name"`
    Level int    `db:"level"`  // Oracle reserved word — auto-quoted
}

cols := qb.Columns(&User{})  // Cached per vendor
f := qb.Filter()

query := qb.Select(cols.Fields("ID", "Name")...).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT "ID", "NAME" FROM users WHERE "LEVEL" = :1
```

**Type-Safe Methods:** `f.Eq`, `f.NotEq`, `f.Lt/Lte/Gt/Gte`, `f.In/NotIn`, `f.Like`, `f.Regex*`, `f.JSONContains` (PG only), `f.Null/NotNull`, `f.Between`, `f.Exists`, `f.NotExists`, `f.InSubquery`. Use `qb.Expr()` for complex SQL inside type-safe methods (no placeholders).

**Escape hatch:** `f.Raw(...)` and `jf.Raw(...)` require a `// SECURITY: Manual SQL review completed - <rationale>` annotation at every call site.

**Defaults applied automatically:** Connection pooling (25 max, keepalive 60s), session timezone (`UTC` per ADR-016), Oracle reserved word quoting.

For named databases (multi-DB single-tenant), table aliases, mixed JOIN conditions, subqueries, SELECT expressions, Oracle UDT registration, pool defaults, and session-timezone opt-out, see [wiki/database.md](wiki/database.md).

### Cache Architecture

Redis-based caching with type-safe CBOR serialization, multi-tenant isolation, and automatic lifecycle management.

```go
type Module struct {
    getCache func(context.Context) (cache.Cache, error)  // Store the function, NOT instance
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getCache = deps.Cache  // Tenant-aware resolution
    return nil
}

func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
    c, err := s.getCache(ctx)
    if err != nil { return nil, err }
    if data, err := c.Get(ctx, fmt.Sprintf("user:%d", id)); err == nil {
        return cache.Unmarshal[*User](data)
    }
    // Cache miss — fall through to DB, then Set with TTL.
}
```

**Operations:** `Get`, `Set`, `GetOrSet` (atomic SET NX), `CompareAndSet` (Lua CAS), `Marshal`/`Unmarshal` (CBOR). Per-tenant cache instances managed automatically (LRU eviction, idle cleanup, singleflight).

For lifecycle defaults, performance benchmarks, configuration, and multi-tenant patterns, see [wiki/cache.md](wiki/cache.md).

### HTTP Client

Production-ready HTTP client with builder pattern, W3C trace propagation, retries with backoff, and interceptors:

```go
client := httpclient.NewBuilder(logger).
    WithTimeout(10 * time.Second).
    WithRetries(3, 500 * time.Millisecond).
    WithW3CTrace(true).
    Build()

resp, err := client.Get(ctx, &httpclient.Request{URL: "https://api.example.com/users"})
```

For full options and interceptor patterns, see [wiki/httpclient.md](wiki/httpclient.md).

### Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention, panic recovery, system APIs at `GET /_sys/jobs` and `POST /_sys/job/:jobId` (CIDR-restricted), OpenTelemetry instrumentation per job.

```go
type Executor interface {
    Execute(ctx JobContext) error  // JobContext gives JobID, TriggerType, Logger, DB, Messaging, Config
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
}
```

**Schedule Types:** `Every(duration)`, `Cron(expression)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`. See [wiki/scheduler.md](wiki/scheduler.md).

### Messaging Architecture

AMQP-based messaging with **validate-once, replay-many** pattern. Declarations validated upfront, replayed per-tenant for isolation. Automatic reconnection with exponential backoff. Context propagation for tenant IDs and tracing.

**Concise declaration pattern (use the helpers, not raw structs):**
```go
func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
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
}
```

**Critical Rules:**
- Each `queue + consumer_tag + event_type` triple must be registered exactly **once** — duplicates panic at startup.
- Handler errors and panics → message nacked WITHOUT requeue (no infinite retry loops). Make handlers thread-safe and idempotent.
- Default consumer concurrency is `runtime.NumCPU() * 4` workers (v0.17+ breaking change). Set `Workers: 1` explicitly when message ordering matters.

For helper API, error handling deep dive, panic recovery, concurrency tuning, and reconnection defaults, see [wiki/messaging.md](wiki/messaging.md).

### Outbox

Transactional outbox for reliable event publishing. Solves the dual-write problem: events written to an outbox table in the **same database transaction** as business data, then delivered to the broker by a background relay job.

```go
fw.RegisterModules(
    scheduler.NewModule(),  // Required: relay runs as a scheduled job
    outbox.NewModule(),     // Outbox module — register BEFORE consumers
    &myapp.OrderModule{},
)

func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderReq) error {
    tx, err := db.Begin(ctx)
    if err != nil { return err }
    defer tx.Rollback(ctx)

    if _, err = tx.Exec(ctx, "INSERT INTO orders ..."); err != nil { return err }

    payload, _ := json.Marshal(OrderCreatedEvent{OrderID: req.ID})
    if _, err = s.outbox.Publish(ctx, tx, &app.OutboxEvent{
        EventType: "order.created", AggregateID: fmt.Sprintf("order-%d", req.ID),
        Payload: payload, Exchange: "order.events",
    }); err != nil { return err }

    return tx.Commit(ctx)
}
```

**Delivery Guarantee:** At-least-once. Consumers MUST be idempotent; use the `x-outbox-event-id` header for deduplication.

For configuration, event-struct fields, retry behavior, and operational defaults, see [wiki/outbox.md](wiki/outbox.md).

### JOSE Middleware

Nested JWE-of-JWS protection on HTTP request and response bodies. Designed for **Visa Token Services**-style integrations and any partner API requiring sign-then-encrypt outbound and decrypt-then-verify inbound on every payload.

```go
type CreateTokenRequest struct {
    _   struct{} `jose:"decrypt=our-signing,verify=visa-vts-verify"`
    PAN string   `json:"pan" validate:"required"`
}

type CreateTokenResponse struct {
    _     struct{} `jose:"sign=our-signing,encrypt=visa-vts-encrypt"`
    Token string   `json:"token"`
}
```

**Strict allowlist:** `RS256`/`PS256` for signing; `RSA-OAEP-256` + `A256GCM` for encryption. `alg=none`, `HS*`, `RSA1_5` rejected at parse time. Bidirectional symmetry enforced (request and response must both have tags or neither). Pre-trust failures emit minimal `{code,message}` plaintext envelopes; post-trust handler errors emit the standard envelope, encrypted.

Register `keystore.NewModule()` BEFORE any module declaring jose-tagged routes. For tag syntax, key resolution, the full failure-mode → `IAPIError` mapping table, replay-protection notes, and test utilities, see [wiki/jose.md](wiki/jose.md).

### Observability

W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, environment-aware export timeouts (10s dev / 60s prod).

**Custom metrics via `deps.MeterProvider`:**
```go
func (m *OrderModule) Init(deps *app.ModuleDeps) error {
    if deps.MeterProvider != nil {
        meter := deps.MeterProvider.Meter("orders")
        m.orderCounter, _ = observability.CreateCounter(meter, "orders.created.total", "Total orders created")
    }
    return nil
}
```

**Helper Functions:** `CreateCounter`, `CreateHistogram`, `CreateUpDownCounter` in `observability/metrics.go`. When `observability.enabled: false`, a no-op provider is used (zero overhead, nil-safe).

For dual-mode log routing, runtime metrics, custom-metric patterns, vendor authentication (New Relic/Honeycomb/Datadog), and OTLP collector deployment, see [wiki/observability.md](wiki/observability.md).

## Context Deadlines & Timeouts

> **Mental model:** GoBricks treats `context.Context` as the primary carrier of deadlines and cancellation. The framework configures timeouts at every external boundary — HTTP server, HTTP client, database pool, AMQP, Redis, observability exporter, startup — and lets those deadlines propagate. Inside business logic, **the default is to use the inherited deadline**: do not introduce new timeouts unless you have a specific reason to *shorten* what's already in flight.

| Boundary | Config key | Default |
|---|---|---|
| HTTP request handler (deadline on `c.Request().Context()`) | `server.timeout.middleware` | **5s** |
| HTTP server read / write / idle / shutdown | `server.timeout.{read,write,idle,shutdown}` | 15s / 30s / 60s / 10s |
| Outbound HTTP client | `httpclient.NewBuilder(...).WithTimeout(d)` | 30s |
| Cache (Redis) dial / read / write | `cache.redis.{dialtimeout,readtimeout,writetimeout}` | 5s / 3s / 3s |
| AMQP connection establishment | `messaging.reconnect.connection_timeout` | 30s |
| Scheduler slow-job WARN / shutdown | `scheduler.timeout.{slowjob,shutdown}` | 25s / 30s |
| Observability export | `observability.trace.export.timeout` | 10s (dev) / 60s (prod) |

**The default pattern is to do nothing** — the request context already carries a 5s deadline, and every framework call propagates it. Shorten only when one sub-operation should fail fast (e.g., cap a cache lookup at 200–500ms so Redis hiccups don't burn the whole request budget). For fire-and-forget background work that must outlive the request, use `context.WithoutCancel(ctx)` to inherit values (trace ID, tenant ID) while severing cancellation — never `context.Background()`.

For the full deep dive (when to shorten, when to detach, common pitfalls, why context-only timeouts), see [wiki/context-deadlines.md](wiki/context-deadlines.md).

## Testing

### Test Naming Conventions (MANDATORY)

**Use camelCase for ALL test function names.** Snake_case in test function names is forbidden. The codebase has 100% compliance across >800 test functions.

```go
// CORRECT
func TestUserServiceCreateUser(t *testing.T) { }
func TestCacheManagerGetOrCreateCache(t *testing.T) { }

// WRONG
func TestUserService_CreateUser(t *testing.T) { }
func Test_CacheManager_GetOrCreateCache(t *testing.T) { }
```

**Exception:** Test case descriptions inside table-driven tests use **snake_case** for readability:
```go
tests := []struct{ name string }{
    {name: "simple_equality"},
    {name: "with_invalid_credentials"},
}
```

### Testing Strategy
- **Unit tests:** testify, `database/testing` (DB mocking), `cache/testing` (cache mocking), `outbox/testing` (outbox mocking), httptest (server), fake adapters (messaging).
- **Integration tests:** testcontainers, `-tags=integration` flag.
- **Race detection:** All tests run with `-race` in CI.
- **Coverage target:** 80% (SonarCloud).

For the testing utilities (TestDB fluent expectations, TenantDBMap, MockCache configurable failures, MockOutbox event tracking, testcontainers patterns), see [wiki/testing.md](wiki/testing.md).

## Development Workflow

### Pre-commit Workflow
```bash
# Daily development (fast feedback)
make check        # Framework only: fmt, lint, test with race detection

# Before committing framework API changes (comprehensive validation)
make check-all    # Framework + tool: catches breaking changes in tool

# Tool-only development
cd tools/openapi && make check
```

**When to use `check-all`:**
- Modifying public interfaces (server, database, config, observability).
- Changing struct tags or validation logic.
- Refactoring shared types or error handling.
- Before creating PRs that touch framework APIs.

### Branch Model
- Main branch: `main` (stable releases).
- Feature branches: `feature/*`.

### CI/CD Pipeline
- **Unified CI (`ci-v2.yml`):** Single workflow with intelligent path-based job execution via `dorny/paths-filter@v3`.
- Framework jobs run only when framework code changes (excludes `tools/**`); tool jobs run only when `tools/openapi/**` changes.
- **Test Matrix:** Ubuntu/Windows × Go 1.25.
- **Coverage:** Merged unit + integration coverage → SonarCloud.

### Tool Selection
| Tool | Primary Use Case |
|------|------------------|
| `make check` | Daily development, pre-commit (fast feedback on framework code) |
| `make check-all` | Before PRs that modify public interfaces |
| `make test-integration` | Testing database/messaging vendor differences (requires Docker) |
| `go test -run TestName` | Debugging specific failing tests |
| SonarCloud | Coverage metrics (80% target), quality gate validation |

For Windows-specific test patterns, CI workflow internals, and operational issues, see [wiki/troubleshooting.md](wiki/troubleshooting.md).

## OpenAPI Tool

```bash
cd tools/openapi
make install                    # Install CLI tool
go-bricks-openapi generate -project . -output docs/openapi.yaml
go-bricks-openapi doctor        # Check compatibility
make demo                       # Test on example service
```

Features: static analysis-based spec generation, automatic route discovery, typed request/response models.

## Breaking Changes

GoBricks has shipped several breaking changes for idiomatic Go conventions. Greenfield work uses the new APIs only — the migration tables are kept in [wiki/migrations.md](wiki/migrations.md) for projects upgrading from older versions:

- **S8179 (getter naming):** `GetX()` → `X()` across all packages (Config, ResourceProvider, ModuleDeps fields, etc.).
- **S8196 (interface naming):** `Job` → `Executor`, `HealthProbe` → `Prober`, `TenantStore` → `DBConfigProvider`, etc.
- **ToSQL standardization (ADR-017):** Insert builders return `types.InsertQueryBuilder` with `ToSQL()` (not `ToSql()`).
- **Session timezone (ADR-016):** Default is now `UTC`. Opt out with `database.timezone: "-"`.
- **Consumer concurrency (v0.17.0):** Default workers `1` → `NumCPU * 4`. Set `Workers: 1` for sequential ordering.
- **Message error handling (v2.X):** Errors and panics now nack without requeue (no infinite retry).
- **MongoDB removed (ADR-012):** Only PostgreSQL and Oracle supported.

## File Organization

- **internal/** — Private packages (reflection utilities, test helpers).
- **testing/** — Framework-wide testing utilities.
  - **testing/mocks/** — Testify-based mocks for database, messaging, query builder interfaces.
  - **testing/fixtures/** — Pre-configured mocks and SQL result builders.
  - **testing/containers/** — Testcontainers helpers (PostgreSQL, Oracle, RabbitMQ, Redis).
- **database/testing/** — Database-specific testing (TestDB, TenantDBMap, fluent expectations).
- **cache/testing/** — Cache-specific testing (MockCache, assertion helpers).
- **observability/testing/** — Test utilities for spans and metrics.
- **outbox/** — Transactional outbox pattern (Publisher, Relay, Store, multi-vendor).
- **outbox/testing/** — Outbox-specific testing (MockOutbox, assertion helpers).
- **keystore/** — Named RSA key pair management (DER files + base64 env vars).
- **keystore/testing/** — KeyStore-specific testing (MockKeyStore, assertion helpers).
- **tools/** — Development tooling (OpenAPI generator).
- **wiki/** — Architecture documentation and ADRs.
- **.claude/tasks/** — Development task planning.
- **llms.txt** — Quick reference examples for LLM code generation.
- Tests alongside source files (`*_test.go`).

## Key Interfaces

```go
// Database — see wiki/database.md for full surface
type Interface interface {
    Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
    Begin(ctx context.Context) (types.Tx, error)
    Health(ctx context.Context) error
    DatabaseType() string
}

// Messaging
type Client interface {
    Publish(ctx context.Context, destination string, data []byte) error
    Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error)
    IsReady() bool
}

// Observability
type Provider interface {
    TracerProvider() *sdktrace.TracerProvider
    MeterProvider() *sdkmetric.MeterProvider
    LoggerProvider() *sdklog.LoggerProvider
    ShouldDisableStdout() bool
    Shutdown(ctx context.Context) error
}

// Outbox
type OutboxPublisher interface {
    Publish(ctx context.Context, tx dbtypes.Tx, event *OutboxEvent) (string, error)
}

// KeyStore (used by JOSE middleware)
type KeyStore interface {
    PublicKey(name string) (*rsa.PublicKey, error)
    PrivateKey(name string) (*rsa.PrivateKey, error)
}
```

## Dependencies

- **Echo v4** — HTTP framework
- **zerolog** — Structured logging
- **pgx/v5** — PostgreSQL driver
- **go-ora/v2** — Oracle driver
- **Squirrel** — SQL query builder
- **Koanf v2** — Configuration management
- **amqp091-go** — RabbitMQ client
- **validator/v10** — Request validation
- **testify** — Testing framework
- **testcontainers-go** — Integration testing
