package inbox

import (
	"context"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

// PostgreSQL DDL for the hold's two tables and their indexes. The row table
// keys on the delivery's identity; the tenant table carries one row per held
// tenant, which is what the drain leases and what a runner's held set reads.
const (
	postgresCreateHoldTableSQL = `
CREATE TABLE IF NOT EXISTS %s (
    consumer      VARCHAR(255) NOT NULL,
    stream        VARCHAR(255) NOT NULL,
    stream_offset BIGINT       NOT NULL,
    tenant_id     VARCHAR(255) NOT NULL,
    data          BYTEA,
    properties    BYTEA,
    held_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (consumer, stream, stream_offset)
)`

	postgresCreateHoldTenantTableSQL = `
CREATE TABLE IF NOT EXISTS %s (
    consumer        VARCHAR(255) NOT NULL,
    tenant_id       VARCHAR(255) NOT NULL,
    held_since      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    attempts        INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    lease_owner     VARCHAR(255),
    lease_until     TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (consumer, tenant_id)
)`

	postgresCreateHoldOrderIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_tenant_order ON %s (consumer, tenant_id, stream, stream_offset)`

	postgresCreateHoldDueIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_%s_tenant_due ON %s (consumer, next_attempt_at)`
)

// postgresHoldStore implements HoldStore for PostgreSQL.
type postgresHoldStore struct {
	table       string
	tenantTable string
}

// NewPostgresHoldStore creates a PostgreSQL hold store, refusing a table name
// whose derived names would not fit.
func NewPostgresHoldStore(tableName string) (HoldStore, error) {
	if err := validateHoldTableName(tableName); err != nil {
		return nil, err
	}
	return &postgresHoldStore{
		table:       tableName,
		tenantTable: tableName + holdTenantTableSuffix,
	}, nil
}

// Park writes the row and marks the tenant held in one transaction (the caller's).
// Both statements are idempotent: a redelivery of a parked offset inserts
// nothing, and the tenant marker is upserted rather than inserted so a row
// arriving just after the drain released that tenant holds it again.
func (s *postgresHoldStore) Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (bool, error) {
	insert := fmt.Sprintf(
		`INSERT INTO %s (consumer, stream, stream_offset, tenant_id, data, properties, held_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW())
		 ON CONFLICT (consumer, stream, stream_offset) DO NOTHING`,
		s.table,
	)
	res, err := tx.Exec(ctx, insert, row.Consumer, row.Stream, row.Offset, row.TenantID, row.Data, row.Properties)
	if err != nil {
		return false, fmt.Errorf("inbox postgres: park row failed: %w", err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inbox postgres: rows affected failed: %w", err)
	}

	// The marker is upserted in the SAME transaction as the row, so a drain pass
	// that released this tenant between the two writes cannot leave a row behind
	// with nothing holding it.
	upsert := fmt.Sprintf(
		`INSERT INTO %s (consumer, tenant_id, held_since, attempts, next_attempt_at)
		 VALUES ($1, $2, NOW(), 0, NOW())
		 ON CONFLICT (consumer, tenant_id) DO NOTHING`,
		s.tenantTable,
	)
	if _, err := tx.Exec(ctx, upsert, row.Consumer, row.TenantID); err != nil {
		return false, fmt.Errorf("inbox postgres: mark tenant held failed: %w", err)
	}
	return inserted == 1, nil
}

func (s *postgresHoldStore) HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error) {
	query := fmt.Sprintf(`SELECT tenant_id FROM %s WHERE consumer = $1`, s.tenantTable)
	return scanTenantIDs(ctx, db, query, consumer)
}

func (s *postgresHoldStore) ListTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]HoldTenant, error) {
	query := fmt.Sprintf(
		`SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, COALESCE(last_error, '')
		 FROM %s WHERE consumer = $1 ORDER BY held_since`,
		s.tenantTable,
	)
	return scanHoldTenants(ctx, db, "list tenants", query, consumer)
}

func (s *postgresHoldStore) DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error) {
	query := fmt.Sprintf(
		`SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, COALESCE(last_error, '')
		 FROM %s
		 WHERE consumer = $1 AND next_attempt_at <= NOW() AND (lease_until IS NULL OR lease_until < NOW())
		 ORDER BY held_since LIMIT $2`,
		s.tenantTable,
	)
	return scanHoldTenants(ctx, db, "list due tenants", query, consumer, limit)
}

// AcquireLease takes the lease when it is free or already this owner's. The
// lease_until comparison is the database's own clock, so replicas need not agree
// on the time, only on the row.
func (s *postgresHoldStore) AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error) {
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = $1, lease_until = NOW() + ($2 * INTERVAL '1 second')
		 WHERE consumer = $3 AND tenant_id = $4
		   AND (lease_until IS NULL OR lease_until < NOW() OR lease_owner = $1)`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "acquire lease", query, owner, lease.Seconds(), consumer, tenant)
}

func (s *postgresHoldStore) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = NULL, lease_until = NULL
		 WHERE consumer = $1 AND tenant_id = $2 AND lease_owner = $3`,
		s.tenantTable,
	)
	if _, err := db.Exec(ctx, query, consumer, tenant, owner); err != nil {
		return fmt.Errorf("inbox postgres: release lease failed: %w", err)
	}
	return nil
}

func (s *postgresHoldStore) NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error) {
	query := fmt.Sprintf(
		`SELECT consumer, stream, stream_offset, tenant_id, data, properties, held_at
		 FROM %s WHERE consumer = $1 AND tenant_id = $2
		 ORDER BY stream, stream_offset LIMIT $3`,
		s.table,
	)
	return scanHoldRows(ctx, db, query, consumer, tenant, limit)
}

// DeleteRow removes a replayed row only while this owner still holds the lease.
// A zero-row result is lease loss, not a missing row: another drainer may already
// be replaying the tenant, and deleting under it would lose the message.
func (s *postgresHoldStore) DeleteRow(ctx context.Context, db dbtypes.Interface, consumer, stream string, offset int64, tenant, owner string) (bool, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE consumer = $1 AND stream = $2 AND stream_offset = $3
		   AND EXISTS (SELECT 1 FROM %s t WHERE t.consumer = $1 AND t.tenant_id = $4
		               AND t.lease_owner = $5 AND t.lease_until > NOW())`,
		s.table, s.tenantTable,
	)
	return affectedOne(ctx, db, "delete held row", query, consumer, stream, offset, tenant, owner)
}

func (s *postgresHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (bool, error) {
	query := fmt.Sprintf(
		`UPDATE %s SET attempts = attempts + 1,
		        next_attempt_at = NOW() + ($1 * INTERVAL '1 second'),
		        last_error = $2, lease_owner = NULL, lease_until = NULL
		 WHERE consumer = $3 AND tenant_id = $4 AND lease_owner = $5 AND lease_until > NOW()`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "defer tenant", query, backoff.Seconds(), ledgererr.Bound(lastErr), consumer, tenant, owner)
}

// Release drops the tenant's marker once its last row is gone, under the lease.
// The NOT EXISTS keeps a tenant held whose rows a concurrent park just added.
func (s *postgresHoldStore) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE consumer = $1 AND tenant_id = $2
		   AND lease_owner = $3 AND lease_until > NOW()
		   AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.consumer = $1 AND r.tenant_id = $2)`,
		s.tenantTable, s.table,
	)
	return affectedOne(ctx, db, "release tenant", query, consumer, tenant, owner)
}

func (s *postgresHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
	query := fmt.Sprintf(
		`SELECT (SELECT COUNT(*) FROM %s WHERE consumer = $1),
		        (SELECT COUNT(*) FROM %s WHERE consumer = $1),
		        (SELECT COALESCE(MIN(held_since), NOW()) FROM %s WHERE consumer = $1)`,
		s.tenantTable, s.table, s.tenantTable,
	)
	return scanHoldStats(ctx, db, query, consumer)
}

func (s *postgresHoldStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	statements := []string{
		fmt.Sprintf(postgresCreateHoldTableSQL, s.table),
		fmt.Sprintf(postgresCreateHoldTenantTableSQL, s.tenantTable),
		fmt.Sprintf(postgresCreateHoldOrderIndexSQL, s.table, s.table),
		fmt.Sprintf(postgresCreateHoldDueIndexSQL, s.table, s.tenantTable),
	}
	for _, stmt := range statements {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("inbox postgres: create hold table failed: %w", err)
		}
	}
	return nil
}
