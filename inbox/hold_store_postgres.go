package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

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

// postgresHoldStore implements HoldStore for PostgreSQL. Statements are built
// through the query builder, which renders this vendor's placeholders; the two
// table names are the constructor's own validated values.
type postgresHoldStore struct {
	table       string
	tenantTable string
	qb          *database.QueryBuilder
	// holdQueries carries every statement the query builder renders identically on
	// both vendors. What stays in this file is the SQL the builder cannot express,
	// and the dialect those statements answer in.
	holdQueries
}

// NewPostgresHoldStore creates a PostgreSQL hold store, refusing a table name
// whose derived names would not fit.
func NewPostgresHoldStore(tableName string) (HoldStore, error) {
	if err := validateHoldTableName(tableName); err != nil {
		return nil, err
	}
	qb := database.NewQueryBuilder(dbtypes.PostgreSQL)
	return &postgresHoldStore{
		table:       tableName,
		tenantTable: tableName + holdTenantTableSuffix,
		qb:          qb,
		holdQueries: holdQueries{
			qb:          qb,
			vendor:      "postgres",
			table:       tableName,
			tenantTable: tableName + holdTenantTableSuffix,
			now:         "NOW()",
			noError:     "COALESCE(last_error, '')",
		},
	}, nil
}

// Park writes the row and marks the tenant held in one transaction (the caller's).
// Both statements are idempotent: a redelivery of a parked offset inserts
// nothing, and the tenant marker is upserted rather than inserted so a row
// arriving just after the drain released that tenant holds it again.
func (s *postgresHoldStore) Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (bool, error) {
	if err := validateHoldRow(row); err != nil {
		return false, err
	}

	// The marker FIRST, and with a DO UPDATE that touches the row: an existing
	// marker is locked by it for the rest of this transaction, so a drain deciding
	// to release this tenant blocks until the row below is committed and its
	// no-rows-remain check sees it. Inserting the row first would let a release
	// commit in between, leaving a held row with nothing holding its tenant.
	//
	// SECURITY: Manual SQL review completed - the only interpolated identifier is
	// the tenant table from the constructor-validated config; the consumer and the
	// tenant are bound. Raw because the lock is a DO UPDATE that touches a CONFLICT
	// column, which BuildUpsert refuses on every vendor for Oracle MERGE parity
	// (ORA-38104) — and every other column here would change data rather than take
	// a lock. held_since, attempts and next_attempt_at take the DDL's defaults.
	marker := fmt.Sprintf(
		`INSERT INTO %s (consumer, tenant_id) VALUES ($1, $2)
		 ON CONFLICT (consumer, tenant_id) DO UPDATE SET consumer = EXCLUDED.consumer`,
		s.tenantTable,
	)
	if _, err := tx.Exec(ctx, marker, row.Consumer, row.TenantID); err != nil {
		return false, fmt.Errorf("inbox postgres: mark tenant held failed: %w", err)
	}

	// held_at takes its column default too.
	insert, insertArgs, err := s.qb.BuildUpsert(s.table,
		[]string{colConsumer, colStream, colStreamOffset},
		map[string]any{
			colConsumer: row.Consumer, colStream: row.Stream, colStreamOffset: row.Offset,
			colTenantID: row.TenantID, colData: row.Data, colProperties: row.Properties,
		},
		// No update columns: a redelivered offset is already parked, so the insert
		// does nothing rather than rewriting it.
		nil,
	)
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build park row failed: %w", err)
	}
	res, err := tx.Exec(ctx, insert, insertArgs...)
	if err != nil {
		return false, fmt.Errorf("inbox postgres: park row failed: %w", err)
	}

	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inbox postgres: rows affected failed: %w", err)
	}
	return inserted == 1, nil
}

// AcquireLease takes the lease when it is free or already this owner's. The
// lease_until comparison is the database's own clock, so replicas need not agree
// on the time, only on the row.
func (s *postgresHoldStore) AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error) {
	// SECURITY: Manual SQL review completed - the only interpolated identifier is
	// the tenant table from the constructor-validated config; every value (owner,
	// lease seconds, consumer, tenant) is bound. Raw because the SET side assigns
	// an EXPRESSION over the database clock, which the builder's Set carries only
	// as a bound value — see the migration note in the lane report.
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = $1, lease_until = NOW() + ($2 * INTERVAL '1 second')
		 WHERE consumer = $3 AND tenant_id = $4
		   AND (lease_until IS NULL OR lease_until < NOW() OR lease_owner = $1)`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "acquire lease", query, owner, lease.Seconds(), consumer, tenant)
}

func (s *postgresHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (bool, error) {
	// SECURITY: Manual SQL review completed - the only interpolated identifier is
	// the tenant table from the constructor-validated config; the backoff, the
	// bounded error text, the consumer, the tenant and the owner are all bound.
	// Raw because the SET side both INCREMENTS a column and assigns an expression
	// over the database clock, neither of which the builder's Set can carry.
	query := fmt.Sprintf(
		`UPDATE %s SET attempts = attempts + 1,
		        next_attempt_at = NOW() + ($1 * INTERVAL '1 second'),
		        last_error = $2, lease_owner = NULL, lease_until = NULL
		 WHERE consumer = $3 AND tenant_id = $4 AND lease_owner = $5 AND lease_until > NOW()`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "defer tenant", query, backoff.Seconds(), ledgererr.Bound(lastErr), consumer, tenant, owner)
}

func (s *postgresHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
	// SECURITY: Manual SQL review completed - both table names come from the
	// constructor-validated config and the consumer is bound three times. Raw
	// because this is three correlated aggregate subqueries in one projection,
	// which the builder's Select cannot compose.
	query := fmt.Sprintf(
		`SELECT (SELECT COUNT(*) FROM %s WHERE consumer = $1),
		        (SELECT COUNT(*) FROM %s WHERE consumer = $1),
		        (SELECT MIN(held_since) FROM %s WHERE consumer = $1)`,
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
