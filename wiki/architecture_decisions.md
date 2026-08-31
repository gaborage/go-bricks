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
modules → observability → closers**, with a new additive
`Manager.StopConsumers()` that quiesces consumers (idempotent) without closing connections.
Superseded in part by [ADR-067](adr_067_lifecycle_slots.md): the manager-cleanup-loop phase is
gone — each manager stops its own sweep in `Close()`, which the closers still run last.
Amended 2026-08-29: the observability phase is best-effort — its failures are warned, never
folded into the error `App.Run()` returns (#1225).

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

**Amended (2026-08-29):** poison gains a second class — a publish refused with
`messaging.ErrInvalidPublishDestination` (a destination past the AMQP shortstr limit) is
message-intrinsic, so it parks at `MaxRetries` instead of retrying forever; and the bound moved to
the source, where `outbox.Publish` runs the exported `messaging.ValidatePublishDestination` before
the INSERT and `Init` refuses an over-long `outbox.defaultexchange` (#1229, `[C61.19]`).

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

### [ADR-047: Database absence is a config-resolution verdict, distinct from misconfiguration](adr_047_database_absence_vs_misconfiguration.md)

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

### [ADR-049: Debug endpoints refuse to register without access control](adr_049_debug_endpoints_fail_closed.md)

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
`config.ApplyDatabasePoolDefaults` applies the same inference on the dynamic
multi-tenant resolution path, which bypasses `Validate` entirely. Inference is
unconditional on both paths — the `Options.DatabaseConnector` exemption covers the
startup guard only, and `config.Validate` has always been connector-blind — and the
seam also runs `validateVendorSpecificFields`, so Oracle's TLS rejection and
PostgreSQL's `sslcert`/`sslkey` pairing now apply to every dynamic config instead of
being silently dropped.
Fixes [#877](https://github.com/gaborage/go-bricks/issues/877). See
[migrations.md](migrations.md) `[C57.5]`, `[C59.6]`.

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

### [ADR-052: Delete `jose.PolicyRegistry` rather than wire it up](adr_052_remove_jose_policy_registry.md)

**Date:** 2026-08-07 | **Status:** Accepted

`jose.PolicyRegistry` was a `sync.Map`-backed cache of scanned-and-resolved JOSE policies keyed
by `(reflect.Type, Direction)`, fully tested, exporting four symbols, and called by nothing. Its
doc comment justified the design with a claim that does not hold: that the nil-caching avoids
"re-scanning untagged types on every request, which would otherwise dominate the request hot
path for non-JOSE routes". `jose:` tag scanning happens in `server.scanRouteJOSE`, which
`RegisterHandler` calls once per route at startup; it resolves every kid and writes the policies
onto the route descriptor (`InboundJOSE`/`OutboundJOSE`), and the request path reads those
fields. There is no per-request scan, so the cache had nothing to serve. That measurement is what
rejected the alternative of wiring it into `scanRouteJOSE` — it would memoize a startup-time
operation that runs a handful of times per process — and keeping it as an "extension point" would
preserve four exported symbols whose only justification was the disproved claim. A stale second
copy of the same claim in `jose/policy.go` (a `Policy` described as "cached in the registry",
which no grep for the deleted identifiers would find) is corrected in the same change. No security
control moves: the algorithm allowlist, bidirectional-symmetry check and fail-fast kid resolution
all live in `scanRouteJOSE`, independent of the registry.

**Key Benefits:** Removes four exported symbols and, more importantly, a doc comment asserting a
performance property the framework does not have — which would have misled the next reader
reasoning about JOSE request cost. Fixes
[#817](https://github.com/gaborage/go-bricks/issues/817). See [migrations.md](migrations.md)
`[C58.1]`.

---

### [ADR-053: Delete `server`'s exported test-timeout constants](adr_053_remove_server_test_timeout_constants.md)

**Date:** 2026-08-07 | **Status:** Accepted

`server/constants.go` exported `TestShortTimeout` (100ms), `TestMediumTimeout` (1s) and
`TestLongTimeout` (5s) under a header asserting they "are used exclusively in test files for
simulating timeout scenarios and synchronization". Nothing in the repository referenced them, in
production or test code, for their whole life — the header described a convention that was never
adopted rather than one that lapsed. Adopting them was counted before being rejected: 14
occurrences of `100 * time.Millisecond`, 10 of `1 * time.Second`, 3 of `5 * time.Second`, but most
of the one-second hits are `SlowRequestThreshold` values (a threshold, not a timeout) and several
100ms hits are rate-limiter refill sleeps, so substituting a shared constant would name each value
after something it is not. Deprecating in place was rejected as the compatibility shim the
manifesto forbids. Where a shared test vocabulary is genuinely wanted, an unexported constant in
the test file is the right home — a framework exporting test-timing values constrains nothing,
since no production code ever read them.

**Key Benefits:** Three exported symbols leave the public surface along with a comment asserting a
convention the codebase never followed; nothing behavioral changes, since no framework code read
these values. Fixes [#818](https://github.com/gaborage/go-bricks/issues/818). See
[migrations.md](migrations.md) `[C58.2]`.

---

### [ADR-054: A cache the framework cannot construct aborts startup](adr_054_cache_construction_fails_startup.md)

**Date:** 2026-08-07 | **Status:** Accepted

`ResourceManagerFactory.CreateCacheManager` logged a WARN and returned a bare `nil` when
`cache.NewCacheManager` rejected its options — defeating the intent `BuildCacheOptions`
documents one function away, that a negative `cache.manager.*` value "must pass through and
fail loudly there instead of being silently swallowed into a live pool". The nil then
bypassed ADR-046 entirely: the probe walk of the time (`createHealthProbes`) registered a
cache probe only when the manager was non-nil, so with no manager there was no probe (since
ADR-067 the cache slot always registers its probe and a nil manager is the probe's `disabled`
result), `/ready` reported the cache `disabled`, and the pod answered `200` — a service that asked for a cache, got none, and
joined the rotation. Two paths reached it, and neither is the obvious one:
`app.NewWithConfig` with a hand-assembled `*config.Config` (that constructor never runs
`config.Validate`, so nothing checks the pool values), and `cache.enabled: false` carrying a
leftover negative (`validateCache` returns early for a disabled cache). A `config.Load`
deployment with the cache enabled was already caught by `applyCacheManagerDefaults`.
`CreateCacheManager` now returns `(*cache.CacheManager, error)`,
`appBootstrap.dependencies` propagates it, and `Builder.ResolveDependencies` records it in
`b.err`, so startup aborts. Promoting the WARN to a fatal inside the factory was rejected —
an exported constructor that terminates the process is a worse contract than one that
returns an error. Reaching the cache stays best-effort: the cache pre-init (now `Builder.performPreInitialization` over the cache slot, ADR-067) still WARNs and
continues on an unreachable Redis, which is a runtime condition, not a construction one.

**Key Benefits:** A cache misconfiguration fails at boot rather than at the first
cache-dependent request; the exported factory stops handing out a bare `nil` that panics on
use (the #859 zero-value guards cover `&cache.CacheManager{}`, not a nil pointer); and
ADR-046's critical-by-default posture stops being bypassable by breaking the cache badly
enough that no manager exists to probe. Fixes
[#861](https://github.com/gaborage/go-bricks/issues/861). See
[migrations.md](migrations.md) `[C58.3]`.

---

### [ADR-055: Reserve resource-identity namespaces in the OTel log bridge](adr_055_reserved_log_attribute_namespaces.md)

**Date:** 2026-08-07 | **Status:** Accepted

The OTel log bridge copied every zerolog field key verbatim into record attributes, and the
resource-attribute exporter deliberately lets record attributes win over resource attributes on
collision (that precedence keeps a caller-set `log.type` authoritative for dual-mode routing) — so
a log call could set a record-level `service.name` that shadows the service's identity on backends
that flatten record attributes over resource attributes. The bridge now reserves `service.*`,
`telemetry.sdk.*`, and `deployment.environment.name` at the boundary where field names become
attributes: a colliding top-level key is remapped under the `app.` prefix with its value preserved,
and the first remap per bridge instance (one bridge per process in practice) emits a one-time WARN
naming the keys (never the values — the WARN bypasses zerolog, and therefore the
`SensitiveDataFilter`, to avoid writer-chain recursion). Dropping the field was rejected as
anti-forensic and data-destroying; a per-key WARN dedup map was rejected as an unbounded allocation
under caller-influenced key churn. `log.type` stays caller-settable; the exporter's precedence and
the resource-level identity are untouched.

**Key Benefits:** Record-level identity spoofing neutralized for every backend; the no-collision
hot path stays allocation-free; the remap is self-evidencing per record. Fixes
[#915](https://github.com/gaborage/go-bricks/issues/915). See
[migrations.md](migrations.md) `[C58.4]`.

---

### [ADR-056: The log enricher stamps only the `log.type` delta](adr_056_log_enricher_delta_attributes.md)

**Date:** 2026-08-07 | **Status:** Accepted

OTel's `LoggerProvider` holds one resource for all processors, so dual-mode logging wraps each batch
processor's exporter to stamp its own `log.type` on records. That wrapper was handed the **merged**
resource instead of the delta, so it worked from the resource's whole attribute set — `service.name`,
`service.version`, `deployment.environment.name` and the `telemetry.sdk.*` triplet, plus anything
`OTEL_RESOURCE_ATTRIBUTES` injects — adding to every exported log record each of those keys the
record did not already carry,
duplicating what the OTLP `ResourceLogs.resource` block already ships once per batch, while
dropping the one attribute it existed for, because every record leaves the bridge already carrying
`log.type` and the record-wins collision branch therefore fired every time. The wrapper (renamed
`processorAttributeExporter`) is now constructed with the delta alone, and `createLogResource` is
deleted. Record-wins precedence is unchanged: a caller-set `log.type` stays authoritative for
dual-mode routing, and a third-party record emitted straight through the OTel API — carrying no
`log.type` — still gets stamped by the trace processor, which is why the wrapper survives at all.

**Key Benefits:** At least six fewer attributes per log record emitted through the go-bricks
logger — ADR-055's bridge remaps colliding caller fields under `app.` first, so those six were
always being added — and more wherever `OTEL_RESOURCE_ATTRIBUTES` adds to the resource, since the
full resource attribute set is affected. A record that already carries `log.type`, which is every
record the bridge emits, now skips enrichment entirely (no `Clone`, no `AddAttributes`); a record
without one still takes exactly one of each. The framework-provided service identity appears
exactly once on the wire, in the resource block where it was never spoofable,
strengthening [ADR-055](adr_055_reserved_log_attribute_namespaces.md). Fixes
[#914](https://github.com/gaborage/go-bricks/issues/914). See
[migrations.md](migrations.md) `[C58.5]`.

### [ADR-057: The client IP is derived through trusted proxies, not raw `X-Forwarded-For`](adr_057_trusted_proxy_ip_extraction.md)

**Date:** 2026-08-10 | **Status:** Accepted

[ADR-015](adr_015_echo_v5_migration.md) installed `echo.LegacyIPExtractor()` to restore v4-compatible
`RealIP()` across the echo v5 hop and recorded replacing it with trusted-proxy-aware extraction as a
follow-up. The shim returns the left-most `X-Forwarded-For` entry with no validation, else `X-Real-IP`,
else the peer — so the identifier both rate limiters throttle on was a string the caller writes. The IP
pre-guard keys on nothing else and was defeated outright by rotating the header; the global limiter was
defeated for exactly the untenanted traffic its IP fallback covers, including every `/health` and
`/ready` request, whose per-IP ceiling the framework documents as the only throttle on an
unauthenticated database round trip. Because the bucket key was attacker-chosen, so were collisions: a
caller could consume the prober's budget and push `/ready` to `429`, dropping the instance from the load
balancer's rotation. [ADR-043](adr_043_forwarded_client_cert.md) had already named this finding (F23) as
the anti-pattern it refused to repeat. `New` now installs
`echo.ExtractIPFromXFFHeader`, which walks the chain right-to-left and returns the first untrusted hop,
plus the new `server.trustedproxies` CIDR list as additive trust. Echo's loopback/link-local/RFC1918
defaults are kept, so an in-VPC ALB deployment is correct with zero configuration; a malformed entry —
unparseable, host bits set, or a default route — aborts startup rather than silently changing who is
trusted. `X-Real-IP` is deliberately not honored.

**Key Benefits:** Both limiters throttle on an address that only a caller already inside loopback,
link-local, RFC1918, or IPv6 unique-local space can choose — the trust boundary moved rather than
vanished — so the pre-guard's per-IP ceiling on `/ready` holds against the public internet and
`client_ip` in access logs stops being authored by an arbitrary caller.
No access-control decision was ever affected — the debug allowlist and scheduler CIDR middleware already
used the safe `server.ClientIP` path. **Watch:** rate-limit buckets and the client address logged at all
five `RealIP()` sites change value; a proxy on a public address now needs a `server.trustedproxies`
entry; and a proxy that writes a non-IP XFF entry (AWS ALB's `routing.http.xff_client_port.enabled`)
makes echo abandon the chain and key the whole fleet on the load balancer — a deployment-side fix that
`server.trustedproxies` cannot rescue. The tenant-keyed half of F23 remains open. See
[migrations.md](migrations.md) `[C59.1]`.

### [ADR-058: Consumers carry per-consumer AMQP arguments, at the cost of struct comparability](adr_058_consumer_scoped_amqp_arguments.md)

**Date:** 2026-08-11 | **Status:** Accepted

[ADR-040](adr_040_declaration_args_passthrough.md) made declaration `Args` reach the broker, which was
enough to **declare** a RabbitMQ stream queue (`x-queue-type: stream`) but not to **consume** one:
`ConsumeFromQueue` passed a hardcoded `nil` args table, so `x-stream-offset` — a per-consumer argument on
`basic.consume`, not a queue argument — had nowhere to go. Every consumer silently attached at the broker
default `next`, so a stream declared for replay delivered only what was published after the consumer
connected. `Args map[string]any` is added to `ConsumerOptions`, `ConsumerDeclaration` and `ConsumeOptions`,
deep-copied on register/clone and folded into `Declarations.Hash()` so two consumers differing only in
start offset are not treated as a duplicate replay. `DeclareStreamQueue` adds the queue-type and opt-in
retention args; `Validate` rejects four shapes the broker would otherwise refuse with an opaque channel
error (non-durable, exclusive/auto-delete, `AutoAck` on a stream, `x-stream-offset` on a non-stream queue);
a declared `int` offset is widened to `int64` because amqp091 encodes Go `int` as a 32-bit field; and the
flap-resume re-subscribes at `last + 1` on a copied args map, correct because `handleMessages` drains its
worker pool before returning.

**Key Benefits:** Stream queues become declarable *and* correctly consumable over the existing AMQP
connection, port, tenant manager, worker pool and OTel instrumentation — no second messaging stack and no
new dependency. The `Args` field is general, so `x-priority` and future per-consumer arguments need no
further struct widening. **Watch:** a map field makes a struct non-comparable, so `==` and map-key use on
those three types **stop compiling** — accepted and documented rather than shimmed, since a pointer-to-map
would restore only pointer identity and silently change meaning. This lane has no server-side offset
tracking, no single active consumer and no super streams; those are stream-protocol features and are
ADR-059's subject. See [migrations.md](migrations.md) `[C59.2]`.

### [ADR-059: Native stream consumption commits offsets only after successful handling](adr_059_streams_consumption.md)

**Date:** 2026-08-12 | **Status:** Accepted

ADR-058 added the AMQP 0.9.1 stream-queue lane, which cannot reach what streams are for:
AMQP has no server-side offset store, no single active consumer, and no super streams. The new
`messaging/streams` package speaks the native stream protocol (port 5552) through
`rabbitmq-stream-go-client` v1.8.3, for **consumption only** — publishing stays out, and a future
producer must reuse this manager's Environment rather than open a second connection path. The
client's `SetAutoCommit` is deliberately unused because it advances offsets for *delivered*
messages: instead the framework commits the last offset whose handler returned `nil`, on a
count/interval policy plus a final flush before each consumer closes — a flush that narrows the
replay window rather than closing it, since nothing joins an in-flight callback. A handler error or
recovered panic skips the commit, so the failed message is skipped — streams have no nack — only
once a later success commits a higher offset; restart before that and it replays. Handlers run
**inline and sequentially**, the deliberate opposite of the AMQP lane's `NumCPU*4` pool, because a
worker pool would break log order and make a committed offset claim work it had not finished.
`messaging.streams.uri` is configured, never derived from `messaging.broker.url`, and
`multitenant.enabled` beside it fails startup.

**Key Benefits:** A restart resumes from the last stored offset with no client-side offset store to
operate — delivery is at-least-once, so handlers must be idempotent — and SAC gives failover (not
throughput) without a second coordination mechanism. **Watch:** a new
dependency (snappy, lz4, murmur3, pkg/errors, a `klauspost/compress` bump) that Renovate will track
and whose go.mod churns OTel versions; per-consumer throughput is bounded by one handler; a failed
message is lost to the consumer until failed-message parking exists; and multi-tenant deployments
keep the AMQP lane. New keys: `messaging.streams.uri`, `messaging.streams.addressresolver.*`,
`messaging.streams.offsetstore.*`. See [streams.md](streams.md).

### [ADR-060: `CompareAndDelete` gives the cache interface a safe conditional release](adr_060_cache_compare_and_delete.md)

**Date:** 2026-08-12 | **Status:** Accepted

`cache.Cache` has advertised `CompareAndSet` as the distributed-locking primitive since
[ADR-011](adr_011_redis_cache.md) while offering no safe release: the only one was unconditional
`Delete`, so a worker whose work outran the TTL cleared the *next* holder's lock, and the interface's
own godoc told callers to live with it. Two independent reporters asked for the same method — #823
from the locking side, #966 item 2 from conditional eviction — and it could not be emulated, since
`casScript` has no `DEL` branch and `CompareAndSet(…, workerID, nil, 0)` writes an empty string that
still occupies the key, with no expiry. `CompareAndDelete(ctx, key, expectedValue) (deleted, err)`
removes the key only while `expectedValue` is what is stored, via a sibling single-purpose
`cadScript` (rather than a third mode on `casScript`, departing from plan 071's note: a
single-behavior script needs no mode discriminator, which is the stronger reading of what #830
fixed). A nil `expectedValue` is rejected with the new `cache.ErrNilExpectedValue` before any round
trip, because go-redis renders a nil `[]byte` as a zero-length bulk string that would silently match
a key holding the empty string.

**Key Benefits:** The locking contract is completable — a lock that lapsed mid-work is left alone
instead of stolen back — and conditional eviction no longer needs an undecodable tombstone. A new
mock↔client parity test (with an expiry case) pins both implementations to the same answers.
**Watch:** a method on an exported interface **stops external implementers compiling**, and the break
usually surfaces in *test doubles*, which `go build ./...` does not compile — use `go vet ./...`. Two
new caller hazards: a lock acquired with `ttl == 0` is held forever once its token-verified release
returns false, with no expiry to recover it, and `false`/error are both **terminal** — falling back
to `Delete` reinstates the original hazard behind an API that reads as safe. `false` does not
distinguish a failed comparison from an already-gone key, which is why the result is named
`deleted`. See [migrations.md](migrations.md) `[C59.3]`.

### [ADR-061: Redact Role Passwords Before the First-Line Split, and Reject Control Characters in Them](adr_061_role_password_control_chars.md)

**Date:** 2026-08-14 | **Status:** Accepted

`summarizeStmt` exists to keep a resolved role password out of the provisioning errors callers log,
but it split the statement at its first newline **before** applying the redaction regex — and that
pattern, `(?i)(PASSWORD\s+)'(?:[^']|'')*'`, is anchored on the closing quote. A password containing
a newline (the trailing `\n` a file-sourced or `echo`-piped secret normally carries) produced a
multi-line `ALTER ROLE … PASSWORD '…` whose first-line fragment ended mid-literal, matched nothing,
and reached the returned error verbatim. Nothing upstream normalized: `quotePGStringLiteral` passes
newlines through, and `PGRoleSpec` has no in-repo producer, so `Validate` is the only boundary the
framework owns. The redaction now runs over the whole statement before the split and the truncation
(Go's RE2 `[^']` matches `\n`, so no `(?s)` is needed), and `PGRoleSpec.Validate` additionally
rejects CR/LF/NUL in either password field with the new `ErrPGRolePasswordHasControlChar`, naming
the field and never the value.

**Key Benefits:** The redaction holds for the input shape secrets pipelines most commonly produce,
and a value the provisioning path cannot carry on one line is refused at the boundary rather than
trusted to every downstream formatter. The character set (CR/LF/NUL, not "all control characters")
deliberately matches `flyway.go`'s `ErrEnvFieldHasControlChar` so the two boundaries agree.
**Watch:** this is **breaking** — a spec carrying such a password used to provision successfully, so
`ProvisionPGRoles` and `PGRoleProvisioningSQL` now return an error, and callers reading a password
from a file or command substitution must `strings.TrimSpace` it first. Empty passwords stay valid.
Normalization was rejected because trimming silently changes a credential, and no
`minRedactablePasswordLength`-style floor is imported: this redaction is *structural*, never
comparing against the secret's bytes. The fix stops future leaks, not past ones — rotate any
credential whose provisioning failure was logged. See [migrations.md](migrations.md) `[C59.5]`.

### [ADR-062: Fail Closed on `database.tls` Misconfiguration (Mode Allowlist + Material/Mode Coherence)](adr_062_database_tls_fail_closed.md)

**Date:** 2026-08-14 | **Status:** Accepted

`database.tls` reached pgx unvalidated apart from the cert/key pairing check, so five shapes booted
green while doing something other than what was configured: `disable`/`allow`/`prefer`/unset plus
`cert`+`key` connected with **no client certificate**; a path-valued `ca:` with no mode ran with
**no server authentication** (pgx defaults to `prefer`, which sets `InsecureSkipVerify`; the
`ca: system` sentinel was instead force-upgraded to `verify-full`); a typo'd mode passed
validation and died inside `NewConnection` behind go-bricks' deliberate parse-error redaction —
lazily, at first request, for multi-tenant deployments; a `tls:` block alongside `connectionstring`
was silently ignored entirely; and Oracle accepted `database.tls.mode`, implying TLS go-ora never
negotiates. Validation — at startup on every path that runs `config.Validate` (root block, named
databases, static tenants) and, since #1002, at connection acquisition on the dynamic seam — now
enforces an sslmode allowlist, requires
`require`/`verify-ca`/`verify-full` wherever cert/key/ca are set, rejects the block alongside a
connection string, and rejects it wholesale on Oracle — with all four fields trimmed once at the
vendor-dispatch seam.

**Key Benefits:** Every remaining accepted `database.tls` shape does what it says. Failures move
from a redacted connect-time parse error (or no error at all) to a validation error naming
`database.tls` and the fix — at boot for static configs, at acquisition for dynamic records. **Watch:** this is **breaking** — previously-booting configurations now
abort. A valid mode *without* material stays allowed, `disable` included, and a `connectionstring`
with ssl parameters embedded remains the escape hatch for pgx-native semantics the rules refuse.
Not covered: unrecognized-scheme DSNs (the vendor dispatch's `default` arm) and the
`tools/migration` CLI, which never calls `config.Validate`; dynamic `DBConfigProvider` records
ARE covered since #1002 routed that seam through the vendor gate. See
[migrations.md](migrations.md) `[C59.11]`.

### [ADR-063: Native stream publishing is synchronous and confirmed, correlated by message pointer](adr_063_streams_native_publishing.md)

**Date:** 2026-08-15 | **Status:** Accepted

[ADR-059](adr_059_streams_consumption.md) shipped `messaging/streams` for consumption only and left
native publishing as future work with one constraint: it must reuse that manager's Environment. It
now does. `DeclarePublisher` and `DeclareSuperStreamPublisher` return an inert handle at declaration
time that `Manager.Start` binds to a client producer — before any consumer starts, because a handler
may publish from its first delivery. The surface is **sync-confirmed only**: the client's `Send` is
asynchronous and its `checkWriteError` swallows every write error except `FrameTooLarge`, so a `nil`
from it proves nothing and the broker confirmation is the only observable truth. Confirmations are
correlated by the **message pointer** the client hands back, which is valid only at the default
`SubEntrySize` of 1 — the reason the producer options are the client's defaults verbatim, with no
deduplication, batching or compression. Because the client's send path parks on a bare `sync.Cond`
during a reconnect, the send runs on a goroutine the caller's context can abandon; a context expiry
**tombstones** the correlation entry rather than removing it, so a send that has not routed yet still
finds its routing key instead of hashing `""` onto one partition. Super streams route by murmur3 with
RabbitMQ's shared seed, interoperable with the Java/.NET/Python clients; `RoutingKey` is required
non-empty there and rejected on a plain stream.

**Key Benefits:** A natively-consuming service can publish over the same connection with a confirmed
result, and super-stream partitioning reaches the framework surface for the first time. No new config
keys — the client's defaults and the caller's context are the only settings in play, and that context
bounds the caller's wait rather than the send behind it. **Watch:** that wait is only as bounded as
the context passed to it, so background callers must supply a deadline; a context
timeout is **not** proof of failure, delivery stays at-least-once and consumers must be idempotent;
an abandoned send leaks a vendor goroutine and holds its map entry until the publisher closes, **with
no cap on how many accumulate** during a reconnect (the client's `QueueSize` cannot bound them — a
send parked in `isReadyToSend` never reaches the queue), so the growth is publish-rate × outage; and
the correlation rests on a vendor-internal guarantee that a client upgrade must re-verify (the
integration round trip is what fails loudly if it stops holding). Publisher close sweeps every
outstanding waiter with `ErrPublisherClosed`, because the client's `entityClosed` confirmations cannot
reach a send that never enqueued. An outstanding-send limit, deduplication, key routing, sub-entry
batching, compression and outbox relay are all deferred. See [streams.md](streams.md).

### [ADR-064: The App Validates Every Config It Is Handed](adr_064_app_validates_every_config.md)

**Date:** 2026-08-16 | **Status:** Accepted

`config.Load` validates; `app.NewWithConfig` did not. ADR-050 documented the obligation — hand-built
configs must run `config.Validate` before `NewWithConfig` — but nothing enforced it, so parallel
machinery softened the bypass instead: `app.Builder.ConfigureRuntimeHelpers` re-walked the database
tree for untyped DSNs, `app/managers.go` mirrored config defaults, `messaging.NewMessagingManager`
carried a single-tenant-only fallback, and `app/lifecycle.go` guarded cleanup intervals for
Validate-bypassing callers only. `app.Builder.WithConfig` now runs `config.Validate` itself, so every
construction path — `New`, `NewWithOptions`, `NewWithConfig`, direct `Builder` use — validates and
stamps defaults the same way.

**Key Benefits:** One validation path instead of four drifting mirrors, and multi-tenant defaults
reach every construction path instead of only `config.Load` output. `Validate` is idempotent, so
revalidating already-loaded config costs microseconds. **Watch:** this is **breaking** — a hand-built
config that `config.Validate` rejects (missing `app.name`/`app.version`, zero server timeouts, an
invalid vendor) now fails at construction instead of booting on whatever the mirrors papered over;
the fix is named in the `ConfigError`'s action line. The app-side mirrors themselves become dead
weight and are deleted in a follow-up PR. See [migrations.md](migrations.md) `[C59.12]`.

---

### [ADR-065: keystore.secretminlength Is a Tri-State Setting](adr_065_keystore_secretminlength_tristate.md)

**Date:** 2026-08-16 | **Status:** Accepted

ADR-064 exposed a pre-existing bug: `KeyStoreConfig.SecretMinLength` was a plain `int`, so a
hand-built config that never set it booted with the symmetric-secret floor silently off, while the
same absence through `config.Load` meant 32. The `int` encoding could not tell "explicitly off"
apart from "never set". `SecretMinLength` becomes `*int` — the same tri-state pattern `cache.critical`
established (ADR-046): `nil` means "apply the default" (32, the new `config.DefaultKeyStoreSecretMinLength`),
`0` is an explicit, deprecated opt-out, `N > 0` sets the floor to `N`. `normalizeKeyStore` fills the nil
case and `KeyStoreConfig.SecretFloor()` reads it (config owns the nil semantics, as `IsCacheCritical` does); `Module.Init` WARNs once when the
floor is disabled and once per admitted secret under 32 bytes, naming the key and length, never the
material. Go literals write `new(n)`.

**Key Benefits:** Both configuration doors render one value for "nothing configured", and a hand-built
config can no longer silently disable the floor by omission. **Watch:** this is **breaking** for Go
literals only — `SecretMinLength: 0` or `: N` no longer compiles; write `new(0)` / `new(N)`. YAML and
env config are unchanged. A hand-built config that relied on the absent field meaning "off" now enforces
32 bytes, so a shorter secret that used to boot now fails startup — the fix, not a regression. The `0`
opt-out stays but is deprecated; a later ADR is expected to remove it (#1036). See
[migrations.md](migrations.md) `[C59.13]` and `[C59.14]`.

---

### [ADR-066: Readiness Is One Module — One Status Vocabulary, One Gate, One Body Rule](adr_066_readiness_one_module.md)

**Date:** 2026-08-16 | **Status:** Accepted

Readiness was written once per kind: three copies of the lease → liveness → status machine in
`app/health.go`, each inventing its own sub-status strings, plus two different readiness
*models* reading them — `/ready` gated on `Err != nil && Critical` while the debug summary gated
on a status list — so a not-ready messaging client or a streams manager with closed consumers
was 200 on `/ready` and `overall_status: unknown` on the debug view. Readiness becomes one
module (`app/readiness.go`): every kind hands it a **probe description** (name, criticality
decided once, absence, per-tenancy, how to lease, how to check liveness, statistics) and one
machine judges all of them. One status vocabulary (`healthy · unhealthy · not_configured ·
disabled · per_tenant`, `unhealthy` always with an `Err`) ships with the module; the second
slice of the same stack lands one gate (*failing && critical*) shared by `/ready` and the
debug summary and one body rule (`<name>` + `<name>_stats` per kind, public allowlist,
ADR-048 sanitized error text). `Prober`/`HealthStatus` are unchanged.

**Key Benefits:** The next readiness rule lands in one file; adding a kind is one description.
**Watch:** visible strings move — streams `not_ready` → `unhealthy`, the `details.status`
sub-strings collapse into the vocabulary, messaging/cache read `per_tenant` in multi-tenant
deployments, a disabled kind's stats render `{"status":"disabled"}`, and `db_stats`
becomes `database_stats`. No Go API change. See [migrations.md](migrations.md) `[C60.3]`.

---

### [ADR-067: Slots Own the Per-Kind Lifecycle](adr_067_lifecycle_slots.md)

**Date:** 2026-08-17 | **Status:** Proposed (first slice shipped; Accepted when the last slot slice merges)

Every resource kind's lifecycle facts — construct, expose, pre-init, probe, maintenance,
close, render — live in about ten `app/` files that each hand-enumerate the kind set, which
is why adding the streams kind touched six files and still needed a runtime-registration
exception. A **slot** owns one kind's whole lifecycle: an unexported `resourceSlot`
interface with four per-kind structs (compiler-checked completeness), phases
`probe · preInit(fatal) · start · stop · close`, one registration order
`database → messaging → cache → streams` with FIFO close, and maintenance moved
manager-side so `DbManager`/`messaging.Manager` self-start idle cleanup at construction.
`App` keeps its typed manager fields; the Builder steps keep their names and iterate slots;
the streams slot exists at build time with a nil manager and constructs in `start`. Ships
as stacked PRs — the first deletes the pass-through helpers the slots replace.

**Key Benefits:** Adding a resource kind becomes one slot file instead of ten edits, and
the compiler enforces that every phase was considered. **Watch:** the first PR removes
sixteen `app` symbols nothing outside `app/` referenced (`MessagingInitializer` and
`ConnectionPreWarmer` and their methods, `Options.Database`, `Options.MessagingClient`) and
unexports eight debug response types with their JSON unchanged. See
[migrations.md](migrations.md) `[C60.4]`.

---

### [ADR-090: User-Named Config Sections Must Be Reachable By Environment Variable](adr_090_env_reachable_section_names.md)

**Date:** 2026-08-30 | **Status:** Accepted | **Breaking:** a `databases` / `multitenant.tenants` / `keystore.keys` key outside `^[a-z0-9-]+$` now fails startup

`Load` lowercases an environment variable and maps `_` to `.`, koanf's delimiter — a transform that
is not injective. ADR-024 fixed that for FRAMEWORK leaf keys by renaming them; the keys an operator
chooses were never judged. Verified on `main`: a lone `databases.report_db` plus
`DATABASES_REPORT_DB_PORT` fails startup blaming a phantom `databases.report`, and with a real
sibling `databases.report` present the same variable is applied SILENTLY to the sibling's subtree
while `report_db` keeps its YAML value — dropped where the remaining segments name no field, landing
on the sibling's own setting where they do (`report_pool` beside `report` reaches
`databases.report.pool.max.connections`), and the pod comes up green either way. The decision rejects the name instead: a key under `databases`,
`multitenant.tenants` or `keystore.keys` must match `^[a-z0-9-]+$`, checked in `config.Validate`, so
the existing transform is injective over every key that survives startup. The `ConfigError.Field` is
the key path and the action says rename. Hyphen is legal (the resolver's tenant-ID grammar, minus its
length bound); whether a hyphenated name is settable is the runtime's business — Docker and
Kubernetes allow `-`, POSIX `export` does not — and the docs say so. Header maps are excluded:
a header name is a protocol identifier. Dynamic tenant sources are gated by the resolver at request
time, not here. Rejection rather than a warning because the failure it replaces is silent: a warning
is read only when someone is already looking.

**Key Benefits:** a section that cannot be driven from the environment can no longer reach
production, and the silent-sibling collision becomes a startup error naming the offending key.
**Migration:** [migrations.md](migrations.md) `[C61.22]`.

---

### [ADR-089: A Failed Stream Delivery Is Retried, Then Held Per Tenant](adr_089_per_tenant_hold_on_the_streams_lane.md)

**Date:** 2026-08-29 | **Status:** Accepted | **Breaking:** no — a consumer that does not declare `Hold` keeps ADR-059's skip

ADR-059 settled a failed stream delivery by skipping it, which is right for independent events and
wrong for dependent ones: the funding that follows a failed account creation applies to an account
that does not exist. Stalling the partition instead punishes every tenant that hashes to it. A
consumer declaring `Hold: true` now retries in place under a bounded policy (`streams.RetryOptions`,
capped at 10 attempts and 1m of waiting; `streams.Permanent(err)` ends it early; a panic is never
retried), then **parks the tenant**: the message and a tenant marker go to the `inbox` hold ledger in
one durable write, and the offset commits only after it. A held tenant's later messages are gated —
parked, not delivered — so the tenant's order survives. The `inbox-hold-drain` job takes each due
tenant under a lease and replays its rows through the consumer's own handler in ledger order,
deleting each only after its replay succeeds and releasing the tenant when the last row drains. The
ledger is control-plane (`inbox.tenancy: shared`), because the tenant's own database may be exactly
what is down; a held message is never auto-dropped.

**Key Benefits:** a tenant's dependent events survive a failure in order, isolated from its partition
mates, with `inbox.hold.{tenants,rows,oldest_age}` and a max-age WARN over the backlog.

---

### [ADR-088: The Outbox Ledger Is Sequenced, Laned, and Drained by One Leader](adr_088_outbox_ordered_leader_relay.md)

**Date:** 2026-08-30 | **Status:** Accepted | **Breaking:** `config.OutboxConfig` stops being comparable; `outbox.Store` gains `Lead`; a migrated ledger needs an explicit `seq` backfill before the new relay runs

The relay ordered by `created_at`, which TIES under concurrent inserts, and preserved no order
across a failure at all — a row that failed was retried later while the rows behind it went out
immediately, so an aggregate's second event could reach the broker before its first. Nothing
stopped two replicas draining the same ledger either, so the duplicate rate scaled with the
replica count and ordering had no meaning even within one key. Both gaps are invisible in a
single-replica deployment with a healthy broker, which is why they survived. Every row now
carries a database-assigned per-ledger `seq` that `FetchPending` orders by, plus a `lane`
column; a cycle takes a companion `<table>_leader` row `FOR UPDATE NOWAIT` before fetching —
before, so it cannot inherit rows the previous leader published between the read and the lock —
and probes that claim before every record, so a deposed leader stops within one record. A row
that FAILS parks its key's later rows for the cycle without publishing or marking them, keyed by
the scope it competes in: the tenant stamp, the destination, or the stream and partition key.

**Key Benefits:** at-least-once stops meaning "duplicated per replica", and a failing key no
longer lets its own later events overtake it.
**Migration:** [migrations.md](migrations.md) `[C61.23]`.

---

### [ADR-087: The Messaging Kind Has a Tenancy, and the Tenant Travels as a Stamp](adr_087_messaging_tenancy_and_tenant_stamp.md)

**Date:** 2026-08-30 | **Status:** Accepted | **Breaking:** no — `messaging.tenancy` defaults to `per-tenant`, which is the existing behaviour; a caller-supplied `x-tenant-id` publish header is now an error rather than being silently overwritten

The multi-tenant messaging shape was one client per tenant, which is **vhost-per-tenant**: a
connection per vhost per replica, vhost cost that hurts in the thousands, and per-tenant quorum
queues against a Raft ceiling. It is also unusable for a consume-only service, which has no
per-tenant broker to name and had to invent tenant messaging blocks to satisfy a check.
`messaging.tenancy: shared` gives the KIND a tenancy: consumers replay once at boot on the
control-plane key, `deps.Messaging(ctx)` resolves the control-plane publisher with no tenant, and
the tenant travels instead as the `x-tenant-id` **tenant stamp** — an AMQP 0.9.1 header or an AMQP
1.0 application property, written by the framework alone from the publishing context, read back by
the delivery pipeline both lanes share so neither can drift. **Stated goal: zero broker objects per
tenant** — onboarding touches the database side only. A missing or malformed stamp fails closed
before the handler, naming the reason and the byte length but never the value;
`ConsumerOptions.TenantOptional` exempts a control-plane consumer from needing one, never from
refusing a malformed one. The accepted cost is stated plainly: the stamp is identification, not
authorization, so under shared tenancy **the shared queue's publish ACL is the tenant-isolation
boundary**.

### [ADR-086: The Sensitive-Data Filter Masks Inside Opaque Payloads](adr_086_mask_inside_opaque_payloads.md)

**Date:** 2026-08-28 | **Status:** Accepted | **Breaking:** a masked payload is re-encoded (key order, whitespace); an unreadable JSON-looking payload renders as the mask value

The filter masks by field NAME, so an **opaque payload** — bytes or a string whose structure it
cannot see into — was one leaf however many named fields it carried. Verified with the default
config: a `json.RawMessage` `{"password":"pw"}` logged in clear through `Interface`, `WithFields`
and `Bytes`; a root JWK logged its `d`; a plain JWKS leaked (ADR-072's `log.sensitivefields: [keys]`
is opt-in). The decision parses what looks like JSON — first non-space byte `{` or `[` — walks it
with the same needles, and re-encodes ONLY when something was masked, so a clean payload keeps its
key order and number spelling. Two shape rules sit on top of names: an object carrying `kty` has
`d p q dp dq qi k oth` masked wherever it sits, matched exactly rather than by substring; a PEM
block whose label ends in `PRIVATE KEY` is masked whole, while a certificate stays readable.
Unparseable, too deep, or over `FilterConfig.MaxPayloadBytes` (64 KiB default) masks whole — fail
closed, since the filter cannot say what is inside. JWTs, XML, form-encoded bodies and the `Msg`
text are deliberately not inspected.

**Key Benefits:** the needle list finally reaches the pre-encoded bodies consumers actually log, and
key material is caught by shape where no name needle could match.
**Migration:** [migrations.md](migrations.md) `[C61.18]`.

---

### [ADR-085: The Framework Owns the PostgreSQL Flyway JDBC URL](adr_085_framework_owned_flyway_url.md)

**Date:** 2026-08-24 | **Status:** Accepted | **Breaking:** `flyway.url` in a conf is outranked for PG discrete-field configs; PG mTLS migrations refused

`database.tls` reached Flyway only as `DB_SSL*` environment variables that the operator's
`flyway.conf` may or may not have interpolated into its own JDBC URL — so a conf naming no
`sslmode` migrated in cleartext while `database.tls.mode: verify-full` validated cleanly, and a
conf naming a different host migrated a different database than the runtime connects to. The
only signal was a once-per-migrator WARN. The decision has the framework build
`jdbc:postgresql://host[:port]/db?ApplicationName=…[&sslmode=…][&sslrootcert=…]` from
`database.*` and pass it as `-url=`, which outranks the conf, for every PostgreSQL
discrete-field config — TLS configured or not, with no escape hatch. Credentials stay
environment-delivered (argv is world-readable); the WARN and the whole `DB_SSL*` export are
removed; `database.tls.cert`/`key` fail closed on `ErrMigrationMTLSUnsupported` rather than
connecting without the client certificate, because the framework does not forward them as
`sslcert`/`sslkey` — the limit is ours, not a pgjdbc format rule. Oracle and TLS-free
`connectionstring` configs keep the conf-owned URL; a `connectionstring` carrying
`database.tls.*` fails on `ErrMigrationTLSWithConnectionString`, since the migrator cannot
assume the per-tenant config passed `config.Validate`.

**Key Benefits:** a `database.tls` setting that is a guarantee rather than an advisory, one
source of truth for the migration target, and `ApplicationName` without a per-conf hand-edit.
**Migration:** [migrations.md](migrations.md) `[C61.4]`.

---

### [ADR-084: Response Error Details Carry No Request Input](adr_084_response_error_details_carry_no_request_input.md)

**Date:** 2026-08-24 | **Status:** Accepted | **Breaking:** `server.FieldError.Value` removed

The framework's own 400 details echoed caller text twice: `FieldError.Value` was the rejected
input for any failed tag, and `FieldError.Field` carried a `dive`-validated map's input key
verbatim (`Limits[4111111111111111-SECRET]`), as did the message built from it. A bind failure
rendered `bindErr.Error()` — the JSON decoder's field path, or a strconv error quoting the
rejected value. All of it was gated on `IsDevelopment()` alone, and response bodies never pass
through the log path's `SensitiveDataFilter`. The decision moves messaging's safe-rendering
primitives into `internal/saferender` unchanged, removes `Value` rather than redacting it,
redacts the bracketed namespace span at both doors, renders bind failures as a summary naming
the binding source and the destination field by struct tag, and puts every response detail map
behind `Debug && IsDevelopment` at the single `devDetails` funnel — the `[C60.30]` posture, now
at every status and on both renderers.

**Key Benefits:** one owner for a rule two transports need; a `FieldError` that cannot leak
because it holds nothing to leak; and a bind summary whose inputs are all author-written.
**Migration:** [migrations.md](migrations.md) `[C61.1]`, `[C61.2]`.

---

### [ADR-083: Every Framework Span Sink Records an Error By Type, Through One Helper](adr_083_span_sinks_record_errors_by_type.md)

**Date:** 2026-08-24 | **Status:** Accepted | **Breaking:** span `exception.message` and
message-bearing span status descriptions removed at four sinks

`span.RecordError(err)` shipped a consumer-authored `err.Error()` to the tracing backend, and
every site copied the same text into the span status description. Both are off-platform sinks —
the vendor owns their retention and export path, and the logger's `SensitiveDataFilter` never
sees either — while the message is a string the framework did not write: a job's `Execute`
error, a handler's error, an interceptor's error, a caller's `RoundTripper` error. Two sites had
already worked this out in place and in two different spellings (the database sink's `%T`, the
HTTP client's query-stripped message, which still trusted the rest of the stringification). The
decision makes `observability.RecordErrorByType` the only spelling of "record an error on a
span" in framework code: one `exception` event with `exception.type` = the outer `%T`, no
`exception.message`, and `codes.Error` with that type as the description. Log lines at every
converted site are unchanged — that sink is on-platform, and #1168 gives it a consumer redactor.

**Key Benefits:** one rule a reader can predict without opening the site; a `git grep` that
enforces it; and a leak class closed at the seam instead of per-site. **Migration:**
[migrations.md](migrations.md) `[C61.3]`.

---

### [ADR-082: Identifier Arguments Are Validated At Every Door, and the Renderer Escapes Wherever It Quotes](adr_082_identifier_arguments_validated_at_every_door.md)

**Date:** 2026-08-23 | **Status:** Accepted (decision); implementation staged — the renderer and table
arguments ship here, the `Select`/`Insert` column and Filter/JoinFilter stages are tracked in #1143 |
**Supersedes:** the Filter exclusion in ADR-031

ADR-031 closed the M9 identifier-injection class on `From`, the JOIN family, `OrderBy`,
`GroupBy`, `Set`/`SetMap` and `DeleteQueryBuilder.OrderBy`, and excluded the Filter API
because "those are already parameterized". `f.Eq(column, value)` takes TWO arguments: the
value is parameterized, the column is interpolated — verbatim on PostgreSQL. One sentence
excluded thirty methods, and is why database.md lists the guarded doors and is silent on the
rest. Separately both renderers wrapped an identifier without doubling the quotes inside it
(#1104), so `role" = 'admin', "name` rendered as a second assignment rather than a name. The
decision is BOTH remedies: validate every identifier argument at its door, and double
interior quotes wherever the renderer already quotes — narrow, since quoting a bare
PostgreSQL identifier would refold its case (ADR-007/M7). Table arguments join the rule on
all five INSERT and upsert doors. `Having` takes a predicate, not an identifier, so it is
documented as a raw-SQL door instead (#1146) rather than validated against a grammar no real
call could satisfy. Lands in stages; the remaining doors are #1143.

**Key Benefits:** one rule at every door instead of a per-method accident; a renderer that is correct for
every shape except the function-shaped pass-through it documents as a known gap; and a glossary that now names identifier
argument and bound value apart, so the conflation cannot be restated without contradicting
it. **Migration:** [migrations.md](migrations.md) `[C60.24]`, `[C60.25]`.

---

### [ADR-081: A Recovered Panic Value Is Reported By Type, Never By Value](adr_081_recovered_panic_values_reported_by_type.md)

**Date:** 2026-08-22 | **Status:** Accepted | **Corrects:** ADR-079

ADR-079 reported a recovered panic value through the sensitive-data filter and called it
"masked by field name exactly as any other logged value". The filter matches FIELD names and
the field is `panic`, which is no needle — so a bare `panic("secret")` was emitted in clear,
as was a map key the needle list does not name, while a key it names was masked. Protection
varied with the shape of a value chosen by consumer code. All four recovery sites — `migration/audit_emitter.go`,
`scheduler/module.go`, and `messaging/internal/delivery`'s `AppendOutcome` and `settleOnce` —
now report the value's TYPE only. `AppendOutcome` is the delivery spine shared by both
messaging lanes, so the rename reaches every consumer whose message handler panics.
The audit emitter's two-tier report collapses to one, its fallback having existed solely
because rendering the value could panic.

**Key Benefits:** no consumer-chosen value reaches any sink from a FRAMEWORK recovery site,
whatever its shape — log field, span exception, span status and returned error alike. The
qualifier is exact — and the HTTP lane is INSIDE it, not carved out: Echo's `middleware.Recover`
renders the value with its own `%v` and the request logger stamps it on the action line, a path
with no first-party `recover()` of its own, so `sanitizePanicValue` is registered immediately
INSIDE Recover to catch the raw value before Echo can render it. That closes the action line, the
error handler's own lines and the server span here rather than deferring them. Getting
there took a second class of site the first draft missed: three paths rendered `recover()`'s
value with `%v` into an ERROR that later reached a span (`delivery.go:38`'s shared
`panicMessage`, `multitenant/cleanup.go:61`, `messaging/manager.go:213`), so a guard installed
on the panic path never saw them. **The rule binds at the point of CONVERSION**, wherever a
recovered value becomes an error — `httpclient/client.go:750,812` is the model and had it
first. **Watch:** `[C60.23]`'s `scope` table is the surface-by-surface list, and the RULE rather than any
total is the contract — the count grew three times while that atom was written. The `panic` log
field becomes `panic_type` on the audit, scheduler, settle and both delivery-outcome lines; the
`(value unrenderable)` message is retired; both messaging lanes' `exception.message` and span
status description change text; and the HTTP lane changes on every service, which is the half a
messaging-focused reading misses. A span-based alert breaks even if no log-based one does, and
two consumer classes break with no text change at all — a changed `error_type` VALUE, and SDK
exception EVENTS that stop being emitted. The stack trace is retained wherever it was emitted
before, though not under a uniform field name. All of it breaks SILENTLY. See
`[C60.23]` in [migrations.md](migrations.md).

---

### [ADR-080: `server.ClientIP` Answers From Observed Hops Only, and Trusted-Proxy Lists Refuse Total Address-Family Coverage](adr_080_client_ip_answers_only_from_observed_hops.md)

**Date**: 2026-08-21 | **Status**: Accepted | **Completes**: ADR-057

`debug.trustedproxies: ["0.0.0.0/0"]` was accepted where the same value on
`server.trustedproxies` aborts startup — the first two keys routed to the lenient CIDR
parser, the third to the strict one. Trusting every address makes every peer a trusted
proxy, so a caller connecting DIRECTLY had their forwarding headers believed: `RemoteAddr:
203.0.113.9:5555` plus `X-Real-IP: 127.0.0.1` returned `127.0.0.1` and satisfied the
shipped `debug.allowedips` default. Reproduced against `/_sys/job` too, on both doors, with
the shipped empty (localhost-only) allowlist. No proxy transit is required, which is what
makes it P0 rather than a hardening item. `server.ClientIP` now answers with either an
identified untrusted hop or the peer it observed, never a caller-written value: every XFF
field line is read, brackets are stripped, an unparseable hop stops the walk, an
all-trusted chain yields the peer, and `X-Real-IP` is not consulted at all — which is what
ADR-057 already decided and this function never implemented. All three trusted-proxy keys
reject a default route; `debug.allowedips` gains CIDR-syntax validation accepting bare
addresses.

**Key Benefits:** One rule for the whole walk, and the same default-route refusal on every
trust key rather than one of three.
**Watch:** a default route in `debug.trustedproxies` or
`scheduler.security.trustedproxies` now FAILS STARTUP — see `[C60.22]`. Allowlists are
deliberately exempt (`["0.0.0.0/0"]` on `debug.allowedips` stays valid; ADR-049 recommends
it). Residual: a trust list that correctly describes its topology still believes whatever
those proxies append — identification, not authorization (ADR-043).

---

### [ADR-079: The Log Filter Decides Slice Passthrough By Type, And Panic Reporting Cannot Panic](adr_079_log_filter_walks_slices_without_comparing.md)

**Date:** 2026-08-21 | **Status:** Accepted

The sensitive-data filter compared each filtered slice element with the original to preserve
the slice's concrete type. Both sides are `any`, so it panicked on any uncomparable dynamic
type — which is every `map[string]any` or `[]any` inside a list, i.e. every JSON body shaped
`{"…":[{…}]}`, at BOTH doors (`.Interface()` and `WithFields`). The decision now reads the
ELEMENT TYPE before descending. Needle normalization moves into
`NewSensitiveDataFilter`, so `app.Options.LoggerFilterConfig` — which replaces the whole
config and bypassed the old normalizer — can no longer ship a single empty needle that masks
the ENTIRE log stream. And the panic-reporting call in two recovery defers is wrapped in its
own recover: those defers have already spent theirs, so from `deliverToSink` the escape
reached a bare goroutine and killed the process, defeating #686's shipped guarantee.

**Key Benefits:** a list-of-objects payload stops crashing the log path; the code-level needle
door gets the rule the YAML door already had. **Watch:** a slice whose elements the filter
rewrites now emits as `[]any` (serialized output unchanged — only code keyed on the concrete type of
`FilterValue` sees it), and a stray empty needle stops masking everything, so fields that were
being masked by accident reappear. The guard wraps the reporting CALL, not the handler — a
whole-handler recover would contain the crash and still skip `incrementFailed`, losing the job
outcome. See `[C60.21]` in [migrations.md](migrations.md).

---

### [ADR-078: A Delivered-Empty `debug.allowedips` Fails Configuration Resolution](adr_078_delivered_empty_allowedips.md)

**Date:** 2026-08-21 | **Status:** Accepted

`debug.allowedips` is the only list key whose default is a CONTROL (`["127.0.0.1", "::1"]`),
so an empty value removes protection instead of relaxing it. ADR-049's registration gate is a
conjunction, so `debug.bearertoken` alone satisfied it and registration then skipped
`ipWhitelistMiddleware` — a manifest asking for allowlist AND token ran with token only, and
booted. A validate-phase presence check now rejects the key when its RAW koanf value is an
empty string (`DEBUG_ALLOWEDIPS=`, `allowedips: ""`), while an empty SEQUENCE
(`allowedips: []`) stays legal as ADR-049's sanctioned token-only clear — the shape is the
discriminator because no rendering accident produces a sequence.

**Key Benefits:** the one list key that fails open can no longer be emptied by an unset Helm
value or an `envsubst` miss. **Watch:** deployments using an empty env var to mean "token
only" must switch to `allowedips: []`; the error names both routes. ADR-049 carries an
addendum amending two premises this falsified. See `[C60.20]` in [migrations.md](migrations.md).

---

### [ADR-077: A Delivered-Empty Bool Config Value Fails Startup](adr_077_delivered_empty_bool_config.md)

**Date:** 2026-08-21 | **Status:** Accepted

ADR-074 closed `FOO=` for numeric keys and named what it left open: the same empty string
bound to a **bool** still decoded as a legal `false`, because `WeaklyTypedInput` reaches
`SetBool(false)` by an explicit branch rather than a parse failure. Measured on `main`,
that flipped `database.pool.keepalive.enabled` from its default **true** to false, and
`cache.critical` to a non-nil `*false` that defeated ADR-046's strict readiness — `/ready`
answering 200 through a Redis outage. `config.Load` also contradicted `InjectInto`, which
had rejected `""` for a bool all along. The guard now judges bool on ADR-074's exact terms
across all four decoder seams; `EmptyStringToNumericGuardHookFunc` is renamed
`EmptyStringToScalarGuardHookFunc` (internal, never released) to match what it guards.

**Key Benefits:** one delivered-empty rule for every target kind `WeaklyTypedInput` fills
from a string; `Load` and `InjectInto` finally agree. **Watch:** a bool key rendered empty
now fails startup where it used to boot on `false` — including the keep-alive flip that was
silently degrading. YAML null is still absence; explicit `true`/`false`/`1`/`0` unchanged.
See `[C60.18]` in [migrations.md](migrations.md).

---

### [ADR-076: A Database Section's Errors Are Addressed to That Section](adr_076_section_qualified_config_error_field.md)

**Date:** 2026-08-20 | **Status:** Accepted

Root, `databases.<name>`, and `multitenant.tenants.<id>.database` share one normalization module
and therefore one set of error constructors, all spelling their fields against the root
(`database.host`). The startup door used to attach the section path by WRAPPING, so the message
read `databases.reporting: … database.database required` while the `*ConfigError` behind
`errors.As` still said `Field = "database.database"` — a consumer switching on `Field` could not
tell which section failed, and the spelling disagreed with the path-qualified keys
`UntypedDatabaseSections` and ADR-051's delivered-empty check already emit. The path now lives in
`Field` and nowhere else: `databases.reporting.host`, `multitenant.tenants.acme.database.host`,
rewritten on a copy so the connect door — which has no section — keeps the root spelling.

**Key Benefits:** one spelling for one key across the package; the typed error names the section.
**Watch:** `field == "database.host"` matchers break for non-root sections — replace them with
a predicate scoped to the `database.` / `databases.` / `multitenant.tenants.` families, since a
bare suffix match also catches `cache.redis.host` — and the message loses its
`databases.reporting: ` prefix. See
`[C60.16]` in [migrations.md](migrations.md).

**Addendum (2026-08-21, `[C60.19]`):** the two deferrals above are closed.
`normalizeDatabaseValues` takes the section and a new `ApplyDatabasePoolDefaultsForKey`
takes the `DBConfigProvider` resource key — additive, since the pinned `tools/migration`
module could not compile an arity change until the next tag — so the RUNTIME door addresses a
dynamic tenant exactly as a static one, and `database.DbManager` stops wrapping the key back
in. `Action` is re-pointed with `Field`, emitted only when the variable
round-trips to the same key: the old root-spelled hint sent operators to write a partial root
block that ADR-047 then rejected. Three sibling tenant-tree spellings join the rule.
Closes #1113 and #1114; unreachable underscore names are #1124.

**Addendum (2026-08-30, `[C61.24]`):** the runtime CACHE door joins the rule. It held the key and
still reported `cache.redis.host` with a root `CACHE_REDIS_HOST` hint for a resolved tenant, so the
two cache doors disagreed for the same tenant. Both now route through the exported
`config.QualifyCacheConfigErrorForKey` (`""` is the root and returns the error untouched; any other
key is a tenant — caches have no named siblings). `requalifyAction` reads the YAML key out of the
hint rather than rebuilding it from `Field`, which is what lets the not-configured hint
(`cache.enabled` under `Field` `cache`) travel; hints naming a key outside the field, and
hand-written actions, are still untouched. Closes #1125.

### [ADR-075: One Normalized Default per Scheduler Timeout Key](adr_075_scheduler_timeout_single_default.md)

**Date:** 2026-08-20 | **Status:** Accepted

`scheduler.timeout.shutdown` and `scheduler.timeout.slowjob` each had two defaults — the koanf
loader's (30s/25s) and the scheduler module's use-time constants (30s/**30s**) behind `> 0` guards —
and they had already drifted: a YAML deployment ran a 25s slow-job threshold where a config
assembled in Go ran 30s. Normalization now owns both keys through `applyNonNegativeDefault` (zero
applies the default, negative fails validation naming the key), the koanf loader DERIVES its default
from that fill rather than rendering the constants a second time, and the module reads the normalized
config with no fallback. Trusting the config needs an
enforced precondition, so `Init` rejects a nil `deps.Config` and the module's two remaining
`m.config != nil` guards are gone — a nil config was otherwise a panic in `Shutdown` and, in
`determineJobSeverity`, a recovered panic that reported every SUCCESSFUL job as a panicking failure.

**Key Benefits:** one edit moves a default; the divergence class is removed, not re-set.
**Watch:** hand-built configs move 30s → 25s for slowjob, a negative value now aborts startup (the
v0.59 `SlowJob` godoc suggested one, and that disable path was never reachable), and a
`*config.Config` handed straight to `Module.Init`/`NewModuleRegistry` is never normalized — see
`[C60.12]`.

### [ADR-073: The `TestKey*` Config-Key Constants Are Removed, Not Corrected](adr_073_test_key_constants_removed.md)

**Date:** 2026-08-20 | **Status:** Accepted

`config/testkeys.go` exported 33 constants naming config keys "to eliminate string literal
duplication" in tests, and not one had a call site anywhere in the repository. Five named keys
the loader does not read: `TestKeyDatabaseConnectionString` said `database.connection_string`
where the koanf tag is `connectionstring`, and the four broker constants named
`messaging.broker.host`/`.port`/`.username`/`.password`, which have never existed —
`BrokerConfig` has only ever carried `url` and `virtualhost`. A test written against one of
those sets a key nothing reads, takes the zero value, and passes. The file is deleted with no
replacement.

**Key Benefits:** Thirty-three unused exported symbols and five false statements about the
config schema leave the repository at once. **Watch:** apidiff-INCOMPATIBLE — a consumer
importing any `config.TestKey*` stops compiling, which is the compiler-caught kind of break.
The atom carries a correction table so nobody inlines one of the five wrong values on the way
out. See [migrations.md](migrations.md) `[C60.14]`.

---

### [ADR-072: The Default Log Filter Names Key Material Explicitly, Not by a Bare "key"](adr_072_default_log_filter_names_key_material_explicitly.md)

**Date:** 2026-08-20 | **Status:** Accepted

`logger.DefaultFilterConfig` matches field names by case-insensitive SUBSTRING, so its bare
`key` needle masked every field merely containing the word — `keys`, `tenant_key`, `cache_key`,
and the plain `key` the framework logs at fifteen of its own sites, all of them tenant or
resource identifiers. The YAML seam only adds needles, so no consumer could unmask one without
abandoning the whole default list. Key material is now named needle by needle instead — all three
of the `api_key`, `apikey` and `api-key` spellings of each, since the matcher relates them not at
all, and the hyphenated ones because `httpclient` logs whole `http.Header` maps through this
filter under `LogPayloads` — and the bare needle is gone.

**Key Benefits:** Identifiers log in clear without renaming a single field, and the list states
its coverage instead of leaning on a word that happens to appear inside secrets.
**Watch:** this UN-MASKS. A field the new list does not name — `license_key`, `hmac_key`,
`Ocp-Apim-Subscription-Key`, or the JWKS container `keys` — starts logging in clear on upgrade,
silently; the remedy is one `log.sensitivefields` entry. `secret_key` needs no needle: `secret`
already covers it. See [migrations.md](migrations.md) `[C60.13]`.

---

### [ADR-074: A Delivered-Empty Numeric Config Value Fails Startup](adr_074_delivered_empty_numeric_config.md)

**Date:** 2026-08-20 | **Status:** Accepted

`FOO=` — an empty `secretKeyRef`, an `envsubst` over an unset variable — delivers a set-but-empty
string that koanf keeps and mapstructure's `WeaklyTypedInput` rewrites to `0` for any numeric
target. Measured on `main`: `KEYSTORE_SECRETMINLENGTH=` decoded as `*0` and DISABLED the secret
floor, defeating ADR-065's tri-state (normalization fills a nil pointer; the decoder handed it a
non-nil pointer to zero). A decode hook now rejects an empty or whitespace-only string bound to a
numeric field — pointer targets included — in both decoder seams, so the failure names the key
instead of booting a config nobody wrote. Pruning empty keys from the koanf tree was rejected: the
ADR-051 identity check reads key presence, so dropping them boots the very misconfiguration it
exists to catch. `time.Duration` is exempt (it already fails loudly) and YAML null stays absence.

**Key Benefits:** one rule for every numeric key, instead of a `<= 0` fallback each key must
remember.
**Watch:** `FOO=` no longer means `0` — a deployment relying on it, or carrying an empty
`secretKeyRef` it never noticed, now fails startup; `FOO=0` is unchanged. See `[C60.15]`
in [migrations.md](migrations.md).

### [ADR-071: Upsert Column Sets Name Each Column Once, in a Form the Vendor Can Name](adr_071_upsert_column_sets_name_each_column_once.md)

**Date:** 2026-08-20 | **Status:** Accepted

The C59 series taught `BuildUpsert` to judge sameness by vendor identity but stopped at
`conflictColumns`, and `[C59.10]` recorded the rest of the class rather than closing it: two
different INSERT or UPDATE keys can still fold to one Oracle column, so the MERGE declared one
alias twice (ORA-00957 at parse) and the builder returned no error. `BuildUpsert` now requires
each of `insertColumns` and `updateColumns` to name every column at most once by the column each
key NAMES, keys its membership and overlap checks on that same rule, and requires every key to be
a single column name: no qualifier, no function call and no empty name on Oracle, and — on **both**
vendors — no quote that ends the identifier early, since neither escaper doubles the quotes inside
a name.

**Key Benefits:** One identity rule answers every question the call asks about a column, so a
conflict column spelled `"ID"` can no longer be updated as `id` and reach Oracle as ORA-38104.
**Watch:** PostgreSQL is unchanged apart from that interior-quote rejection; the Oracle-identity
fold also flips two shipped outcomes, `[C59.7]` accepting a cross-spelling pairing it refused and
`[C59.9]` refusing one it let through. See [migrations.md](migrations.md) `[C60.11]`.

---

### [ADR-070: Inbound Trace Identifiers Are Validated at the Trace Seam](adr_070_inbound_trace_identifier_validation.md)

**Date:** 2026-08-19 | **Status:** Accepted

`trace.ExtractFromHeaders` stored every inbound identifier verbatim, tested only for non-emptiness.
That is a remote availability attack on a shared resource: an `X-Request-ID` over 255 bytes reaches
`amqp.Publishing.CorrelationId`, amqp091's `writeShortstr` refuses it, and amqp091 answers any
frame-write error by tearing down the whole `Connection` every publisher in the process shares — and
the outbox persists and replays the poisoned header forever. The validator moves down into `trace`,
exported, reusing `server`'s `^[A-Za-z0-9_-]{1,128}$` byte-for-byte; `traceparent` gets spec-exact
validation; `tracestate` is kept only when the same carrier supplied a valid `traceparent` — discarded
otherwise, including when the context holds an inherited one — then capped, with deliberately no
grammar; and `CorrelationId` is capped again at its assignment site because the exported
`WithTraceID`/`EnsureTraceID` bypass the seam. A rejected
id is discarded and regenerated, never truncated — truncation silently forges correlation.

**Amended (2026-08-20):** the doors the original decision did not reach are closed on the same
terms — the HTTP ingress `traceparent`/`tracestate` (`enrichTraceContext` read `req.Header`
directly, bypassing the seam; invalid ⇒ drop-and-mint, never reject), the response reflection
`ensureTraceParentHeader` performs at six call sites plus the access-log metadata reader, and the
classic AMQP lane's four delivery identity fields — `CorrelationId` and `MessageId` (content-header
PROPERTIES) plus `RoutingKey` and `Exchange` (`basic.deliver` ENVELOPE metadata) — none of which a
header extractor reaches. All four are resolved once in
`processMessage` and the one verdict is threaded to every FRAMEWORK sink; a field that fails is omitted
rather than substituted. The routing key answers to a distinct printable-ASCII rule, because the
request-id charset would discard every dotted key. Streams lane untouched. See `[C60.17]`.

**Key Benefits:** The HEADER rules (`ValidateRequestID`, `ValidateTraceParent`,
`ValidateTraceState`) are shared by every ingress door — HTTP, the AMQP classic and streams lanes,
the outbox relay and the exported extractor — as functions rather than constants, so a later clause
cannot reach one door and miss another. The C60.17 delivery-identity checks are narrower: they apply
to the classic AMQP lane only, since streams surfaces none of those fields today. The emit side is explicitly not covered:
see `[C60.17]`, `#1121` and `#1123` for what remains.
**Watch:** previously-accepted identifiers are now discarded — see `[C60.8]` and `[C60.17]`; an
upstream gateway emitting a long or punctuated request id falls through to the id derived from its
`traceparent`, and only to a framework-minted one when no valid traceparent accompanies it.

### [ADR-069: The Delivery Pipeline Owns Settlement Timing](adr_069_pipeline_owns_settlement_timing.md)

**Date:** 2026-08-18 | **Status:** Accepted | **Supersedes in part:** ADR-068

ADR-068 left containment with the lanes, and it drifted immediately: the classic lane grew a
`settling` flag plus a deferred recover with a nested recover, the streams lane grew nothing — an
unrecovered panic in its delivery tail terminated the process and stranded up to 500
handled-but-uncommitted messages per partition. `delivery.Request` now carries `Settle func(*Result)`
and `Run` owns when it runs: the guard is deferred FIRST so it runs LAST, making the order span end →
lease drain → settle. A panic anywhere in the tail still settles, as `Panicked`, so the lane nacks
rather than acks a delivery it could not finish reporting; a panic inside `Settle` is logged and not
retried. Settlement POLICY stays lane-side exactly as ADR-068 decided — only timing and the
at-most-once invocation guarantee move: the pipeline gives the lane exactly one attempt with a decided
result, while the broker completion inside that attempt may still fail, time out, or panic.

**Key Benefits:** A guarantee owned by a structure instead of stated in prose, and a lane that gains
it without writing a guard.
**Watch:** a delivery whose handler SUCCEEDED but whose outcome line panicked is now `Panicked`, so
it nacks — the same action the classic lane's fallback already performed, and new behavior for any
lane that had no fallback.

### [ADR-068: One Delivery Pipeline for Both Messaging Lanes](adr_068_delivery_pipeline.md)

**Date:** 2026-08-17 | **Status:** Accepted

Both lanes implemented span → invoke → recover → record independently, and the copies drifted: the
classic lane counted `messaging.client.consumed.messages` at receive with a hardcoded nil error, the
streams lane at completion with `error.type`; the streams lane extracted no trace context and
installed no per-message lease scope; three issues rewrote `processMessage` in a month without
reaching the streams copy. `messaging/internal/delivery` now owns everything between "bytes arrived"
and "outcome recorded" — the AMQP lane runs on it as shipped, the streams lane in a named follow-up — carrier extraction, the Consumer span, the lease scope, `EnsureTraceID`, the
handler, panic-to-error, one `RecordConsume`, the lane's outcome line — behind
`Run(ctx, *Request) *Result`. Settlement stays lane-side, so "never requeue" and ADR-059's "commit
only after success" do not move.

**Key Benefits:** One body to fix instead of two that drift, and one meaning for the consumed counter.
**Watch:** this is **breaking** — `messaging.StartConsumeSpan` is removed with no replacement export
(a service driving its own consume loop owns its own span), and the classic lane's consumed counter is
recorded at completion with `error.type` instead of at receive without it. OTel span parenting is
deliberately unchanged: a consume span is still a root span. See [migrations.md](migrations.md)
`[C60.6]`.

---

## ADR Lifecycle

- **Proposed**: Under discussion, not yet implemented
- **Accepted**: Decision made and implementation complete
- **Deprecated**: Superseded by newer decision (see related ADRs)
- **Superseded**: Replaced by specific ADR (reference provided)

### Numbering Policy

ADR numbers (ADR-001 through ADR-090) reflect **decision/adoption sequence**, not strict chronological order. The authoritative timeline for each decision is the date in its individual ADR header (e.g., ADR-008 is dated 2025-01-10 while ADR-011 is dated 2025-11-09). When reviewing historical chronology, sort by the dates in the ADR index rather than by number. For example, [ADR-011](adr_011_redis_cache.md) introduced the `ModuleDeps` Cache extension — a breaking API change — and its number simply indicates it was the eleventh decision adopted, not that it followed ADR-010 temporally.

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
