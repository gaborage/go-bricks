package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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
	// holdQueries carries every statement the query builder renders for both
	// vendors; this file supplies the vendor's clock spelling, its DDL, and the
	// one statement that stays raw by design.
	holdQueries
}

// pgNow is the PostgreSQL clock the hold columns hold.
const pgNow = "NOW()"

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
			qb:             qb,
			vendor:         "postgres",
			table:          tableName,
			tenantTable:    tableName + holdTenantTableSuffix,
			now:            pgNow,
			secondsFromNow: pgNow + " + (? * INTERVAL '1 second')",
			noError:        "COALESCE(last_error, '')",
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
	return execAffectedOne(ctx, db, "acquire lease", s.acquireLease(consumer, tenant, owner, lease))
}

func (s *postgresHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (bool, error) {
	return execAffectedOne(ctx, db, "defer tenant", s.deferTenant(consumer, tenant, owner, backoff, lastErr))
}

func (s *postgresHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
	query, args, err := s.stats(consumer).ToSQL()
	if err != nil {
		return HoldStats{}, fmt.Errorf("inbox hold: build stats failed: %w", err)
	}
	return scanHoldStats(ctx, db, query, args...)
}

// CreateTable runs the DDL, which stays hand-written: the builder has no DDL door.
//
// SECURITY: Manual SQL review completed - static DDL constants; only the constructor-validated
// hold table name (validateHoldTableName) and its derived tenant table are interpolated
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
