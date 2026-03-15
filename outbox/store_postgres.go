//nolint:dupl // Intentional: PostgreSQL and Oracle stores share structure but differ in SQL dialect
package outbox

import (
	"context"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// PostgreSQL DDL for the outbox table.
const postgresCreateTableSQL = `
CREATE TABLE IF NOT EXISTS %s (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    VARCHAR(255) NOT NULL,
    aggregate_id  VARCHAR(255) NOT NULL,
    payload       BYTEA NOT NULL,
    headers       BYTEA,
    exchange      VARCHAR(255) NOT NULL DEFAULT '',
    routing_key   VARCHAR(255) NOT NULL DEFAULT '',
    status        VARCHAR(20) NOT NULL DEFAULT 'pending',
    retry_count   INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    published_at  TIMESTAMP WITH TIME ZONE
)`

// PostgreSQL index DDL for efficient pending event lookups.
const postgresCreatePendingIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_pending
    ON %s (created_at) WHERE status = 'pending'`

// PostgreSQL index DDL for efficient published event cleanup.
const postgresCreatePublishedIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_published
    ON %s (published_at) WHERE status = 'published'`

// postgresStore implements Store for PostgreSQL using $1-style placeholders.
type postgresStore struct {
	tableName string
}

// NewPostgresStore creates a new PostgreSQL outbox store.
// Returns an error if the table name contains invalid identifier characters.
func NewPostgresStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &postgresStore{tableName: tableName}, nil
}

func (s *postgresStore) Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error {
	query := fmt.Sprintf(
		`INSERT INTO %s (id, event_type, aggregate_id, payload, headers, exchange, routing_key, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.tableName,
	)

	_, err := tx.Exec(ctx, query,
		record.ID,
		record.EventType,
		record.AggregateID,
		record.Payload,
		record.Headers,
		record.Exchange,
		record.RoutingKey,
		record.Status,
		record.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("outbox postgres: insert failed: %w", err)
	}

	return nil
}

func (s *postgresStore) FetchPending(ctx context.Context, db dbtypes.Interface, batchSize, maxRetries int) ([]Record, error) {
	query := fmt.Sprintf(
		`SELECT id, event_type, aggregate_id, payload, headers, exchange, routing_key, status, retry_count, created_at
		 FROM %s
		 WHERE status = $1 AND retry_count < $2
		 ORDER BY created_at ASC
		 LIMIT $3`,
		s.tableName,
	)

	rows, err := db.Query(ctx, query, StatusPending, maxRetries, batchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox postgres: fetch pending failed: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(
			&r.ID, &r.EventType, &r.AggregateID, &r.Payload, &r.Headers,
			&r.Exchange, &r.RoutingKey, &r.Status, &r.RetryCount, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("outbox postgres: scan failed: %w", err)
		}
		records = append(records, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox postgres: rows iteration failed: %w", err)
	}

	return records, nil
}

func (s *postgresStore) MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET status = $1, published_at = $2 WHERE id = $3`,
		s.tableName,
	)

	_, err := db.Exec(ctx, query, StatusPublished, time.Now(), eventID)
	if err != nil {
		return fmt.Errorf("outbox postgres: mark published failed: %w", err)
	}

	return nil
}

func (s *postgresStore) MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET retry_count = retry_count + 1, error = $1 WHERE id = $2`,
		s.tableName,
	)

	_, err := db.Exec(ctx, query, errMsg, eventID)
	if err != nil {
		return fmt.Errorf("outbox postgres: mark failed failed: %w", err)
	}

	return nil
}

func (s *postgresStore) DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE status = $1 AND published_at < $2`,
		s.tableName,
	)

	result, err := db.Exec(ctx, query, StatusPublished, before)
	if err != nil {
		return 0, fmt.Errorf("outbox postgres: delete published failed: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("outbox postgres: rows affected failed: %w", err)
	}

	return count, nil
}

func (s *postgresStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	// Create table
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreateTableSQL, s.tableName)); err != nil {
		return fmt.Errorf("outbox postgres: create table failed: %w", err)
	}

	// Create pending index
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreatePendingIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("outbox postgres: create pending index failed: %w", err)
	}

	// Create published index
	if _, err := db.Exec(ctx, fmt.Sprintf(postgresCreatePublishedIndexSQL, s.tableName, s.tableName)); err != nil {
		return fmt.Errorf("outbox postgres: create published index failed: %w", err)
	}

	return nil
}
