//nolint:dupl // Intentional: PostgreSQL and Oracle stores share structure but differ in SQL dialect
package inbox

import (
	"context"
	"fmt"
	"time"

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
}

// NewPostgresStore creates a new PostgreSQL inbox store.
// Returns an error if the table name is not a safe, unqualified identifier.
func NewPostgresStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &postgresStore{tableName: tableName}, nil
}

// MarkProcessed inserts the ledger row, using ON CONFLICT DO NOTHING so a
// duplicate is detected via RowsAffected without raising an error (a 23505 would
// otherwise poison the transaction on PostgreSQL).
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
	count, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inbox postgres: rows affected failed: %w", err)
	}
	return count, nil
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

// Compile-time guard: ensure postgresStore satisfies the Store interface.
var _ Store = (*postgresStore)(nil)
