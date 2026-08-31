package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

// PostgreSQL DDL for the hold's two tables and their indexes. The row table
// keys on the delivery's identity; the tenant table carries one row per held
// tenant, which is what the drain leases and what a runner's held set reads.
// postgresNoError renders an absent last_error as the empty string.
//
// SECURITY: Manual SQL review completed - a constant expression over a fixed
// column name, no interpolation and no caller input; it is an Expr only because
// the builder's projection accepts identifiers alone.
func (s *postgresHoldStore) noError() (dbtypes.RawExpression, error) {
	return s.qb.Expr(`COALESCE(` + colLastError + `, '')`)
}

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
		qb:          database.NewQueryBuilder(dbtypes.PostgreSQL),
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

func (s *postgresHoldStore) HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error) {
	f := s.qb.Filter()
	query, args, err := s.qb.Select(colTenantID).From(s.tenantTable).
		Where(f.Eq(colConsumer, consumer)).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build held tenants query failed: %w", err)
	}
	return scanTenantIDs(ctx, db, query, args...)
}

func (s *postgresHoldStore) ListTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]HoldTenant, error) {
	noError, err := s.noError()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build list tenants query failed: %w", err)
	}

	f := s.qb.Filter()
	query, args, err := s.qb.Select(holdTenantColumns(noError)...).From(s.tenantTable).
		Where(f.Eq(colConsumer, consumer)).
		OrderBy(colHeldSince).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build list tenants query failed: %w", err)
	}
	return scanHoldTenants(ctx, db, "list tenants", query, args...)
}

func (s *postgresHoldStore) DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error) {
	noError, err := s.noError()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build due tenants query failed: %w", err)
	}

	f := s.qb.Filter()
	// SECURITY: Manual SQL review completed - both predicates are constant text
	// over fixed column names and the database's own clock: no identifier is
	// interpolated, no caller value is concatenated, and the only bound value is
	// the consumer, which the typed Eq carries.
	due := f.Raw(colNextAttemptAt + ` <= NOW()`)
	free := f.Raw(`(` + colLeaseUntil + ` IS NULL OR ` + colLeaseUntil + ` < NOW())`)

	query, args, err := s.qb.Select(holdTenantColumns(noError)...).From(s.tenantTable).
		Where(f.And(f.Eq(colConsumer, consumer), due, free)).
		OrderBy(colHeldSince).
		Limit(uint64(limit)).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build due tenants query failed: %w", err)
	}
	return scanHoldTenants(ctx, db, "list due tenants", query, args...)
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

func (s *postgresHoldStore) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	f := s.qb.Filter()
	query, args, err := s.qb.Update(s.tenantTable).
		SetMap(map[string]any{colLeaseOwner: nil, colLeaseUntil: nil}).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant), f.Eq(colLeaseOwner, owner))).
		ToSQL()
	if err != nil {
		return fmt.Errorf("inbox postgres: build release lease query failed: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("inbox postgres: release lease failed: %w", err)
	}
	return nil
}

func (s *postgresHoldStore) NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error) {
	f := s.qb.Filter()
	query, args, err := s.qb.Select(holdRowColumns()...).From(s.table).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant))).
		OrderBy(colStream, colStreamOffset).
		Limit(uint64(limit)).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("inbox postgres: build next rows query failed: %w", err)
	}
	return scanHoldRows(ctx, db, query, args...)
}

// DeleteRow removes a replayed row only while this owner still holds the lease.
// A zero-row result is lease loss, not a missing row: another drainer may already
// be replaying the tenant, and deleting under it would lose the message.
func (s *postgresHoldStore) DeleteRow(ctx context.Context, db dbtypes.Interface, consumer, stream string, offset int64, tenant, owner string) (bool, error) {
	f := s.qb.Filter()
	// The fence is a subquery over the tenant table, in the SAME statement as the
	// delete: checking the lease first and deleting second would reopen the window
	// the lease exists to close.
	// SECURITY: Manual SQL review completed - the literal 1 is a constant
	// projection; EXISTS ignores it, and the builder accepts only identifiers in a
	// projection, so it arrives as an Expr.
	one, err := s.qb.Expr("1")
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build delete held row query failed: %w", err)
	}

	lease := s.qb.Select(one).From(s.tenantTable).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			f.Eq(colLeaseOwner, owner),
			// SECURITY: Manual SQL review completed - constant text over a fixed column
			// and the database's own clock; no identifier interpolated, no value
			// concatenated.
			f.Raw(colLeaseUntil+` > NOW()`),
		))

	query, args, err := s.qb.Delete(s.table).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colStream, stream),
			f.Eq(colStreamOffset, offset),
			f.Exists(lease),
		)).
		ToSQL()
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build delete held row query failed: %w", err)
	}
	return affectedOne(ctx, db, "delete held row", query, args...)
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

// Release drops the tenant's marker once its last row is gone, under the lease.
// The NOT EXISTS keeps a tenant held whose rows a concurrent park just added.
func (s *postgresHoldStore) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	f := s.qb.Filter()
	// SECURITY: Manual SQL review completed - the literal 1 is a constant
	// projection EXISTS ignores; the clock predicate is constant text over a fixed
	// column. No identifier interpolated, no value concatenated.
	one, err := s.qb.Expr("1")
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build release tenant query failed: %w", err)
	}

	// One statement, again on purpose: a tenant whose rows a concurrent park just
	// added must stay held, and checking then deleting would let that park land in
	// between.
	rowsRemain := s.qb.Select(one).From(s.table).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant)))

	query, args, err := s.qb.Delete(s.tenantTable).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			f.Eq(colLeaseOwner, owner),
			f.Raw(colLeaseUntil+` > NOW()`),
			f.NotExists(rowsRemain),
		)).
		ToSQL()
	if err != nil {
		return false, fmt.Errorf("inbox postgres: build release tenant query failed: %w", err)
	}
	return affectedOne(ctx, db, "release tenant", query, args...)
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
