# Architecture Decision Records (ADRs)

This document serves as an index to all architectural decisions made during the development of the GoBricks framework. Each ADR documents a significant design choice, its context, alternatives considered, and consequences.

## Overview

Architecture Decision Records help us:

- Document **why** decisions were made, not just what was decided
- Understand the context and trade-offs of past decisions
- Onboard new developers with historical architectural context
- Avoid revisiting settled decisions without new information

## ADR Index

### [ADR-001: Enhanced Handler System Implementation](adr_001_enhanced_handler_system.md)

**Date:** 2025-09-12 | **Status:** Accepted

Type-safe HTTP handler system with automatic binding, validation, and standardized response envelopes. Introduces generic handler wrappers, comprehensive request binding via struct tags, and hierarchical error handling.

**Key Benefits:** Eliminates boilerplate, compile-time type safety, consistent API responses

---

### [ADR-002: Custom Base Path and Health Route Configuration](adr_002_base_path_and_health_routes.md)

**Date:** 2025-09-15 | **Status:** Accepted

Configurable base paths for all routes and customizable health check endpoints. Implements RouteRegistrar abstraction with intelligent path handling and nested group support.

**Key Benefits:** Deployment flexibility, infrastructure compatibility, automatic path inheritance

---

### [ADR-003: Database by Intent Configuration](adr_003_database_by_intent.md)

**Date:** 2025-09-17 | **Status:** Accepted

Explicit database configuration requirement with no framework defaults. Database functionality only enabled when explicitly configured, supporting database-free applications.

**Key Benefits:** Deterministic behavior, clear intent, database-free application support

---

### [ADR-004: Lazy Messaging Registry Creation in ModuleRegistry](adr_004_lazy_messaging_registry.md)

**Date:** 2025-09-24 | **Status:** Superseded by [ADR-014](adr_014_slim_module_interface.md)

Lazy initialization of messaging registry to support context-aware dependency resolution in multi-tenant architecture. Used singleflight protection for thread-safe initialization; that protection moved to `messaging.Manager.EnsureConsumers()` under the `MessagingDeclarer` duck-typing pattern introduced by ADR-014.

**Key Benefits:** Maintains encapsulation, context-aware, supports multi-tenant modes

---

### [ADR-005: Type-Safe WHERE Clause Construction](adr_005_type_safe_where_clauses.md)

**Date:** 2025-09-27 | **Status:** Accepted

Compile-time safe WHERE clause construction replacing string-based API. Introduces type-safe filter methods (`f.Eq`, `f.Lt`, etc. on `FilterFactory`) with automatic Oracle identifier quoting.

**Key Benefits:** Eliminates Oracle quoting bugs, compile-time safety, clear responsibility boundaries

---

### [ADR-006: OpenTelemetry Protocol (OTLP) Log Export Integration](adr_006_otlp_log_export.md)

**Date:** 2025-10-10 | **Status:** Accepted

Unified observability with OTLP log export via io.Writer bridge pattern. Automatic trace correlation, dual-mode logging (action logs 100%, trace logs WARN+), and deterministic sampling.

**Key Benefits:** Unified observability stack, automatic correlation, production-ready sampling

---

### [ADR-007: Struct-Based Column Extraction](adr_007_struct_based_columns.md)

**Date:** 2025-10-28 | **Status:** Accepted

Reflection-based column extraction from struct tags with lazy caching. Eliminates column name repetition, provides vendor-aware quoting, and enables refactor-friendly queries.

**Key Benefits:** DRY principle, type safety, Oracle reserved word auto-quoting, sub-nanosecond performance

---

### [ADR-008: Database Testing with Interface Segregation](adr_008_database_testing_interface_segregation.md)

**Date:** 2025-01-10 | **Status:** Accepted

Interface segregation for database testing utilities, enabling 73% less boilerplate with fluent expectation APIs, multi-tenant support, and vendor-agnostic row builders.

**Key Benefits:** Simplified mocking, transaction tracking, partial SQL matching

---

### [ADR-009: Consumer Worker Pool Concurrency with NumCPU × 4 Default](adr_009_consumer_worker_pool_concurrency.md)

**Date:** 2025-01-13 | **Status:** Accepted

Auto-scaling consumer worker pools with `NumCPU * 4` default, replacing single-threaded message processing for 20-30x throughput improvement.

**Key Benefits:** Automatic I/O-bound scaling, configurable concurrency, resource safeguards

---

### [ADR-010: Convert Panic-Based Validation to Error Returns](adr_010_panic_to_error_conversion.md)

**Date:** 2025-11-29 | **Status:** Accepted

Converts panic-based fail-fast validation to idiomatic error returns for SonarCloud reliability compliance (S8148), improving the reliability rating from C to A.

**Key Benefits:** S8148 compliance, idiomatic error handling, improved reliability rating

---

### [ADR-011: Redis Cache Backend with CBOR Serialization](adr_011_redis_cache.md)

**Date:** 2025-11-09 | **Status:** Accepted

Redis-backed caching with type-safe CBOR serialization, multi-tenant isolation via CacheManager, and automatic lifecycle management (LRU eviction, idle cleanup, singleflight). Breaking change: introduces `ModuleDeps` extension (`Cache` field) which is a breaking API change and may require dependent modules to be updated.

**Key Benefits:** Type-safe serialization, tenant isolation, production-safe defaults

---

### [ADR-012: Remove MongoDB Support](adr_012_remove_mongodb_support.md)

**Date:** 2026-02-06 | **Status:** Accepted

Complete removal of MongoDB support to focus exclusively on PostgreSQL and Oracle. Eliminates ~5,000 lines of code, document-oriented interfaces, and MongoDB driver dependency.

**Key Benefits:** Reduced complexity, smaller dependency tree, clearer framework scope

---

### [ADR-013: Interface Naming Conventions (S8196)](adr_013_interface_naming_conventions.md)

**Date:** 2026-03-11 | **Status:** Accepted

Renames interfaces to follow Go's idiomatic naming per SonarCloud rule S8196. Interfaces renamed for clarity across scheduler, app, database, messaging, server, and cache packages.

**Key Benefits:** Idiomatic Go naming, improved readability, SonarCloud compliance

---

### [ADR-014: Slim Module Interface + Remove Stutter](adr_014_slim_module_interface.md)

**Date:** 2026-03-16 | **Status:** Accepted

Slims the `app.Module` interface from 5 methods to 3, making `RegisterRoutes` and `DeclareMessaging` optional via `RouteRegisterer` and `MessagingDeclarer` interfaces. Removes stuttered framework module names (`OutboxModule` → `Module`, etc.).

**Key Benefits:** Interface Segregation, eliminates no-op methods, removes `//nolint:revive` suppression

---

### [ADR-015: Echo v4 to v5 Migration](adr_015_echo_v5_migration.md)

**Date:** 2026-04-06 | **Status:** Accepted

Migrates the HTTP framework foundation from Echo v4 to v5 (~92 files affected) to stay within the supported window, unlock `otelecho` v5 support, and align with the Echo ecosystem.

**Key Benefits:** Long-term Echo support, OpenTelemetry instrumentation upgrade path, ecosystem alignment

---

### [ADR-016: Database Session Timezone Configuration](adr_016_database_session_timezone.md)

**Date:** 2026-04-23 | **Status:** Accepted

Establishes a session-level timezone setting (default `UTC`) applied to every PostgreSQL/Oracle connection in the pool, eliminating cross-environment time-zone drift. Opt out with `database.timezone: "-"`.

**Key Benefits:** Deterministic `time.Time` round-trips across dev/staging/prod, pool-wide consistency, Oracle host-TZ leak closed

---

### [ADR-017: Standardize on `ToSQL()` Across All Query Builders](adr_017_insert_query_builder.md)

**Date:** 2026-05-01 | **Status:** Accepted

Introduces `types.InsertQueryBuilder` so `qb.Insert*` constructors return a go-bricks-owned interface exposing idiomatic `ToSQL()` (S8179) instead of the upstream `squirrel.InsertBuilder` with lowercase `ToSql()`. Aligns the INSERT surface with `Select`/`Update`/`Delete`.

**Key Benefits:** Consistent public API, S8179-compliant naming, removes docs-vs-code drift

---

### [ADR-018: Multi-Tenant Migration CLI](adr_018_multi_tenant_migration_cli.md)

**Date:** 2026-05-09 | **Status:** Accepted

Introduces `migration.MigrateAll` plus the `go-bricks-migrate` CLI (`tools/migration/`) so CI/CD can roll out new Flyway migrations to every existing tenant. Defines a pre-defined HTTP listing contract using the standard go-bricks `APIResponse` envelope and an AWS Secrets Manager naming convention (`gobricks/migrate/<tenant_id>`) for credentials. Reuses `database.DBConfigProvider` so the existing tenant-store abstraction works unchanged.

**Key Benefits:** Documented multi-tenant migration story, secrets-free listing API, library + CLI parity, framework module stays AWS-SDK-free.

---

### [ADR-019: Migration Audit-Event Delivery — OpenTelemetry-first with Pluggable Sink Override](adr_019_migration_audit_delivery.md)

**Date:** 2026-05-12 | **Status:** Accepted

Resolves issue #381. Migration audit events (`migration.applied`, `state.transitioned`, `quiesce.set/cleared`) emit via the existing OpenTelemetry seam by default (span + structured log record); compliance-grade durability is opt-in via a `migration.AuditRecorder` interface that fans out in parallel. Publishes a stable `ErrorClass` taxonomy so downstream alerting can pin on string identifiers.

**Key Benefits:** Zero-config audit for the majority of adopters, durable opt-in path for PCI/SOC 2 customers, schema consistency across both emission paths via a single `AuditEvent` struct.

---

### [ADR-020: Shared Oracle Container for Integration Tests with Per-Test Schema Isolation](adr_020_oracle_integration_test_container_reuse.md)

**Date:** 2026-05-12 | **Status:** Accepted

Resolves the investigation in issue #402. The `database/oracle` integration suite consumes ~80% of integration-test wall time (572s of ~700s) because every test starts a fresh Oracle container (31 sequential cold starts × ~18.5s = ~573s, matching the measurement to within rounding). Replaces the per-test container with one container per test binary execution, plus per-test schema isolation via `CREATE USER` / `DROP USER ... CASCADE`. PostgreSQL is out of scope (PG cold-starts in ~3s, so the same anti-pattern costs ~60s — not worth changing).

**Key Benefits:** ~55% Oracle suite reduction (~572s → ~250s), clears the 10-minute Go test timeout with comfortable headroom, makes test isolation an explicit grep-able contract, unblocks future `t.Parallel()` adoption.

---

### [ADR-021: Provisioning State Machine — Diverge on the Model, Mirror the Patterns](adr_021_provisioning_state_machine.md)

**Date:** 2026-05-13 | **Status:** Accepted

Resolves issue #379. Per-tenant provisioning state machine with durable, crash-recoverable persistence (`pending → schema_created → role_created → migrated → seeded → ready`, with `cleanup → failed` branches). New `migration/provisioning/` package borrows the engineering patterns from `outbox/` (vendor-pluggable Store, bundled DDL, in-memory mock under `testing/`) but diverges on the data model: outbox is a fire-and-forget event queue; provisioning is a finite-state graph with blocking transitions and per-tenant scope. Rejects the alternatives of sharing storage tables, the Store interface, or extending the outbox package because the consumer APIs and table shapes don't overlap.

**Key Benefits:** Focused consumer surface, no outbox-side churn, mirrored patterns make adding new vendors cheap (Oracle equivalent follows the outbox-Oracle precedent under #385), the state transitions are durably auditable for #382's pending events.

---

### [ADR-022: Environment Policy — Free-Form `app.env` with Predicate-Based Branching](adr_022_env_policy.md)

**Date:** 2026-05-14 | **Status:** Accepted

Resolves issue #435. Replaces the strict `{development, staging, production}` allowlist in `config.validateApp` with a format check (lowercase alphanumeric + hyphen, ≤32 chars). Behavior switches in the framework's six call sites move from string equality against `EnvDevelopment` / `EnvProduction` to predicates (`config.IsDevelopment` / `config.IsProduction`) backed by documented alias maps (`{development, dev, local}` / `{production, prod, prd}`). Consumer projects can now use their own env conventions (e.g. `local/tst/stg/prd`) without forking the validator. Eliminates a latent dead-code path in `server/env.go` and the duplicated inline alias logic in `app/app.go`'s bootstrap logger.

**Key Benefits:** Org-specific naming conventions accepted out of the box, behavior switches read intent (`IsDevelopment()`) rather than enum equality, alias treatment is uniform across CORS + migration + handler call sites, format check still catches structural typos (uppercase, spaces, garbage).

---

### [ADR-023: Scheduler Timezone Configuration](adr_023_scheduler_timezone.md)

**Date:** 2026-06-02 | **Status:** Accepted

Adds `scheduler.timezone`, a single config field applied scheduler-wide via
gocron's `WithLocation`, mirroring the `database.timezone` contract from ADR-016
(default UTC, `"-"` opt-out for host-local, IANA-validated, fail-fast). Resolves
the absence of any timezone knob for scheduled jobs and removes the vestigial
`ScheduleConfiguration.Timezone` field. Breaking: an unset zone now means UTC
instead of host-local.

---

### [ADR-024: Flat-Smushed Config Keys (Underscore-Free for Env Reachability)](adr_024_config_key_flatsmush.md)

**Date:** 2026-06-05 | **Status:** Accepted

Renames 21 snake_case koanf leaf keys (e.g. `log.sensitive_fields`,
`keystore.secret_min_length`, `outbox.batch_size`) to the framework's
flat-smushed convention (`log.sensitivefields`, …). The env loader maps `_`→`.`
(koanf nesting), so underscored leaf keys were silently unreachable from
environment variables — the value landed at an orphan path and the default won.
Only struct tags change; Go field names are unchanged. A `Config`-tree reflection
test enforces underscore-free koanf tags so the bug class cannot recur. Breaking:
old snake_case YAML/env keys fall back to defaults.

---

### [ADR-025: Connection Pool Idle Connections Default to Track Max](adr_025_pool_idle_tracks_max.md)

**Date:** 2026-06-06 | **Status:** Accepted

Changes the default for `database.pool.idle.connections` from a fixed `2` to
tracking `database.pool.max.connections` (default 25). A fixed idle of 2 against a
max of 25 made the pool churn physical connections (TCP+TLS+auth) under sustained
load — profiling showed p95 16.25→1.46 ms and errors 8.15%→0% once idle tracked
max. `database/sql` caps idle at max, so the change is safe; an explicit idle
value still wins. Centralized in `applyConnectionCountDefaults` (called from `applyDatabasePoolDefaults`, covers PostgreSQL,
Oracle, named, and per-tenant DBs); effective pool settings are now logged at
startup. Behavioral change: idle footprint rises (notably per-tenant) and idle
metrics report max rather than 2.

**Key Benefits:** Eliminates connection churn, removes a class of connection-establishment errors, makes effective pool config observable

---

### [ADR-026: Zero-Overhead Request Path When Observability and Logging Are Disabled](adr_026_zero_overhead_request_path.md)

**Date:** 2026-06-06 | **Status:** Accepted

Makes the default-config request path genuinely allocation-free when features are
off, by gating on explicit booleans rather than the OTel no-op providers (which
are non-nil, so the framework was building and discarding span/metric attributes
on every DB query and HTTP request). Gates DB tracking (process-global flag set at
bootstrap) and the OTel HTTP middleware (explicit `SetupMiddlewares` param); adds
`logger.LogEvent.Enabled()` to short-circuit the per-request action log at disabled
levels; consolidates four counter `WithValue`s into one struct and two request
clones into one via `RequestEnrich`; adds `server.gzip.minlength` (default 1024).

**Breaking:** `logger.LogEvent` gains `Enabled()`; `server.SetupMiddlewares` gains
an `observabilityEnabled` param; gzip default 1024; standalone `database`-package
consumers must call `database.SetObservabilityEnabled(true)`.

**Key Benefits:** True zero overhead when observability/logging disabled, fewer per-request allocations, honest no-op contract

---

### [ADR-027: Wire `database.tls.cert/key/ca` Into the Drivers (Fail Closed on Oracle)](adr_027_database_tls_material.md)

**Date:** 2026-06-10 | **Status:** Accepted

`TLSConfig.cert/key/ca` were advertised but never consumed: the PostgreSQL DSN emitted
only `sslmode`, so `mode: require` + `ca:` was encrypted-but-unauthenticated (MITM-able)
and mTLS was impossible; Oracle ignored TLS entirely. PostgreSQL now wires
`sslrootcert`/`sslcert`/`sslkey` (all values `quoteDSN`-quoted, which also closes an
unquoted-`sslmode` DSN-injection vector); Oracle rejects `database.tls.cert/key/ca` at
config validation (tcps/wallet not implemented) rather than silently dropping it.

**Breaking:** a PostgreSQL deployment relying on the silent unauthenticated downgrade now
upgrades to CA verification (a wrong/missing CA now fails the connection); an Oracle
config that set `database.tls.cert/key/ca` now fails validation at startup.

**Key Benefits:** TLS material is actually honored (server auth + mTLS), no silent security degradation, fail-closed on the unsupported vendor

---

### [ADR-028: PostgreSQL `BuildUpsert` Binds Update Values (Parity With Oracle MERGE)](adr_028_pg_upsert_binds_update_values.md)

**Date:** 2026-06-10 | **Status:** Accepted

`BuildUpsert` takes separate insert/update value maps; Oracle's MERGE bound both, but the
PostgreSQL path emitted `DO UPDATE SET col = EXCLUDED.col`, silently ignoring the caller's
update values (updated to the *insert* value) and breaking update columns absent from the
insert set (`EXCLUDED.<not-inserted>`). PostgreSQL now binds the update values as
parameters (`col = $N`, numbered after the insert placeholders), matching Oracle.

**Breaking:** the generated SQL changes (`EXCLUDED.col` → `$N`); runtime behavior changes
only when update values differ from insert values (or an update column is absent from the
insert) — those now apply the caller's intended value.

**Key Benefits:** PostgreSQL/Oracle upsert parity, no silent data divergence, update-only columns work

---

### [ADR-029: Graceful Shutdown Phase Ordering (Stop Inbound Work Before Teardown)](adr_029_graceful_shutdown_order.md)

**Date:** 2026-06-10 | **Status:** Accepted

`App.Shutdown` tore down modules **first**, while the HTTP server was still serving and AMQP
consumers were still delivering — so in-flight handlers ran against already-shut-down modules
(shutdown-window panics). Reordered to stop inbound work first: **server → consumers →
modules → observability → manager cleanup loops → closers**, with a new additive
`Manager.StopConsumers()` that quiesces consumers (idempotent) without closing connections.

**Behavioral change (not an API break):** the framework stops admitting new HTTP requests and
AMQP deliveries before modules are torn down (consumers are cancelled, not synchronously
joined, so in-flight handlers may briefly overlap teardown); no application code must change.
`messaging.Manager.StopConsumers()` is additive.

**Key Benefits:** No shutdown-window panics, in-flight work drains against live modules, consumer-quiesce hook

---

### [ADR-030: `PoolKeepAliveConfig.Enabled` Is Optional (`*bool`) So an Explicit `false` Is Honored](adr_030_keepalive_enabled_optional.md)

**Date:** 2026-06-16 | **Status:** Accepted

`PoolKeepAliveConfig.Enabled` was a plain `bool`, and `applyDatabasePoolDefaults` flipped it
back to `true` whenever `Interval` was zero — so the natural opt-out (`enabled: false` with
`interval` unset) was silently overridden and keep-alive ran anyway (M5). A `bool` can't tell
"absent" from "explicit false." Changed to `*bool` (nil → default true; `&true`/`&false` →
honored, independent of `Interval`), with a nil-safe `IsEnabled()` reader consumed by both
vendor connection layers.

**Breaking:** `PoolKeepAliveConfig.Enabled` is now `*bool` — direct struct construction must
use a `*bool` and reads must go through `IsEnabled()`. YAML/env config is unchanged.

**Key Benefits:** Explicit `enabled: false` is honored regardless of interval, nil/true/false are distinguishable, no silent re-enable

---

### [ADR-031: Validate Direct-String Identifier Arguments in the Query Builder (Close M9 SQL Injection)](adr_031_query_builder_identifier_validation.md)

**Date:** 2026-06-16 | **Status:** Accepted

The query builder's direct-string APIs (`From`, the JOIN family, `OrderBy`, `GroupBy`, `Set`, `SetMap`,
`DeleteQueryBuilder.OrderBy`) interpolated their string identifier arguments directly into the SQL, with
quoting applied **only for Oracle** — the PostgreSQL/default branch returned the argument verbatim. A
user-controlled identifier passed to one of
these APIs on PostgreSQL was therefore a SQL-injection vector (M9): `.OrderBy("name; DROP TABLE users--")`
was interpolated as an executable second statement. These identifier args are now validated against a safe
grammar (simple/qualified identifier, optional inline alias for `From`, optional `ASC/DESC [NULLS FIRST|LAST]`
for clauses) on **all vendors** before interpolation; violations surface as a `ToSQL()` error (never a panic).
Valid identifiers on PostgreSQL stay **unquoted** to avoid a case-folding regression.

**Breaking:** Identifiers outside the grammar — notably SQL **function expressions** passed as plain strings to
`OrderBy`/`GroupBy` — now error from `ToSQL()` and must move to `qb.Expr()`/`Raw()`.

**Key Benefits:** Closes the M9 injection vector on both vendors, no PostgreSQL case-folding regression, forces computed expressions through the annotated `Expr()`/`Raw()` escape hatch

---

### [ADR-032: Lease/Refcount Per-Tenant Resource Handles to Close the Eviction-While-In-Use Race](adr_032_lease_refcount_tenant_handles.md)

**Date:** 2026-06-17 | **Status:** Accepted

The per-tenant resource managers (`cache.CacheManager`, `database.DbManager`, `messaging.Manager`)
handed out the raw handle from `Get()`/`Publisher()` and later `Close()`d that same handle from LRU
eviction or idle cleanup with **no reference counting** — so a handle could be closed while a request
that obtained it was still mid-operation (M3, issue #606; PR #605 was the non-breaking mitigation).
The managers now **reference-count** each entry: `Get()`/`Publisher()` return `(handle, ReleaseFunc, error)`,
eviction/idle-cleanup **detach** an entry immediately but **defer its `Close()` until the last lease is
released**, and a brand-new entry carries a **seed lease** so concurrent eviction/`Remove` can only detach
(never close) it during the acquisition window. A private `internal/leasescope` package carries a per-unit
lease scope in `context.Context`; the framework installs it at three seams (HTTP `RequestEnrich`, AMQP
`processMessage`, scheduler `executeJob`) covering six unit-of-work types via context inheritance, and the
per-tenant accessors register each lease there — so **`deps.DB/Cache/Messaging` and `ResourceProvider` are
unchanged and applications do not change**.

**Breaking:** the raw managers' `Get()`/`Publisher()` return types gain a `ReleaseFunc` (third return).
Direct callers must capture and invoke it; unscoped contexts release immediately (non-leaking, unprotected).

**Key Benefits:** An in-use handle is never closed — the M3 race is closed on every concurrent multi-tenant path (HTTP, consumers, jobs, outbox relay, inbox); no application-facing API change; robust under heavy eviction/`Remove` churn via the seed-lease hand-off

---

### [ADR-033: Bounded Publish Retries + Status-Driven Outbox Dead-Lettering](adr_033_outbox_retry_count_status_parking.md)

**Date:** 2026-06-30 | **Status:** Accepted

The outbox relay's per-row `retry_count` stayed frozen under negative conditions (broker down,
missing exchange) even though the relay logged that it was "retrying". Two defects: (1)
`AMQPClientImpl.PublishToExchange` retried in an **unbounded loop** whose counter was only logged,
never a ceiling — and the relay calls it with a deadline-free context, so it never returned to run
`MarkFailed`; (2) the relay **early-returned** when the broker was not ready, skipping the whole
batch. The fix bounds the publish loop (`messaging.reconnect.maxpublishattempts`, default 5) so it
always returns a **classifiable** error (new `ErrPublishRetriesExhausted` / `ErrPublishNacked` /
`ErrPublishConfirmTimeout` sentinels, wrapping the last cause), and reworks the relay to advance
`retry_count` on **every** failed attempt — including a full outage (outage fast-path) — under a
per-record `outbox.publishtimeout` (default 60s). Parking is decoupled from the counter:
`FetchPending` is status-gated only, and a new `MarkDeadLettered` sets `status = 'failed'` **only for
poison (undecodable headers) at `MaxRetries`** — a broker NACK is transient and a missing exchange
surfaces as a NACK, so both are connectivity and never park, meaning neither an outage nor a
recoverable broker fault can exhaust a healthy event. Shutdown/cancel does not count. No DB schema
migration.

**Breaking:** `PublishToExchange` returns after `maxpublishattempts` instead of looping forever
(observable to every publisher); the `outbox.Store` interface changed (`FetchPending` drops its
`maxRetries` param, gains `MarkDeadLettered`).

**Key Benefits:** the reported frozen-`retry_count` bug is fixed and operator-visible during outages; genuine poison becomes a visible `status = 'failed'` row instead of lingering `pending`; a prolonged broker outage can never permanently park healthy events; one stuck record can no longer starve the relay batch

---

### [ADR-034: Echo-Free Boundary Types](adr_034_echo_boundary_types.md)

**Date:** 2026-06-30 | **Status:** Accepted

> *Numbering note: ADR-033 is reserved for a concurrent change (outbox retry-count, PR #626) landing in parallel; this echo-boundary ADR took the next free number, 034.*

Resolves issue #623. Wraps every remaining `github.com/labstack/echo/v5` leak on the
public surface behind go-bricks boundary types while Echo stays the unchanged engine
inside `server/`. Introduces a flat `server.MiddlewareFunc func(c HandlerContext, next func() error) error`
and an untyped `server.Handler func(c HandlerContext) error`; replaces the
`HandlerContext.Echo` field with stdlib-typed accessors (`RequestContext()`,
`Request()`, `SetRequestContext()`, …); makes `RouteRegistrar` echo-free (drops the
`echo.RouteInfo` return); removes `ServerRunner.Echo()`, adds `RootGroup()`, and
retypes `RegisterReadyHandler` to `server.Handler`. `scheduler.CIDRMiddleware`, the
framework middleware-constructor class (call sites unchanged), `SkipperFunc`, and
`EscalateSeverity` all move to go-bricks types. Big-bang removal (no `// Deprecated:`).

**Breaking:** all six echo leak classes are removed from the consumer surface; custom
middleware moves to the flat shape and `HandlerContext.Echo` field accesses move to accessors. The
typed handler hot path stays echo-direct via an unexported `addEcho` seam (ADR-026
preserved); only middleware routes pay a bounded +1 baton alloc.

**Key Benefits:** No `echo.*` symbol on the consumer path, downstream services decoupled from Echo's version, security improvement (no spoofable `RealIP()` accessor), uniform flat middleware shape

---

### [ADR-035: Route Template and Path-Parameter Accessors on HandlerContext](adr_035_route_template_path_params.md)

**Date:** 2026-07-02 | **Status:** Accepted

Resolves issue #633. ADR-034's accessor set was scoped to "what handlers actually use
today" without checking capability parity against `echo.Context` — the matched route
template (`Path()`), ordered path parameters (`PathValues()`), and path-parameter
mutation (`SetPathValues()`) were dropped by silent omission, breaking template-keyed
registries, positional param substitution, and query-param→path-param promotion
middleware. Adds `RouteTemplate() string`, `PathParams() []PathParam` (defensive copy —
echo's `PathValues()` aliases the pooled context's backing array), and
`SetPathParams([]PathParam)` (always passes echo a non-nil `PathValues`; echo v5.2.1
panics on nil) plus the neutral `PathParam{Name, Value}` struct, delegating through the
unexported `echoContext()` hatch. Mutation had **no** public channel (`Set()`/
`SetRequestContext()`/stdlib `SetPathValue()` are all invisible to `Param()` and the
`param:"x"` binder), and blessing `Request().Pattern` for the template would re-couple
consumers to unpromised engine behavior. Purely additive — apidiff green, minor
v0.46.0, `feat(server):` not `feat!:`.

**Key Benefits:** Template-keyed registries, positional substitution, and param-rewriting middleware work through a supported vendor-neutral surface; no `reflect`+`unsafe` hacks against the escape hatch; echo's pooled-aliasing and nil-panic gotchas absorbed at the boundary

---

### [ADR-036: Module-Contributed Global Middleware](adr_036_global_middleware.md)

**Date:** 2026-07-05 | **Status:** Accepted

Adds an optional duck-typed module interface `GlobalMiddlewareRegisterer` (mirroring
`RouteRegisterer` / `MessagingDeclarer`) so a module can contribute app-wide middleware —
canonically an auth gate — that runs once per request, after tenant resolution, before
handlers, and cannot be skipped per-route. The framework collects implementers after
`Init()` and registers them once on the **raw root echo chain** (`s.echo.Use`, not a group:
root `Use` recompiles the global chain and applies to every request regardless of
route-registration order, dissolving echo's group-scoping/order trap) via a new
`*server.Server.RegisterGlobalMiddleware`, wrapping each with the health/ready probe skipper.
The app invokes it through an optional type assertion, leaving the exported `ServerRunner`
interface byte-identical — purely additive, apidiff green, `feat:` not `feat!:`. Documented
limitations: runs after rate-limiting and before JOSE body decryption (header/token/tenant
auth only), and emits the standard envelope on raw-response routes.

**Key Benefits:** Un-skippable app-wide auth/audit gates with the tenant already resolved; single framework-controlled registration (no per-module duplication or order-fragility); no change to the `ServerRunner` contract

---

### [ADR-037: Minimum Database Password Length](adr_037_min_database_password_length.md)

**Date:** 2026-07-10 | **Status:** Accepted

Rejects a non-empty database password shorter than `config.MinDatabasePasswordLength` (8) at two boundaries: `config.Validate` (static config — fail-fast at startup) and the migrate path (`FlywayMigrator.runFor` → `ErrDatabasePasswordTooShort`, covering per-tenant configs that never pass through `config.Validate`). Closes the ADR-019 audit false-negative from #674, where a short-password migration's output was suppressed and audited as `Outcome=failed` even on success. Empty passwords (trust/IAM auth) are exempt. `migration.redactPassword`'s `minRedactablePasswordLength` is single-sourced from the new exported constant.

**Key Benefits:** Closes the migration audit false-negative for single- and multi-tenant paths; a clear startup / pre-flight error (never echoing the password) instead of a suppressed-output false failure; a single-sourced password-length floor.

---

### [ADR-038: Require Explicit Opt-In for Dev-Permissive CORS](adr_038_cors_dev_wildcard_opt_in.md)

**Date:** 2026-07-12 | **Status:** Accepted

Requires `CORS_DEV_WILDCARD=true` (raw process env, alongside `config.IsDevelopment(appEnv)`)
before `server/cors.go` grants the reflect-any-origin + `AllowCredentials=true` dev posture.
Without the flag, a development-alias (or koanf-defaulted) `APP_ENV` now fails closed exactly
like neutral and production envs — eliminating the *accidental* wildcard-by-omission default
that PR #696's WARN only made loud, not safe (a deployment that explicitly ships the flag with
an unset `APP_ENV` still gets the wildcard; the residual risk becomes a deliberate, grep-able,
named action). The flag is inert outside `config.IsDevelopment`
(a non-dev env with the flag set still fails closed, with a WARN noting it's ignored), and
unparseable values are treated as false with a WARN. Amends the CORS paragraph of
[ADR-022](adr_022_env_policy.md).

**Key Benefits:** Forgetting `APP_ENV` in a real deployment now fails CORS closed instead of
granting the most permissive posture available; the opt-in follows the existing raw-env
`CORS_ORIGINS` precedent rather than a committable Koanf config key; the containment property
(flag never weakens non-dev envs) is test-proven.

---

### [ADR-039: Require an Explicit Composite Tenant Resolver Order](adr_039_composite_resolver_order.md)

**Date:** 2026-07-14 | **Status:** Accepted

Makes `multitenant.resolver.order` **required** for `type: composite` — there is no implicit
default, and a composite config without it fails `config.Validate` at startup. Replaces the
hardcoded header → subdomain → path order, under which the `header` sub-resolver (the only one
that participates with zero configuration — it always exists and defaults to `X-Tenant-ID`)
unconditionally preempted whatever source the operator had explicitly configured, with no knob to
change it. All three sources are caller-written (the URL path is authored by the caller; `Host` is
itself a request header, constrained only if the ingress pins it), so no ordering makes any of them
trustworthy — and both candidate defaults silently harm a real population: header-first lets a
caller-supplied header override an explicitly-configured subdomain/path scoping, while a
subdomain-first default would silently escalate gateway-fronted deployments whose gateway owns
`X-Tenant-ID`. The framework therefore refuses to guess. `config.DefaultResolverOrder()` is demoted
to the *recommended* order (`[subdomain, path, header]`) plus a last-resort fallback in
`server/middleware.go` for configs that bypassed `config.Validate()` (preventing a fail-open
zero-sub-resolver composite). Validation rejects unknown/duplicate entries, `order` on a
non-composite type, and an order naming an unconfigured sub-resolver (`path` needs `path.segment`;
`subdomain` needs a real `domain`).

**Key Benefits:** Precedence becomes an explicit operator decision instead of an unverifiable
framework bet on the deployment's edge topology; the zero-config header sub-resolver no longer
silently outranks an explicitly-wired subdomain/path; fails fast at startup with the env var, YAML
key, and both candidate orders in the error. Tenant resolution remains *identification, not
authorization* — the deployment still owes `Host` validation at the ingress, header stripping at
the gateway, and an entitlement check on the resolved tenant.

---

### [ADR-040: Forward Declaration `Args` to the Broker on Queue/Exchange/Binding Declares](adr_040_declaration_args_passthrough.md)

**Date:** 2026-07-17 | **Status:** Accepted

Appends a trailing `args map[string]any` parameter to `AMQPClient.DeclareQueue` /
`DeclareExchange` / `BindQueue` and forwards it to amqp091 at every implementation
(`AMQPClientImpl`), pass-through (`tenantAwarePublisher`), and replay call site
(`Registry.DeclareInfrastructure`). `QueueDeclaration.Args`, `ExchangeDeclaration.Args`, and
`BindingDeclaration.Args` were already deep-copied on registration and folded into the topology
hash, but `AMQPClientImpl` hardcoded a `nil` arguments table at the one place that talks to the
broker — silently discarding them. That meant handler errors/panics (which nack without requeue
by design) dropped messages permanently with no `x-dead-letter-exchange` escape hatch, and a
service could never attach to an ops-provisioned queue declared with `x-queue-type=quorum` or any
other broker argument, since RabbitMQ's declare-equivalence check would fail with `406
PRECONDITION_FAILED`. A deliberate, compile-time-enforced breaking change over the rejected
alternatives of parallel `...WithArgs` methods (equally apidiff-incompatible, and leaves the dead
field live) or a struct-based signature reshape (the scalar AMQP fields are stable; `Args` is
already the extensible unit).

**Key Benefits:** Closes a silent, permanent message-loss path (nacked-without-requeue deliveries
can now be parked via `x-dead-letter-exchange` instead of dropped); unblocks attaching to
operator-provisioned queues whose arguments must match at declare time; topology hash and actual
broker state agree again since `Args` now reaches both.

---

### [ADR-041: Shared (Control-Plane) Ledger Tenancy for Outbox/Inbox](adr_041_shared_ledger_tenancy.md)

**Date:** 2026-07-23 | **Status:** Accepted

Adds `outbox.tenancy` / `inbox.tenancy` (`"per-tenant"` default, unchanged; or `"shared"`) so a
**pool-model** deployment (one shared database, `multitenant.enabled: true` only for HTTP tenant
resolution) can use the outbox/inbox with one control-plane ledger, relayed in a single pass,
instead of the per-tenant fan-out the #581 guards otherwise require enumerable tenants for. Shared
mode resolves resources via the empty key (`""`) — the same key the built-in store already maps to
the root `database:`/`messaging:` blocks, unreachable from HTTP tenant resolution. No new public
`ModuleDeps` resource accessors: the two ledger modules receive the shared resolvers via an
unexported duck-typed setter (`sharedResolverSetter`, mirroring the existing `declarationSetter`
precedent), injected by `App.RegisterModule` before `Init` runs. Shared-mode outbox publishes
**require** a framework-originated transaction (`RunInSharedTx` + an opaque `sharedTx` marker type,
exposed to applications via `app.SharedTxRunner`) — `dbtypes.Tx` cannot otherwise be verified to
target the control-plane database, so a docs-only contract would reintroduce the exact silent-loss
class the #581 guards exist to prevent. Consumers on the shared broker are explicitly out of scope
(publisher-only). Additive-only; default tenancy behavior is byte-for-byte unchanged.

**Key Benefits:** Unblocks a legitimate topology (#758) that previously had no supported mode short
of disabling the outbox or a decorative static-tenant config lie — which was itself
attacker-reachable via a guessable `X-Tenant-ID`; `RunInSharedTx`'s marker enforcement makes
ledger/business co-location a build-time-adjacent guarantee instead of an operator-trust
convention; zero behavior change for every existing per-tenant/single-tenant deployment.

---

### [ADR-042: Server TLS Listener (Client Verification Deferred)](adr_042_server_tls.md)

**Date:** 2026-07-27 | **Status:** Accepted

Adds `server.tls.*` (`enabled`, `certfile`/`certvalue`, `keyfile`/`keyvalue`, `minversion`) so
`server.Start()` can serve HTTPS via Echo's `StartConfig.TLSConfig`, which go-bricks never
populated. PEM material loads through the same `internal/secretfile` guards the httpclient TLS
loader uses; TLS 1.2 is the floor; bad or unreadable material fails `Start()` fast rather than
degrading to plaintext. HTTP/1.1-only by design — advertising `h2` without the h2 server wired
(echo's `StartTLS`, which go-bricks does not use) would break handshakes. Client-certificate
verification was split out to a gated follow-up: an infrastructure review found every named
deployment sits behind an ALB that already terminates partner mTLS at the edge, so app-side
verification activates only once a deployment terminates partner TLS at the app itself
(NLB/static-IP ingress) — no such deployment exists yet. No raw `*tls.Config` escape hatch; the
framework owns the listener's posture. Additive-only; the zero value leaves every deployment on
plaintext, unchanged.

**Key Benefits:** Closes the config-surface gap ADR-034's engine seal created (a consumer could no
longer add TLS from outside); covers the ALB→target encryption-in-transit hop and any deployment
without an edge proxy; keeps the deferred mTLS half gated on a real deployment need instead of
shipped speculatively.

---

### [ADR-043: ALB Forwarded-Client-Cert Identity Middleware (`X-Amzn-Mtls-*`)](adr_043_forwarded_client_cert.md)

**Date:** 2026-07-27 | **Status:** Accepted

Adds `server.forwardedclientcert.*` (`enabled`, `require`) so a config-gated middleware can
parse ALB verify-mode `X-Amzn-Mtls-Clientcert-*` headers (`-Subject`, `-Issuer`,
`-Serial-Number`, `-Leaf`) and expose a typed `ForwardedClientCert` identity via
`ForwardedClientCertFromContext` — identification, not authorization (ADR-039's stance).
`-Leaf` is percent-encoded with `+=/` left literal per AWS's docs, so `url.PathUnescape` is
the correct decoder (`url.QueryUnescape` would corrupt a literal `+` into a space).
`Require` rejects (401) when both `-Subject` and `-Serial-Number` are absent, or when any
one of the four headers carries more than one value (duplicate check first, so a duplicated
`-Issuer` rejects even with a valid Subject); a present Subject whose `-Leaf` fails to
decode still passes (`Leaf == nil` + WARN). Health/
ready probes are exempt. AWS does not publicly document that the ALB strips or overwrites
client-supplied copies of these headers — verified across four ALB documentation pages and
the mTLS launch blogs — so the trust model rests entirely on the deployment posture (an
mTLS-verify listener, closed security groups, single ingress path to the target group), and
per-subject authorization is safe only where the trust store scopes a single partner CA. No
in-app IP/proxy trust (F23 precedent). Additive-only; the zero value leaves every
deployment unchanged.

**Key Benefits:** Replaces per-service hand-rolled header parsing (and its URL-decoding
trap) with one audited implementation; corrects the ALB-stripping assumption ADR-042's
Consequences section had carried forward, replacing it with a documented-silence finding;
keeps the identification/authorization boundary explicit so a deployment can't mistake "the
ALB let it through" for application-level authorization.

---

### [ADR-044: `httpclient.Builder.Build` Fails Closed on Unsafe Transport Composition](adr_044_httpclient_build_fail_closed.md)

**Date:** 2026-08-01 | **Status:** Accepted

`Builder.Build()` changes from `Client` to `(Client, error)`: it now fails construction,
instead of logging an easily-missed WARN, when a `WithTransport`/`WithTLSConfig`/
`WithHTTPClient` composition would silently discard a client certificate, pinned roots, or a
caller-supplied transport. Two fixes precede the hard failure so it is neither unfixable nor
overly aggressive: `WithTLSConfig` now clones and composes onto an incumbent
`*http.Transport` in the base-transport slot instead of always displacing it with a fresh
`DefaultTransport` clone (so `WithTransport(custom).WithTLSConfig(cfg)` keeps `custom`'s
proxy/dialer settings *and* gets `cfg`'s TLS material — provided `custom` decides no TLS of
its own, meaning no meaningful `TLSClientConfig` and no `DialTLS`/`DialTLSContext`, both of
which `WithTLSConfig` replaces or clears), and the case-1 discard predicate is
suppressed when the displacing transport is itself a `*http.Transport` deciding its own TLS
(`transportCarriesTLSMaterial`, not a bare non-nil check —
an ALPN-only or nil config still reports) — the caller followed the old WARN's advice
literally, so the fix now actually works. An earlier draft (PR #839) shipped a separate `BuildStrict` entrypoint
instead; this ADR collapses to one `Build` rather than two doors into the same hazard. Error,
not panic, because the predicates are data-dependent on runtime config values, so a panic
would be green in staging and crash-looping in production from identical source. Validates
base-transport-slot displacement only — not `WithHTTPClient`'s deliberate override,
`InsecureSkipVerify`, a hand-built `tls.Config` missing `MinVersion`, `WithTLSConfig(nil)`
from a swallowed loader error, or a replaced `net/http.DefaultTransport`. `NewClient`'s
signature is unchanged. Landed within days of `WithTLSConfig`'s v0.55.0 release, before any
fleet accumulated call sites depending on the WARN-only behavior.

**Key Benefits:** Turns a silently-degraded TLS/transport posture into a compile-time
call-site error — for every site that captures the result by single-value assignment;
four other call shapes still compile and drop the error, so they need manual review
(see [migrations.md](migrations.md) `[C56.6]`, the authoritative list) — and, where the
builder runs at startup, a startup-time construction error instead of a runtime surprise;
fixes two real compositions (proxy+TLS, and the case-1 remedy) that were previously
either impossible or non-functional; keeps one builder entrypoint instead of a
`Build`/`BuildStrict` split.

---

### [ADR-045: Resource Managers Expose No Producer-Side Interface](adr_045_no_producer_side_manager_interfaces.md)

**Date:** 2026-08-04 | **Status:** Accepted

Deletes the exported `cache.Manager` interface and establishes that per-tenant resource
managers carry no consumer-facing interface. `cache.Manager` was implemented by nothing,
consumed by nothing, and had silently drifted from `*cache.CacheManager`: the concrete `Stats()`
returns `ManagerStats` where the interface demanded `map[string]any`, so
`var _ cache.Manager = (*cache.CacheManager)(nil)` would not have compiled had anyone written
it. Nobody did, and no consumer ever used the interface where the compiler would have checked
the two against each other, which is why the drift stayed invisible.
The two sibling managers, `database.DbManager` and `messaging.Manager`, are plain structs and
were already the precedent. Interface seams in go-bricks sit on manager **inputs**
(`Connector`, `ConfigProvider`), the **leaf resource** (`cache.Cache`, `database.Interface`,
`messaging.AMQPClient`), and the **app boundary** (`app.ResourceProvider`) — never on a
manager type. Interface Segregation does not apply: an interface with no client segregates
nothing. Repairing the signature instead was rejected because `apidiff` rates it Incompatible
too (identical `!` title, hop atom, and minor bump), every substitution need is already served
by `Connector` and `MockCache`, and the repair means either re-weakening `Stats()` — breaking
`app/health.go` — or making the interface a strict-subset shadow of the concrete method set.
Consumers wanting a seam declare a narrow one on their own side, where the compiler checks the
concrete manager's method set against it at every call that passes one as the other — the
guarantee is method-set compatibility, not behavior, and it only holds for an interface that is
actually used somewhere. Breaking despite zero in-repo implementations: a
consumer's own adapter over `*CacheManager` can satisfy it (see
[migrations.md](migrations.md) `[C56.9]`).

**Key Benefits:** Eliminates the drift class at the root rather than guarding it; makes all
three resource managers consistent; removes an exported symbol that cost documentation
maintenance and delivered no abstraction. The `CacheManager` → `Manager` rename that deletion
now permits is deliberately deferred — it is a separate exported-API break across five `app/`
files and nine `CacheManager` type references.

### [ADR-046: Cache Readiness Is Strict by Default, with a Visible Opt-Out](adr_046_cache_readiness_strict_default.md)

**Date:** 2026-08-04 | **Status:** Accepted

Flips `cache.critical` from opt-in to strict: an absent key now means the cache probe is
critical, so a cache-enabled service answers `/ready` with `503` while Redis is unreachable
instead of reporting ready with a dead cache. An opt-in fix for a silent-failure bug
protects only the operators who already suspected the problem. The escape hatch is kept
rather than removed, because the framework does not own the probe wiring: banning
`cache.critical: false` would push the lenient deployment into a `readinessProbe` →
`/health` manifest rewrite that is invisible to the framework, unauditable by `git grep`,
and silences the database probe too — a greppable one-line opt-out is the hardening
feature, and it emits a startup WARN on every boot (ADR-038's precedent), never a
validation error. `CacheConfig.Critical` is a `*bool` with no registered koanf default
(the `ServerConfig.LogRoutes` precedent): with a strict default a bare `bool`'s zero value
would mean the opposite of the shipped default, and any registered default would collapse
absent and explicit-`false` into one state, destroying the signal the WARN is gated on —
though `IsCacheCritical` deliberately returns `true` on a nil receiver where
`ShouldLogRoutes` returns `false`. The `503` body is sanitized per-probe to the constant
`cache unavailable` (the connector error names the Redis host, port and resolved IP on an
unauthenticated endpoint); the full error still reaches the application log and the
debug health endpoint (`<debug.pathprefix>/health-debug`, default `/_sys`). The database body was left byte-identical here and sanitized in turn by `[C57.1]` (fixed string `database unavailable`, same seam); messaging is never critical, so it never renders a `503` body. [ADR-048](adr_048_ready_sanitize_by_default.md) then reversed the per-probe shape itself: sanitization became the shared branch's default (`"<name> unavailable"`), the two constants were deleted, and `PublicErr` became an override — the emitted strings are unchanged. The
correlated-eviction risk — one Redis blip draining every replica at once — is accepted and
mitigated by `readinessProbe.failureThreshold`, deliberately not reimplemented in-framework.

**Key Benefits:** Makes the default safe for the services that most need it without
inventing a `degraded` status or a fatal startup path; keeps the weakened posture visible,
greppable, and scoped to one probe instead of pushed into an unauditable manifest; closes
the Redis-topology disclosure on an unauthenticated endpoint before a strict default makes
it default-on, while preserving the full diagnostic on the two channels operators actually
own.

---

### [ADR-047: Database Absence Is a Config-Resolution Verdict, Distinct from Misconfiguration](adr_047_database_absence_vs_misconfiguration.md)

**Date:** 2026-08-04 | **Status:** Accepted | **Supersedes in part:** ADR-003

Splits two conditions the framework had conflated: a database that is absent (benign — a
DynamoDB-only or HTTP-forwarding service) versus one that is misconfigured (the operator
asked for something real and got it wrong). The verdict moves to config resolution, scoped
to the default `""` key, in `TenantStore.DBConfig` — which had a structurally dead
`defaultDB == nil` check, since `defaultDB` is `&cfg.Database` and never nil, while its
`BrokerURL`/`CacheConfig` siblings tested config *content*. Deliberately not placed in
`database.NewConnection`, which is key-blind and would stamp "absent" on a half-provisioned
tenant. `IsDatabaseConfigured` widens from three fields to every connection-identity field — the
seven shared ones plus Oracle's two target identifiers (`oracle.service.name`, `oracle.service.sid`) — so a partially delivered config fails startup instead of reading as intentional
absence; defaulted fields (timezone/pool/query) are excluded so the verdict is stable across
defaulting. The database probe stays `critical: true`, and multi-tenant deployments report a
distinct `per_tenant` status — a consequence worth stating plainly is that multi-tenant
the probe there stays `critical: true` but reports `per_tenant` with a nil error, so it never blocks readiness (a cache-enabled service still has the critical cache probe from ADR-046).

**Key Benefits:** `/ready` returns 200 for a database-free service, which `app/health.go`
always intended; every static multi-tenant deployment stops returning a permanent 503; a
half-injected secret now fails at startup rather than at first query. Fixes
[#872](https://github.com/gaborage/go-bricks/issues/872); pairs with `app.DatabaseRequirer`
(#878) for the intent a config can never carry. See [migrations.md](migrations.md)
`[C56.14]`, `[C56.15]`.

---

### [ADR-048: `/ready` Error Sanitization Is the Default, Not an Opt-In](adr_048_ready_sanitize_by_default.md)

**Date:** 2026-08-05 | **Status:** Accepted | **Supersedes in part:** ADR-046

Inverts the seam ADR-046 built: `publicProbeError` now synthesizes `"<name> unavailable"`
when `HealthStatus.PublicErr` is empty instead of rendering `Err` verbatim, so a critical
probe is safe by omission and `PublicErr` becomes an override for probes wanting different
wording. Under the opt-in shape the safe path depended on memory — two probes each declared
a constant, and a third critical probe added later leaked its raw error into an
unauthenticated body by doing nothing, with only a prose contract and a test over
`createHealthProbes` (blind to a consumer's own `Prober`) standing in the way. The flip
emits byte-identical output today, because `componentDatabase` and `componentCache` are
literally `"database"` and `"cache"`; the `cacheUnavailableMessage` and
`databaseUnavailableMessage` constants are deleted, since two places agreeing on one string
is the drift being removed. `Err` is untouched and still carries the driver detail to the
app log and `<debug.pathprefix>/health-debug`, and `publicProbeError` no longer dereferences
it at all, so no future caller can panic it on an unauthenticated path. Rejected: fusing
`critical` and `PublicErr` into one field (conflates blocking readiness with disclosure, and
would make operator-set `cache.critical: false` change what leaks), and a required
constructor parameter (an in-package composite literal can always omit the field — a nudge,
not a guard).

**Key Benefits:** A critical probe added tomorrow needs no action to be safe; one function
decides what the unauthenticated `503` discloses instead of every probe constructor; the
invariant test now asserts the rendered string is safe rather than merely non-empty, which
also holds for probes the framework never constructs. See [migrations.md](migrations.md)
`[C57.2]`.

---

### [ADR-049: Debug Endpoints Refuse to Register Without Access Control](adr_049_debug_endpoints_fail_closed.md)

**Date:** 2026-08-05 | **Status:** Accepted | **Related:** ADR-038, ADR-046

`RegisterDebugEndpoints` now returns an error — fatal at startup — when
`debug.enabled: true` would expose one or more debug endpoints with neither
`debug.allowedips` nor `debug.bearertoken` set. That state used to register the
group behind a pass-through `ipWhitelistMiddleware` and a startup WARN naming the
exposed endpoints, leaving `/_sys/health-debug` (full probe errors incl.
connection identity, per-key pool detail) and `/_sys/goroutines` reachable by any
peer that could reach the port. A WARN is not a control. The two access controls
now compose explicitly: each middleware is applied only when its key is
configured, and the allowlist's pass-through branch is gone, so its residual
failure mode is deny-all. `len(exposed) > 0` gates the refusal, so an enabled
group with every endpoint flag off is unaffected, as is the `debug.enabled: false`
default.

**Key Benefits:** the exposure the WARN failed to prevent is no longer
deployable; every doc describing `/_sys/health-debug` as access-controlled
becomes unconditionally true. Follows the ADR-038 (CORS dev wildcard opt-in) and
ADR-046 (cache probe critical by default) precedent. Deploy-time break for a
service currently in that state — see [migrations.md](migrations.md) `[C57.7]`.

---

### [ADR-050: Infer `database.type` from the Connection-String Scheme, Fail Fast on What's Left Untyped](adr_050_connectionstring_type_inference.md)

**Date:** 2026-08-05 | **Status:** Accepted

A `database.connectionstring` with no `database.type` passed `config.Validate` and then
could never connect: `database.NewConnection` dispatches solely on `Type` and errors
`unsupported database type: ""` at first use — validation accepted a guaranteed-dead
config. `validateDatabaseWithConnectionString` now infers `Type` from a recognized DSN
scheme (`postgres://`/`postgresql://` → `postgresql`, `oracle://` → `oracle`) when `Type`
is empty, and rejects an explicit `Type` that conflicts with the inferred scheme. An
unrecognized scheme is not a validation error — whether it's fatal depends on who connects.
`app.Builder.ConfigureRuntimeHelpers` closes that residue: it fails startup when the root
`database:` block, a `databases.*` entry, or — only under `multitenant.enabled: true`, the
same gate `config.NewTenantStore` uses — a `multitenant.tenants.*` entry still carries a
connection string with no resolved type, but only for the built-in connector; a caller-supplied
`Options.DatabaseConnector` parses the DSN itself and is exempt, as is the quiesce CLI's
tolerated-empty-`Type` PostgreSQL path. Rejected: a hard `type` requirement (breaks both
exemptions), inference without the builder guard (leaves the boots-then-fails residue for
unrecognized schemes), and the guard without inference (forces redundant `type` on every
working DSN). Because inference makes Oracle's vendor validation run on a DSN-only config
for the first time, `validateOracleFields` waives its "exactly one of
`oracle.service.name` / `oracle.service.sid` / `database`" requirement when a connection
string is set — `buildOracleDSN` returns that string verbatim and never reads those fields.
The `count > 1` ambiguity error and the Oracle TLS rejection are not waived.

**Key Benefits:** A connstring-only config with a recognized scheme now connects instead of
booting into a dead database — including `oracle://…` with no separate identifier field; an
unrecognized scheme on the built-in connector fails fast
at startup instead of at first query; a `type`/scheme conflict is caught at validation.
Fixes [#877](https://github.com/gaborage/go-bricks/issues/877). See
[migrations.md](migrations.md) `[C57.5]`.

---

### [ADR-051: A Delivered-but-Empty Database Identity Field Fails Startup](adr_051_delivered_empty_database_identity.md)

**Date:** 2026-08-06 | **Status:** Accepted

`config.IsDatabaseConfigured` (ADR-047) infers intent from decoded values, so an identity
field delivered as an empty string — an empty `secretKeyRef`, `envsubst` over an unset
variable, `DATABASE_HOST=""` — is indistinguishable from one never set and reads as
absence: the service boots, `/ready` reports `not_configured`, and the first query fails.
`validateNoDeliveredEmptyDatabase` now runs inside `config.Validate`, immediately before
`validateMultitenant`, and consults the koanf instance `config.Load` already stores on
`cfg.k` before validating: for the root `database` section, each `databases.<name>`, and —
only when `multitenant.enabled: true` — each static `multitenant.tenants.<id>.database`, a
section that decodes as unconfigured but has any identity key present in koanf fails startup
naming every such key. A leftover `tenants:` block under disabled multitenancy stays inert, matching
`TenantStore` and `ManagerConfigBuilder`, which both ignore it. Sections with real
values short-circuit via `IsDatabaseConfigured` unchanged. Placement before
`validateMultitenant` means a delivered-empty tenant section gets the precise key path
instead of `validateMultitenantTenants`'s generic "configuration required" message. The
issue's original six-call-site `IsDatabaseConfigured` signature change was rejected as
strictly more churn for the identical verdict. Hand-built `Config` literals (no koanf
instance) and dynamic-source tenant configs (never routed through koanf) remain out of
reach, unchanged from ADR-047.

**Key Benefits:** A partially-injected Kubernetes secret now fails loudly at startup
instead of silently booting as database-free; `IsDatabaseConfigured`'s signature and all
six existing call sites are untouched — the fix is additive at the `Validate` level only.
Fixes [#880](https://github.com/gaborage/go-bricks/issues/880). See
[migrations.md](migrations.md) `[C57.6]`.

---

## ADR Lifecycle

- **Proposed**: Under discussion, not yet implemented
- **Accepted**: Decision made and implementation complete
- **Deprecated**: Superseded by newer decision (see related ADRs)
- **Superseded**: Replaced by specific ADR (reference provided)

### Numbering Policy

ADR numbers (ADR-001 through ADR-051) reflect **decision/adoption sequence**, not strict chronological order. The authoritative timeline for each decision is the date in its individual ADR header (e.g., ADR-008 is dated 2025-01-10 while ADR-011 is dated 2025-11-09). When reviewing historical chronology, sort by the dates in the ADR index rather than by number. For example, [ADR-011](adr_011_redis_cache.md) introduced the `ModuleDeps` Cache extension — a breaking API change — and its number simply indicates it was the eleventh decision adopted, not that it followed ADR-010 temporally.

## Writing New ADRs

When creating a new ADR:

1. **Use the template structure**:
   - Problem Statement
   - Options Considered (with pros/cons)
   - Decision (what was chosen and why)
   - Implementation Details
   - Consequences (positive, negative, neutral)
   - Migration Impact
   - Related ADRs

2. **Create individual file**: `adr_XXX_short_title.md`

3. **Update this index**: Add entry with summary and key benefits

4. **Reference in CLAUDE.md**: If it affects developer workflows or key architecture

## Related Documentation

- **[CLAUDE.md](../CLAUDE.md)**: Development guide and quick reference
- **[llms.txt](../llms.txt)**: Code examples for LLM code generation
- **[CLAUDE.md → Developer Manifesto](../CLAUDE.md#developer-manifesto-mandatory)**: Project governance — principles, security guidelines, engineering practices
- **[Demo Project](https://github.com/gaborage/go-bricks-demo-project)**: Working examples

---

*ADRs document the "why" behind our architecture. They're living documents—update them when new information changes our understanding of past decisions.*
