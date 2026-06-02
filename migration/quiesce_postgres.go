package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresQuiesceTableDDL is the CREATE TABLE statement used by CreateTable.
// Exported so operators managing schema externally can run it via their own
// tooling. The %s placeholder is replaced with the validated, quoted table
// identifier. One row per scope (id); v1 uses the single "global" scope.
//
// expires_at is the TTL hard stop: IsSet treats now >= expires_at as released
// even when cleared_at is NULL, so a migration job that crashes after Set can
// never block provisioning beyond the TTL (read-side auto-release, no sweeper).
const PostgresQuiesceTableDDL = `CREATE TABLE IF NOT EXISTS %s (
    id          VARCHAR(64)  PRIMARY KEY,
    set_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    set_by      VARCHAR(255) NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    expires_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    cleared_at  TIMESTAMP WITH TIME ZONE
)`

// PostgresQuiesceController is a PostgreSQL-backed QuiesceController. It stores
// the flag in a control-plane table (zero new dependencies) so it survives a
// process restart and is queryable by operators / the CLI. Construct with
// NewPostgresQuiesceController.
type PostgresQuiesceController struct {
	db          *sql.DB
	tableName   string
	quotedTable string
	now         func() time.Time
	audit       Emitter
}

// NewPostgresQuiesceController returns a controller backed by db. An empty
// tableName resolves to DefaultQuiesceTable. The name may be schema-qualified
// ("schema.table"). Returns ErrInvalidQuiesceTable if it fails the safe-
// identifier check.
func NewPostgresQuiesceController(db *sql.DB, tableName string) (*PostgresQuiesceController, error) {
	if db == nil {
		return nil, errors.New("migration: NewPostgresQuiesceController requires a non-nil *sql.DB")
	}
	if tableName == "" {
		tableName = DefaultQuiesceTable
	}
	quoted, err := validateAndQuoteQuiesceTable(tableName)
	if err != nil {
		return nil, err
	}
	return &PostgresQuiesceController{
		db:          db,
		tableName:   tableName,
		quotedTable: quoted,
	}, nil
}

// WithClock installs a deterministic clock (tests). Passing nil restores
// time.Now. Builder method — call before concurrent use. Returns the controller
// for chaining.
func (c *PostgresQuiesceController) WithClock(now func() time.Time) *PostgresQuiesceController {
	c.now = now
	return c
}

// WithAudit enables quiesce.set / quiesce.cleared audit emission. The audited
// principal is the operator who performed the action (Set's By / Clear's by),
// never inferred. Returns the controller for chaining.
func (c *PostgresQuiesceController) WithAudit(em Emitter) *PostgresQuiesceController {
	c.audit = em
	return c
}

func (c *PostgresQuiesceController) timestamp() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now()
}

// CreateTable provisions the quiesce table idempotently.
func (c *PostgresQuiesceController) CreateTable(ctx context.Context) error {
	// #nosec G201 -- c.quotedTable is quotePGIdent of a safePGIdentifier-validated
	// name captured at construction; no user-controlled value reaches the SQL.
	stmt := fmt.Sprintf(PostgresQuiesceTableDDL, c.quotedTable)
	if _, err := c.db.ExecContext(ctx, stmt); err != nil { // NOSONAR S2077: validated identifier substitution; no user input
		return fmt.Errorf("quiesce postgres: create table: %w", err)
	}
	return nil
}

// Set activates (or renews) the flag. Idempotent on the scope id: an existing
// row is updated (renewing expires_at and un-clearing it).
func (c *PostgresQuiesceController) Set(ctx context.Context, opts QuiesceSetOptions) (*QuiesceStatus, error) {
	now := c.timestamp()
	ttl, err := resolveTTL(opts.TTL)
	if err != nil {
		return nil, err
	}
	expires := now.Add(ttl)
	// #nosec G201 -- c.quotedTable is the validated, quoted identifier captured
	// at construction; all value-side input flows through $N placeholders.
	stmt := fmt.Sprintf(
		`INSERT INTO %s (id, set_at, set_by, reason, expires_at, cleared_at)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 ON CONFLICT (id) DO UPDATE SET
		     set_at = EXCLUDED.set_at,
		     set_by = EXCLUDED.set_by,
		     reason = EXCLUDED.reason,
		     expires_at = EXCLUDED.expires_at,
		     cleared_at = NULL`,
		c.quotedTable,
	)
	if _, err := c.db.ExecContext(ctx, stmt, // NOSONAR S2077: insert built from validated-identifier template; all values via $N
		DefaultQuiesceScope, now, opts.By, opts.Reason, expires,
	); err != nil {
		return nil, fmt.Errorf("quiesce postgres: set: %w", err)
	}
	rec := &quiesceRecord{setAt: now, setBy: opts.By, reason: opts.Reason, expiresAt: expires}
	emitQuiesceEvent(ctx, c.audit, opts.By, AuditEventTypeQuiesceSet, rec, now)
	return statusFrom(rec, now), nil
}

// Clear deactivates an uncleared flag (active OR auto-released by TTL) — the
// unconditional operator override, keyed on scope, not the setter's session.
// Returns ErrQuiesceNotSet only when no row exists or it was already cleared;
// an expired-but-uncleared row is still clearable so operators can tidy a
// crashed deploy's flag and produce the quiesce.cleared audit trail.
func (c *PostgresQuiesceController) Clear(ctx context.Context, by string) (*QuiesceStatus, error) {
	now := c.timestamp()
	// #nosec G201 -- c.quotedTable is the validated, quoted identifier; all
	// value-side input flows through $N placeholders.
	stmt := fmt.Sprintf(
		`UPDATE %s SET cleared_at = $1
		 WHERE id = $2 AND cleared_at IS NULL
		 RETURNING set_at, set_by, reason, expires_at, cleared_at`,
		c.quotedTable,
	)
	row := c.db.QueryRowContext(ctx, stmt, now, DefaultQuiesceScope) // NOSONAR S2077: update built from validated-identifier template; values via $N
	rec, err := scanQuiesceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrQuiesceNotSet
	}
	if err != nil {
		return nil, fmt.Errorf("quiesce postgres: clear: %w", err)
	}
	emitQuiesceEvent(ctx, c.audit, by, AuditEventTypeQuiesceCleared, rec, now)
	return statusFrom(rec, now), nil
}

// IsSet reports whether the flag is currently active.
func (c *PostgresQuiesceController) IsSet(ctx context.Context) (bool, error) {
	st, err := c.Query(ctx)
	if err != nil {
		return false, err
	}
	return st.Active, nil
}

// Query returns the full status snapshot. A missing row is reported as the
// zero status (never quiesced).
func (c *PostgresQuiesceController) Query(ctx context.Context) (*QuiesceStatus, error) {
	now := c.timestamp()
	// #nosec G201 -- c.quotedTable is the validated, quoted identifier; id via $1.
	stmt := fmt.Sprintf(
		`SELECT set_at, set_by, reason, expires_at, cleared_at FROM %s WHERE id = $1`,
		c.quotedTable,
	)
	row := c.db.QueryRowContext(ctx, stmt, DefaultQuiesceScope) // NOSONAR S2077: query built from validated-identifier template; id via $1
	rec, err := scanQuiesceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return &QuiesceStatus{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quiesce postgres: query: %w", err)
	}
	return statusFrom(rec, now), nil
}

// rowScanner is the minimal *sql.Row surface used by scanQuiesceRow.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanQuiesceRow(row rowScanner) (*quiesceRecord, error) {
	var (
		rec     quiesceRecord
		cleared sql.NullTime
	)
	if err := row.Scan(&rec.setAt, &rec.setBy, &rec.reason, &rec.expiresAt, &cleared); err != nil {
		return nil, err
	}
	if cleared.Valid {
		t := cleared.Time
		rec.clearedAt = &t
	}
	return &rec, nil
}
