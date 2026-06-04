# DB primitives + outbox header + consumer inbox (#533–536) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver four go-bricks issues as five sequential PRs: a shared SQL-identifier validator (prefactor), a vendor-aware DB error classifier (#533), a `WithTx` transaction helper + tracking-noise fix (#534), exported outbox header constants + getter (#535), and a new durable consumer-side `inbox` package (#536).

**Architecture:** Each PR is additive and independently reviewable, branched off updated `main`, driven through CI + CodeRabbit + SonarCloud. `#536` composes the three primitives. Spec: `docs/superpowers/specs/2026-06-04-outbox-inbox-db-primitives-design.md`.

**Tech Stack:** Go 1.26, pgx/v5 (`pgconn`), sijms/go-ora/v2 (`network`), rabbitmq/amqp091-go, testify, `database/testing` (TestDB fluent mock), koanf config, release-please.

**Conventions (apply to every PR):**
- TDD: failing test → run (red) → implement → run (green) → `make check` → commit.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Conventional-Commit PR title IS the changelog entry (release-please owns `CHANGELOG.md`; never hand-edit it). Titles: `feat(scope): …` / `refactor(scope): …` / `fix(scope): …`.
- Tests: camelCase `TestXxx`, `t.Context()` (Go 1.26), `require`/`assert`, partial SQL-pattern matching, `var _ Iface = (*concrete)(nil)` conformance guards.
- Run `make check` before every commit; `/code-review` + `/security-review` before opening each PR.

---

## PR1 — `internal/sqlid` shared table-name validator (prefactor, behavior-preserving)

**Branch:** `feat/sqlid-validator` (off `main`)
**PR title:** `refactor(database): extract shared SQL table-name validator to internal/sqlid`

**Files:**
- Create: `internal/sqlid/sqlid.go`
- Create: `internal/sqlid/sqlid_test.go`
- Modify: `outbox/store.go:13-44` (delegate `validateTableName` to `sqlid`, keep `outbox:` prefix)

### Task 1.1: sqlid package (replicate outbox's exact rules)

- [ ] **Step 1 — failing test** `internal/sqlid/sqlid_test.go`:
```go
package sqlid

import "testing"

func TestValidateTableName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "gobricks_inbox", false},
		{"schema_qualified", "myschema.gobricks_inbox", false},
		{"dollar_hash", "outbox$events#1", false},
		{"empty", "", true},
		{"semicolon", "t; DROP TABLE x", true},
		{"comment_dashes", "t--x", true},
		{"block_comment_open", "t/*x", true},
		{"block_comment_close", "t*/x", true},
		{"three_parts", "a.b.c", true},
		{"leading_digit", "1table", true},
		{"space", "my table", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTableName(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTableName(%q) = nil, want error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTableName(%q) = %v, want nil", tc.input, err)
			}
		})
	}
}
```
- [ ] **Step 2 — run red:** `go test ./internal/sqlid/` → FAIL (package/func undefined).
- [ ] **Step 3 — implement** `internal/sqlid/sqlid.go`:
```go
// Package sqlid validates SQL identifiers (table names) before they are
// interpolated into DDL/DML, guarding against SQL identifier injection.
// The rules mirror the historical outbox validator and database/internal/columns/parser.go.
package sqlid

import (
	"fmt"
	"regexp"
	"strings"
)

// validIdentifierPattern matches safe SQL identifiers (letters, digits, underscore, $, #).
var validIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`)

// ValidateTableName checks that name is a safe SQL identifier.
// Supports optional schema-qualified names (e.g., "myschema.table").
// Returns a descriptive error when name is empty, contains dangerous SQL
// fragments, has more than two dot-separated parts, or any part is not a
// valid identifier.
func ValidateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("table name must not be empty")
	}
	for _, dangerous := range []string{";", "--", "/*", "*/"} {
		if strings.Contains(name, dangerous) {
			return fmt.Errorf("table name %q contains dangerous SQL characters", name)
		}
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return fmt.Errorf("table name %q has too many dot-separated parts (expected schema.table or table)", name)
	}
	for _, part := range parts {
		if !validIdentifierPattern.MatchString(part) {
			return fmt.Errorf("table name part %q contains invalid identifier characters", part)
		}
	}
	return nil
}
```
- [ ] **Step 4 — run green:** `go test ./internal/sqlid/ -v` → PASS.
- [ ] **Step 5 — commit:** `git add internal/sqlid && git commit -m "refactor(database): add internal/sqlid table-name validator"`

### Task 1.2: outbox delegates to sqlid (behavior-preserving)

- [ ] **Step 1** — Modify `outbox/store.go`: replace the `validIdentifierPattern` var and `validateTableName` body (lines 13-44) with a thin wrapper that delegates and keeps the `outbox:` prefix; drop now-unused `regexp`/`strings` imports if no longer referenced (verify with the compiler):
```go
// validateTableName checks that name is a safe SQL identifier, wrapping the
// shared sqlid validator with the outbox package error prefix.
func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}
```
Add `"github.com/gaborage/go-bricks/internal/sqlid"` to the import block; remove `"regexp"` and (if unused elsewhere in the file) `"strings"`.
- [ ] **Step 2 — verify existing outbox tests still pass** (behavior preserved): `go test ./outbox/` → PASS. The existing `outbox/store_test.go` cases (`outbox$events`, `outbox#events` valid; dangerous rejected) must still pass; the error strings now read `outbox: table name …` (was `outbox: table name …` — confirm test assertions match; if a test asserts an exact old substring, update it minimally).
- [ ] **Step 3 — `make check`** → PASS.
- [ ] **Step 4 — commit:** `git add outbox/store.go outbox/*_test.go && git commit -m "refactor(outbox): use internal/sqlid for table-name validation"`

### Task 1.3: open + babysit PR1
- [ ] `/code-review` then `/security-review` on the diff; address findings.
- [ ] `gh pr create --base main --title "refactor(database): extract shared SQL table-name validator to internal/sqlid" --body <see PR body template>`.
- [ ] Babysit: CI green, CodeRabbit comments addressed, SonarCloud quality gate pass. Then merge (per merge policy confirmed with user).

---

## PR2 — #533 `database` error classifier

**Branch:** `feat/db-error-classifier` (off updated `main`)
**PR title:** `feat(database): add vendor-aware unique/FK/not-found error classifiers`

**Files:**
- Create: `database/errors.go`
- Create: `database/errors_test.go`
- Create: `database/errors_integration_test.go` (`//go:build integration`)

### Task 2.1: classifier predicates (unit-tested with fabricated driver errors)

- [ ] **Step 1 — failing test** `database/errors_test.go`:
```go
package database

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	oranet "github.com/sijms/go-ora/v2/network"
)

func TestIsUniqueViolation(t *testing.T) {
	pgUnique := &pgconn.PgError{Code: "23505", ConstraintName: "uq_email"}
	oraUnique := &oranet.OracleError{ErrCode: 1}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"pg_unique", pgUnique, true},
		{"pg_unique_wrapped", fmt.Errorf("insert: %w", pgUnique), true},
		{"pg_fk_not_unique", &pgconn.PgError{Code: "23503"}, false},
		{"oracle_unique", oraUnique, true},
		{"oracle_other", &oranet.OracleError{ErrCode: 942}, false},
		{"plain", errors.New("boom"), false},
		{"no_rows", sql.ErrNoRows, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("IsUniqueViolation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	if !IsForeignKeyViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("pg 23503 should be FK violation")
	}
	if !IsForeignKeyViolation(&oranet.OracleError{ErrCode: 2291}) {
		t.Fatal("ORA-02291 should be FK violation")
	}
	if IsForeignKeyViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("pg 23505 is not FK violation")
	}
	if IsForeignKeyViolation(nil) {
		t.Fatal("nil is not FK violation")
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(sql.ErrNoRows) {
		t.Fatal("sql.ErrNoRows should be not-found")
	}
	if !IsNotFound(fmt.Errorf("scan: %w", sql.ErrNoRows)) {
		t.Fatal("wrapped sql.ErrNoRows should be not-found")
	}
	if IsNotFound(nil) || IsNotFound(errors.New("x")) {
		t.Fatal("nil/plain are not not-found")
	}
}

func TestConstraintName(t *testing.T) {
	name, ok := ConstraintName(&pgconn.PgError{Code: "23505", ConstraintName: "uq_email"})
	if !ok || name != "uq_email" {
		t.Fatalf("ConstraintName = %q,%v want uq_email,true", name, ok)
	}
	if _, ok := ConstraintName(&oranet.OracleError{ErrCode: 1}); ok {
		t.Fatal("Oracle exposes no constraint name; want ok=false")
	}
	if _, ok := ConstraintName(nil); ok {
		t.Fatal("nil → ok=false")
	}
}
```
- [ ] **Step 2 — run red:** `go test ./database/ -run 'Unique|ForeignKey|NotFound|ConstraintName'` → FAIL.
- [ ] **Step 3 — implement** `database/errors.go`:
```go
package database

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	oranet "github.com/sijms/go-ora/v2/network"
)

// SQLSTATE / ORA codes for constraint classification.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	oraUniqueViolation    = 1    // ORA-00001
	oraForeignKeyViolation = 2291 // ORA-02291
)

// IsUniqueViolation reports whether err is a unique/primary-key constraint
// violation (PostgreSQL SQLSTATE 23505, Oracle ORA-00001), traversing the
// framework wrap chain via errors.As.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	var oraErr *oranet.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == oraUniqueViolation
	}
	return false
}

// IsForeignKeyViolation reports whether err is a foreign-key constraint
// violation (PostgreSQL 23503, Oracle ORA-02291).
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgForeignKeyViolation
	}
	var oraErr *oranet.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == oraForeignKeyViolation
	}
	return false
}

// IsNotFound reports whether err is a no-rows result (sql.ErrNoRows).
// This is a scan-path signal; it is not produced by Exec.
func IsNotFound(err error) bool {
	return err != nil && errors.Is(err, sql.ErrNoRows)
}

// ConstraintName returns the violated constraint name when the driver exposes
// it. PostgreSQL populates this; Oracle does not, so ok is false on Oracle.
func ConstraintName(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName != "" {
		return pgErr.ConstraintName, true
	}
	return "", false
}
```
- [ ] **Step 4 — run green:** `go test ./database/ -run 'Unique|ForeignKey|NotFound|ConstraintName' -v` → PASS.
- [ ] **Step 5 — `make check`** → PASS (gofmt may reorder the const block; let it).
- [ ] **Step 6 — commit:** `git add database/errors.go database/errors_test.go && git commit -m "feat(database): add IsUniqueViolation/IsForeignKeyViolation/IsNotFound/ConstraintName"`

### Task 2.2: integration test (both vendors, real driver error)

- [ ] **Step 1** — `database/errors_integration_test.go` (first line `//go:build integration`): using the existing testcontainers helpers (mirror `database/postgresql/connection_integration_test.go` setup), insert a duplicate key against Postgres and Oracle, then assert `IsUniqueViolation(err)` is true and (Postgres) `ConstraintName` returns the constraint. Reference the existing integration harness for container bootstrap — do not hand-roll.
- [ ] **Step 2 — run:** `make test-integration` (Docker required) → PASS. If the local environment lacks Docker, note it in the PR and rely on CI's integration job.
- [ ] **Step 3 — commit:** `git add database/errors_integration_test.go && git commit -m "test(database): integration coverage for unique-violation classifier (both vendors)"`

### Task 2.3: open + babysit PR2 (same babysit loop as PR1).

---

## PR3 — P0.2 tracking `ErrTxDone` downgrade + #534 `WithTx`

**Branch:** `feat/withtx-helper` (off updated `main`)
**PR title:** `feat(database): add WithTx/WithTxOptions transaction helpers`

**Files:**
- Modify: `database/internal/tracking/utils.go:114-125` (TrackDBOperation) and `:246-253` (createDBSpan)
- Create: `database/internal/tracking/utils_test.go` additions (ErrTxDone downgrade test) — or extend existing
- Create: `database/transaction.go`
- Create: `database/transaction_test.go`
- Modify (docs, dogfood): `outbox/outbox.go` doc example, `database/types/transactor.go` doc example, `llms.txt`, `wiki/outbox.md`

### Task 3.1: downgrade post-commit `sql.ErrTxDone` in tracking (P0.2)

- [ ] **Step 1 — failing test** (extend tracking tests): assert that a `sql.ErrTxDone` passed through `TrackDBOperation` logs at Debug (not Error) and that `createDBSpan` does not record it as a span error, while a generic rollback error still logs Error. Mirror the existing `sql.ErrNoRows` test in `database/internal/tracking/utils_test.go`.
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement** in `database/internal/tracking/utils.go`:
  - TrackDBOperation error block (currently lines 114-122):
```go
	if err != nil {
		// Treat sql.ErrNoRows and post-commit/rollback sql.ErrTxDone specially -
		// not actual errors, log as debug.
		switch {
		case errors.Is(err, sql.ErrNoRows):
			logEvent.Debug().Msg("Database operation returned no rows")
		case errors.Is(err, sql.ErrTxDone):
			logEvent.Debug().Msg("Database transaction already finalized")
		default:
			logEvent.Error().Err(err).Msg("Database operation error")
		}
	} else if elapsed > tc.Settings.SlowQueryThreshold() {
		logEvent.Warn().Msgf("Slow database operation detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database operation executed")
	}
```
  - createDBSpan record-error guard (currently lines 246-251):
```go
	if err != nil {
		// sql.ErrNoRows and sql.ErrTxDone are not real errors.
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, sql.ErrTxDone) {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}
```
  (No new imports — `errors` and `database/sql` already imported.)
- [ ] **Step 4 — run green** + `make check`.
- [ ] **Step 5 — commit:** `git commit -am "fix(database): treat post-commit sql.ErrTxDone as benign in tracking (no ERROR log/span)"`

### Task 3.2: WithTx + WithTxOptions

- [ ] **Step 1 — failing test** `database/transaction_test.go` (use `database/testing` TestDB; assert commit on success, rollback+original-error on fn error, rollback+re-panic on panic, and no ERROR log on happy path):
```go
package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTxCommitsOnSuccess(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().ExpectExec(`INSERT INTO t`).WillReturnRowsAffected(1)
	err := database.WithTx(t.Context(), db, func(ctx context.Context, tx dbtypes.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO t VALUES (1)")
		return e
	})
	require.NoError(t, err)
}

func TestWithTxRollsBackAndReturnsFnError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction()
	sentinel := errors.New("boom")
	err := database.WithTx(t.Context(), db, func(ctx context.Context, tx dbtypes.Tx) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestWithTxRollsBackAndRepanics(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction()
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = database.WithTx(t.Context(), db, func(ctx context.Context, tx dbtypes.Tx) error {
			panic("kaboom")
		})
	})
}
```
(If `dbtesting` cannot satisfy a bare `ExpectTransaction()` with no exec on the rollback path, adjust to the TestDB's documented rollback expectation API — consult `database/testing/fake_tx.go` `IsRolledBack()`.)
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement** `database/transaction.go`:
```go
package database

import (
	"context"
	"database/sql"
	"errors"
)

// WithTx runs fn inside a database transaction. It commits when fn returns nil,
// rolls back when fn returns an error (returning fn's original error), and rolls
// back then re-panics if fn panics. After a successful commit the deferred
// rollback is skipped, so there is no post-commit rollback noise.
func WithTx(ctx context.Context, db Interface, fn func(ctx context.Context, tx Tx) error) (err error) {
	return WithTxOptions(ctx, db, nil, fn)
}

// WithTxOptions is WithTx with an explicit isolation level / read-only mode.
func WithTxOptions(ctx context.Context, db Interface, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) (err error) {
	var tx Tx
	if opts != nil {
		tx, err = db.BeginTx(ctx, opts)
	} else {
		tx, err = db.Begin(ctx)
	}
	if err != nil {
		return err
	}

	committed := false
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if !committed {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && err == nil {
				err = rbErr
			}
		}
	}()

	if err = fn(ctx, tx); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
```
- [ ] **Step 4 — run green** + `make check`.
- [ ] **Step 5 — commit:** `git add database/transaction.go database/transaction_test.go && git commit -m "feat(database): add WithTx and WithTxOptions transaction helpers"`

### Task 3.3: dogfood docs (update examples to use WithTx)
- [ ] Update the doc-comment examples in `outbox/outbox.go` and `database/types/transactor.go` to show `database.WithTx`; update `llms.txt` and `wiki/outbox.md`. Keep one manual Begin/Commit example for callers needing the tx handle outside a closure. Commit: `docs: show WithTx in transaction examples`.

### Task 3.4: open + babysit PR3.

---

## PR4 — #535 outbox header constants + getter

**Branch:** `feat/outbox-header-export` (off updated `main`)
**PR title:** `feat(outbox): export x-outbox-event-id header name and EventIDFromHeaders getter`

**Files:**
- Create: `outbox/headers.go`
- Create: `outbox/headers_test.go`
- Modify: `outbox/relay.go:84-85` (reference the constants)
- Modify tests/docs referencing the literals (see occurrence list in spec/anchor): `outbox/relay_test.go:191,192,208`, `outbox/publisher_test.go:404`, `outbox/outbox.go:8` doc.

### Task 4.1: headers.go

- [ ] **Step 1 — failing test** `outbox/headers_test.go`:
```go
package outbox

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestEventIDFromHeaders(t *testing.T) {
	id, ok := EventIDFromHeaders(amqp.Table{HeaderEventID: "evt-1"})
	assert.True(t, ok)
	assert.Equal(t, "evt-1", id)

	id, ok = EventIDFromHeaders(amqp.Table{HeaderEventID: []byte("evt-2")})
	assert.True(t, ok)
	assert.Equal(t, "evt-2", id)

	_, ok = EventIDFromHeaders(amqp.Table{})
	assert.False(t, ok)

	_, ok = EventIDFromHeaders(nil)
	assert.False(t, ok)

	_, ok = EventIDFromHeaders(amqp.Table{HeaderEventID: 42})
	assert.False(t, ok)

	_, ok = EventIDFromHeaders(amqp.Table{HeaderEventID: ""})
	assert.False(t, ok)
}
```
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement** `outbox/headers.go`:
```go
package outbox

import amqp "github.com/rabbitmq/amqp091-go"

// AMQP delivery header names stamped by the relay for consumer idempotency.
// These are the single source of truth; relay.go references them.
const (
	// HeaderEventID carries the unique outbox event id (UUID) for deduplication.
	HeaderEventID = "x-outbox-event-id"
	// HeaderEventType carries the event type.
	HeaderEventType = "x-outbox-event-type"
)

// EventIDFromHeaders extracts the outbox event id from AMQP delivery headers,
// returning ok=false when absent, empty, or not a string/[]byte. Header values
// may arrive as string or []byte over the wire, so both are normalized.
func EventIDFromHeaders(h amqp.Table) (string, bool) {
	raw, present := h[HeaderEventID]
	if !present {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "", false
		}
		return v, true
	case []byte:
		if len(v) == 0 {
			return "", false
		}
		return string(v), true
	default:
		return "", false
	}
}
```
- [ ] **Step 4 — run green** + `make check`.
- [ ] **Step 5 — commit.**

### Task 4.2: rewire literals to constants
- [ ] `outbox/relay.go:84-85` → `headers[HeaderEventID] = record.ID` / `headers[HeaderEventType] = record.EventType`.
- [ ] Update `outbox/relay_test.go:191,192,208`, `outbox/publisher_test.go:404` to reference `HeaderEventID`/`HeaderEventType`. Update `outbox/outbox.go:8` doc to mention `outbox.HeaderEventID` / `outbox.EventIDFromHeaders`.
- [ ] `go test ./outbox/` → PASS; `make check` → PASS. Commit: `refactor(outbox): reference HeaderEventID/HeaderEventType constants at the relay`.

### Task 4.3: open + babysit PR4.

---

## PR5 — #536 `inbox` package (integrates 1–4)

**Branch:** `feat/inbox-idempotency` (off updated `main`)
**PR title:** `feat(inbox): add durable consumer-side idempotency ledger (ProcessOnce)`

**Files:**
- Create: `inbox/store.go`, `inbox/store_postgres.go`, `inbox/store_oracle.go`
- Create: `inbox/config.go`
- Create: `inbox/cleanup.go`
- Create: `inbox/module.go`
- Create: `inbox/inbox.go` (the `Inbox` processor implementing `app.InboxProcessor`)
- Create: `inbox/testing/mock_inbox.go`, `inbox/testing/assertions.go`
- Create: tests: `inbox/store_postgres_test.go`, `inbox/store_oracle_test.go`, `inbox/inbox_test.go`, `inbox/config_test.go`, `inbox/*_integration_test.go`
- Modify: `app/module.go` (add `InboxProcessor` + `InboxProvider` interfaces near the Outbox triad at 68-112; add `Inbox InboxProcessor` field after `Outbox` at line 190)
- Modify: `app/module_registry.go` (insert `InboxProvider` branch after KeyStoreProvider, between lines 89 and 91)
- Modify: `config/types.go` (add `InboxConfig` after OutboxConfig at line 505; add `Inbox InboxConfig` field after line 27)
- Modify: `config/types.go:480-482` + `config.example.yaml:112` (outbox `auto_create_table` doc → default **false**, behavior-preserving) and add an `inbox:` block to `config.example.yaml`.

### Task 5.1: app wiring (interfaces + deps field + registry branch)
- [ ] In `app/module.go`, after the OutboxProvider block (line 112) add:
```go
// InboxProcessor runs a handler exactly once per event id, recording the id in a
// durable, tenant-aware ledger atomically with the handler's writes. Defined here
// to avoid an app<->inbox import cycle; the inbox package implements it.
type InboxProcessor interface {
	// ProcessOnce runs fn inside a transaction exactly once per eventID. A redelivery
	// of an already-processed id short-circuits (fn is not run) and returns nil.
	ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error
}

// InboxProvider is an optional interface modules implement to provide an
// InboxProcessor for DI. The ModuleRegistry wires it into ModuleDeps.Inbox.
type InboxProvider interface {
	InboxProcessor() InboxProcessor
}
```
- [ ] Add the `ModuleDeps` field after `Outbox OutboxPublisher` (line 190):
```go
	// Inbox provides durable consumer-side idempotency (ProcessOnce).
	// This field is nil if no inbox module is registered or inbox.enabled is false.
	Inbox InboxProcessor
```
- [ ] In `app/module_registry.go`, after the KeyStoreProvider branch (line 89), before line 91:
```go
	// Special case: If this module is an InboxProvider (inbox module),
	// make it available to other modules via deps.Inbox
	if inboxProvider, ok := module.(InboxProvider); ok {
		r.deps.Inbox = inboxProvider.InboxProcessor()
		r.logger.Info().
			Str("module", moduleName).
			Msg("Inbox module registered - available to other modules via deps.Inbox")
	}
```
- [ ] `make check` (app compiles with nil Inbox everywhere — keyed ModuleDeps literals unaffected). Commit: `feat(app): add InboxProcessor/InboxProvider wiring and deps.Inbox`.

### Task 5.2: config (InboxConfig + outbox doc fix + example)
- [ ] Add `InboxConfig` to `config/types.go` after OutboxConfig (mirror tag style; `AutoCreateTable` honest off-by-default; `RetentionPeriod time.Duration`):
```go
// InboxConfig holds consumer-side idempotency (inbox) settings.
// Production-safe defaults are applied when inbox is enabled:
//   - TableName: "gobricks_inbox"
//   - AutoCreateTable: false (opt-in; set true in dev or set false with managed migrations)
//   - RetentionPeriod: 168h (7d; must exceed the broker's max redelivery window)
type InboxConfig struct {
	Enabled         bool          `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`
	TableName       string        `koanf:"table_name" json:"table_name" yaml:"table_name" toml:"table_name" mapstructure:"table_name"`
	AutoCreateTable bool          `koanf:"auto_create_table" json:"auto_create_table" yaml:"auto_create_table" toml:"auto_create_table" mapstructure:"auto_create_table"`
	RetentionPeriod time.Duration `koanf:"retention_period" json:"retention_period" yaml:"retention_period" toml:"retention_period" mapstructure:"retention_period"`
}
```
- [ ] Add `Inbox InboxConfig` to the `Config` struct after the `Outbox` line (run gofmt to realign).
- [ ] Fix outbox doc (`config/types.go:480-482`): change `AutoCreateTable` comment to "Default: false. Set true in development; keep false for managed migrations." and `config.example.yaml:112` `auto_create_table: false`. Add an `inbox:` block to `config.example.yaml` after the outbox block (`enabled: false`, `table_name: gobricks_inbox`, `auto_create_table: false`, `retention_period: 168h`).
- [ ] `make check`. Commit: `feat(config): add InboxConfig; correct outbox auto_create_table default doc to false`.

### Task 5.3: inbox store (Record, Store iface, PG + Oracle, sqlid guard)
- [ ] **Record + Store + validateTableName** `inbox/store.go` (mirror `outbox/store.go` shape; package `inbox`; reuse `sqlid`):
```go
package inbox

import (
	"context"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// Record is one row in the inbox ledger.
type Record struct {
	TenantID    string
	EventID     string
	ProcessedAt time.Time
}

// Store abstracts inbox ledger operations for vendor-agnostic SQL.
type Store interface {
	// MarkProcessed records (tenant_id, event_id) within tx. Returns inserted=true
	// the first time and inserted=false on a duplicate (already processed).
	MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (inserted bool, err error)
	// DeleteProcessed removes ledger rows processed before the given time.
	DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)
	// CreateTable creates the inbox table and index if they do not exist.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}

func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
	return nil
}
```
- [ ] **Postgres** `inbox/store_postgres.go` (`//nolint:dupl` line before `package inbox`; `$N`; **ON CONFLICT DO NOTHING + RowsAffected**):
```go
//nolint:dupl // Intentional: PostgreSQL and Oracle stores share structure but differ in SQL dialect
package inbox

import (
	"context"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const postgresCreateTableSQL = `
CREATE TABLE IF NOT EXISTS %s (
    tenant_id     VARCHAR(255) NOT NULL DEFAULT '',
    event_id      VARCHAR(255) NOT NULL,
    processed_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, event_id)
)`

const postgresCreateProcessedIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_processed ON %s (processed_at)`

type postgresStore struct{ tableName string }

// NewPostgresStore creates a PostgreSQL inbox store.
func NewPostgresStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &postgresStore{tableName: tableName}, nil
}

func (s *postgresStore) MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (bool, error) {
	query := fmt.Sprintf(
		`INSERT INTO %s (tenant_id, event_id, processed_at) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, event_id) DO NOTHING`,
		s.tableName,
	)
	res, err := tx.Exec(ctx, query, rec.TenantID, rec.EventID, rec.ProcessedAt)
	if err != nil {
		return false, fmt.Errorf("inbox postgres: mark processed failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inbox postgres: rows affected failed: %w", err)
	}
	return n == 1, nil
}

func (s *postgresStore) DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s WHERE processed_at < $1`, s.tableName)
	res, err := db.Exec(ctx, query, before)
	if err != nil {
		return 0, fmt.Errorf("inbox postgres: delete processed failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inbox postgres: rows affected failed: %w", err)
	}
	return n, nil
}

func (s *postgresStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreateTableSQL, s.tableName)); err != nil {
		return fmt.Errorf("inbox postgres: create table failed: %w", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreateProcessedIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox postgres: create index failed: %w", err)
	}
	return nil
}

var _ Store = (*postgresStore)(nil)
```
- [ ] **Oracle** `inbox/store_oracle.go` (`:N`; **plain INSERT + catch `database.IsUniqueViolation`**; no `IF NOT EXISTS`; function index). NOTE: imports `github.com/gaborage/go-bricks/database` for `IsUniqueViolation`:
```go
//nolint:dupl // Intentional: Oracle and PostgreSQL stores share structure but differ in SQL dialect
package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const oracleCreateTableSQL = `
CREATE TABLE %s (
    tenant_id     VARCHAR2(255) DEFAULT '' NOT NULL,
    event_id      VARCHAR2(255) NOT NULL,
    processed_at  TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    CONSTRAINT pk_%s PRIMARY KEY (tenant_id, event_id)
)`

const oracleCreateProcessedIndexSQL = `
CREATE INDEX idx_%s_processed ON %s (processed_at)`

type oracleStore struct{ tableName string }

// NewOracleStore creates an Oracle inbox store.
func NewOracleStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &oracleStore{tableName: tableName}, nil
}

func (s *oracleStore) MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (bool, error) {
	query := fmt.Sprintf(
		`INSERT INTO %s (tenant_id, event_id, processed_at) VALUES (:1, :2, :3)`,
		s.tableName,
	)
	_, err := tx.Exec(ctx, query, rec.TenantID, rec.EventID, rec.ProcessedAt)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return false, nil // already processed (Oracle statement-level rollback keeps tx usable)
		}
		return false, fmt.Errorf("inbox oracle: mark processed failed: %w", err)
	}
	return true, nil
}

func (s *oracleStore) DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s WHERE processed_at < :1`, s.tableName)
	res, err := db.Exec(ctx, query, before)
	if err != nil {
		return 0, fmt.Errorf("inbox oracle: delete processed failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inbox oracle: rows affected failed: %w", err)
	}
	return n, nil
}

func (s *oracleStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreateTableSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox oracle: create table failed: %w", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreateProcessedIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox oracle: create index failed: %w", err)
	}
	return nil
}

var _ Store = (*oracleStore)(nil)
```
NOTE on Oracle PK name: `pk_%s` consumes tableName once; if the table name is schema-qualified, the constraint name would contain a dot — guard by deriving the constraint suffix from the last segment, or document that schema-qualified inbox names are unsupported in v1 (simplest: require an unqualified `table_name` for inbox; validate in config). Decide during implementation; default to requiring unqualified inbox table names and asserting it in `validateConfig`.
- [ ] **Tests** `inbox/store_postgres_test.go` / `inbox/store_oracle_test.go` mirror outbox store tests: `MarkProcessed` inserted=true (RowsAffected 1), duplicate inserted=false (PG RowsAffected 0; Oracle `WillReturnError` with a fabricated `*network.OracleError{ErrCode:1}` → inserted=false), `DeleteProcessed`, `CreateTable`. Conformance guards already in the store files.
- [ ] `make check`. Commit: `feat(inbox): vendor-split ledger store (PG ON CONFLICT / Oracle catch unique violation)`.

### Task 5.4: config defaults + validation
- [ ] `inbox/config.go` (mirror `outbox/config.go`):
```go
package inbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// DefaultTableName is the default inbox ledger table name.
const DefaultTableName = "gobricks_inbox"

// DefaultRetentionPeriod is the default processed-event retention (7 days).
// Must exceed the broker's maximum redelivery window. Written as a duration
// (168h); Go's time.ParseDuration does not accept "7d".
const DefaultRetentionPeriod = 7 * 24 * time.Hour

func applyDefaults(c *config.InboxConfig) {
	if c.TableName == "" {
		c.TableName = DefaultTableName
	}
	if c.RetentionPeriod == 0 {
		c.RetentionPeriod = DefaultRetentionPeriod
	}
	// AutoCreateTable intentionally left at its zero value (false, opt-in).
}

func validateConfig(c *config.InboxConfig) error {
	if c.RetentionPeriod < 0 {
		return fmt.Errorf("inbox: retention_period must not be negative, got %s", c.RetentionPeriod)
	}
	if strings.Contains(c.TableName, ".") {
		return fmt.Errorf("inbox: table_name %q must be unqualified (schema-qualified names unsupported)", c.TableName)
	}
	return nil
}
```
- [ ] `inbox/config_test.go`: defaults applied, negative retention rejected, dotted table rejected. `make check`. Commit: `feat(inbox): config defaults and validation`.

### Task 5.5: Inbox processor (ProcessOnce — composes WithTx + store) + cleanup + module
- [ ] `inbox/inbox.go`:
```go
package inbox

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/multitenant"
)

// Inbox runs handlers exactly once per event id, backed by a durable ledger.
type Inbox struct {
	module *Module
}

// ProcessOnce records eventID in the ledger and runs fn exactly once per id,
// atomically. A redelivery of an already-processed id short-circuits (fn not run)
// and returns nil. Tenant is resolved from ctx.
func (i *Inbox) ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error {
	if err := i.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	db, err := i.module.getDB(ctx)
	if err != nil {
		return err
	}
	tenantID, _ := multitenant.GetTenant(ctx) // "" in single-tenant mode
	rec := Record{TenantID: tenantID, EventID: eventID, ProcessedAt: time.Now()}
	return database.WithTx(ctx, db, func(ctx context.Context, tx dbtypes.Tx) error {
		inserted, err := i.module.store.MarkProcessed(ctx, tx, rec)
		if err != nil {
			return err
		}
		if !inserted {
			return nil // already processed → skip fn, commit no-op
		}
		return fn(ctx, tx)
	})
}
```
- [ ] `inbox/cleanup.go` (mirror `outbox/cleanup.go`; `DeleteProcessed`):
```go
package inbox

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/scheduler"
)

// Cleanup removes processed-event rows older than the retention period.
type Cleanup struct {
	store           Store
	retentionPeriod time.Duration
}

func (c *Cleanup) Execute(ctx scheduler.JobContext) error {
	db := ctx.DB()
	if db == nil {
		return fmt.Errorf("inbox cleanup: database not available")
	}
	cutoff := time.Now().Add(-c.retentionPeriod)
	deleted, err := c.store.DeleteProcessed(ctx, db, cutoff)
	if err != nil {
		return fmt.Errorf("inbox cleanup: delete failed: %w", err)
	}
	if deleted > 0 {
		ctx.Logger().Info().Int64("deleted", deleted).Str("cutoff", cutoff.Format(time.RFC3339)).Msg("Inbox cleanup completed")
	}
	return nil
}
```
- [ ] `inbox/module.go` (mirror `outbox/module.go`: Module struct with `logger/config/getDB/store/cfg/initMu/tableCreated/processor`, `NewModule`, `Name`="inbox", `Init` (capture deps.Logger/Config/DB, `m.cfg = m.config.Inbox`, applyDefaults, validateConfig, Enabled short-circuit, fail-fast `getDB==nil`, build `&Inbox{module:m}`), `ensureStoreInitialized` (verbatim shape — switch `db.DatabaseType()` → `NewPostgresStore`/`NewOracleStore`, `tableCreated` guard, CreateTable-as-warning), `InboxProcessor()` provider method returning the `*Inbox`, `RegisterJobs` (only the cleanup `DailyAt("inbox-cleanup", …)` when `RetentionPeriod > 0` — **no relay**, so implement `app.JobProvider` for cleanup only; use a `lazyStore` wrapper for the cleanup job mirroring outbox), `Shutdown`). Inbox has no `getMsg`/`publisher`/messaging fail-fast.
- [ ] `inbox/inbox_test.go`: `ProcessOnce` runs fn once on first id (TestDB: ExpectTransaction → ExpectExec INSERT RowsAffected 1 → fn side-effect exec → commit), skips fn on duplicate (RowsAffected 0 → fn NOT called → commit), and propagates fn errors (rollback). Use a captured bool to assert fn invocation.
- [ ] `make check`. Commit: `feat(inbox): ProcessOnce idempotency helper, cleanup job, and module wiring`.

### Task 5.6: inbox/testing mock + assertions
- [ ] `inbox/testing/mock_inbox.go`: `MockInbox` implementing `app.InboxProcessor` — records each `ProcessOnce(eventID)`, configurable "already processed" set (skip fn) and error; runs `fn(ctx, nil)` when not a duplicate so handler logic is exercised; thread-safe (mutex). `inbox/testing/assertions.go`: `AssertProcessed`, `AssertNotProcessed`, `AssertProcessCount` mirroring outbox/testing.
- [ ] `make check`. Commit: `feat(inbox): testing mock and assertions`.

### Task 5.7: integration tests (both vendors)
- [ ] `inbox/*_integration_test.go` (`//go:build integration`): same event id processed twice against PG and Oracle containers → fn runs once, second call is a no-op; verify PG path not poisoned and Oracle path survives. `make test-integration`.
- [ ] Commit: `test(inbox): integration coverage for exactly-once across both vendors`.

### Task 5.8: open + babysit PR5.

---

## Self-review notes (run before executing)
- **Spec coverage:** P0.1→PR1; #533→PR2; P0.2+#534→PR3; #535→PR4; #536+InboxConfig+outbox-doc-fix→PR5. All §-items mapped.
- **Type consistency:** `Store.MarkProcessed(ctx, tx, Record) (bool, error)` used identically in PG/Oracle/Inbox/tests; `app.InboxProcessor.ProcessOnce(ctx, eventID, fn func(ctx, tx dbtypes.Tx) error) error` matches `inbox.Inbox.ProcessOnce`; `database.IsUniqueViolation(error) bool` consumed by `inbox/store_oracle.go`; `sqlid.ValidateTableName` consumed by outbox + inbox.
- **Decision encodings:** PG ON CONFLICT vs Oracle catch (Task 5.3); auto-create OFF-by-default honest bool (Task 5.2/5.4); error-only WithTx (Task 3.2); retention 168h not 7d (Task 5.4).
- **Open impl decision:** Oracle PK constraint name with schema-qualified table → resolved by requiring unqualified inbox `table_name` (validateConfig).
