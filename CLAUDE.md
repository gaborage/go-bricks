# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Delegation policy (read first)

The session model (Fable) is the orchestrator, not the worker. Fable is the most expensive tier; spend its tokens only on decomposition, judgment calls, synthesis, and talking to the user. Delegate everything else to subagents via the Agent tool, picking the cheapest model that can do the job well:

- `model: "opus"` — the default worker tier. Anything requiring real judgment: implementation, debugging, architecture-aware exploration, adversarial review.
- `model: "sonnet"` — cheap tier for mechanical or low-stakes work: running tests and reporting output, simple greps/lookups with a known target, rote refactors from an exact spec, formatting, screenshot capture, admin chores. If getting it slightly wrong is cheap to catch, use sonnet.

- **Exploration/research**: never read broadly yourself. Spawn `Explore` agents (model: opus) with tightly scoped questions; consume their synthesized reports, not raw files. Trivial "find the file that defines X" lookups can go to sonnet.
- **Implementation**: for any multi-file change, spawn `general-purpose` agents (model: opus) with exact file paths, the relevant doctrine from this file, and a definition of done (tests to run). Independent changes get parallel agents in one message.
- **Verification/review**: adversarial review and blast-radius checks go to opus agents. Plain test runs and lint passes go to sonnet agents.
- Fable itself only edits directly when the change is small (one or two files, already-known locations).
- **Tripwire (added after Fable did a 5-file change inline, 2026-08-26): before the first Edit/Write, count the files the change will touch. Three or more, or any screenshot/browser-proof chore: stop and spawn agents instead. Inline Fable work is only sequential diagnosis (each command depends on the previous answer) and 1-2 file edits.**

Token rules:
- Batch independent agent launches in a single message so they run concurrently.
- Give agents file paths and constraints up front so they don't rediscover this file's contents; paste the relevant doctrine into the prompt.
- Never re-read files an agent already summarized; trust the report, spot-check only what you'll edit.
- Read only the line ranges you need from large files (`docs/core/PROGRESS.md` is 835 lines — read the lessons ledger at the end, not the whole file).
- Don't echo file contents or long diffs back to the user; report conclusions.

## Project Overview

GoBricks is an enterprise-grade Go framework for building microservices with modular, reusable components. It provides a complete foundation for production-ready applications with HTTP servers, AMQP messaging, multi-database connectivity (PostgreSQL/Oracle), and clean architecture patterns.

## Workflow Rules

- Always run `make check` before committing and pushing. Never commit or push without a passing build.
- When fixing lint/build errors, run `make check` after each fix cycle. Common issues: import ordering, trailing newlines, type narrowing errors.
- **Before pushing code, run the three pre-push gates IN ORDER: `/simplify` → `/security-audit` → `/code-review` (CodeRabbit).** **This is mandatory.** The order is load-bearing: `/simplify` applies reuse/simplification/efficiency cleanups first (it *mutates* the diff, so it must run before anything that judges the diff); `/security-audit` then audits the refactored result (credential leaks, boundary-validation gaps, panic/race classes on shutdown paths, and other threat-model issues that style-focused bots don't reason about); `/code-review` (CodeRabbit) renders the final independent verdict on the end state. After the agent gates settle, run `make mutate` once as the final machine gate before pushing (surviving mutants on changed lines block the push; see wiki/testing.md#mutation-gate); if any later fix changes code after it ran, re-run it — the same rule as `make check`. Any gate that changes code requires `make check` again before the next gate; if findings are applied after CodeRabbit's pass, re-run `/code-review` — CodeRabbit must always see the final diff; if a third pass still returns findings, push and note the open ones in the PR body. The trivial-fixes exception is **narrowly** defined: single-line typo fixes, comment/doc-only changes, and dependency bumps. Multi-file changes, new functionality (even tests-only), and config additions beyond a single value all need all three gates. When in doubt, run them.
- **Run `make mutate` and `make check` in the background (`run_in_background`), not the foreground.** A real `make mutate` run is ~440s median and the foreground command ceiling is 600s. Backgrounding also lets reading, editing, and review work overlap the gate instead of queueing behind it, and keeps the deliberate `MUTATE_CPU`/`MUTATE_COOLDOWN` thermal throttle free in wall-clock terms. Foreground is fine only when the gate is expected to no-op in seconds.
- Once `make check` and the pre-push gates pass, commit and push to the feature branch automatically — no need to wait for the user to ask.
- **Wait for CI with one backgrounded watcher, never a burst of foreground polls.** Launch a single `run_in_background` command that blocks until the run is terminal, then keep working — the harness re-invokes you when it exits. Resolve the run ID from the pushed sha and let the watch block on it: `gh run watch "$(gh run list --commit "$sha" --json databaseId -q '.[0].databaseId')" --interval 30 --exit-status`. Always pass that ID — a bare `gh run view`/`gh run watch` needs a TTY and dies with `run or job ID required when not running interactively` under `run_in_background`. Poll remote state no faster than every 30s; `gh pr checks --watch` defaults to 10s, which polls far harder than waiting on CI needs.
- **Stacked PRs for large features (mandatory above ~400 LoC):** a diff over ~400 changed LoC ships as a stack via `/gh-stack`, never one big PR. Decompose by dependency + logical unit; each PR is self-contained: builds, passes `make check`, the gates and `make mutate` alone, reviewable without later PRs. `ci-v2.yml` runs `pull_request` for any base, so each link (base = the branch below, as `gh stack submit` chains them) carries its own checks — judge by check count, not `mergeStateStatus`. Merge bottom-up, maintainer-only; after each, `gh stack sync --prune`. Independent changes get separate branches, not a stack.
- Keep responses and Claude-authored artifacts (plans, reports, summaries) proportional to the task: cover the substance, and skip filler sections, redundant summaries, and boilerplate.
- **A blocking question costs a fixed minute before its content matters.** Measured locally, a one-question `AskUserQuestion` stalls ~59s at the median and a three-or-four-question one ~114s. So decide routine calls yourself and carry on, and when you genuinely must ask, batch the open decisions into one call (the tool takes four) rather than draining them one at a time across the session.

## Git Rules

- Always confirm the current Git branch before committing or pushing. Never push directly to `main` unless explicitly instructed.

## PR Review Workflow

- For PR review fix sessions: read every review comment first, implement all fixes, run `make check` plus the pre-push gates, then push once — one coherent diff instead of a CI cycle per comment.
- **Address findings from every automated reviewer, not just CodeRabbit.** SonarCloud's "Quality Gate passed" banner hides the per-PR NEW-issue list — run `/sonar-pr <N>` (`.claude/skills/sonar-pr`) to fetch and triage it; same all-or-nothing standard as CodeRabbit nitpicks: fix or document the skip in the commit message.

## Quick Reference

**Key File:** [llms.txt](llms.txt) — quick code examples for LLMs (the "full example" target referenced below).

**Wiki (deep dives — read on demand):**

- Architecture: [database.md](wiki/database.md) · [cache.md](wiki/cache.md) · [messaging.md](wiki/messaging.md) · [outbox.md](wiki/outbox.md) · [scheduler.md](wiki/scheduler.md) · [httpclient.md](wiki/httpclient.md) · [jose.md](wiki/jose.md) · [sealing.md](wiki/sealing.md) · [keystore.md](wiki/keystore.md) · [observability.md](wiki/observability.md) · [multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md)
- Patterns: [handler_patterns.md](wiki/handler_patterns.md) · [context_deadlines.md](wiki/context_deadlines.md) · [global_middleware.md](wiki/global_middleware.md) · [testing.md](wiki/testing.md)
- Reference: [troubleshooting.md](wiki/troubleshooting.md) · [migrations.md](wiki/migrations.md) (breaking changes) · [startup_defaults.md](wiki/startup_defaults.md) · [linting.md](wiki/linting.md) (consumer lint config)
- ADRs: [wiki/architecture_decisions.md](wiki/architecture_decisions.md), files `wiki/adr_NNN_*.md`
- Vendor docs: [observability_headers_auth.md](wiki/observability_headers_auth.md) · [new_relic_otlp.md](wiki/new_relic_otlp.md) · [otel_collector.md](wiki/otel_collector.md)

**External Resources:**

- [Demo Project](https://github.com/gaborage/go-bricks-demo-project) — Complete examples
- [SonarCloud](https://sonarcloud.io/project/overview?id=gaborage_go-bricks) — Code quality metrics
- [GitHub Issues](https://github.com/gaborage/go-bricks/issues?q=is%3Aopen%20label%3Akind%2Ffeature) — Technical backlog. Titles use `<area>: <description>` (lowercase); labels combine `area/<package>` with `kind/<type>` or top-level `bug`/`documentation`.

## Developer Manifesto

### Framework Philosophy

GoBricks is a **production-grade framework for building MVPs fast**: enterprise-quality tooling (validation, observability, tracing, type safety) at rapid velocity.

### Core Principles

- **Explicit > Implicit** → No hidden defaults, no magic configuration.
- **Type Safety > Dynamic Hacks** → Breaking changes prioritized for compile-time safety.
- **Deterministic > Dynamic Flow** → Same inputs always produce same outputs.
- **Composition > Inheritance** → Use interfaces and embedding over inheritance.
- **Robustness** → Handle errors idiomatically, wrap once at boundaries; no silent failures.
- **Fail Fast** → Module `Init()` errors are fatal; validation crashes at startup.
- **Patterns, not Over-Design** → Only when they solve real problems; justify abstractions.
- **Security First** → Input validation mandatory; secrets from env/vault; audit raw-SQL escape hatches (below).
- **Context-First Design** → Always pass `context.Context` first (tracing, cancellation, deadlines). See [wiki/context_deadlines.md](wiki/context_deadlines.md).
- **Interface Segregation** → Small, focused interfaces for testability.
- **Vendor Agnosticism** → Abstract high-cost dependencies (databases); embrace low-cost ones.
- **Backward Compatibility** → Do not preserve it in GoBricks' own API surface: remove obsolete paths instead of adding compatibility layers, fallbacks, or in-code migration shims; document the break (ADR + [wiki/migrations.md](wiki/migrations.md)), don't shim it. Consumer-facing migration aids (e.g. Raw Response Mode for Strangler Fig migrations of *consumer* legacy APIs) are bounded product features, not compat shims.

### Security Guidelines

- Input validation is **mandatory** at all boundaries.
- Raw-SQL escape hatches (`f.Raw()`, `jf.Raw()`, `database.Raw()`, an UPDATE `SetExpr()`, and a STRING predicate passed to `Having()`) require an inline `// SECURITY: Manual SQL review completed - <what was verified>` annotation at every call site, which makes them grep-discoverable (`git grep -E 'f\.Raw\(|jf\.Raw\(|database\.Raw\(|SetExpr\(|Having\('`). The rationale should name the property checked: identifier quoting, value-side parameterization, no user-input concatenation. A `qb.Expr()`/`qb.MustExpr()` `RawExpression` is the sanctioned non-string path, exempt for consistency with `Select`/`GroupBy`/`OrderBy` and NOT because it is safer — `Validate()` never inspects the SQL body, so review it as raw SQL and grep it by name (`git grep -nE 'MustExpr\(|[.]Expr\(|RawExpression\{'`).
- A recovered panic value is reported by **type**, never by value (ADR-081): render it with `%T`, and never pass the value itself to `%v`/`%+v`/`%s`/`%q`/`Msgf` or `Interface(…, r)`. The verb is innocent, the operand is the defect — `fmt.Errorf("panic (type: %s)", panicType)` on an already-`%T`-rendered string is correct. The value is consumer-chosen, so the log filter cannot help: it matches field NAMES, and a bare `panic("secret")` or an unlisted map key has none. The rule binds wherever `recover()`'s result becomes an error or a message, not only where it is logged — converting a panic to an error one frame lower routes it down the error path, where every sink prints it. Rendering one by value requires an inline `// SECURITY: panic value - <why this one cannot carry a secret>` annotation, grep-discoverable as `git grep -n 'SECURITY: panic value' -- '*.go'`. Framework recovery sites only.
- Secrets from environment variables or secret managers (AWS Secrets Manager, Vault).
- No hardcoded credentials, no secrets in logs or error messages. The framework's logger applies a `SensitiveDataFilter` to every log line; for PII not already covered by defaults (PAN variants, SSN, tax ID) extend the list via `log.sensitivefields` in YAML — additive, merged into the defaults — or in code via `app.Options.LoggerFilterConfig`, which REPLACES the whole config: start from `logger.DefaultFilterConfig()` and append, don't hand it a bare struct literal — see [wiki/observability.md#sensitive-data-filtering](wiki/observability.md#sensitive-data-filtering) for the field list, two-seam injection, matching semantics, and defense-in-depth guidance. The filter also masks INSIDE an opaque payload (ADR-086): a `json.RawMessage`, `[]byte`, `[]json.RawMessage`, the `Bytes()` door or a JSON-looking string — including a DEFINED type over either (`type Blob []byte`, `type JSONText string`), judged by kind, not spelling — is parsed and walked with the same needles, re-encoded only when something matched, with JWK private members (`kty` marker) and PEM `PRIVATE KEY` blocks masked by shape and an unparseable or over-cap payload masked whole (the cap is `FilterConfig.MaxPayloadBytes`, 64 KiB by default — a code-only field, with no YAML key).
- Audit logging for sensitive operations.

### Framework vs. Application Development

**GoBricks Framework (this codebase):** 80% coverage (SonarCloud enforced), race detection, multi-platform CI. Breaking changes acceptable when justified (documented in ADRs).

**Applications Built with GoBricks:** 60-70% coverage on core business logic, happy paths + critical errors, always test database/HTTP/messaging, defer exotic edge cases.

### Engineering Principles

- **Context Propagation:** No global variables for tenant IDs or trace IDs — always thread context through calls.
- **Documentation:** Just enough to understand quickly; examples over exhaustive docs.

## Architecture

The eight packages whose rules had their own section here — `database/`, `cache/`, `httpclient/`, `messaging/`, `scheduler/`, `observability/`, `outbox/`, `jose/` — now keep them in `<pkg>/CLAUDE.md`, loaded automatically when you work under that directory. Every other package's rules are its Core Components bullet plus the linked wiki page.

### Core Components

- **app/** — Application framework and module system
- **config/** — Configuration management (Koanf: YAML + env vars)
- **database/** — Multi-database interface with query builder
- **cache/** — Redis caching with type-safe CBOR serialization
- **httpclient/** — HTTP client with retries, W3C trace propagation, and interceptors. OpenTelemetry metrics: see [wiki/httpclient.md#metrics](wiki/httpclient.md#metrics). OpenTelemetry tracing (parent Do span + child attempt spans, OTel propagator for `traceparent` injection): see [wiki/httpclient.md#tracing](wiki/httpclient.md#tracing).
- **logger/** — Structured logging (zerolog)
- **messaging/** — RabbitMQ AMQP client. `messaging.tenancy: shared` consumes and publishes once on the control-plane key with the tenant carried as an `x-tenant-id` stamp the framework alone writes and the shared delivery pipeline reads back (ADR-087; the queue's publish ACL is then the tenant boundary). `messaging/streams` consumes/publishes natively and is opt-in at the build graph (ADR-091 — import the package, or a leftover `messaging.streams.uri` fails startup): server-side offsets on success, handlers inline, one goroutine/partition, SAC; publish sync-confirmed, super streams hash-routed; single-tenant or shared tenancy, never per-tenant ([streams.md](wiki/streams.md), ADR-059/063).
- **scheduler/** — gocron-based job scheduling with observability and CIDR-restricted APIs
- **server/** — Echo-based HTTP server, but no `echo.*` type appears on the consumer surface (ADR-034): middleware is `server.MiddlewareFunc`, raw/ready handlers are `server.Handler`, request access goes through `ctx.RequestContext()` / `ctx.Request()` / `ctx.RouteTemplate()` / `ctx.PathParams()`. Duplicate route registration (same method + path) fails startup. Startup route logging via `server.logroutes` ([startup_defaults.md](wiki/startup_defaults.md#startup-route-logging)); HTTPS via `server.tls.*` ([server_tls.md](wiki/server_tls.md), ADR-042); ALB forwarded-client-cert identity via `server.forwardedclientcert.*` — identification, not authorization, so trust rests on deployment posture ([forwarded_client_cert.md](wiki/forwarded_client_cert.md), ADR-043).
- **migration/** — Flyway integration with single- and multi-tenant runners; pairs with the `tools/migration` CLI (`go-bricks-migrate`) for CI/CD fleet rollouts. Emits `migration.applied` audit events via OTel (opt-in `AuditRecorder` for durable delivery); PostgreSQL migrator-vs-runtime role separation; a crash-recoverable per-tenant provisioning state machine; and a deployment **quiesce flag** that pauses worker pickup and tenant fan-out while in-flight work drains. See [multi_tenant_migration.md](wiki/multi_tenant_migration.md), [migration_roles.md](wiki/migration_roles.md), [migration_provisioning.md](wiki/migration_provisioning.md), [migration_quiesce.md](wiki/migration_quiesce.md), [migration_audit.md](wiki/migration_audit.md), [ADR-018](wiki/adr_018_multi_tenant_migration_cli.md), [ADR-019](wiki/adr_019_migration_audit_delivery.md), and [ADR-021](wiki/adr_021_provisioning_state_machine.md).
- **multitenant/** — Tenant identifier resolution from incoming HTTP requests: `header`, `subdomain`, `path`, and `composite` (first-match chain whose `multitenant.resolver.order` is **required** — no default, ADR-039). Resolution is identification, not authorization — every source is caller-written, so the deployment must still authorize the resolved tenant. Runs before route matching, so per-tenant accessors (`deps.DB(ctx)`, …) consume it transparently. See [multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md).
- **observability/** — OpenTelemetry tracing and metrics
- **outbox/** — Transactional outbox for reliable event publishing (at-least-once delivery). `outbox.tenancy: shared` runs one control-plane ledger (relayed as a single pass, resolved via the empty `""` key) for pool-model multi-tenant deployments that don't need per-tenant fan-out (ADR-041). See [wiki/outbox.md](wiki/outbox.md).
- **inbox/** — Exactly-once consumer-side processing (`InboxProcessor`); consumer-side complement to the transactional outbox. `inbox.tenancy: shared` mirrors the outbox's control-plane ledger mode, and `inbox.hold.*` parks a tenant's failed stream deliveries in order and drains them back through the consumer (ADR-089). See [wiki/outbox.md](wiki/outbox.md).
- **keystore/** — Named key-material management: RSA key pairs (DER, or a password-protected PKCS#12 bundle whose password comes from `password.env`/`password.file`, never a literal) and raw symmetric secrets (HMAC/HKDF) from files or base64 env vars; per-entry RSA-or-secret-or-PKCS#12 with a startup mutual-exclusivity check. See [keystore.md](wiki/keystore.md)
- **jose/** — Nested JWE-of-JWS protection on HTTP request and response bodies
- **jose/sealed/** + **messaging/sealed/** — Field-level sealing of AMQP events (ADR-097): one `seal:"subject"` field travels as a compact JWE inside a compact JWS the producer signs; engaged from tags on the typed publish/consume doors, import-gated like streams (keeps `messaging` jose-free and makes a missing import a startup error — not a size win). Tags carry Logical kids, the keystore holds `<logical>-v<N>` generations, `messaging.seal.active` picks the producer's; the seal layer never judges replay — `Meta.DedupKey()` + `inbox.ProcessOnce` do. No accept-unsealed mode. See [sealing.md](wiki/sealing.md)

### Module System

Modules implement `app.Module` (`Name` / `Init(*ModuleDeps)` / `Shutdown`); capabilities are opt-in via duck-typing — the framework detects each interface at startup and calls it: `RouteRegisterer` (HTTP routes), `MessagingDeclarer` (AMQP declarations, validated once and replayed per-tenant), `GlobalMiddlewareRegisterer` (middleware that runs once per request after tenant resolution and cannot be skipped per-route — ADR-036), `DatabaseRequirer` (registration, and therefore startup, fails when no database is configured, so a service whose database config never reached it aborts instead of booting green; multi-tenant, dynamic-config and dynamic-resource deployments are exempt). `ModuleDeps` carries the accessors (`DB`, `Cache`, `Messaging`, `Config`, `Logger`, `Scheduler`, `Outbox`, `Tracer`, `MeterProvider`, `DBByName`, …) — see `app/module.go`.

Registration order is a startup contract: `outbox.NewModule()` needs `scheduler.NewModule()` registered (the relay is a scheduled job) and must itself register BEFORE consumer modules; `keystore.NewModule()` registers BEFORE any module declaring jose-tagged routes (details: `outbox/CLAUDE.md`, `jose/CLAUDE.md`).

### Configuration Injection

Service-specific configuration with automatic validation: declare a struct with `config:` tags and call `deps.Config.InjectInto(&cfg)` in `Init` (full example in [llms.txt](llms.txt)).

**Struct Tags:** `config:"key.path"` (required), `required:"true"`, `default:"value"`.
**Supported Types:** string, int, int64, float64, bool, time.Duration, `[]string` (comma-separated via env/`default`, native sequence via YAML).
**Configuration Priority:** Environment variables > `config.<env>.yaml` > `config.yaml` > defaults.

### Enhanced Handler Pattern

Type-safe handlers eliminate boilerplate: `func(req T, ctx server.HandlerContext) (server.Result[R], server.IAPIError)`, registered via `server.POST(handlerRegistry, r, "/users", h.createUser)`; request structs carry `json` + `validate` tags (full example in [llms.txt](llms.txt)). Benefits: automatic binding/validation, standardized response envelopes, type safety.

Use `server.ResultWithMeta[R]` when a handler needs to contribute extra entries to the response envelope's `meta` map (pagination `total`/`limit`/`offset`/`hasMore`, deprecation notices, rate-limit headroom). Framework keys `timestamp` and `traceId` remain authoritative — handler values for those keys are dropped with a structured WARN.

For pointer-vs-value request/response trade-offs (file uploads, bulk exports), **Raw Response Mode** for Strangler Fig migrations (legacy-shape JSON without the `data`/`meta` envelope), and the `ResultWithMeta` envelope-meta extension hook, see [wiki/handler_patterns.md](wiki/handler_patterns.md).

## Context Deadlines & Timeouts

> **Mental model:** GoBricks treats `context.Context` as the primary carrier of deadlines and cancellation. The framework configures timeouts at every external boundary — HTTP server, HTTP client, database pool, AMQP, Redis, observability exporter, startup — and lets those deadlines propagate. Inside business logic, **the default is to use the inherited deadline**: do not introduce new timeouts unless you have a specific reason to *shorten* what's already in flight.

**The default pattern is to do nothing** — the request context already carries a 5s deadline, and every framework call propagates it. Shorten only when one sub-operation should fail fast (e.g., cap a cache lookup at 200–500ms so Redis hiccups don't burn the whole request budget). For fire-and-forget background work that must outlive the request, use `context.WithoutCancel(ctx)` to inherit values (trace ID, tenant ID) while severing cancellation — never `context.Background()`.

For the per-boundary defaults table and the full deep dive (when to shorten, when to detach, common pitfalls, why context-only timeouts), see [wiki/context_deadlines.md](wiki/context_deadlines.md).

## Testing

### Test Naming Conventions (MANDATORY)

**Use camelCase for ALL test function names** (`TestUserServiceCreateUser`, never `TestUserService_CreateUser`). 100% compliance across >800 test functions; the `check-test-conventions.sh` PostToolUse hook flags violations.

**Exception:** table-driven case `name` fields use **snake_case** (`"with_invalid_credentials"`) for readability.

### Testing Strategy

- **Unit tests:** testify, `database/testing`, `cache/testing`, `outbox/testing`, httptest (server), fake adapters (messaging).
- **Integration tests:** testcontainers, `-tags=integration`.
- **Race detection:** all tests run with `-race` in CI.
- **Coverage target:** 80% (SonarCloud).

For the testing utilities (TestDB fluent expectations, TenantDBMap, MockCache configurable failures, MockOutbox event tracking, testcontainers patterns), see [wiki/testing.md](wiki/testing.md).

## Development Workflow

### Branch Model

- Main branch: `main` (stable releases).
- Feature branches: `feature/*`.

### CI/CD Pipeline

CI is the single path-filtered `ci-v2.yml` workflow. Framework jobs run on Go and build-file changes (the `framework` filter's `**/*.go` intentionally also matches `tools/**/*.go`, so tool changes re-run the framework matrix); the `tools/migration` CLI additionally has its own path-gated jobs.

For Windows-specific test patterns, CI workflow internals, and operational issues, see [wiki/troubleshooting.md](wiki/troubleshooting.md).

## OpenAPI Tool

The OpenAPI generator now lives in its own repository: [**gaborage/go-bricks-openapi**](https://github.com/gaborage/go-bricks-openapi) — static-analysis spec generation, automatic route discovery, typed request/response models. Install with `go install github.com/gaborage/go-bricks-openapi/cmd/go-bricks-openapi@latest`.

## Breaking Changes

GoBricks breaks its own API surface when justified. Greenfield work uses the new APIs only. The per-change index — one line per break with its ADR, config keys, and match rules — lives in the `breaking-changes` skill ([.claude/skills/breaking-changes/SKILL.md](.claude/skills/breaking-changes/SKILL.md)); every `fix(scope)!:` adds an entry there alongside its ADR (`wiki/adr_NNN_*.md`) and [wiki/migrations.md](wiki/migrations.md) atom. Read those before upgrading.

## Agent skills

### Issue tracker

GitHub Issues via `gh`; titles `<area>: <description>`, labels `area/*` + `kind/*` or `bug`/`documentation`, plus `needs-triage`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context; ADRs live in `wiki/adr_NNN_*.md`, not `docs/adr/`. See `docs/agents/domain.md`.
