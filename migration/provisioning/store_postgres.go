package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultPostgresTable is the table name used when NewPostgresStore is called
// with an empty tableName argument. Operators with strict naming conventions
// can override at construction time.
const DefaultPostgresTable = "provisioning_jobs"

// PostgresStateTableDDL is the CREATE TABLE statement used by CreateTable.
// Exported so operators managing schema externally can run it via their own
// migration tooling. The %s placeholder is replaced with the validated
// (and quoted) table identifier; double-call the helper with the same
// table name to apply.
//
// metadata is JSONB so consumers can record step-specific state (applied
// migration version range, seeded fixture IDs, etc.) without schema churn.
const PostgresStateTableDDL = `CREATE TABLE IF NOT EXISTS %s (
    id          VARCHAR(255) PRIMARY KEY,
    tenant_id   VARCHAR(255) NOT NULL,
    state       VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
)`

// PostgresStateTableIndexes are the supporting indexes for tenant- and
// state-scoped lookups. The dispatcher (out of scope for #379) will scan
// rows by (state) to find work; ad-hoc operator queries usually filter by
// tenant. Both are idempotent.
var PostgresStateTableIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_%[2]s_tenant ON %[1]s (tenant_id)`,
	`CREATE INDEX IF NOT EXISTS idx_%[2]s_state  ON %[1]s (state)`,
}

// maxPGTableSegment caps the per-segment length of a provisioning table
// name. NAMEDATALEN-1 = 63 is PostgreSQL's identifier limit, but supporting
// indexes are derived as `idx_<base>_<suffix>` where the longest suffix is
// "_tenant" (7 chars) plus the "idx_" prefix (4 chars), so reserving 11
// chars for the index decoration leaves 52 for the base. Bumping this past
// 52 risks supporting indexes silently overflowing on the extreme case.
const maxPGTableSegment = 52

// safePGTableIdent matches PostgreSQL identifiers acceptable as table names
// for the provisioning store. Permits an optional schema-qualified form
// `schema.table` where each segment matches the safe-identifier subset
// used elsewhere in the migration package (see migration/roles.go). The
// per-segment cap (maxPGTableSegment) is below NAMEDATALEN-1 so the
// derived index names also fit; see PostgresStateTableIndexes.
var safePGTableIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,51}(\.[A-Za-z_][A-Za-z0-9_]{0,51})?$`)

// ErrInvalidTableName is returned by NewPostgresStore when the supplied
// table name fails the safe-identifier check.
var ErrInvalidTableName = errors.New("provisioning: invalid PostgreSQL table name")

// PostgresStore is the PostgreSQL-backed reference implementation of
// StateStore. Construct via NewPostgresStore so the table name is validated.
type PostgresStore struct {
	db        *sql.DB
	tableName string
	// quotedTable is the double-quoted form used in DDL/DML. Computed once
	// at construction so per-call SQL building doesn't repeat the quoting.
	quotedTable string
	// indexBase is the identifier-safe form of the table name used in
	// index names (last segment of a schema.table form).
	indexBase string
	// now lets tests inject deterministic timestamps.
	now func() time.Time
}

// NewPostgresStore returns a PostgresStore backed by db with the supplied
// table name. Empty tableName resolves to DefaultPostgresTable. Returns
// ErrInvalidTableName if the table name fails the safe-identifier check.
func NewPostgresStore(db *sql.DB, tableName string) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("provisioning: NewPostgresStore requires a non-nil *sql.DB")
	}
	if tableName == "" {
		tableName = DefaultPostgresTable
	}
	if !safePGTableIdent.MatchString(tableName) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTableName, tableName)
	}
	return &PostgresStore{
		db:          db,
		tableName:   tableName,
		quotedTable: quoteQualified(tableName),
		indexBase:   indexBaseName(tableName),
	}, nil
}

// WithClock returns s with the supplied clock function installed. Useful
// for deterministic timestamps in tests.
func (s *PostgresStore) WithClock(now func() time.Time) *PostgresStore {
	s.now = now
	return s
}

func (s *PostgresStore) timestamp() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

// CreateTable creates the provisioning table and its supporting indexes
// idempotently. Safe to call on a database where the table already exists.
func (s *PostgresStore) CreateTable(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(PostgresStateTableDDL, s.quotedTable)); err != nil {
		return fmt.Errorf("provisioning postgres: create table: %w", err)
	}
	for _, tpl := range PostgresStateTableIndexes {
		stmt := fmt.Sprintf(tpl, s.quotedTable, s.indexBase)
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("provisioning postgres: create index: %w", err)
		}
	}
	return nil
}

// Get loads the job by ID. Returns ErrJobNotFound when no row exists.
func (s *PostgresStore) Get(ctx context.Context, jobID string) (*Job, error) {
	// #nosec G201 -- s.quotedTable is the validated, double-quoted form of
	// a regex-checked identifier (safePGTableIdent). All value-side input
	// flows through $N placeholders.
	query := fmt.Sprintf(
		`SELECT id, tenant_id, state, attempts, last_error, metadata, created_at, updated_at
		 FROM %s WHERE id = $1`,
		s.quotedTable,
	)
	row := s.db.QueryRowContext(ctx, query, jobID)
	var (
		job          Job
		stateStr     string
		metadataJSON []byte
	)
	if err := row.Scan(&job.ID, &job.TenantID, &stateStr, &job.Attempts, &job.LastError,
		&metadataJSON, &job.CreatedAt, &job.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("provisioning postgres: get: %w", err)
	}
	job.State = State(stateStr)
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &job.Metadata); err != nil {
			return nil, fmt.Errorf("provisioning postgres: decode metadata: %w", err)
		}
	}
	return &job, nil
}

// Upsert inserts job at StatePending when no row exists for job.ID, or
// returns the existing row unchanged. Matches the StateStore contract.
// Rejects nil jobs and jobs missing ID or TenantID via ErrInvalidJob.
func (s *PostgresStore) Upsert(ctx context.Context, job *Job) (*Job, error) {
	if err := validateJobForUpsert(job); err != nil {
		return nil, err
	}
	now := s.timestamp()
	metadataJSON, err := encodeMetadata(job.Metadata)
	if err != nil {
		return nil, err
	}
	if metadataJSON == nil {
		// metadata column is NOT NULL; substitute the empty object for new
		// rows. (Transition uses COALESCE so nil-on-update preserves the
		// persisted value; INSERT has no row to fall back to.)
		metadataJSON = []byte("{}")
	}
	// INSERT ... ON CONFLICT DO NOTHING then SELECT. Two roundtrips, but
	// keeps the "return existing row unchanged" semantics clean and avoids
	// the gotcha where ON CONFLICT DO UPDATE would rewrite created_at.
	// #nosec G201 -- s.quotedTable is the validated, double-quoted form of
	// a regex-checked identifier (safePGTableIdent). All value-side input
	// flows through $N placeholders.
	insert := fmt.Sprintf(
		`INSERT INTO %s (id, tenant_id, state, attempts, last_error, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		 ON CONFLICT (id) DO NOTHING`,
		s.quotedTable,
	)
	if _, err := s.db.ExecContext(ctx, insert,
		job.ID, job.TenantID, string(StatePending), 0, "", metadataJSON, now,
	); err != nil {
		return nil, fmt.Errorf("provisioning postgres: upsert insert: %w", err)
	}
	return s.Get(ctx, job.ID)
}

// Transition applies the from -> to edge atomically. ValidateTransition
// gates the graph; the SQL UPDATE ... WHERE state = $from gates concurrent
// writers.
func (s *PostgresStore) Transition(
	ctx context.Context, jobID string, from, to State, metadata map[string]string, lastError string,
) error {
	if err := ValidateTransition(from, to); err != nil {
		return err
	}
	metadataJSON, err := encodeMetadata(metadata)
	if err != nil {
		return err
	}
	// Attempts increments on every forward transition (cleanup/failed
	// excluded) so retry telemetry stays aligned with the in-memory store.
	incrAttempts := to != StateCleanup && to != StateFailed
	attemptsExpr := "attempts"
	if incrAttempts {
		attemptsExpr = "attempts + 1"
	}
	// metadata=COALESCE($5::jsonb, metadata) keeps the persisted value when
	// the caller passes nil so the Executor's success path (which threads
	// the existing job.Metadata back through) doesn't accidentally clear
	// step-supplied state.
	// #nosec G201 -- s.quotedTable is the validated, double-quoted form of
	// a regex-checked identifier (safePGTableIdent); attemptsExpr is one of
	// two literal strings selected by an in-package bool, never user input.
	// All value-side data flows through $N placeholders.
	update := fmt.Sprintf(
		`UPDATE %s
		   SET state = $1,
		       attempts = %s,
		       last_error = $2,
		       metadata = COALESCE($3::jsonb, metadata),
		       updated_at = $4
		 WHERE id = $5 AND state = $6`,
		s.quotedTable, attemptsExpr,
	)
	res, err := s.db.ExecContext(ctx, update,
		string(to), lastError, metadataJSON, s.timestamp(), jobID, string(from),
	)
	if err != nil {
		return fmt.Errorf("provisioning postgres: transition update: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("provisioning postgres: rows affected: %w", err)
	}
	if affected == 0 {
		// Either the row doesn't exist or the state has moved on. Disambiguate
		// so callers get the right sentinel — important for the dispatcher
		// (out of scope here) which retries on stale reads but not on missing.
		// Propagate non-NotFound errors from the disambiguation Get verbatim
		// so transient failures (context cancellation, connection drop, bad
		// metadata decode) aren't silently rewritten to ErrStaleRead.
		_, getErr := s.Get(ctx, jobID)
		switch {
		case errors.Is(getErr, ErrJobNotFound):
			return ErrJobNotFound
		case getErr != nil:
			return fmt.Errorf("provisioning postgres: disambiguate zero-row update: %w", getErr)
		default:
			return ErrStaleRead
		}
	}
	return nil
}

// encodeMetadata returns metadata as JSON bytes, or nil if the input is nil.
// nil is preserved (vs `{}`) so the COALESCE in Transition can keep the
// persisted metadata when the caller doesn't supply a new value.
func encodeMetadata(m map[string]string) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("provisioning: encode metadata: %w", err)
	}
	return b, nil
}

// quoteQualified turns "schema.table" or "table" into the double-quoted PG
// form: "schema"."table" or "table". Caller must have verified the name
// via safePGTableIdent.
func quoteQualified(name string) string {
	parts := strings.SplitN(name, ".", 2)
	for i, p := range parts {
		parts[i] = `"` + p + `"`
	}
	return strings.Join(parts, ".")
}

// indexBaseName returns the identifier-safe last segment of a possibly
// schema-qualified name; used to construct supporting index names that
// stay short and identifier-clean.
func indexBaseName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}
