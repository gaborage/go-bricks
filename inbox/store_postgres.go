package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// PostgreSQL DDL for the inbox ledger table.
const postgresCreateTableSQL = `
CREATE TABLE IF NOT EXISTS %s (
    tenant_id     VARCHAR(255) NOT NULL DEFAULT '',
    event_id      VARCHAR(255) NOT NULL,
    processed_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, event_id)
)`

// PostgreSQL index DDL for retention cleanup by processed_at.
const postgresCreateProcessedIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_processed ON %s (processed_at)`

// postgresStore implements Store for PostgreSQL using $1-style placeholders.
type postgresStore struct {
	tableName string
	qb        *database.QueryBuilder
}

// NewPostgresStore creates a new PostgreSQL inbox store.
// Returns an error if the table name is not a safe, unqualified identifier.
func NewPostgresStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &postgresStore{tableName: tableName, qb: database.NewQueryBuilder(dbtypes.PostgreSQL)}, nil
}

// MarkProcessed inserts the ledger row, using ON CONFLICT DO NOTHING so a
// duplicate is detected via RowsAffected without raising an error (a 23505 would
// otherwise poison the transaction on PostgreSQL).
func (s *postgresStore) MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (bool, error) {
	// No update columns: the builder renders ON CONFLICT (tenant_id, event_id)
	// DO NOTHING, and RowsAffected tells a fresh row from a duplicate.
	query, args, err := s.qb.BuildUpsert(s.tableName,
		[]string{"tenant_id", "event_id"},
		map[string]any{"tenant_id": rec.TenantID, "event_id": rec.EventID, "processed_at": rec.ProcessedAt},
		nil)
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build mark processed failed: %w", err)
	}
	res, err := tx.Exec(ctx, query, args...)
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
	return deleteProcessedBefore(ctx, db, s.qb, s.tableName, "inbox postgres: delete processed failed", before)
}

// CreateTable runs the DDL, which stays hand-written: the builder has no DDL
// door and the statements interpolate no value.
//
// SECURITY: Manual SQL review completed - static DDL constants; only the constructor-validated
// table name (validateTableName) is interpolated, twice for the index; no caller value
func (s *postgresStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreateTableSQL, s.tableName)); err != nil {
		return fmt.Errorf("inbox postgres: create table failed: %w", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreateProcessedIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("inbox postgres: create index failed: %w", err)
	}
	return nil
}

// Compile-time guard: ensure postgresStore satisfies the Store interface.
var _ Store = (*postgresStore)(nil)
