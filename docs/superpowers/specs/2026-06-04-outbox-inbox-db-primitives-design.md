# Design: DB error/tx primitives + outbox header export + consumer-side inbox (#533–536)

Date: 2026-06-04
Status: Approved design — pending spec review, then implementation plan
Issues: gaborage/go-bricks #533, #534, #535, #536

## 1. Overview

Four GitHub issues form one coherent **transactional-outbox → consumer-idempotency** story.
Three are small, independent primitives; the fourth (#536) composes all three into a new
durable consumer-side "inbox".

```
#533 database.IsUniqueViolation ─┐
#534 database.WithTx ────────────┼──►  #536 inbox.ProcessOnce  (new package)
#535 outbox.HeaderEventID ───────┘
```

- **#533** — vendor-aware error classifier (`IsUniqueViolation`, `IsForeignKeyViolation`,
  `IsNotFound`, `ConstraintName`) in package `database`.
- **#534** — `WithTx` / `WithTxOptions` transaction-scope helpers (free functions).
- **#535** — export the `x-outbox-event-id` header name + a typed getter from `outbox`.
- **#536** — new `inbox` package: a durable, tenant-aware idempotency ledger whose
  `ProcessOnce` runs a handler exactly once per event id, atomically with the ledger marker.

All work is **additive** (new exported symbols) and **non-breaking**. The project is pre-1.0
(v0.39.1); a correct `feat(scope):` PR title is the CHANGELOG entry (release-please owns
`CHANGELOG.md`; do **not** hand-edit it).

## 2. Locked decisions

| # | Decision | Choice |
|---|----------|--------|
| D1 | Scope/sequencing | All four in one effort, primitives first, then inbox builds on them |
| D2 | #535 header constant home | `outbox` package (relay is the single source of truth) |
| D3 | #536 `ProcessOnce` tx model | Inbox **owns the tx** and passes `tx` to `fn` (atomic dedup + side effects) |
| D4 | #536 packaging | New `inbox` package + module, mirroring `outbox` |
| D5 | Auto-create-table default | **OFF by default (opt-in)**; honest zero-value bool; outbox fix is **doc/example-only** (behavior-preserving) |
| D6 | #534 shape | **Error-only** `WithTx` + `WithTxOptions`; no generics (YAGNI) |

Secondary defaults (locked, low-stakes): #533 = boolean predicates (no `errors.Is` sentinels),
scope = unique + FK + not-found (hold check/not-null — Oracle mapping is fuzzier),
`ConstraintName` is Postgres-only; #534 callback is `func(ctx, tx) error`; inbox ledger keyed on
`(tenant_id, event_id)`, no payload/event_type columns in v1; `DefaultTableName = "gobricks_inbox"`;
`RetentionPeriod` default `7*24*time.Hour` (written `168h` in YAML — Go rejects `7d`); `0` = keep forever.

## 3. Critical invariants (must be encoded in code + comments + tests)

1. **PG vs Oracle dedup asymmetry.** On **Postgres** a `23505` unique violation *poisons the
   transaction* (subsequent statements fail with "current transaction is aborted"), so the PG
   path MUST use `INSERT … ON CONFLICT (tenant_id,event_id) DO NOTHING` and detect a duplicate via
   `RowsAffected()==0`. On **Oracle** a unique violation is statement-level (the tx survives), so the
   Oracle path uses a plain `INSERT` and catches `database.IsUniqueViolation`. **Never** unify these
   into one "insert-then-catch" path — it works in Oracle tests and silently corrupts the PG tx.
2. **`errors.As` wrap-chain survival.** The hot path returns driver errors verbatim
   (`wrapper`/`tracking` never `fmt.Errorf`-wrap exec/query errors), so `errors.As` reaches
   `*pgconn.PgError` and `*network.OracleError`. Callers MUST wrap with `%w` (not `%v`) to preserve this.
3. **Read `RowsAffected()` from the `tx.Exec` `sql.Result` directly** — not the tracking layer's
   best-effort `extractRowsAffected` (it swallows errors to 0; using it for control flow would treat
   every insert as a duplicate).
4. **Ledger insert precedes `fn`.** On a duplicate, short-circuit before running `fn` (PG: 0 rows, no
   error, tx intact; Oracle: caught unique violation, tx intact).

## 4. Phase 0 — Prefactors ("make the change easy")

The repo has **no production hand-rolled tx blocks to retrofit** (exhaustive grep: only doc-comments,
tests, and `llms.txt` examples carry the begin/commit/rollback shape). The prefactors below are the
behavior-preserving (or doc-only) changes that make the four issues mechanical.

### P0.1 — Extract `internal/sqlid.ValidateTableName` (do-first, behavior-preserving for outbox)
- New package **`internal/sqlid`** at the **repo root** (NOT `database/internal/…` — sibling packages
  `outbox/` and `migration/` cannot import another tree's `internal/`).
- `func ValidateTableName(name string) error` — replicate outbox's exact current rules: optional
  `schema.table` split (≤2 parts), reject empty and dangerous fragments (`;`, `--`, `/*`, `*/`),
  per-segment regex `^[A-Za-z_][A-Za-z0-9_$#]*$`. Returns a neutral sentinel each caller wraps with
  its own package prefix.
- Migrate `outbox/store.go`'s private `validateTableName` to delegate (behavior-preserving — keeps the
  `outbox:`-prefixed wrapping). The new `inbox` package consumes `sqlid.ValidateTableName` from day one.
- **Out of scope:** do NOT migrate `provisioning`/`quiesce`/`roles` (their charset/length/quoting rules
  deliberately differ — a separate PR). Do NOT fix outbox's pre-existing schema-qualified index-name /
  unquoted-DDL bug here (see §9 discovered issues).

### P0.2 — Downgrade post-commit `sql.ErrTxDone` in the tracking layer (recommended; observability-only)
- `database/internal/tracking/transaction.go` `Rollback` + `database/internal/tracking/utils.go`
  `TrackDBOperation` / `createDBSpan`: add an `errors.Is(err, sql.ErrTxDone)` arm that mirrors the
  existing `sql.ErrNoRows` special-case — log at DEBUG and skip `span.RecordError` instead of ERROR.
  Keep returning the original error to callers.
- **Why:** the idiomatic `defer tx.Rollback()` after a successful `Commit` returns `ErrTxDone`, which
  today logs at **ERROR** + records an error span — spurious noise for tests, the documented pattern,
  and downstream apps. This is the exact pain #534 cites.
- **Separable:** `WithTx` is correct *without* this (its `committed` flag means it never calls Rollback
  after Commit). This prefactor de-noises everyone else. It is the one behavior-touching item — drop it
  if you want strictly-minimal blast radius; risk is low (only `ErrTxDone` downgraded, never a real
  rollback error — add a test asserting a genuine rollback error still logs ERROR).

### P0.3 — Config plumbing (mostly mirror; one doc-only outbox fix)
- Correct outbox's doc/example to state `auto_create_table` default = **false** (matches 4 releases of
  actual shipped behavior; `applyDefaults` never set it). `config/types.go:480-482` doc comment +
  `config.example.yaml:112`. **Behavior-preserving** (code already does false).
- This establishes the honest, opt-in, off-by-default convention the inbox inherits cleanly.

## 5. #533 — `database` error classifier

New file **`database/errors.go`** (package `database`; it already imports the vendor packages, so
importing `pgconn` and `go-ora/v2/network` is cycle-free; do NOT place in `database/types`, which must
stay driver-free).

```go
// IsUniqueViolation reports whether err is a unique/primary-key constraint violation
// (PostgreSQL SQLSTATE 23505, Oracle ORA-00001), traversing the framework wrap chain.
func IsUniqueViolation(err error) bool

// IsForeignKeyViolation reports whether err is a foreign-key violation
// (PostgreSQL 23503, Oracle ORA-02291).
func IsForeignKeyViolation(err error) bool

// IsNotFound reports whether err is a no-rows result (sql.ErrNoRows). Scan-path only.
func IsNotFound(err error) bool

// ConstraintName returns the violated constraint name when the driver exposes it.
// PostgreSQL only; returns ("", false) on Oracle (the driver carries no constraint name).
func ConstraintName(err error) (string, bool)
```

Implementation: nil-guard, then `errors.As` onto `*pgconn.PgError` (`.Code == "23505"/"23503"`,
`.ConstraintName`) and `*network.OracleError` (`.ErrCode == 1/2291`). Match the existing predicate idiom
(`config.IsNotConfigured`, `httpclient.IsHTTPStatusError`).

Tests: fabricate driver errors (unit) for both pointer **and** value `OracleError` targets; negative
cases; `//go:build integration` round-trips inserting a real duplicate against containerized PG + Oracle
to confirm the real driver error survives the wrap chain.

**Sequenced first** — the inbox's Oracle path depends on it.

## 6. #534 — `database.WithTx`

New file **`database/transaction.go`** (package `database`). Free functions (forced: `database.Interface`
is a type *alias* to `types.Interface` — a method would break every implementer/mock).

```go
// WithTx runs fn inside a transaction: commits on nil, rolls back on error,
// rolls back and re-panics on panic. No post-commit rollback (a committed flag
// suppresses the deferred rollback so there is no error-log noise on the happy path).
func WithTx(ctx context.Context, db Interface, fn func(ctx context.Context, tx Tx) error) (err error)

// WithTxOptions is WithTx with an explicit isolation level / read-only mode via BeginTx.
func WithTxOptions(ctx context.Context, db Interface, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) (err error)
```

Contract:
- `Begin`/`BeginTx` → `defer` closure: `recover()` → rollback → **re-panic**; else if `committed` skip;
  else rollback.
- On `fn` error: rollback, return the **original `fn` error** (not the rollback error; join a rollback
  error only if it is not `sql.ErrTxDone`).
- On success: `Commit`, set `committed = true`. After commit the deferred rollback is skipped; if it ever
  fires its `sql.ErrTxDone` is ignored (never surfaced as the return value).
- `WithTx` delegates to `WithTxOptions(ctx, db, nil, fn)`.

Dogfooding (the only places the hand-rolled shape lives): update doc examples `outbox/outbox.go:18-31`,
`database/types/transactor.go:22-42`, `llms.txt`, `wiki/outbox.md` to show `WithTx`; keep one manual
example for callers that need the tx handle outside a closure. Add `database` tests: commit-on-success,
fn-error-rolls-back-and-returns-fn-error, panic-rolls-back-and-re-panics, and **no ERROR log/span on the
happy path** (ties to P0.2).

## 7. #535 — export the outbox header name + getter

In the `outbox` package:

```go
const HeaderEventID   = "x-outbox-event-id"   // referenced by the relay (single source of truth)
const HeaderEventType = "x-outbox-event-type"

// EventIDFromHeaders extracts the outbox event id from AMQP delivery headers,
// returning ok=false when absent or empty. Normalizes string and []byte values.
func EventIDFromHeaders(h amqp091.Table) (string, bool)
```

- `outbox` imports `github.com/rabbitmq/amqp091-go` directly (cycle-free; it already imports `messaging`
  which imports it). `amqp091.Table` is what a consumer's `*amqp.Delivery.Headers` is.
- Normalize **both** `string` and `[]byte` (the wire can deliver either).
- Rewire `relay.go:84-85` and all literal sites (`relay_test.go:191,208`, `publisher_test.go:404`) and the
  doc in `outbox/outbox.go:8` to reference the constants; update `wiki`/`llms.txt`.

## 8. #536 — new `inbox` package

### 8.1 Public surface
```go
// app package (mirrors the OutboxPublisher/OutboxProvider triad; avoids the app<->inbox cycle)
type InboxProcessor interface {                       // role-named to avoid an Inbox/Inbox/Inbox() collision
    ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error
}
type InboxProvider interface { InboxProcessor() InboxProcessor }
// ModuleDeps gains: Inbox InboxProcessor   // nil if no inbox module / inbox.enabled=false

// inbox package
func NewModule() *Module                               // app.Module + app.JobProvider + app.InboxProvider
type InboxStore interface {                            // pluggable, vendor-split
    MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (inserted bool, err error)
    DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)
    CreateTable(ctx context.Context, db dbtypes.Interface) error
}
func NewPostgresStore(tableName string) (InboxStore, error)
func NewOracleStore(tableName string) (InboxStore, error)
const DefaultTableName = "gobricks_inbox"
// inbox/testing: MockInbox + Assert* helpers (mirror outbox/testing)
```

### 8.2 `ProcessOnce` flow (D3 — inbox owns the tx)
```go
db, err := m.getDB(ctx)                 // tenant-resolved from the handler's tenant-scoped ctx
if err != nil { return err }
return database.WithTx(ctx, db, func(ctx context.Context, tx dbtypes.Tx) error {   // #534
    inserted, err := store.MarkProcessed(ctx, tx, Record{TenantID: tid, EventID: eventID, ProcessedAt: now})
    if err != nil { return err }
    if !inserted { return nil }         // duplicate → skip fn, commit no-op
    return fn(ctx, tx)                  // side effects atomic with the marker
})
```
- **Postgres `MarkProcessed`:** `INSERT … (tenant_id,event_id,processed_at) VALUES ($1,$2,$3)
  ON CONFLICT (tenant_id,event_id) DO NOTHING`; `inserted = res.RowsAffected()==1`.
- **Oracle `MarkProcessed`:** plain `INSERT` (`:1,:2,:3`); on error `if database.IsUniqueViolation(err)
  { return inserted=false }` else return the error. (#533 is the backstop.)

### 8.3 Storage / scaffolding (MIRROR outbox, do not extract — §4 rule-of-three)
- `inbox/store.go` (`InboxStore`, `Record`, `sqlid.ValidateTableName` guard), `store_postgres.go`,
  `store_oracle.go` with vendor DDL **consts** (exported, so managed-migration shops can run them):
  - PG: `CREATE TABLE IF NOT EXISTS %s (tenant_id VARCHAR(255) NOT NULL DEFAULT '', event_id VARCHAR(255)
    NOT NULL, processed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(), PRIMARY KEY (tenant_id,event_id))`
    + `CREATE INDEX IF NOT EXISTS idx_%s_processed ON %s (processed_at)`.
  - Oracle: `VARCHAR2(255)`, `SYSTIMESTAMP`, no `IF NOT EXISTS` (tolerate ORA-00955 as warning),
    function-based index; mirror outbox's reserved-word handling.
- `inbox/module.go`: copy `ensureStoreInitialized` (double-checked mutex, `switch db.DatabaseType()`,
  `tableCreated` guard, CreateTable-failure-as-warning) and the `lazyStore` wrapper **verbatim in shape**
  from outbox; add a one-line "sibling pattern: see outbox" comment for future extraction.
- `inbox/cleanup.go` + `RegisterJobs`: copy outbox's `DailyAt` cleanup executor (cutoff =
  `now - RetentionPeriod`, `DeleteProcessed`); register only when `RetentionPeriod > 0`. Inbox implements
  `app.JobProvider` **only for this cleanup job** (the dedup itself is synchronous — no relay). Same
  "register scheduler before inbox" caveat applies to the cleanup job only.
- `inbox/config.go`: `const DefaultTableName`, `const DefaultRetentionPeriod = 7*24*time.Hour`,
  module-owned `applyDefaults` + `validateConfig` (called in `Init` as `applyDefaults → validateConfig →
  Enabled short-circuit`, mirroring outbox). Opt-in module defaults live in the module, NOT
  `config/loadDefaults()` and NOT `config/validation.go`.

### 8.4 App + config wiring (pure mirror — additive, non-breaking)
- `app/module.go`: add `InboxProcessor`, `InboxProvider`, and the `ModuleDeps.Inbox` field
  (`app` already imports `dbtypes` and uses `dbtypes.Tx` → zero new imports). All `ModuleDeps{…}`
  literals in the repo are keyed → the new field is non-breaking.
- `app/module_registry.go`: add the duck-typed auto-wire branch after the outbox branch
  (`if p, ok := module.(InboxProvider); ok { r.deps.Inbox = p.InboxProcessor(); log }`). No generic
  `Provider[T]` (4 divergent branches — keep them explicit).
- `config/types.go`: add `InboxConfig{Enabled, TableName, AutoCreateTable, RetentionPeriod}` (five-tag
  style, snake_case keys) + the `Inbox InboxConfig` field on `Config`. `AutoCreateTable` is the honest
  off-by-default bool (D5). `RetentionPeriod` is `time.Duration`.
- `config.example.yaml`: add an `inbox:` block after the outbox block (`retention_period: 168h`, never `7d`).

## 9. Discovered issues (out of scope — propose as separate tickets)
- **Outbox schema-qualified table names are mishandled:** a name like `myschema.outbox_events` passes
  validation but yields an invalid index name `idx_myschema.outbox_events_pending` and an unquoted
  `CREATE TABLE`. Fixing needs quoting + last-segment index derivation (with a PG case-folding caveat).
  Pre-existing; not triggered by this work. Recommend a separate issue.
- Unifying `provisioning`/`quiesce`/`roles` identifier validators onto `internal/sqlid` (their rules
  differ deliberately) — separate, behavior-changing PR.

## 10. Testing strategy
- Unit: per-vendor mirror-image test files driving the concrete store against
  `dbtesting.NewTestDB(dbtypes.PostgreSQL|Oracle)` fluent expectations; `testify`;
  `var _ InboxStore = (*postgresStore)(nil)` compile-time guards.
- Integration (`//go:build integration`, testcontainers): #533 real duplicate-key round-trip on both
  vendors; #536 same-event-id-twice (assert `fn` runs once, second is a no-op) on both vendors, including
  the PG tx-not-poisoned and Oracle tx-survives paths.
- `make check` (fmt + lint + `-race`, 80% coverage, SonarCloud S8179 getter naming) must pass; run
  `/code-review` and `/security-review` before pushing.

## 11. Sequencing (one connected effort)
1. **P0.1** `internal/sqlid` + outbox delegation (behavior-preserving).
2. **#533** `database.IsUniqueViolation` et al. (+ **P0.2** tracking `ErrTxDone` downgrade).
3. **#534** `WithTx`/`WithTxOptions` (+ dogfood docs/tests).
4. **#535** `outbox.HeaderEventID`/`EventIDFromHeaders` (+ rewire literals).
5. **P0.3** config doc fix + `InboxConfig` + example.
6. **#536** `inbox` package + app wiring (consumes 1–5).

Each step is independently reviewable; #536 is the integrating step that exercises every primitive.
