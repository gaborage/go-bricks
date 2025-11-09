# Architecture Decision Records (ADRs)

This document serves as an index to all architectural decisions made during the development of the GoBricks framework. Each ADR documents a significant design choice, its context, alternatives considered, and consequences.

## Overview

Architecture Decision Records help us:
- Document **why** decisions were made, not just what was decided
- Understand the context and trade-offs of past decisions
- Onboard new developers with historical architectural context
- Avoid revisiting settled decisions without new information

## ADR Index

### [ADR-001: Enhanced Handler System Implementation](adr-001-enhanced-handler-system.md)
**Date:** 2025-09-12 | **Status:** Accepted

Type-safe HTTP handler system with automatic binding, validation, and standardized response envelopes. Introduces generic handler wrappers, comprehensive request binding via struct tags, and hierarchical error handling.

**Key Benefits:** Eliminates boilerplate, compile-time type safety, consistent API responses

---

### [ADR-002: Custom Base Path and Health Route Configuration](adr-002-base-path-and-health-routes.md)
**Date:** 2025-09-15 | **Status:** Accepted

Configurable base paths for all routes and customizable health check endpoints. Implements RouteRegistrar abstraction with intelligent path handling and nested group support.

**Key Benefits:** Deployment flexibility, infrastructure compatibility, automatic path inheritance

---

### [ADR-003: Database by Intent Configuration](adr-003-database-by-intent.md)
**Date:** 2025-09-17 | **Status:** Accepted

Explicit database configuration requirement with no framework defaults. Database functionality only enabled when explicitly configured, supporting database-free applications.

**Key Benefits:** Deterministic behavior, clear intent, database-free application support

---

### [ADR-004: Lazy Messaging Registry Creation in ModuleRegistry](adr-004-lazy-messaging-registry.md)
**Date:** 2025-09-24 | **Status:** Accepted

Lazy initialization of messaging registry to support context-aware dependency resolution in multi-tenant architecture. Uses singleflight protection for thread-safe initialization.

**Key Benefits:** Maintains encapsulation, context-aware, supports multi-tenant modes

---

### [ADR-005: Type-Safe WHERE Clause Construction](adr-005-type-safe-where-clauses.md)
**Date:** 2025-09-27 | **Status:** Accepted

Compile-time safe WHERE clause construction replacing string-based API. Introduces type-safe methods (`WhereEq`, `WhereLt`, etc.) with automatic Oracle identifier quoting.

**Key Benefits:** Eliminates Oracle quoting bugs, compile-time safety, clear responsibility boundaries

---

### [ADR-006: OpenTelemetry Protocol (OTLP) Log Export Integration](adr-006-otlp-log-export.md)
**Date:** 2025-10-10 | **Status:** Accepted

Unified observability with OTLP log export via io.Writer bridge pattern. Automatic trace correlation, dual-mode logging (action logs 100%, trace logs WARN+), and deterministic sampling.

**Key Benefits:** Unified observability stack, automatic correlation, production-ready sampling

---

### [ADR-007: Struct-Based Column Extraction](adr-007-struct-based-columns.md)
**Date:** 2025-10-28 | **Status:** Accepted

Reflection-based column extraction from struct tags with lazy caching. Eliminates column name repetition, provides vendor-aware quoting, and enables refactor-friendly queries.

**Key Benefits:** DRY principle, type safety, Oracle reserved word auto-quoting, sub-nanosecond performance

---

### [ADR-008: Cache Interface and Manager (Draft)](adr-008-cache-draft.md)
**Status:** Draft

Proposed cache abstraction layer with pluggable backends and optional observability integration.

**Status:** Under development

---

## ADR Lifecycle

- **Proposed**: Under discussion, not yet implemented
- **Accepted**: Decision made and implementation complete
- **Deprecated**: Superseded by newer decision (see related ADRs)
- **Superseded**: Replaced by specific ADR (reference provided)

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

2. **Create individual file**: `adr-XXX-short-title.md`

3. **Update this index**: Add entry with summary and key benefits

4. **Reference in CLAUDE.md**: If it affects developer workflows or key architecture

## Related Documentation

- **[CLAUDE.md](../CLAUDE.md)**: Development guide and quick reference
- **[llms.txt](../llms.txt)**: Code examples for LLM code generation
- **[.specify/memory/constitution.md](../.specify/memory/constitution.md)**: Project governance framework
- **[Demo Project](https://github.com/gaborage/go-bricks-demo-project)**: Working examples

---

*ADRs document the "why" behind our architecture. They're living documents—update them when new information changes our understanding of past decisions.*
