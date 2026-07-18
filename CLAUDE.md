# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle), and clean architecture patterns.

**Requirements:**
- Go 1.26 required
- Docker Desktop or Docker Engine (integration tests only)

## Workflow Rules

- Always run `make check` before committing and pushing. Never commit or push without a passing build.
- When fixing lint/build errors, run `make check` after each fix cycle rather than assuming the fix is correct. Common issues: import ordering, trailing newlines, type narrowing errors.
- **Before pushing code, run the three pre-push gates IN ORDER: `/simplify` → `/security-audit` → `/code-review` (CodeRabbit).** **This is mandatory** — the cost (a few minutes of agent time) is negligible compared to the cost of missing a finding (review-cycle ping-pong on top of a real bug). The order is load-bearing: `/simplify` applies reuse/simplification/efficiency cleanups first (it *mutates* the diff, so it must run before anything that judges the diff); `/security-audit` then audits the refactored result (credential leaks, boundary-validation gaps, panic/race classes on shutdown paths, and other threat-model issues that style-focused bots don't reason about); `/code-review` (CodeRabbit) renders the final independent verdict on the end state. Any gate that changes code requires `make check` again before the next gate; if findings are applied after CodeRabbit's pass, re-run `/code-review` until it reports 0 findings — CodeRabbit must always see the final diff. The trivial-fixes exception is **narrowly** defined: single-line typo fixes, comment/doc-only changes, and dependency bumps. Multi-file changes, new functionality (even tests-only), and config additions beyond a single value all need all three gates. When in doubt, run them.
- After completing code changes, commit and push automatically (if build passes) without waiting for the user to ask.

## Git Rules

- Always confirm the current Git branch before committing or pushing. Never push directly to `main` unless explicitly instructed.

## PR Review Workflow

- For PR review fix sessions: read ALL review comments first, implement all fixes, run `make check`, then push once — not incrementally.
- **Address findings from every automated reviewer, not just CodeRabbit.** SonarCloud's "Quality Gate passed" banner hides the per-PR NEW-issue list — run `/sonar-pr <N>` (`.claude/skills/sonar-pr`) to fetch and triage it; same all-or-nothing standard as CodeRabbit nitpicks: fix or document the skip in the commit message.

## Quick Reference

**Most Common Commands:**
```bash
make check              # Pre-commit: fmt + lint + test + alloc guards + vuln scan
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
- Architecture: [database.md](wiki/database.md) · [cache.md](wiki/cache.md) · [messaging.md](wiki/messaging.md) · [outbox.md](wiki/outbox.md) · [scheduler.md](wiki/scheduler.md) · [httpclient.md](wiki/httpclient.md) · [jose.md](wiki/jose.md) · [keystore.md](wiki/keystore.md) · [observability.md](wiki/observability.md) · [multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md)
- Patterns: [handler_patterns.md](wiki/handler_patterns.md) · [context_deadlines.md](wiki/context_deadlines.md) · [global_middleware.md](wiki/global_middleware.md) · [testing.md](wiki/testing.md)
- Reference: [troubleshooting.md](wiki/troubleshooting.md) · [migrations.md](wiki/migrations.md) (breaking changes) · [startup_defaults.md](wiki/startup_defaults.md)
- ADRs: [wiki/architecture_decisions.md](wiki/architecture_decisions.md), files `wiki/adr_NNN_*.md`
- Vendor docs: [observability_headers_auth.md](wiki/observability_headers_auth.md) · [new_relic_otlp.md](wiki/new_relic_otlp.md) · [otel_collector.md](wiki/otel_collector.md)

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
- **Context-First Design** → Always pass `context.Context` as first parameter for tracing, cancellation, deadlines. See [wiki/context_deadlines.md](wiki/context_deadlines.md).
- **Interface Segregation** → Small, focused interfaces for testability (e.g., `Client` vs `AMQPClient`).
- **Vendor Agnosticism** → Abstract high-cost dependencies (databases), embrace low-cost ones (HTTP frameworks).

### Security Guidelines
- Input validation is **mandatory** at all boundaries (HTTP, messaging, database).
- Raw-SQL escape hatches (`f.Raw()` and `jf.Raw()`) require an inline `// SECURITY: Manual SQL review completed - <what was verified>` annotation at every call site. The annotation is a forcing function for review and makes call sites grep-discoverable (`git grep -E 'f\.Raw\(|jf\.Raw\('`). The rationale should name the specific property checked: identifier quoting for vendor reserved words, parameterization of value sides, absence of user-input concatenation, etc.
- Secrets from environment variables or secret managers (AWS Secrets Manager, HashiCorp Vault).
- No hardcoded credentials, no secrets in logs or error messages. The framework's logger applies a `SensitiveDataFilter` to every log line; for PCI/PII workloads (PAN, CVV2, OTP) extend the default list via `log.sensitivefields` in YAML or `app.Options.LoggerFilterConfig` in code — see [wiki/observability.md#sensitive-data-filtering](wiki/observability.md#sensitive-data-filtering) for the field list, two-seam injection, matching semantics, and defense-in-depth guidance.
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

## Code Quality

- Linting: `.golangci.yml` with staticcheck, gosec, gocritic.
- Linting: `.golangci.yml` with staticcheck, gosec, gocritic.
- SonarCloud: Project `gaborage_go-bricks`, 80% coverage target.
- CI/CD: Multi-platform (Ubuntu, Windows) × Go 1.26.
- Race detection enabled on all platforms.

## Architecture

### Core Components
- **app/** — Application framework and module system
- **config/** — Configuration management (Koanf: YAML + env vars)
- **database/** — Multi-database interface with query builder
- **cache/** — Redis caching with type-safe CBOR serialization
- **httpclient/** — HTTP client with retries, W3C trace propagation, and interceptors. OpenTelemetry metrics: see [wiki/httpclient.md#metrics](wiki/httpclient.md#metrics). OpenTelemetry tracing (parent Do span + child attempt spans, OTel propagator for `traceparent` injection): see [wiki/httpclient.md#tracing](wiki/httpclient.md#tracing).
- **logger/** — Structured logging (zerolog)
- **messaging/** — AMQP client for RabbitMQ
- **scheduler/** — gocron-based job scheduling with observability and CIDR-restricted APIs
- **server/** — Echo-based HTTP server. Echo stays the engine but no `echo.*` type appears on the consumer surface (ADR-034): custom middleware is `server.MiddlewareFunc` (flat `func(c HandlerContext, next func() error) error`), raw/ready handlers are `server.Handler`, and request access goes through `ctx.RequestContext()` / `ctx.Request()` accessors (the `Echo` field is removed); route template + ordered path params via `ctx.RouteTemplate()` / `ctx.PathParams()` / `ctx.SetPathParams()` (v0.46). Set `server.logroutes` (env `SERVER_LOGROUTES`; tri-state, default dev-on/prod-off) to emit one Info line per registered route at startup (`Route registered  module=… method=… path=…`) — see [startup_defaults.md](wiki/startup_defaults.md#startup-route-logging).
- **migration/** — Flyway integration with single- and multi-tenant runners; pairs with `tools/migration` CLI (`go-bricks-migrate`) for CI/CD fleet rollouts. Emits `migration.applied` audit events on every migrate invocation via OTel by default; opt-in `AuditRecorder` for compliance-grade durable delivery. PostgreSQL migrator-vs-runtime role separation (`ProvisionPGRoles` / `PGRoleProvisioningSQL`) gives auditors a flat *no* to "can the running service alter its own schema?". The `migration/provisioning/` subpackage carries a durable, crash-recoverable per-tenant state machine (`pending → schema_created → role_created → migrated → seeded → ready`, with `cleanup → failed` branches) for dynamic tenant provisioning. A deployment **quiesce flag** (`QuiesceController` / `Executor.WithQuiesce` / `MigrateAllOptions.Quiesce`) lets a deployment-time migration pause worker pickup and tenant fan-out — in-flight work drains, nothing is interrupted — with read-side TTL auto-release (crash-safe, no sweeper) and fail-open on control-plane errors. See [multi_tenant_migration.md](wiki/multi_tenant_migration.md), [migration_roles.md](wiki/migration_roles.md), [migration_provisioning.md](wiki/migration_provisioning.md), [migration_quiesce.md](wiki/migration_quiesce.md), [migration_audit.md](wiki/migration_audit.md), [ADR-018](wiki/adr_018_multi_tenant_migration_cli.md), [ADR-019](wiki/adr_019_migration_audit_delivery.md), and [ADR-021](wiki/adr_021_provisioning_state_machine.md).
- **multitenant/** — Tenant identifier resolution from incoming HTTP requests. Four resolver types: `header` (default `X-Tenant-ID`), `subdomain` (`<tenant>.<domain>`), `path` (1-indexed segment with optional prefix gate; e.g. `/itsp/{tenantID}/...`), and `composite` (first-match fallback chain; `resolver.order` is **required** — no default, composite fails at startup without it. Recommended `[subdomain, path, header]`; header-first if a trusted gateway owns `X-Tenant-ID` — ADR-039). Resolution is identification, not authorization: all three sources are caller-written, so the deployment must still authorize the resolved tenant. All run before route matching so the resolved tenant is in `context.Context` for every middleware and handler. Per-tenant DB/cache/messaging accessors (`deps.DB(ctx)`, etc.) consume the value transparently. See [multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md).
- **observability/** — OpenTelemetry tracing and metrics
- **outbox/** — Transactional outbox for reliable event publishing (at-least-once delivery)
- **inbox/** — Exactly-once consumer-side processing (`InboxProcessor`); consumer-side complement to the transactional outbox
- **keystore/** — Named key-material management: RSA key pairs and raw symmetric secrets (HMAC/HKDF) from files or base64 env vars; per-entry RSA-or-secret with a startup mutual-exclusivity check. See [keystore.md](wiki/keystore.md)
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

// Optional: contribute global middleware (e.g. auth) that runs once per request,
// after tenant resolution, before handlers, and cannot be skipped per-route (ADR-036).
type GlobalMiddlewareRegisterer interface {
    GlobalMiddleware() []server.MiddlewareFunc
}

// Simplified — see app/module.go for the full struct (~12 fields including
// Scheduler, Outbox, Tracer, MeterProvider, DBByName, etc.)
type ModuleDeps struct {
    DB        func(context.Context) (database.Interface, error)
    Logger    logger.Logger
    Messaging func(context.Context) (messaging.AMQPClient, error)
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
**Supported Types:** string, int, int64, float64, bool, time.Duration, `[]string` (comma-separated via env/`default`, native sequence via YAML).
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

server.POST(handlerRegistry, r, "/users", h.createUser)
```

Benefits: automatic binding/validation, standardized response envelopes, type safety.

Use `server.ResultWithMeta[R]` when a handler needs to contribute extra entries to the response envelope's `meta` map (pagination `total`/`limit`/`offset`/`hasMore`, deprecation notices, rate-limit headroom). Framework keys `timestamp` and `traceId` remain authoritative — handler values for those keys are dropped with a structured WARN.

For pointer-vs-value request/response trade-offs (file uploads, bulk exports), **Raw Response Mode** for Strangler Fig migrations (legacy-shape JSON without the `data`/`meta` envelope), and the `ResultWithMeta` envelope-meta extension hook, see [wiki/handler_patterns.md](wiki/handler_patterns.md).

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

query := qb.Select(cols.Cols("ID", "Name")).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT id, name FROM users WHERE "level" = :1
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
    WithPeerName("downstream-service").
    Build()

resp, err := client.Get(ctx, &httpclient.Request{URL: "https://api.example.com/users"})
```

For full options and interceptor patterns, see [wiki/httpclient.md](wiki/httpclient.md).

### Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention, panic recovery, system APIs at `GET /_sys/job` and `POST /_sys/job/:jobId` (CIDR-restricted), OpenTelemetry instrumentation per job.
Jobs run in **UTC** by default; set `scheduler.timezone` (IANA name; `-` = host-local) to change the zone for all wall-clock schedules.

```go
type Executor interface {
    Execute(ctx JobContext) error  // JobContext gives JobID, TriggerType, Logger, DB, Messaging, Config
}

func (m *Module) RegisterJobs(s app.JobRegistrar) error {
    return s.DailyAt("cleanup-job", &CleanupJob{}, scheduler.ParseTime("03:00"))
}
```

**Schedule Methods:** `FixedRate(duration)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`, `HourlyAt(minute)`, `MonthlyAt(dayOfMonth, time)`. See [wiki/scheduler.md](wiki/scheduler.md).

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
- Handler errors and panics → message nacked WITHOUT requeue (no infinite retry loops). Make handlers thread-safe and idempotent; declare `Args["x-dead-letter-exchange"]` on the queue to park failures instead of dropping them (set `Args` on the declaration BEFORE registering — `decls.DeclareQueue` snapshots at register time; see [wiki/messaging.md](wiki/messaging.md)).
- Default consumer concurrency is `runtime.NumCPU() * 4` workers (v0.17+ breaking change). Set `Workers: 1` explicitly when message ordering matters.

For helper API, error handling deep dive, panic recovery, concurrency tuning, and reconnection defaults, see [wiki/messaging.md](wiki/messaging.md).

### Outbox

Transactional outbox for reliable event publishing. Solves the dual-write problem: events written to an outbox table in the **same database transaction** as business data, then delivered to the broker by a background relay job.

```go
for _, m := range []app.Module{
    scheduler.NewModule(), // Required: relay runs as a scheduled job
    outbox.NewModule(),    // Outbox module — register BEFORE consumers
    &myapp.OrderModule{},
} {
    if err := fw.RegisterModule(m); err != nil {
        return err
    }
}

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

**Migration audit events**: every Flyway `migrate` invocation emits a `migration.applied` audit event via the OTel seam (one span + one structured log record). Compliance-grade durability is opt-in via `FlywayMigrator.WithAuditRecorder(sink)`; the sink runs on a bounded-queue goroutine so it cannot stall migrations, and sink errors log but don't abort. Operators MUST supply the principal explicitly (`Config.Audit.Principal` for the engine, `provisioning.AuditContext.Principal` for the orchestrator) — the framework refuses to infer it from IAM/OS and emits `<unspecified>` with a warning when empty. The provisioning state machine emits `state.transitioned` for every persisted edge via the shared `migration.Emitter` (`migration.NewEmitter(log, sink)` + `Executor.WithAudit(...)`), and the deployment quiesce flag emits `quiesce.set`/`quiesce.cleared` through the same seam (`QuiesceController.WithAudit(...)`) — so the audit schema can't drift from `migration.applied`. The `go-bricks-migrate` CLI surfaces the principal via `--applied-by`/`--git-sha`/`--pipeline-run-id` and manages the flag via `quiesce set|clear|status`. See [wiki/migration_audit.md](wiki/migration_audit.md) for the event schema, `ErrorClass` taxonomy, and `AuditRecorder` examples; [ADR-019](wiki/adr_019_migration_audit_delivery.md) for the design rationale.

## Context Deadlines & Timeouts

> **Mental model:** GoBricks treats `context.Context` as the primary carrier of deadlines and cancellation. The framework configures timeouts at every external boundary — HTTP server, HTTP client, database pool, AMQP, Redis, observability exporter, startup — and lets those deadlines propagate. Inside business logic, **the default is to use the inherited deadline**: do not introduce new timeouts unless you have a specific reason to *shorten* what's already in flight.

| Boundary | Config key | Default |
|---|---|---|
| HTTP request handler (deadline on `c.Request().Context()`) | `server.timeout.middleware` | **5s** |
| HTTP server read / write / idle / shutdown | `server.timeout.{read,write,idle,shutdown}` | 15s / 30s / 60s / 10s |
| Outbound HTTP client | `httpclient.NewBuilder(...).WithTimeout(d)` | 30s |
| Cache (Redis) dial / read / write | `cache.redis.{dialtimeout,readtimeout,writetimeout}` | 5s / 3s / 3s |
| AMQP publish readiness pre-flight (cold/reconnecting client); also bounds the startup publisher pre-warm wait (WARN-only, never fails startup) | `messaging.reconnect.readytimeout` | 5s |
| AMQP publish confirmation | `messaging.reconnect.connectiontimeout` | 30s |
| Scheduler slow-job WARN / shutdown | `scheduler.timeout.{slowjob,shutdown}` | 25s / 30s |
| Observability export | `observability.trace.export.timeout` | 10s (dev) / 60s (prod) |

**The default pattern is to do nothing** — the request context already carries a 5s deadline, and every framework call propagates it. Shorten only when one sub-operation should fail fast (e.g., cap a cache lookup at 200–500ms so Redis hiccups don't burn the whole request budget). For fire-and-forget background work that must outlive the request, use `context.WithoutCancel(ctx)` to inherit values (trace ID, tenant ID) while severing cancellation — never `context.Background()`.

For the full deep dive (when to shorten, when to detach, common pitfalls, why context-only timeouts), see [wiki/context_deadlines.md](wiki/context_deadlines.md).

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

### Branch Model
- Main branch: `main` (stable releases).
- Feature branches: `feature/*`.

### CI/CD Pipeline
- **Unified CI (`ci-v2.yml`):** Single workflow with intelligent path-based job execution via `dorny/paths-filter`.
- Framework jobs run on Go and build-file changes (the `framework` filter's `**/*.go` intentionally also matches `tools/**/*.go`, so tool changes re-run the framework matrix); the `tools/migration` CLI additionally has its own path-gated jobs.
- **Test Matrix:** Ubuntu/Windows × Go 1.26.
- **Coverage:** Merged unit + integration coverage → SonarCloud.

### Tool Selection
| Tool | Primary Use Case |
|------|------------------|
| `make check` | Daily development, pre-commit (fast feedback) |
| `make test-integration` | Testing database/messaging vendor differences (requires Docker) |
| `go test -run TestName` | Debugging specific failing tests |
| SonarCloud | Coverage metrics (80% target), quality gate validation |

For Windows-specific test patterns, CI workflow internals, and operational issues, see [wiki/troubleshooting.md](wiki/troubleshooting.md).

## OpenAPI Tool

The OpenAPI generator now lives in its own repository: **[gaborage/go-bricks-openapi](https://github.com/gaborage/go-bricks-openapi)** — static-analysis spec generation, automatic route discovery, typed request/response models. Install with `go install github.com/gaborage/go-bricks-openapi/cmd/go-bricks-openapi@latest`.

## Breaking Changes

GoBricks has shipped several breaking changes for idiomatic Go conventions. Greenfield work uses the new APIs only — the migration tables are kept in [wiki/migrations.md](wiki/migrations.md) for projects upgrading from older versions:

- **S8179 (getter naming):** `GetX()` → `X()` across all packages (Config, ResourceProvider, ModuleDeps fields, etc.).
- **S8196 (interface naming):** `Job` → `Executor`, `HealthProbe` → `Prober`, `TenantStore` → `DBConfigProvider`, etc.
- **ToSQL standardization (ADR-017):** Insert builders return `types.InsertQueryBuilder` with `ToSQL()` (not `ToSql()`).
- **Session timezone (ADR-016):** Default is now `UTC`. Opt out with `database.timezone: "-"`.
- **Consumer concurrency (v0.17.0):** Default workers `1` → `NumCPU * 4`. Set `Workers: 1` for sequential ordering.
- **Message error handling (v2.X):** Errors and panics now nack without requeue (no infinite retry).
- **Bounded publish + outbox dead-lettering (ADR-033):** `messaging.PublishToExchange`/`Publish` now return `ErrPublishRetriesExhausted` (wrapping the cause) after `messaging.reconnect.maxpublishattempts` (default 5) instead of retrying forever — match publish errors with `errors.Is`, not `==`. The outbox relay advances `retry_count` on every failed attempt incl. outages; `outbox.Store.FetchPending` drops its `maxRetries` param and gains `MarkDeadLettered` (parks poison at `maxretries` as `status='failed'`; connectivity never parks). New keys: `messaging.reconnect.maxpublishattempts`, `outbox.publishtimeout`. No DB schema migration.
- **MongoDB removed (ADR-012):** Only PostgreSQL and Oracle supported.
- **Env policy (ADR-022):** `app.env` is no longer enum-validated to `{development, staging, production}`; consumer projects may use any conforming string (`local`, `tst`, `stg`, `prd`, `production-eu`, …). Behavior switches go through `config.IsDevelopment()` / `config.IsProduction()` predicates with documented alias sets (`{development, dev, local}` / `{production, prod, prd}`). `APP_ENV=prd`/`prod` now triggers production-strict CORS; `APP_ENV=dev`/`local` now enable framework dev conveniences and auto-migrate.
- **CORS dev wildcard opt-in (ADR-038):** the dev-permissive reflect-any-origin + `AllowCredentials` CORS posture now requires `CORS_DEV_WILDCARD=true` in addition to a development-alias (or koanf-defaulted) `APP_ENV`; without the flag, dev fails closed like every other env. `CORS_ORIGINS` strict allowlisting is unchanged. The flag is inert outside development aliases.

## File Organization

- **internal/** — Private packages (reflection utilities, test helpers).
- **testing/** — Framework-wide testing utilities.
  - **testing/mocks/** — Testify-based mocks for database, messaging, query builder interfaces.
  - **testing/fixtures/** — Pre-configured mocks and SQL result builders.
  - **testing/containers/** — Testcontainers helpers (PostgreSQL, Oracle, RabbitMQ, Redis).
- **database/testing/** — Database-specific testing (TestDB, TenantDBMap, fluent expectations).
- **cache/testing/** — Cache-specific testing (MockCache, assertion helpers).
- **observability/testing/** — Test utilities for spans and metrics.
- **outbox/testing/** — Outbox-specific testing (MockOutbox, assertion helpers).
- **keystore/testing/** — KeyStore-specific testing (MockKeyStore, assertion helpers).
- **tools/** — Development tooling (`migration` CLI / `go-bricks-migrate`).
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
    Close() error
    IsReady() bool
}

// Observability
type Provider interface {
    TracerProvider() trace.TracerProvider
    MeterProvider() metric.MeterProvider
    LoggerProvider() *sdklog.LoggerProvider
    ShouldDisableStdout() bool
    Shutdown(ctx context.Context) error
    ForceFlush(ctx context.Context) error
}

// Outbox
type OutboxPublisher interface {
    Publish(ctx context.Context, tx dbtypes.Tx, event *OutboxEvent) (string, error)
}

// KeyStore (used by JOSE middleware)
type KeyStore interface {
    PublicKey(name string) (*rsa.PublicKey, error)
    PrivateKey(name string) (*rsa.PrivateKey, error)
    Secret(name string) ([]byte, error) // raw symmetric key material (defensive copy)
}
```
