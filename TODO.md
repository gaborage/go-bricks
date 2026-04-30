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

### JOSE: ECDSA Keystore Support
**Status:** Conditional / Idea Stage
**Context:** `ES256` was removed from the JOSE signature-algorithm allowlist because `keystore.NewModule()` returns only `*rsa.PrivateKey`/`*rsa.PublicKey` — selecting `sig_alg=ES256` would crash at runtime. To re-enable ES256 (or future ECDSA-family algorithms like `ES384`/`ES512`), the keystore needs to surface ECDSA keys.

**Implementation outline:**
- Generalize `keystore.KeyStore` from RSA-only to `crypto.Signer` / `crypto.PublicKey` (Go's standard interfaces) — a non-trivial API change since `*rsa.PrivateKey` callers currently rely on the concrete type
- Update `cryptoadapter.{Sign,Verify,Encrypt,Decrypt}` to accept the broader interfaces and dispatch by algorithm class (RSA vs ECDSA)
- Re-add `jose.ES256` to `allowedSigAlgs` in `jose/algorithms.go`
- Remove the guard test `TestAllowlistRejectsES256` in `jose/algorithms_test.go`
- Update CLAUDE.md JOSE Middleware allowlist line

**Decision criteria:** Implement only when a real partner integration requires ES256/ECDSA. Visa Token Services uses RS256/PS256 today. Speculative API expansion now would lock the framework into a `crypto.Signer` shape before we know how a real ECDSA caller wants to interact with the keystore (file-based DER? PKCS#8? raw point coordinates? per-tenant rotation?).

**Trade-offs:**
- Keeps keystore API focused on the actual production case (PRO)
- Defers a known feature gap (CON, but documented and guard-tested)

**Related:**
- [jose/algorithms.go](jose/algorithms.go) — `allowedSigAlgs` + comment block citing this entry
- [jose/algorithms_test.go](jose/algorithms_test.go) — `TestAllowlistRejectsES256` guard test
- [keystore/keystore.go](keystore/keystore.go)

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

### JOSE: Wire `Header.Typ` Into Observability or Drop
**Status:** Conditional / Idea Stage
**Context:** Both `cryptoadapter.Header.Typ` and the public `jose.Header.Typ` (returned from `Open()` as part of `OpenHeader{JWE, JWS}`) are populated on every JOSE request via `extractStringExtra(..., jose.HeaderType)`, but no caller reads either value. Server middleware discards the `OpenHeader` return at `server/jose.go`, and no observability span/log uses the field. Surfaced during `/simplify` review of the `AllowedTyps` cleanup PR — the same dormant-surface argument applies, with one asymmetry: `Typ` is part of a public return type, so dropping it is a (minor) API break, whereas `AllowedTyps` was an unused input flag.

**Two paths (pick one when this is taken up):**

1. **Wire it up** — add `jwe.typ`/`jws.typ` attributes to the `jose.decode_request` span and to the request-log fields in `server/jose.go`. Cheap, makes the field actually load-bearing for diagnostics, and aligns with the reason the field was added in the first place.
2. **Drop it** — remove from both `cryptoadapter.Header` and `jose.Header`, plus the two `extractStringExtra` calls. Minor break since `OpenHeader` is exposed by the public `Open()` API.

**Decision criteria:** Pick Path 1 if anyone hits a JOSE issue where knowing the peer's `typ` header would have helped triage. Pick Path 2 at the next opportunistic cleanup if nobody touches it.

**Related:**
- [jose/internal/cryptoadapter/cryptoadapter.go](jose/internal/cryptoadapter/cryptoadapter.go) — `Header.Typ` field + extractor calls in `Decrypt`/`Verify`
- [jose/opener.go](jose/opener.go) — `Header.Typ` in the public diagnostic struct
- [server/jose.go](server/jose.go) — current consumer that discards the return

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

### ~~JOSE: Drop ES256 from Algorithm Allowlist~~
**Status:** ✅ Completed
**Context:** `jose.ES256` was in the JOSE signature-algorithm allowlist but `keystore.NewModule()` returned RSA keys only — selecting `sig_alg=ES256` in a `jose:` tag passed registration but crashed at runtime when the cryptoadapter passed an `*rsa.PrivateKey` into go-jose's ECDSA signer path. Documented as a "v1 limitation" in CLAUDE.md but unenforced — a runtime footgun.

**Resolution (Path 1 of two options):**
- Removed `jose.ES256` from `allowedSigAlgs` in [jose/algorithms.go](jose/algorithms.go); added a comment citing the keystore RSA-only constraint
- Added `TestAllowlistRejectsES256` as a guard test so the algorithm cannot be silently re-added without also extending keystore support
- Updated CLAUDE.md JOSE Middleware allowlist line: now reads `RS256`/`PS256` only, `ES256` listed alongside `alg=none`/`HS*`/`RSA1_5` as parse-time rejected
- Future ECDSA support tracked separately as P3 conditional work (see "JOSE: ECDSA Keystore Support" entry)

**Why Path 1 over Path 2 (add ECDSA support):** No real partner integration currently requires ES256; Visa Token Services uses RS256/PS256. Adding `crypto.Signer`/`crypto.PublicKey` plumbing speculatively would lock the framework into an interface shape before observing how a real ECDSA caller wants to interact with the keystore.

**Related:**
- [jose/algorithms.go](jose/algorithms.go) — `allowedSigAlgs` declaration + comment block
- [jose/algorithms_test.go](jose/algorithms_test.go) — `TestAllowlistRejectsES256` guard
- [CLAUDE.md](CLAUDE.md) — JOSE Middleware → "Strict algorithm allowlist"

---

### ~~JOSE: Drop `cryptoadapter.AllowedTyps`~~
**Status:** ✅ Completed
**Context:** `cryptoadapter.{Decrypt,Verify}Options.AllowedTyps` was a list-allowlist for the JWE/JWS `typ` header added speculatively in the original JOSE design. The field had test coverage in cryptoadapter's own unit tests but no production caller (jose package, server middleware, httpclient transport) ever set it. Discovered during `/simplify` review of PR #332.

**Resolution (Path 1 of two options — drop, not wire):**
- Removed `AllowedTyps` field from both `DecryptOptions` and `VerifyOptions`
- Removed the `if len(opts.AllowedTyps) > 0` branches in `Decrypt` and `Verify`
- Removed the `ErrTypRejected` sentinel from cryptoadapter and the parent jose package
- Removed the `mapDecryptError` arm producing `JOSE_TYP_REJECTED` (the verify mapper never had one)
- Removed the dead `contains()` helper that only the typ check used
- Removed two cryptoadapter tests (`TestVerifyAcceptsEmptyTypInAllowlist`, `TestVerifyRejectsTypNotInAllowlist`)
- Removed the `typ_rejected` case from the `TestMapDecryptErrorAllArms` table
- Kept `Header.Typ` field and its extraction from `ExtraHeaders` — still useful for diagnostic logging on every request

**Why Path 1 over Path 2 (add `Policy.AllowedTyps` + tag grammar):** No production code path read or set `AllowedTyps`. Visa Token Services and similar partners don't use the `typ` header for security gating. Removing the dead surface area is cheaper than continuing to maintain it as half-loaded API.

**Note on CLAUDE.md:** the `JOSE_TYP_REJECTED` row was never actually in the failure-mode table on `main` — it was only in an early plan-file draft. Confirmed via grep before resolving.

**Related:**
- [jose/internal/cryptoadapter/cryptoadapter.go](jose/internal/cryptoadapter/cryptoadapter.go)
- [jose/opener.go](jose/opener.go) — `mapDecryptError` (typ arm removed)
- [jose/errors.go](jose/errors.go) — `ErrTypRejected` sentinel removed

---

### ~~JOSE: Normalize `errors.As`/`errors.Is` → `require.ErrorAs`/`assert.ErrorIs` in Tests~~
**Status:** ✅ Completed
**Context:** The JOSE test suite mixed `require.True(t, errors.As(err, &jerr))` and `assert.True(t, errors.Is(err, X))` with the preferred testify idioms `require.ErrorAs(t, err, &jerr)` and `assert.ErrorIs(t, err, X)`. The newer style produces better failure messages on mismatch and saves a line per call site. Newer test files (e.g. `jose/policy_test.go`, `jose/resolver_test.go`) already used the upgraded idiom.

**Resolution:**
- Converted all 11 `errors.As` sites in `jose/sealer_opener_test.go` (8), `jose/tag_test.go` (1), `jose/scanner_test.go` (1), `jose/testing/helpers_test.go` (1)
- Converted all 8 `errors.Is` sites in `jose/errors_test.go` (2 — including one `assert.False` → `assert.NotErrorIs`) and `jose/internal/cryptoadapter/cryptoadapter_test.go` (6)
- Dropped the now-unused `errors` import from 5 files (kept in `jose/errors_test.go` because `errors.New` is still used to construct fixture errors)
- Bundled `errors.Is` conversions in via `/simplify` agent surfaced them as the same idiom upgrade — saved a follow-up PR

**Related:**
- [jose/sealer_opener_test.go](jose/sealer_opener_test.go), [jose/tag_test.go](jose/tag_test.go), [jose/scanner_test.go](jose/scanner_test.go)
- [jose/testing/helpers_test.go](jose/testing/helpers_test.go)
- [jose/errors_test.go](jose/errors_test.go), [jose/internal/cryptoadapter/cryptoadapter_test.go](jose/internal/cryptoadapter/cryptoadapter_test.go)

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