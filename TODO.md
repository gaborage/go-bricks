# GoBricks Technical Backlog

This document tracks future enhancements, technical improvements, and nice-to-have features for the GoBricks framework.

## Priority Levels
- **P0 (Critical):** Security, reliability, or breaking bugs
- **P1 (High):** Developer experience improvements, performance optimizations
- **P2 (Medium):** Quality of life features, additional tooling
- **P3 (Low):** Nice-to-haves, future explorations

---

## P0 - Critical

### Security Audit Annotations for WhereRaw()
**Status:** Planned
**Context:** `WhereRaw()` bypasses SQL safety mechanisms. All usage should require explicit security review annotation.

**Requirements:**
- Audit all existing `WhereRaw()` usage in codebase
- Add linting rule or pre-commit hook to require annotation
- Document annotation format in CLAUDE.md

**Annotation Format:**
```go
// SECURITY: Manual SQL review completed - identifier quoting verified for Oracle
query := qb.Select("id", "number").
    From("accounts").
    WhereRaw(`"number" = ? AND ROWNUM <= ?`, value, 10)
```

**Tasks:**
- [ ] Search codebase for all `WhereRaw()` usage
- [ ] Add annotations to existing usage
- [ ] Document in CLAUDE.md security section
- [ ] Consider custom linter rule (optional)

**Related:**
- ADR-005: Type-Safe WHERE Clause Construction
- [database/internal/builder/](database/internal/builder/)

---

## P1 - High Priority

### Enhanced Observability Testing Helpers
**Status:** Under Review
**Context:** Current `observability/testing` package provides basic span/metric assertions. It could be expanded based on usage patterns.

**Potential Enhancements:**
- Span hierarchy assertions (parent-child relationships)
- Timing/duration assertions for performance testing
- Metric aggregation helpers (sum, average over time window)
- Baggage/attribute propagation assertions

**Action Required:**
- Analyze test coverage to determine if helpers are actively used
- Survey team for pain points in observability testing
- Only implement if usage justifies complexity (YAGNI principle)

**Related:**
- [observability/testing/](observability/testing/)
- CLAUDE.md: Observability section

---

### JOSE: ECDSA Keystore Support or Drop ES256
**Status:** Planned
**Context:** `ES256` is in the JOSE signature-algorithm allowlist (`jose/algorithms.go:19`) but `keystore.NewModule()` only returns `*rsa.PrivateKey`/`*rsa.PublicKey`. A user who picks `sig_alg=ES256` in a `jose:` tag passes registration (the algorithm is in the allowlist), then crashes at runtime when the sealer/opener tries to use an ECDSA algorithm against an RSA key. Documented as a "v1 limitation" in CLAUDE.md but currently unenforced — a real footgun.

**Two paths (pick one):**

1. **Drop ES256** — remove `jose.ES256` from `allowedSigAlgs` until ECDSA support lands. One-line change, eliminates the footgun, no API breakage (no production code can be using ES256 today since it would crash).
2. **Add ECDSA support** — extend `keystore.KeyStore` to return `crypto.Signer`/`crypto.PublicKey` (interface generalization), update `cryptoadapter.{Sign,Verify,Encrypt,Decrypt}` signatures to accept the broader types, dispatch by algorithm. Significant API change.

**Trade-offs:**
- Path 1 is safe and immediate; only removes a non-functional option.
- Path 2 is the "right" answer architecturally but expands surface area without confirmed demand. Visa Token Services uses RS256/PS256 today; ES256 is forward-looking.

**Recommendation:** Path 1 now, Path 2 only when a real partner requires ES256.

**Related:**
- [jose/algorithms.go](jose/algorithms.go)
- [keystore/keystore.go](keystore/keystore.go)
- [CLAUDE.md](CLAUDE.md) — JOSE Middleware → "ES256 caveat"

---

### Context Timeout Defaults
**Status:** Planned
**Context:** Framework requires `context.Context` everywhere but doesn't enforce timeout best practices.

**Requirements:**
- Document recommended timeout values for common operations
- Consider helper functions for context creation with sensible defaults
- Add examples in CLAUDE.md

**Example:**
```go
// Potential helpers in a new package: context/defaults.go
func NewHTTPRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
    return context.WithTimeout(parent, 30*time.Second)
}

func NewDatabaseQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
    return context.WithTimeout(parent, 10*time.Second)
}
```

**Related:**
- Context-First Design principle (to be added to CLAUDE.md)

---

### ~~Go Runtime Metrics Export via OTLP~~
**Status:** ✅ Completed (v0.13.0)
**Context:** Framework now automatically exports Go runtime metrics via OTLP when observability is enabled.

**Implementation:**
- Added `go.opentelemetry.io/contrib/instrumentation/runtime@v0.63.0` dependency
- Integrated `runtime.Start()` in `initMeterProvider()` for automatic metrics collection
- Registered `runtime.NewProducer()` with periodic reader for scheduler histogram metrics
- Zero configuration required—works automatically when `observability.enabled: true`

**Metrics Exported:**
- **Memory:** `go.memory.used`, `go.memory.limit`, `go.memory.allocated`, `go.memory.allocations`, `go.memory.gc.goal`
- **Goroutines:** `go.goroutine.count`
- **CPU:** `go.processor.limit` (GOMAXPROCS)
- **Scheduler:** `go.schedule.duration` (goroutine scheduler latency histogram)
- **Configuration:** `go.config.gogc`

**Testing:**
- Comprehensive tests in `observability/metrics_runtime_test.go`
- Verifies all standard and scheduler metrics are exported
- Tests disabled state when metrics are explicitly disabled

**Documentation:**
- Updated CLAUDE.md with runtime metrics section
- Added link to OpenTelemetry semantic conventions

**Related:**
- [observability/metrics.go](observability/metrics.go) (lines 7, 41-68)
- [observability/metrics_runtime_test.go](observability/metrics_runtime_test.go)
- [CLAUDE.md](CLAUDE.md) (Observability section)

---

## P2 - Medium Priority

### Code Generation for Handler Scaffolding
**Status:** Idea Stage
**Context:** ADR-001 mentions "Consider code generation tools for handler scaffolding"

**Potential Features:**
- CLI tool to generate handler boilerplate from templates
- Generate request/response structs from JSON schema
- Automatic test file generation

**Example Usage:**
```bash
go-bricks generate handler --name CreateUser --method POST --path /users
```

**Trade-offs:**
- Reduces boilerplate (PRO)
- Additional tooling complexity (CON)
- Must maintain templates as framework evolves (CON)

**Decision:** Defer until pain points are validated with real usage

---

### Additional Type-Safe Query Methods
**Status:** Idea Stage
**Context:** ADR-005 introduced type-safe WHERE methods. Could expand to other SQL operations.

**Potential Additions:**
- `WhereLike(column, pattern)` - LIKE operator with proper escaping
- `WhereExists(subquery)` - EXISTS clause with subquery
- `WhereJsonContains(column, path, value)` - JSON operations (PostgreSQL)
- `WhereRegex(column, pattern)` - Regex matching (vendor-specific)

**Requirements:**
- Only add when user demand is clear
- Maintain vendor-specific behavior (Oracle vs PostgreSQL)
- Comprehensive testing for each operator

**Related:**
- ADR-005: Type-Safe WHERE Clause Construction
- [database/internal/builder/](database/internal/builder/)

---

### OpenAPI Schema Validation
**Status:** Idea Stage
**Context:** `tools/openapi` generates OpenAPI specs but doesn't validate runtime requests against them.

**Potential Features:**
- Middleware to validate requests against generated OpenAPI spec
- Response validation in development mode
- Automatic test generation from OpenAPI spec

**Trade-offs:**
- Catches API contract violations (PRO)
- Runtime performance overhead (CON)
- Tight coupling between spec and runtime (CON)

**Decision:** Research existing Go libraries for OpenAPI validation

---

### JOSE: Sample Module in Demo Project
**Status:** Planned
**Context:** The original JOSE design plan called for a working VTS-style example in the [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) repository — a `POST /tokens` handler with realistic key fixtures, a sealed request payload, and a verified-and-encrypted response. Useful for onboarding teams who haven't worked with nested JOSE before. Not blocking framework adoption, but reduces the "where do I start?" friction.

**Requirements:**
- End-to-end module demonstrating bidirectional JOSE (request decrypt+verify, response sign+encrypt)
- `keystore` config showing both file-based DER and base64-env key sources
- A small `cmd/seal-payload` helper that generates a sample compact JWE-of-JWS for use with `curl`
- README walkthrough: configure keys → start app → seal payload → POST → decrypt response

**Out of scope:** ECDSA, replay cache, multi-tenant key overrides — these aren't in v1.

**Related:**
- External repo: `go-bricks-demo-project`
- [CLAUDE.md](CLAUDE.md) — JOSE Middleware section
- [llms.txt](llms.txt) — JOSE quick-reference snippets

---

## P3 - Low Priority / Future Exploration

### Multi-Database Transaction Support
**Status:** Exploration
**Context:** Current `database.Tx` interface supports single-database transactions. Distributed transactions would require significant complexity.

**Questions:**
- Is there real-world demand for this?
- Would 2PC (two-phase commit) be sufficient, or do we need full SAGA pattern?
- Should this be a separate package to avoid bloating core?

**Related:**
- [database/types/](database/types/)

---

### GraphQL Support
**Status:** Not Planned
**Context:** Framework currently focused on REST APIs. GraphQL would require significant architectural changes.

**Reasons for Deferral:**
- REST APIs cover 95% of use cases
- GraphQL library ecosystem is mature (e.g., gqlgen)
- Would increase framework complexity significantly
- Users can integrate GraphQL libraries directly if needed

**Decision:** Not in scope for GoBricks core

---

### Message Schema Validation (AMQP)
**Status:** Idea Stage
**Context:** Current messaging system doesn't enforce message schemas.

**Potential Approaches:**
- JSON Schema validation for message payloads
- Protobuf integration for strongly-typed messages
- Message version negotiation

**Trade-offs:**
- Prevents invalid messages (PRO)
- Requires schema management overhead (CON)
- May conflict with dynamic messaging patterns (CON)

**Decision:** Monitor user feedback before committing

---

### JOSE: Replay Cache Interface (`jose.ReplayCache`)
**Status:** Conditional / Idea Stage
**Context:** Today the JOSE middleware verifies the JWS signature and exposes verified JWT claims via `jose.ClaimsFromContext(ctx)`, but does *not* enforce `iat`/`exp`/`jti` policies — applications must implement skew windows and replay detection themselves. The original plan noted this as a follow-up: *"if apps consistently re-implement skew/jti checks, lift them into a pluggable interface"*.

**Decision criteria:** Build only when at least two production handlers have duplicated the same `iat`/`jti` enforcement pattern. Not earlier — the right interface depends on what real handlers need, and committing prematurely locks the framework into the wrong abstraction.

**Open questions** (only worth answering once real demand is observed):
- Backend: in-memory LRU? Redis-backed via existing `cache/`? DynamoDB? Pluggable?
- Scope: per-tenant `jti` namespace, or global?
- Policy: `jti`-only deduplication, or `jti` + `iat` skew window combined?
- Failure mode: hard-reject on duplicate `jti`, or log-and-accept (audit trail only)?

**Trade-offs:**
- Building now: framework-level consistency (PRO), but speculative API surface that may not match real needs (CON).
- Deferring: apps repeat boilerplate (CON), but the interface gets designed against real evidence (PRO).

**Decision:** Defer until real-traffic evidence justifies it. Track via complaints / duplicated handler code.

**Related:**
- [jose/claims.go](jose/claims.go), [jose/context.go](jose/context.go)
- [CLAUDE.md](CLAUDE.md) — JOSE Middleware → "Replay protection"

---

### JOSE: Drop or Wire `cryptoadapter.AllowedTyps`
**Status:** Cleanup / Planned
**Context:** `cryptoadapter.{Decrypt,Verify}Options.AllowedTyps` was added speculatively at the time of the original JOSE design as a list-allowlist for the `typ` JOSE header. The field is exercised only by cryptoadapter's own unit tests — no production caller (jose package, server middleware, httpclient transport) ever sets it. Discovered during `/simplify` review of PR #332.

**The two states are both honest; the current half-loaded state is not:**

1. **Drop entirely:** remove `AllowedTyps` field, the corresponding `if len(opts.AllowedTyps) > 0` branches in `Decrypt`/`Verify`, the `ErrTypRejected` sentinel, and the `mapDecryptError`/`mapVerifyError` arms producing `JOSE_TYP_REJECTED`. Also drop the `JOSE_TYP_REJECTED` row from the CLAUDE.md failure-mode table (it would no longer be triggerable).
2. **Wire it up:** add `Policy.AllowedTyps []string`, plumb it from the parser → opener → cryptoadapter, document configurable typ allowlists in the JOSE tag grammar (e.g. `typ=JOSE,JWS`).

**Recommendation:** Path 1. Visa Token Services and similar partners don't use the `typ` header for security gating, so the option is unlikely to ever be wired up. Removing dead surface area reduces reader confusion (the `/simplify` agent flagged it as "dormant API surface, not a live pattern to mirror").

**Related:**
- [jose/internal/cryptoadapter/cryptoadapter.go](jose/internal/cryptoadapter/cryptoadapter.go)
- [jose/opener.go](jose/opener.go) — `mapDecryptError`/`mapVerifyError` `typ_rejected` arms
- [jose/errors.go](jose/errors.go) — `ErrTypRejected`
- [CLAUDE.md](CLAUDE.md) — JOSE failure-mode table

---

### JOSE: Normalize `errors.As` → `require.ErrorAs` in Tests
**Status:** Cleanup / Planned
**Context:** Mix of older `require.True(t, errors.As(err, &jerr))` and newer `require.ErrorAs(t, err, &jerr)` across the JOSE test suite (~11 sites in `jose/sealer_opener_test.go`, `jose/tag_test.go`, `jose/scanner_test.go`, `jose/testing/helpers_test.go`). `require.ErrorAs` is the preferred testify idiom: better failure messages on type mismatch, one line shorter, and already used by newer test files (e.g. `jose/policy_test.go`, `jose/resolver_test.go`).

**Implementation:** Mechanical conversion. Each occurrence:
```go
// Before
var jerr *Error
require.True(t, errors.As(err, &jerr))
// After
var jerr *Error
require.ErrorAs(t, err, &jerr)
```

**Trade-offs:**
- Better test ergonomics (PRO)
- Pure churn in git history (CON — minor)

**Decision:** Bundle into a single low-risk cleanup PR; don't drip individual conversions across feature PRs.

**Related:**
- [jose/sealer_opener_test.go](jose/sealer_opener_test.go), [jose/tag_test.go](jose/tag_test.go), [jose/scanner_test.go](jose/scanner_test.go)
- [jose/testing/helpers_test.go](jose/testing/helpers_test.go)

---

## Completed / Won't Do

### ~~Raw String WHERE Clauses~~
**Status:** ❌ Won't Do
**Reason:** ADR-005 made the explicit decision to remove `.Where()` method entirely in favor of type-safe methods. This is a breaking change prioritizing compile-time safety over backward compatibility.

---

### ~~Database Connection Pooling Configuration~~
**Status:** ✅ Completed
**Context:** PostgreSQL uses pgx driver with built-in connection pooling. Oracle uses go-ora with connection management.

**Implementation:**
- [database/postgresql/connection.go](database/postgresql/connection.go)
- [database/oracle/connection.go](database/oracle/connection.go)

---

### ~~Stack Traces in Development Mode~~
**Status:** ✅ Completed
**Context:** `BaseAPIError` now captures the call-site stack on construction when running in `app.env = development` (or `dev`), and the response formatter renders the resolved frames under `error.details.stackTrace` for both the standard `APIResponse` envelope and raw-response routes.

**Resolution:**
- **Process-global capture flag** in [server/errors.go](server/errors.go) (`captureStackTraces atomic.Bool`) flipped by [server.New()](server/server.go) based on `cfg.App.Env`. Production paths pay one `atomic.Bool.Load()` per error and never invoke `runtime.Callers`.
- **Lazy resolution**: PCs are stored as `[]uintptr` at construction; the expensive `runtime.CallersFrames` symbol resolution runs only when the formatter calls `StackTrace()` (which only happens in dev).
- **`StackTracer` interface** mirrors the well-known `pkg/errors` convention. All built-in wrapper types (`NotFoundError`, `BadRequestError`, …) inherit it via method promotion since they embed `*BaseAPIError`. User-defined error types can implement it independently.
- **Capture depth** capped at 32 frames; render key is `stackTrace`.

**Related:**
- [server/errors.go](server/errors.go) — `SetCaptureStackTraces`, `StackTracer`, `(*BaseAPIError).StackTrace`
- [server/handler.go](server/handler.go) — `devDetails` helper used by both `formatErrorResponse` and `formatRawErrorResponse`
- [server/server.go](server/server.go) — bootstrap wiring

---

### ~~Slim Module Interface + Remove Stutter from Framework Modules~~
**Status:** ✅ Completed (ADR-014, 2026-03-16)
**Context:** The framework modules `outbox`, `scheduler`, and `keystore` previously stuttered (`OutboxModule`, `SchedulerModule`, `KeystoreModule`) with `//nolint:revive` suppressions, and were forced to provide no-op `RegisterRoutes` / `DeclareMessaging` implementations because the core `app.Module` interface required all five methods. This violated Interface Segregation and made the no-op stubs look like missing implementations rather than intentional opt-outs.

**Resolution:**
- **Stutter removed**: `outbox.OutboxModule` → `outbox.Module`, `scheduler.SchedulerModule` → `scheduler.Module`, `keystore.KeystoreModule` → `keystore.Module`. All `//nolint:revive` suppressions deleted. Constructors are now `outbox.NewModule()`, `scheduler.NewModule()`, `keystore.NewModule()`.
- **`app.Module` slimmed to three methods**: `Name()`, `Init(*ModuleDeps) error`, `Shutdown() error`. Modules that don't serve HTTP or AMQP no longer implement no-op stubs.
- **Optional capabilities are duck-typed at registration**: `RouteRegisterer`, `MessagingDeclarer`, `JobProvider`, `OutboxProvider`, and `KeyStoreProvider` live in [app/module.go](app/module.go). `ModuleRegistry` type-asserts each module before invoking the optional method — same pattern Go's standard library uses (e.g., `io.ReaderFrom` is opportunistically detected by `io.Copy`). This is the Go-idiomatic answer to Java's "implement just the interfaces you need" — composition at the **call site**, not at the **type definition**.

**Related:**
- [wiki/adr-014-slim-module-interface.md](wiki/adr-014-slim-module-interface.md)
- [app/module.go](app/module.go) — core + optional interface declarations
- [app/module_registry.go](app/module_registry.go) — type-assertion dispatch

---

## Contributing to This Backlog

When adding items to this backlog:
1. Choose appropriate priority level based on impact and urgency
2. Provide context explaining why this is needed
3. Reference related ADRs, files, or issues
4. Include implementation notes if design is clear
5. Highlight trade-offs and alternatives considered

For P0 items, create a GitHub issue immediately.
For P1-P3 items, discuss in team meetings before implementation.