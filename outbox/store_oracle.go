//nolint:dupl // Intentional: Oracle and PostgreSQL stores share structure but differ in SQL dialect
package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// Oracle DDL for the outbox table.
// Uses VARCHAR2 instead of VARCHAR, BLOB instead of BYTEA, CLOB instead of TEXT,
// SYS_GUID() instead of gen_random_uuid(), SYSTIMESTAMP instead of NOW().
// Column "error" is renamed to "error_msg" (Oracle reserved word).
const oracleCreateTableSQL = `
CREATE TABLE %s (
    id            VARCHAR2(36) PRIMARY KEY,
    event_type    VARCHAR2(255) NOT NULL,
    aggregate_id  VARCHAR2(255) NOT NULL,
    payload       BLOB NOT NULL,
    headers       BLOB,
    -- Nullable: Oracle treats '' as NULL, so a DEFAULT '' NOT NULL is self-contradictory and
    -- rejects events targeting the AMQP default (empty) exchange with ORA-01400. FetchPending
    -- maps the resulting NULL back to "" on scan. See issue #586.
    exchange      VARCHAR2(255),
    routing_key   VARCHAR2(255),
    status        VARCHAR2(20) DEFAULT 'pending' NOT NULL,
    retry_count   NUMBER(10) DEFAULT 0 NOT NULL,
    error_msg     CLOB,
    created_at    TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    published_at  TIMESTAMP WITH TIME ZONE
)`

// Oracle function-based index for pending events (partial indexes not supported).
const oracleCreatePendingIndexSQL = `
CREATE INDEX idx_%s_pending ON %s (
    CASE WHEN status = 'pending' THEN created_at END
)`

// Oracle function-based index for published events cleanup.
const oracleCreatePublishedIndexSQL = `
CREATE INDEX idx_%s_published ON %s (
    CASE WHEN status = 'published' THEN published_at END
)`

// oracleStore implements Store for Oracle using :1-style placeholders.
type oracleStore struct {
	tableName string
}

// NewOracleStore creates a new Oracle outbox store.
// Returns an error if the table name contains invalid identifier characters.
func NewOracleStore(tableName string) (Store, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	return &oracleStore{tableName: tableName}, nil
}

func (s *oracleStore) Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error {
	query := fmt.Sprintf(
		`INSERT INTO %s (id, event_type, aggregate_id, payload, headers, exchange, routing_key, status, created_at)
		 VALUES (:1, :2, :3, :4, :5, :6, :7, :8, :9)`,
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
		return fmt.Errorf("outbox oracle: insert failed: %w", err)
	}

	return nil
}

// FetchPending returns up to batchSize pending events, oldest first. Selection is
// status-gated only (no retry_count filter) so an outage-inflated count cannot
// freeze a healthy event. An empty exchange or routing_key is stored as NULL on
// Oracle (an empty string is NULL there) and mapped back to "" on scan; see issue #586.
func (s *oracleStore) FetchPending(ctx context.Context, db dbtypes.Interface, batchSize int) ([]Record, error) {
	// Oracle uses FETCH FIRST N ROWS ONLY (12c+) instead of LIMIT
	query := fmt.Sprintf(
		`SELECT id, event_type, aggregate_id, payload, headers, exchange, routing_key, status, retry_count, created_at
		 FROM %s
		 WHERE status = :1
		 ORDER BY created_at ASC
		 FETCH FIRST :2 ROWS ONLY`,
		s.tableName,
	)

	rows, err := db.Query(ctx, query, StatusPending, batchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox oracle: fetch pending failed: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var r Record
		// Oracle stores an empty exchange/routing_key (the AMQP default exchange) as NULL,
		// because '' IS NULL in Oracle — so scan them NULL-tolerantly and map NULL -> "".
		// See issue #586.
		var exchange, routingKey sql.NullString
		if err := rows.Scan(
			&r.ID, &r.EventType, &r.AggregateID, &r.Payload, &r.Headers,
			&exchange, &routingKey, &r.Status, &r.RetryCount, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("outbox oracle: scan failed: %w", err)
		}
		r.Exchange = exchange.String
		r.RoutingKey = routingKey.String
		records = append(records, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox oracle: rows iteration failed: %w", err)
	}

	return records, nil
}

func (s *oracleStore) MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET status = :1, published_at = :2 WHERE id = :3`,
		s.tableName,
	)

	_, err := db.Exec(ctx, query, StatusPublished, time.Now(), eventID)
	if err != nil {
		return fmt.Errorf("outbox oracle: mark published failed: %w", err)
	}

	return nil
}

func (s *oracleStore) MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET retry_count = retry_count + 1, error_msg = :1 WHERE id = :2`,
		s.tableName,
	)

	_, err := db.Exec(ctx, query, errMsg, eventID)
	if err != nil {
		return fmt.Errorf("outbox oracle: mark failed failed: %w", err)
	}

	return nil
}

func (s *oracleStore) MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	// Oracle's error column is error_msg (a reserved-word rename), not error.
	query := fmt.Sprintf(
		`UPDATE %s SET retry_count = retry_count + 1, status = :1, error_msg = :2 WHERE id = :3`,
		s.tableName,
	)

	_, err := db.Exec(ctx, query, StatusFailed, errMsg, eventID)
	if err != nil {
		return fmt.Errorf("outbox oracle: mark dead-lettered failed: %w", err)
	}

	return nil
}

func (s *oracleStore) DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE status = :1 AND published_at < :2`,
		s.tableName,
	)

	result, err := db.Exec(ctx, query, StatusPublished, before)
	if err != nil {
		return 0, fmt.Errorf("outbox oracle: delete published failed: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("outbox oracle: rows affected failed: %w", err)
	}

	return count, nil
}

// CreateTable creates the outbox table and indexes in Oracle.
// Unlike PostgreSQL (which uses IF NOT EXISTS), Oracle DDL will return ORA-00955
// if the table or indexes already exist. The caller (ensureStoreInitialized) treats
// all CreateTable errors as warnings, so existing objects are handled gracefully.
func (s *oracleStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	idxBase := sqlid.IndexBaseName(s.tableName)

	// Create table (ORA-00955 if already exists — handled by caller as warning)
	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreateTableSQL, s.tableName)); err != nil {
		return fmt.Errorf("outbox oracle: create table failed: %w", err)
	}

	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreatePendingIndexSQL, idxBase, s.tableName)); err != nil {
		return fmt.Errorf("outbox oracle: create pending index failed: %w", err)
	}

	if _, err := db.Exec(ctx, fmt.Sprintf(oracleCreatePublishedIndexSQL, idxBase, s.tableName)); err != nil {
		return fmt.Errorf("outbox oracle: create published index failed: %w", err)
	}

	return nil
}
