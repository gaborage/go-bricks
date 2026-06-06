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

**Date:** 2025-09-24 | **Status:** Accepted

Lazy initialization of messaging registry to support context-aware dependency resolution in multi-tenant architecture. Uses singleflight protection for thread-safe initialization.

**Key Benefits:** Maintains encapsulation, context-aware, supports multi-tenant modes

---

### [ADR-005: Type-Safe WHERE Clause Construction](adr_005_type_safe_where_clauses.md)

**Date:** 2025-09-27 | **Status:** Accepted

Compile-time safe WHERE clause construction replacing string-based API. Introduces type-safe methods (`WhereEq`, `WhereLt`, etc.) with automatic Oracle identifier quoting.

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

### [ADR-011: Redis Cache Backend with CBOR Serialization (ModuleDeps Extension)](adr_011_redis_cache.md)

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

Adds `scheduler.timezone`, a single config field applied scheduler-wide via
gocron's `WithLocation`, mirroring the `database.timezone` contract from ADR-016
(default UTC, `"-"` opt-out for host-local, IANA-validated, fail-fast). Resolves
the absence of any timezone knob for scheduled jobs and removes the vestigial
`ScheduleConfiguration.Timezone` field. Breaking: an unset zone now means UTC
instead of host-local.

### [ADR-024: Flat-Smushed Config Keys (Underscore-Free for Env Reachability)](adr_024_config_key_flatsmush.md)

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
value still wins. Centralized in `applyDatabasePoolDefaults` (covers PostgreSQL,
Oracle, named, and per-tenant DBs); effective pool settings are now logged at
startup. Behavioral change: idle footprint rises (notably per-tenant) and idle
metrics report max rather than 2.

**Key Benefits:** Eliminates connection churn, removes a class of connection-establishment errors, makes effective pool config observable

---

## ADR Lifecycle

- **Proposed**: Under discussion, not yet implemented
- **Accepted**: Decision made and implementation complete
- **Deprecated**: Superseded by newer decision (see related ADRs)
- **Superseded**: Replaced by specific ADR (reference provided)

### Numbering Policy

ADR numbers (ADR-001 through ADR-025) reflect **decision/adoption sequence**, not strict chronological order. The authoritative timeline for each decision is the date in its individual ADR header (e.g., ADR-008 is dated 2025-01-10 while ADR-011 is dated 2025-11-09). When reviewing historical chronology, sort by the dates in the ADR index rather than by number. For example, [ADR-011](adr_011_redis_cache.md) introduced the `ModuleDeps` Cache extension — a breaking API change — and its number simply indicates it was the eleventh decision adopted, not that it followed ADR-010 temporally.

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
