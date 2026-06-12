package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Oracle DDL for the inbox ledger table.
// Uses VARCHAR2 instead of VARCHAR, SYSTIMESTAMP instead of NOW(), a named
// primary-key constraint, and no IF NOT EXISTS (ORA-00955 if it already exists
// is tolerated by the caller, which treats CreateTable errors as warnings).
// tenant_id is NOT NULL and part of the primary key, so it can never be NULL. Oracle treats
// the empty string as NULL, so the empty/default tenant is stored as oracleEmptyTenantSentinel instead (see
// resolveOracleTenant) — no DEFAULT clause, which on Oracle would be a contradictory DEFAULT
// NULL on a NOT NULL column (issue #590).
const oracleCreateTableSQL = `
CREATE TABLE %s (
    tenant_id     VARCHAR2(255) NOT NULL,
    event_id      VARCHAR2(255) NOT NULL,
    processed_at  TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    CONSTRAINT pk_%s PRIMARY KEY (tenant_id, event_id)
)`

// Oracle index DDL for retention cleanup by processed_at.
const oracleCreateProcessedIndexSQL = `
CREATE INDEX idx_%s_processed ON %s (processed_at)`

// oracleStore implements Store for Oracle using :1-style placeholders.
type oracleStore struct {
	tableName string
}

// NewOracleStore creates a new Oracle inbox store.
// Returns an error if the table name is not a safe, unqualified identifier.
func NewOracleStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &oracleStore{tableName: tableName}, nil
}

// oracleEmptyTenantSentinel is the placeholder stored for the empty/default tenant on Oracle.
// tenant_id is part of the primary key (so it cannot be NULL), and Oracle treats the empty string as NULL,
// so single-tenant deployments (tenant id "") would otherwise fail every insert with ORA-01400.
// This reserved value is non-NULL and structurally unlikely to be a real tenant id; a real
// tenant id equal to it is rejected by resolveOracleTenant. The inbox never reads tenant_id
// back, so the value only needs to be consistent for primary-key dedup. See issue #590.
const oracleEmptyTenantSentinel = "__gobricks_default_tenant__"

// resolveOracleTenant maps the empty/default tenant id to the non-NULL sentinel for storage,
// and rejects a real tenant id that collides with the reserved sentinel.
func resolveOracleTenant(tenantID string) (string, error) {
	if tenantID == oracleEmptyTenantSentinel {
		return "", fmt.Errorf("inbox oracle: tenant id %q is reserved for the default tenant and cannot be used", oracleEmptyTenantSentinel)
	}
	if tenantID == "" {
		return oracleEmptyTenantSentinel, nil
	}
	return tenantID, nil
}

// MarkProcessed inserts the ledger row. Oracle has no ON CONFLICT, but a
// unique-violation (ORA-00001) is statement-level and leaves the transaction
// usable, so a duplicate is detected by catching it via database.IsUniqueViolation.
func (s *oracleStore) MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (bool, error) {
	tenantID, err := resolveOracleTenant(rec.TenantID)
	if err != nil {
		return false, err
	}

	query := fmt.Sprintf(
		`INSERT INTO %s (tenant_id, event_id, processed_at) VALUES (:1, :2, :3)`,
		s.tableName,
	)
	_, err = tx.Exec(ctx, query, tenantID, rec.EventID, rec.ProcessedAt)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return false, nil // already processed
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
	count, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inbox oracle: rows affected failed: %w", err)
	}
	return count, nil
}

// CreateTable creates the inbox table and index in Oracle.
// Unlike PostgreSQL (which uses IF NOT EXISTS), Oracle DDL returns ORA-00955 if
// the object already exists; the caller (ensureStoreInitialized) treats all
// CreateTable errors as warnings, so existing objects are handled gracefully.
func (s *oracleStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreateTableSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox oracle: create table failed: %w", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreateProcessedIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox oracle: create index failed: %w", err)
	}
	return nil
}

// Compile-time guard: ensure oracleStore satisfies the Store interface.
var _ Store = (*oracleStore)(nil)
