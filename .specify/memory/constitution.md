<!--
SYNC IMPACT REPORT
==================
Version Change: 0.0.0 (new) → 1.0.0
Change Type: MAJOR - Initial constitution ratification with comprehensive governance framework

Sections Added:
- I. Explicit Over Implicit (code clarity)
- II. Type Safety Over Dynamic Hacks (compile-time safety)
- III. Test-First Development (80% coverage mandate)
- IV. Security First (input validation, secrets management)
- V. Observability as First-Class Citizen (OpenTelemetry standards)
- VI. Performance Standards (production-grade benchmarks)
- VII. User Experience Consistency (standardized patterns)

Templates Requiring Updates:
✅ plan-template.md - Constitution Check section aligned with 7 principles
✅ spec-template.md - Requirements section references security and performance principles
✅ tasks-template.md - Task categorization reflects principle-driven phases (security, observability, testing)

Follow-up TODOs: None - all placeholders filled with concrete values from CLAUDE.md manifesto

Rationale for MAJOR version:
- Initial constitution establishing non-negotiable governance framework
- Sets foundation for all future development practices
- Defines quality gates that affect all contributions
-->

# GoBricks Framework Constitution

## Core Principles

### I. Explicit Over Implicit

Code MUST be clear and self-documenting. No hidden defaults, no magic configuration, no implicit behavior that requires tribal knowledge.

**Rules:**
- Configuration values MUST be explicit with documented defaults
- Function signatures MUST clearly express intent and dependencies
- Context propagation MUST be explicit (`context.Context` as first parameter)
- Error handling MUST be visible and intentional (no silent failures)
- Dependencies MUST be injected, never globally accessed

**Rationale:** Explicit code enables onboarding, refactoring, and debugging without requiring deep framework knowledge. It reduces cognitive load and prevents production surprises.

### II. Type Safety Over Dynamic Hacks

Refactor-friendly code through compile-time guarantees. Breaking changes are acceptable when they prevent runtime failures.

**Rules:**
- Public APIs MUST use typed interfaces over `interface{}` where feasible
- Query builders MUST use type-safe filter methods (prevents Oracle reserved word errors)
- Handler functions MUST use typed request/response structures with validation tags
- Configuration injection MUST validate types at startup, not runtime
- Breaking changes MUST be documented in ADRs when improving type safety

**Rationale:** Type safety catches errors at compile time, enables IDE support, and makes refactoring safe. A breaking change that adds compile-time safety is preferable to runtime panics.

### III. Test-First Development (NON-NEGOTIABLE)

Framework code MUST achieve 80% test coverage (SonarCloud enforced). Tests verify behavior, not implementation.

**Rules:**
- All public APIs MUST have unit tests with race detection enabled
- Database integrations MUST have integration tests (testcontainers)
- HTTP handlers MUST have contract tests verifying request/response envelopes
- Tests MUST run on multi-platform matrix (Ubuntu/Windows × Go 1.24/1.25)
- Coverage below 80% MUST block merges (enforced by SonarCloud quality gate)
- Test utilities MUST be justified by active usage (measure calls, not speculation)

**Test Priorities:**
- **Always test:** Database queries, HTTP handlers, messaging consumers, configuration injection
- **Integration tests:** New contracts, contract changes, multi-database vendor differences
- **Defer:** Exotic configuration combinations, rare edge cases, purely cosmetic logging

**Rationale:** Framework users depend on stability. 80% coverage ensures production-grade reliability while avoiding test theater. Multi-platform testing catches platform-specific issues early.

### IV. Security First

Input validation is mandatory. Secrets stay out of code and logs. SQL operations are auditable.

**Rules:**
- Input validation MUST occur at all boundaries (HTTP, messaging, database)
- `WhereRaw()` usage MUST include annotation: `// SECURITY: Manual SQL review completed - identifier quoting verified`
- Secrets MUST come from environment variables or secret managers (AWS Secrets Manager, HashiCorp Vault)
- Credentials MUST NOT appear in logs, error messages, or version control
- Audit logging MUST track sensitive operations (access control, data modifications)
- Multi-tenant context MUST include tenant ID for security correlation

**Rationale:** Security failures are unacceptable in production frameworks. Mandatory validation and audit trails prevent entire classes of vulnerabilities.

### V. Observability as First-Class Citizen

Production systems MUST be observable. OpenTelemetry standards are non-negotiable.

**Rules:**
- W3C `traceparent` propagation MUST work across HTTP and messaging boundaries
- Dual-mode logging MUST separate action logs (100% sampling) from trace logs (WARN+ only)
- Health endpoints MUST expose liveness (`/health`) and readiness (`/ready`) checks
- Database, HTTP, and messaging operations MUST emit OpenTelemetry metrics
- Go runtime metrics MUST be automatically exported when observability is enabled
- Slow request threshold MUST be configurable to auto-escalate performance issues

**Rationale:** Observability is not optional for production systems. Dual-mode logging reduces costs by ~95% while maintaining debuggability. Standardized tracing enables cross-service correlation.

### VI. Performance Standards

Framework overhead MUST be minimal. Performance degradation requires explicit justification.

**Rules:**
- Database connection pooling MUST be enabled by default
- Context propagation MUST avoid allocations in hot paths
- Batching MUST be environment-aware (500ms dev, 5s prod)
- Memory allocations MUST be measured for high-frequency operations
- Benchmarks MUST prevent regressions in critical paths
- Breaking changes for performance MUST be justified in ADRs

**Performance Targets (Framework):**
- HTTP middleware overhead: <1ms p99
- Database query builder: <100μs overhead per query
- Context extraction: <50ns (no allocations)
- Observability batching: Configurable (default: 500ms dev, 5s prod)

**Rationale:** Framework tax compounds across all user applications. Low overhead ensures users can build high-performance services without fighting the framework.

### VII. User Experience Consistency

Standardized patterns reduce cognitive load. Framework users should feel productive, not frustrated.

**Rules:**
- Error messages MUST follow structured format: `config_<category>: <field> <message> <action>`
- Handler responses MUST use consistent `{data:…, meta:…}` envelope structure
- Configuration priority MUST be: Environment variables > `config.<env>.yaml` > `config.yaml` > defaults
- Module lifecycle hooks MUST execute in documented order (Init → RegisterRoutes → DeclareMessaging → Shutdown)
- Failure modes MUST be deterministic (same inputs produce same outputs)
- API consistency MUST be maintained across PostgreSQL, Oracle, MongoDB adapters

**Rationale:** Predictable, consistent APIs reduce learning curves and enable muscle memory. Users should spend time solving business problems, not decoding framework quirks.

## Code Quality Standards

### Linting and Static Analysis
- **golangci-lint** MUST pass with staticcheck, gosec, gocritic enabled (`.golangci.yml`)
- **SonarCloud** MUST maintain quality gate: 80% coverage, no critical/blocker issues
- **Race detection** MUST be enabled for all test runs (`-race` flag)
- **Dependency scanning** via Dependabot MUST be reviewed within 7 days

### Architecture Patterns
- **SOLID principles** when they simplify, not when forced
- **Interface segregation** for testability (e.g., `Client` vs `AMQPClient`)
- **Composition over inheritance** via embedding and interfaces
- **CQS (Command-Query Separation)** where it adds clarity
- **Fail Fast** at startup for validation errors (no silent degradation)

### YAGNI Exceptions (When Abstractions Are Justified)
- Vendor-specific database differences (PostgreSQL vs Oracle placeholders, reserved words)
- Cloud provider integrations (AWS Secrets Manager, HashiCorp Vault)
- Multi-platform compatibility (Ubuntu/Windows path handling)

## Testing Discipline

### Framework Testing Requirements
- **Coverage Target:** 80% (SonarCloud enforced)
- **Race Detection:** All tests run with `-race` on all platforms
- **Multi-Platform CI:** Ubuntu/Windows × Go 1.24/1.25
- **Integration Tests:** testcontainers for MongoDB, Docker required
- **Build Tag Isolation:** `//go:build integration` separates integration from unit tests

### Application Testing Guidance (For Framework Users)
- **Coverage Target:** 60-70% on core business logic
- **Testing Focus:** Happy paths + critical error scenarios
- **Always Test:** Database queries, HTTP handlers, messaging consumers
- **Defer:** Exotic configuration combinations, rare edge cases
- **Iterate:** Expect some code to be throwaway as requirements evolve

### Test Automation
- Pre-commit checks: `make check` (framework only: fmt, lint, test with race detection)
- Comprehensive validation: `make check-all` (framework + tool, catches breaking changes)
- Integration tests: `make test-integration` (Docker required)
- Coverage reporting: `make test-coverage` (unit + integration merged to SonarCloud)

## Development Workflow

### Pre-Commit Requirements
1. Run `make check` for fast feedback during daily development
2. Run `make check-all` before committing framework API changes
3. Ensure all tests pass with race detection enabled
4. Verify linting passes (golangci-lint)
5. Confirm coverage meets 80% threshold locally

### When to Use `make check-all`
- Modifying public interfaces (server, database, config, observability)
- Changing struct tags or validation logic
- Refactoring shared types or error handling
- Before creating PRs that touch framework APIs

### CI/CD Requirements
- **Unified CI Workflow:** Single workflow (`ci-v2.yml`) with intelligent path-based job execution
- **Path Detection:** Automatic via `dorny/paths-filter@v3` action
- **Framework changes:** Run framework jobs only (skip tool tests)
- **Tool changes:** Run tool jobs only (skip framework tests)
- **Quality Gates:** SonarCloud, security scanning, coverage reports

### Documentation Standards
- **Just enough for understanding quickly** (examples over exhaustive docs)
- **ADRs for breaking changes** (see `wiki/architecture_decisions.md`)
- **Task planning archived** (`.claude/tasks/archive/`)
- **Demo project examples** ([go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project))

## Governance

### Amendment Process
1. Propose change via GitHub issue with rationale and impact analysis
2. Document breaking changes in ADR format (`wiki/architecture_decisions.md`)
3. Update dependent templates (plan, spec, tasks) to reflect principle changes
4. Increment constitution version following semantic versioning
5. Require approval from maintainers for MAJOR/MINOR changes

### Versioning Policy
- **MAJOR:** Backward-incompatible governance changes, principle removals/redefinitions
- **MINOR:** New principles added, materially expanded guidance, new quality gates
- **PATCH:** Clarifications, wording improvements, typo fixes, non-semantic refinements

### Compliance Review
- All PRs MUST verify compliance with constitution principles
- Complexity violations MUST be justified in `plan.md` "Complexity Tracking" section
- Breaking changes for safety/correctness MUST be documented in ADRs
- Security violations MUST be rejected (no exceptions for input validation, secrets management)

### Constitution Supersedes All Practices
When conflicts arise between this constitution and existing practices:
1. Constitution principles take precedence
2. Existing code should be refactored to comply (create tracking issue)
3. Temporary exceptions MUST be documented with migration plan

### Runtime Development Guidance
For agent-specific development instructions, refer to `CLAUDE.md` which provides:
- Development commands and make targets
- Architecture overview and key interfaces
- Testing guidelines and integration test setup
- Database-specific notes (Oracle, PostgreSQL, MongoDB)
- Observability configuration and debugging tips

**Version**: 1.0.0 | **Ratified**: 2025-10-16 | **Last Amended**: 2025-10-16
