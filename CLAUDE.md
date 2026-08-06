# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle), and clean architecture patterns.

**Requirements:**

- Go 1.26 required
- Docker Desktop or Docker Engine (integration tests only)

## Workflow Rules

- Always run `make check` before committing and pushing. Never commit or push without a passing build.
- When fixing lint/build errors, run `make check` after each fix cycle. Common issues: import ordering, trailing newlines, type narrowing errors.
- **Before pushing code, run the three pre-push gates IN ORDER: `/simplify` → `/security-audit` → `/code-review` (CodeRabbit).** **This is mandatory** — the cost (a few minutes of agent time) is negligible compared to the cost of missing a finding (review-cycle ping-pong on top of a real bug). The order is load-bearing: `/simplify` applies reuse/simplification/efficiency cleanups first (it *mutates* the diff, so it must run before anything that judges the diff); `/security-audit` then audits the refactored result (credential leaks, boundary-validation gaps, panic/race classes on shutdown paths, and other threat-model issues that style-focused bots don't reason about); `/code-review` (CodeRabbit) renders the final independent verdict on the end state. After the agent gates settle, run `make mutate` once as the final machine gate before pushing (surviving mutants on changed lines block the push; see wiki/testing.md#mutation-gate); if any later fix changes code after it ran, re-run it — the same rule as `make check`. Any gate that changes code requires `make check` again before the next gate; if findings are applied after CodeRabbit's pass, re-run `/code-review` — CodeRabbit must always see the final diff; if a third pass still returns findings, push and note the open ones in the PR body. The trivial-fixes exception is **narrowly** defined: single-line typo fixes, comment/doc-only changes, and dependency bumps. Multi-file changes, new functionality (even tests-only), and config additions beyond a single value all need all three gates. When in doubt, run them.
- **Run `make mutate` and `make check` in the background (`run_in_background`), not the foreground.** A real `make mutate` run is ~440s median and the foreground command ceiling is 600s — a run that crosses it is killed, returns nothing, and costs the full ten minutes again on the re-run. Backgrounding also makes the deliberate `MUTATE_CPU`/`MUTATE_COOLDOWN` throttle free in wall-clock terms (it caps the run to 4 cores and sleeps between packages to keep the chassis cool — a thermal budget, not a speed knob; see the Makefile rationale), and lets reading, editing, and review work overlap the gate instead of queueing behind it. Foreground is fine only when the gate is expected to no-op in seconds.
- Once `make check` and the pre-push gates pass, commit and push to the feature branch automatically — no need to wait for the user to ask.
- **Wait for CI with one backgrounded watcher, never a burst of foreground polls.** Launch a single `run_in_background` command that blocks until the run is terminal, then keep working — the harness re-invokes you when it exits. Resolve the run ID from the pushed sha and let the watch block on it: `gh run watch "$(gh run list --commit "$sha" --json databaseId -q '.[0].databaseId')" --interval 30 --exit-status`. Always pass that ID — a bare `gh run view`/`gh run watch` needs a TTY and dies with `run or job ID required when not running interactively` under `run_in_background`. Poll remote state no faster than every 30s; `gh pr checks --watch` defaults to 10s, which polls far harder than waiting on CI needs.
- **Stacked PRs for large features (mandatory above ~400 LoC):** any feature whose total diff would exceed ~400 changed LoC ships as a stack of dependent PRs via the `/gh-stack` skill — never one big PR. Decompose by dependency + logical unit (e.g. core API → integrations → docs/wiring); each PR in the stack must be self-contained: builds, passes `make check`, the pre-push gates, and `make mutate` on its own, and is reviewable without reading later PRs. The base PR targets `main`; each successor targets its predecessor. Merging is bottom-up by the maintainer (merge is admin-gated — never self-merge); after each merge, re-sync the remaining stack with `/gh-stack`. Changes that are independent of each other are NOT a stack — give them separate branches off `main`.
- Keep responses and Claude-authored artifacts (plans, reports, summaries) proportional to the task: cover the substance, and skip filler sections, redundant summaries, and boilerplate.
- **A blocking question costs a fixed minute before its content matters.** Measured locally, a one-question `AskUserQuestion` stalls ~59s at the median and a three-or-four-question one ~114s — roughly double the wait for three to four times the answers. So decide routine calls yourself and carry on, and when you genuinely must ask, batch the open decisions into one call (the tool takes four) rather than draining them one at a time across the session.

## Git Rules

- Always confirm the current Git branch before committing or pushing. Never push directly to `main` unless explicitly instructed.

## PR Review Workflow

- For PR review fix sessions: read every review comment first, implement all fixes, run `make check` plus the pre-push gates, then push once — one coherent diff instead of a CI cycle per comment.
- **Address findings from every automated reviewer, not just CodeRabbit.** SonarCloud's "Quality Gate passed" banner hides the per-PR NEW-issue list — run `/sonar-pr <N>` (`.claude/skills/sonar-pr`) to fetch and triage it; same all-or-nothing standard as CodeRabbit nitpicks: fix or document the skip in the commit message.

## Quick Reference

**Most Common Commands:**

```bash
make check              # Pre-commit: fmt + lint + test + alloc guards + vuln scan + gosec (mirrors CI)
make test               # Unit tests with race detection
make test-integration   # Integration tests (Docker required)
make mutate             # Diff-scoped mutation gate: mutants on changed lines vs origin/main must die (~440s — run in background)
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

## Developer Manifesto

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
- **Backward Compatibility** → Do not preserve it in GoBricks' own API surface: remove obsolete paths instead of adding compatibility layers, fallbacks, or in-code migration shims. Breaking is fine — document it (ADR + [wiki/migrations.md](wiki/migrations.md)), don't shim it. Consumer-facing migration aids (e.g. Raw Response Mode for Strangler Fig migrations of *consumer* legacy APIs) are bounded product features, not compat shims.

### Security Guidelines

- Input validation is **mandatory** at all boundaries (HTTP, messaging, database).
- Raw-SQL escape hatches (`f.Raw()`, `jf.Raw()`, and `database.Raw()`) require an inline `// SECURITY: Manual SQL review completed - <what was verified>` annotation at every call site. The annotation is a forcing function for review and makes call sites grep-discoverable (`git grep -E 'f\.Raw\(|jf\.Raw\(|database\.Raw\('`). The rationale should name the specific property checked: identifier quoting for vendor reserved words, value-side parameterization, absence of user-input concatenation, etc.
- Secrets from environment variables or secret managers (AWS Secrets Manager, HashiCorp Vault).
- No hardcoded credentials, no secrets in logs or error messages. The framework's logger applies a `SensitiveDataFilter` to every log line; for PII not already covered by defaults (PAN variants, SSN, tax ID) extend the list via `log.sensitivefields` in YAML — additive, merged into the defaults — or in code via `app.Options.LoggerFilterConfig`, which REPLACES the whole config: start from `logger.DefaultFilterConfig()` and append, don't hand it a bare struct literal — see [wiki/observability.md#sensitive-data-filtering](wiki/observability.md#sensitive-data-filtering) for the field list, two-seam injection, matching semantics, and defense-in-depth guidance.
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
- **server/** — Echo-based HTTP server, but no `echo.*` type appears on the consumer surface (ADR-034): middleware is `server.MiddlewareFunc`, raw/ready handlers are `server.Handler`, request access goes through `ctx.RequestContext()` / `ctx.Request()` / `ctx.RouteTemplate()` / `ctx.PathParams()`. Duplicate route registration (same method + path) fails startup. Startup route logging via `server.logroutes` ([startup_defaults.md](wiki/startup_defaults.md#startup-route-logging)); HTTPS via `server.tls.*` ([server_tls.md](wiki/server_tls.md), ADR-042); ALB forwarded-client-cert identity via `server.forwardedclientcert.*` — identification, not authorization, so trust rests on deployment posture ([forwarded_client_cert.md](wiki/forwarded_client_cert.md), ADR-043).
- **migration/** — Flyway integration with single- and multi-tenant runners; pairs with the `tools/migration` CLI (`go-bricks-migrate`) for CI/CD fleet rollouts. Emits `migration.applied` audit events via OTel (opt-in `AuditRecorder` for durable delivery); PostgreSQL migrator-vs-runtime role separation; a crash-recoverable per-tenant provisioning state machine; and a deployment **quiesce flag** that pauses worker pickup and tenant fan-out while in-flight work drains. See [multi_tenant_migration.md](wiki/multi_tenant_migration.md), [migration_roles.md](wiki/migration_roles.md), [migration_provisioning.md](wiki/migration_provisioning.md), [migration_quiesce.md](wiki/migration_quiesce.md), [migration_audit.md](wiki/migration_audit.md), [ADR-018](wiki/adr_018_multi_tenant_migration_cli.md), [ADR-019](wiki/adr_019_migration_audit_delivery.md), and [ADR-021](wiki/adr_021_provisioning_state_machine.md).
- **multitenant/** — Tenant identifier resolution from incoming HTTP requests: `header`, `subdomain`, `path`, and `composite` (first-match chain whose `resolver.order` is **required** — no default, ADR-039). Resolution is identification, not authorization — every source is caller-written, so the deployment must still authorize the resolved tenant. Runs before route matching, so per-tenant accessors (`deps.DB(ctx)`, …) consume it transparently. See [multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md).
- **observability/** — OpenTelemetry tracing and metrics
- **outbox/** — Transactional outbox for reliable event publishing (at-least-once delivery). `outbox.tenancy: shared` runs one control-plane ledger (relayed as a single pass, resolved via the empty `""` key) for pool-model multi-tenant deployments that don't need per-tenant fan-out (ADR-041). See [wiki/outbox.md](wiki/outbox.md).
- **inbox/** — Exactly-once consumer-side processing (`InboxProcessor`); consumer-side complement to the transactional outbox. `inbox.tenancy: shared` mirrors the outbox's control-plane ledger mode. See [wiki/outbox.md](wiki/outbox.md).
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

// Optional: declare that this module cannot function without a database.
// Registration — and therefore startup — fails when no database is configured,
// so a service whose database config never reached it aborts instead of booting
// green. Deployments that resolve database config at runtime are exempt
// (multi-tenant, dynamic config source, dynamic resource source).
type DatabaseRequirer interface {
    RequiresDatabase() bool
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

Service-specific configuration with automatic validation: declare a struct with `config:` tags and call `deps.Config.InjectInto(&cfg)` in `Init` (full example in [llms.txt](llms.txt)).

**Struct Tags:** `config:"key.path"` (required), `required:"true"`, `default:"value"`.
**Supported Types:** string, int, int64, float64, bool, time.Duration, `[]string` (comma-separated via env/`default`, native sequence via YAML).
**Configuration Priority:** Environment variables > `config.<env>.yaml` > `config.yaml` > defaults.

### Enhanced Handler Pattern

Type-safe handlers eliminate boilerplate: `func(req T, ctx server.HandlerContext) (server.Result[R], server.IAPIError)`, registered via `server.POST(handlerRegistry, r, "/users", h.createUser)`; request structs carry `json` + `validate` tags (full example in [llms.txt](llms.txt)). Benefits: automatic binding/validation, standardized response envelopes, type safety.

Use `server.ResultWithMeta[R]` when a handler needs to contribute extra entries to the response envelope's `meta` map (pagination `total`/`limit`/`offset`/`hasMore`, deprecation notices, rate-limit headroom). Framework keys `timestamp` and `traceId` remain authoritative — handler values for those keys are dropped with a structured WARN.

For pointer-vs-value request/response trade-offs (file uploads, bulk exports), **Raw Response Mode** for Strangler Fig migrations (legacy-shape JSON without the `data`/`meta` envelope), and the `ResultWithMeta` envelope-meta extension hook, see [wiki/handler_patterns.md](wiki/handler_patterns.md).

### Database Architecture

Unified `database.Interface` supporting PostgreSQL (pgx, `$1` placeholders) and Oracle (`:1` placeholders, SEQUENCE built-in, UDT registration) with vendor-specific SQL generation, type-safe WHERE clauses, performance tracking, and connection pooling.

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

**Escape hatch:** `f.Raw(...)`, `jf.Raw(...)`, and `database.Raw(sql, args...)` (the Execute Helpers' hand-written-SQL adapter, broader than `f.Raw` — it replaces the whole statement, see [wiki/database.md#execute-helpers](wiki/database.md#execute-helpers)) require a `// SECURITY: Manual SQL review completed - <rationale>` annotation at every call site.

**Defaults applied automatically:** Connection pooling (25 max, keepalive 60s), session timezone (`UTC` per ADR-016), Oracle reserved word quoting.

For named databases (multi-DB single-tenant), table aliases, mixed JOIN conditions, subqueries, SELECT expressions, Oracle UDT registration, pool defaults, and session-timezone opt-out, see [wiki/database.md](wiki/database.md).

### Cache Architecture

Redis-based caching with type-safe CBOR serialization, multi-tenant isolation, and automatic lifecycle management. Store the accessor function (`deps.Cache`), NOT a resolved instance — resolution is tenant-aware per call (full example in [llms.txt](llms.txt)).

**Operations:** `Get`, `Set`, `GetOrSet` (atomic SET NX), `CompareAndSet` (Lua CAS), `Marshal`/`Unmarshal` (CBOR). Per-tenant cache instances managed automatically (LRU eviction, idle cleanup, singleflight).

For lifecycle defaults, performance benchmarks, configuration, and multi-tenant patterns, see [wiki/cache.md](wiki/cache.md).

### HTTP Client

Production-ready HTTP client: `httpclient.NewBuilder(logger)` fluent chain (`WithTimeout`, `WithRetries`, `WithW3CTrace`, `WithPeerName`) then `Build()`, which returns `(Client, error)` and fails construction when a `WithTransport`/`WithTLSConfig`/`WithHTTPClient` composition would silently discard TLS material or a caller-supplied `RoundTripper` (ADR-044; full example in [llms.txt](llms.txt)). For full options and interceptor patterns, see [wiki/httpclient.md](wiki/httpclient.md).

### Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention, panic recovery, system APIs at `GET /_sys/job` and `POST /_sys/job/:jobId` (CIDR-restricted), OpenTelemetry instrumentation per job.
Jobs run in **UTC** by default; set `scheduler.timezone` (IANA name; `-` = host-local) to change the zone for all wall-clock schedules.

Jobs implement `Executor` (`Execute(ctx JobContext) error` — JobContext gives JobID, TriggerType, Logger, DB, Messaging, Config) and register in `RegisterJobs(s app.JobRegistrar)` (full example in [llms.txt](llms.txt)).

**Schedule Methods:** `FixedRate(duration)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`, `HourlyAt(minute)`, `MonthlyAt(dayOfMonth, time)`. See [wiki/scheduler.md](wiki/scheduler.md).

### Messaging Architecture

AMQP-based messaging with **validate-once, replay-many** pattern. Declarations validated upfront, replayed per-tenant for isolation. Automatic reconnection with exponential backoff. Context propagation for tenant IDs and tracing.

**Concise declaration pattern (use the helpers, not raw structs):** in `DeclareMessaging`, use `decls.DeclareTopicExchange` / `DeclareQueue` / `DeclareBinding` / `DeclarePublisher` / `DeclareConsumer` (full example in [llms.txt](llms.txt)).

**Critical Rules:**

- Each `queue + consumer_tag + event_type` triple must be registered exactly **once** — duplicates panic at startup.
- Handler errors and panics → message nacked WITHOUT requeue (no infinite retry loops). Make handlers thread-safe and idempotent; use `DeclareQueueWithDLQ` to park failures in a dead-letter queue instead of dropping them (raw `Args["x-dead-letter-exchange"]` remains the custom-topology escape hatch — set Args before registration; see [wiki/messaging.md](wiki/messaging.md)).
- Default consumer concurrency is `runtime.NumCPU() * 4` workers (v0.17+ breaking change). Set `Workers: 1` explicitly when message ordering matters.

For helper API, error handling deep dive, panic recovery, concurrency tuning, and reconnection defaults, see [wiki/messaging.md](wiki/messaging.md).

### Outbox

Transactional outbox for reliable event publishing. Solves the dual-write problem: events written to an outbox table in the **same database transaction** as business data, then delivered to the broker by a background relay job.

Registration order matters: `scheduler.NewModule()` is required (the relay runs as a scheduled job) and `outbox.NewModule()` must register BEFORE consumer modules. Publish inside the business transaction: `s.outbox.Publish(ctx, tx, &app.OutboxEvent{...})` before `tx.Commit` (full example in [llms.txt](llms.txt)).

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

W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, export timeouts gated on `observability.environment` (independent of `app.env`, defaults to `development`) and the signal endpoint — 10s for `development`/`stdout`, 60s otherwise.

**Custom metrics via `deps.MeterProvider`:** nil-check it in `Init`, get a `Meter`, create instruments (full example in [llms.txt](llms.txt)).

**Helper Functions:** `CreateCounter`, `CreateHistogram`, `CreateUpDownCounter` in `observability/metrics.go`. When `observability.enabled: false`, a no-op provider is used (zero overhead, nil-safe).

For dual-mode log routing, runtime metrics, custom-metric patterns, vendor authentication (New Relic/Honeycomb/Datadog), and OTLP collector deployment, see [wiki/observability.md](wiki/observability.md).

**Migration audit events**: every Flyway `migrate` emits a `migration.applied` event via the OTel seam; durable delivery is opt-in (`FlywayMigrator.WithAuditRecorder(sink)` — bounded-queue goroutine, sink errors never abort a migration). Operators MUST supply the principal explicitly (`Config.Audit.Principal`, `provisioning.AuditContext.Principal`) — the framework refuses to infer it and emits `<unspecified>` with a warning. Provisioning transitions (`state.transitioned`) and the quiesce flag (`quiesce.set`/`quiesce.cleared`) go through the same `migration.Emitter` seam, so the audit schema can't drift. See [wiki/migration_audit.md](wiki/migration_audit.md) and [ADR-019](wiki/adr_019_migration_audit_delivery.md).

## Context Deadlines & Timeouts

> **Mental model:** GoBricks treats `context.Context` as the primary carrier of deadlines and cancellation. The framework configures timeouts at every external boundary — HTTP server, HTTP client, database pool, AMQP, Redis, observability exporter, startup — and lets those deadlines propagate. Inside business logic, **the default is to use the inherited deadline**: do not introduce new timeouts unless you have a specific reason to *shorten* what's already in flight.


| Boundary | Config key | Default |
| --- | --- | --- |
| HTTP request handler (deadline on `c.Request().Context()`) | `server.timeout.middleware` | **5s** |
| HTTP server read / write / idle / shutdown | `server.timeout.read`, `server.timeout.write`, `server.timeout.idle`, `server.timeout.shutdown` | 15s / 30s / 60s / 10s |
| Outbound HTTP client | `httpclient.NewBuilder(...).WithTimeout(d)` | 30s |
| Cache (Redis) dial / read / write | `cache.redis.dialtimeout`, `cache.redis.readtimeout`, `cache.redis.writetimeout` | 5s / 3s / 3s |
| AMQP publish readiness pre-flight (cold/reconnecting client); also bounds the startup publisher pre-warm wait (WARN-only, never fails startup) | `messaging.reconnect.readytimeout` | 5s |
| AMQP publish confirmation | `messaging.reconnect.connectiontimeout` | 30s |
| Scheduler slow-job WARN / shutdown | `scheduler.timeout.slowjob`, `scheduler.timeout.shutdown` | 25s / 30s |
| Observability export | `observability.trace.export.timeout`, `observability.metrics.export.timeout`, `observability.logs.export.timeout` | 10s when `observability.environment` is `development` (its default — **not** derived from `app.env`) or the signal's endpoint is `stdout`; 60s otherwise |


**The default pattern is to do nothing** — the request context already carries a 5s deadline, and every framework call propagates it. Shorten only when one sub-operation should fail fast (e.g., cap a cache lookup at 200–500ms so Redis hiccups don't burn the whole request budget). For fire-and-forget background work that must outlive the request, use `context.WithoutCancel(ctx)` to inherit values (trace ID, tenant ID) while severing cancellation — never `context.Background()`.

For the full deep dive (when to shorten, when to detach, common pitfalls, why context-only timeouts), see [wiki/context_deadlines.md](wiki/context_deadlines.md).

## Testing

### Test Naming Conventions (MANDATORY)

**Use camelCase for ALL test function names.** The codebase has 100% compliance across >800 test functions.

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

For Windows-specific test patterns, CI workflow internals, and operational issues, see [wiki/troubleshooting.md](wiki/troubleshooting.md).

## OpenAPI Tool

The OpenAPI generator now lives in its own repository: [**gaborage/go-bricks-openapi**](https://github.com/gaborage/go-bricks-openapi) — static-analysis spec generation, automatic route discovery, typed request/response models. Install with `go install github.com/gaborage/go-bricks-openapi/cmd/go-bricks-openapi@latest`.

## Breaking Changes

GoBricks breaks its own API surface when justified. Greenfield work uses the new APIs only. **The lines below are an index, not the migration** — every entry has a tracked ADR (`wiki/adr_NNN_*.md`), and hops that need one have a table in [wiki/migrations.md](wiki/migrations.md). Read those before upgrading.

- **S8179 (getter naming):** `GetX()` → `X()` across all packages.
- **S8196 (interface naming):** `Job` → `Executor`, `HealthProbe` → `Prober`, `TenantStore` → `DBConfigProvider`.
- **ToSQL standardization (ADR-017):** Insert builders return `types.InsertQueryBuilder` with `ToSQL()`.
- **Session timezone (ADR-016):** default `UTC`; opt out with `database.timezone: "-"`.
- **Consumer concurrency (v0.17.0):** default workers `1` → `NumCPU * 4`; set `Workers: 1` when ordering matters.
- **Message error handling (v2.X):** handler errors and panics cause messages to be nacked without requeue.
- **MongoDB removed (ADR-012):** PostgreSQL and Oracle only.
- **Bounded publish + outbox dead-lettering (ADR-033):** publish returns `ErrPublishRetriesExhausted` after `messaging.reconnect.maxpublishattempts` — match with `errors.Is`, not `==`. `outbox.Store.FetchPending` drops `maxRetries` and gains `MarkDeadLettered`. New keys: `messaging.reconnect.maxpublishattempts`, `outbox.publishtimeout`.
- **Env policy (ADR-022):** `app.env` accepts any conforming string; behavior switches go through `config.IsDevelopment()` / `config.IsProduction()` alias sets, never equality.
- **CORS dev wildcard opt-in (ADR-038):** the dev reflect-any-origin posture also requires `CORS_DEV_WILDCARD=true`; without it, dev fails closed.
- **Composite resolver order required (ADR-039):** `resolver.order` is mandatory — no default.
- **Declaration Args reach the broker (ADR-040):** declare methods take `(ctx, *…Declaration)` and forward `Args`; may surface `406 PRECONDITION_FAILED` against ops-provisioned queues.
- **Database absence vs misconfiguration (ADR-047):** the `database:` block is all-or-nothing — omitting it is supported (`/ready` reports `not_configured`, 200), but setting *any* identity field (`type`, `host`, `port`, `database`, `username`, `password`, `connectionstring`, `oracle.service.name`, `oracle.service.sid`) marks the section as intended, and an incomplete intended block now fails startup. Modules needing a database implement `app.DatabaseRequirer`.
- **httpclient Build fail-closed (ADR-044):** `Build()` returns `(Client, error)` and refuses compositions that would silently discard TLS material or a caller's `RoundTripper`.
- **Readiness strict + sanitized by default (ADR-046, ADR-048):** an absent `cache.critical` means the cache probe IS critical (503 during a Redis outage); every critical probe's 503 body serves a fixed `"<name> unavailable"` unless `HealthStatus.PublicErr` overrides it.

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
- **cmd/seal-payload/** — installable CLI sealing JSON into compact JWE-of-JWS for curl-testing jose endpoints (#776).
- **tools/** — Development tooling (`migration` CLI / `go-bricks-migrate`).
- **wiki/** — Architecture documentation and ADRs.
- **.claude/tasks/** — Development task planning.
- **llms.txt** — Quick reference examples for LLM code generation.
- Tests alongside source files (`*_test.go`).


