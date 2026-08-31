package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	oranet "github.com/sijms/go-ora/v2/network"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

// Oracle DDL for the hold's two tables and their indexes. VARCHAR2 rather than
// VARCHAR, SYSTIMESTAMP rather than NOW(), named constraints, and no
// IF NOT EXISTS — ORA-00955 on an existing object is tolerated by the caller,
// which treats CreateTable errors as warnings.
// oracleObjectExistsCode is ORA-00955, "name is already used by an existing
// object" — what this DDL raises for anything a previous run created.
const oracleObjectExistsCode = 955

const (
	oracleCreateHoldTableSQL = `
CREATE TABLE %s (
    consumer      VARCHAR2(255) NOT NULL,
    stream        VARCHAR2(255) NOT NULL,
    stream_offset NUMBER(19)    NOT NULL,
    tenant_id     VARCHAR2(255) NOT NULL,
    data          BLOB,
    properties    BLOB,
    held_at       TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    CONSTRAINT pk_%s PRIMARY KEY (consumer, stream, stream_offset)
)`

	oracleCreateHoldTenantTableSQL = `
CREATE TABLE %s (
    consumer        VARCHAR2(255) NOT NULL,
    tenant_id       VARCHAR2(255) NOT NULL,
    held_since      TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    attempts        NUMBER(10)    DEFAULT 0 NOT NULL,
    next_attempt_at TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    last_error      CLOB,
    lease_owner     VARCHAR2(255),
    lease_until     TIMESTAMP WITH TIME ZONE,
    CONSTRAINT pk_%s PRIMARY KEY (consumer, tenant_id)
)`

	oracleCreateHoldOrderIndexSQL = `
CREATE INDEX idx_%s_tenant_order ON %s (consumer, tenant_id, stream, stream_offset)`

	oracleCreateHoldDueIndexSQL = `
CREATE INDEX idx_%s_tenant_due ON %s (consumer, next_attempt_at)`
)

// oracleHoldStore implements HoldStore for Oracle using :N placeholders.
type oracleHoldStore struct {
	table       string
	tenantTable string
	qb          *database.QueryBuilder
	// holdQueries carries every statement the query builder renders identically on
	// both vendors. What stays in this file is the SQL the builder cannot express,
	// and the dialect those statements answer in.
	holdQueries
}

// NewOracleHoldStore creates an Oracle hold store, refusing a table name whose
// derived names would not fit.
func NewOracleHoldStore(tableName string) (HoldStore, error) {
	if err := validateHoldTableName(tableName); err != nil {
		return nil, err
	}
	qb := database.NewQueryBuilder(dbtypes.Oracle)
	return &oracleHoldStore{
		table:       tableName,
		tenantTable: tableName + holdTenantTableSuffix,
		qb:          qb,
		holdQueries: holdQueries{
			qb:          qb,
			vendor:      "oracle",
			table:       tableName,
			tenantTable: tableName + holdTenantTableSuffix,
			now:         "SYSTIMESTAMP",
			noError:     "NVL(last_error, ' ')",
		},
	}, nil
}

// Park writes the row and marks the tenant held in the caller's transaction.
// Oracle has no ON CONFLICT: a unique violation is statement-level and leaves the
// transaction usable, so a redelivery of a parked offset is detected by catching
// ORA-00001 rather than by asking the statement to ignore it.
func (s *oracleHoldStore) Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (bool, error) {
	if err := validateHoldRow(row); err != nil {
		return false, err
	}

	// The marker FIRST, and locked for the rest of this transaction, so a drain
	// deciding to release this tenant waits until the row below is committed and
	// visible to its no-rows-remain check. Oracle has no upsert, so an existing
	// marker is locked with a SELECT ... FOR UPDATE and a missing one is inserted.
	if err := s.holdTenantMarker(ctx, tx, row); err != nil {
		return false, err
	}

	insert := fmt.Sprintf(
		`INSERT INTO %s (consumer, stream, stream_offset, tenant_id, data, properties, held_at)
		 VALUES (:1, :2, :3, :4, :5, :6, SYSTIMESTAMP)`,
		s.table,
	)
	inserted := true
	if _, err := tx.Exec(ctx, insert, row.Consumer, row.Stream, row.Offset, row.TenantID, row.Data, row.Properties); err != nil {
		if !database.IsUniqueViolation(err) {
			return false, fmt.Errorf("inbox oracle: park row failed: %w", err)
		}
		inserted = false
	}

	return inserted, nil
}

// holdTenantMarker holds this tenant, locking the marker when it already exists so
// a concurrent release cannot delete it while the caller's row is still uncommitted.
func (s *oracleHoldStore) holdTenantMarker(ctx context.Context, tx dbtypes.Tx, row *HoldRow) error {
	lock := fmt.Sprintf(
		`SELECT tenant_id FROM %s WHERE consumer = :1 AND tenant_id = :2 FOR UPDATE`,
		s.tenantTable,
	)
	insert := fmt.Sprintf(
		`INSERT INTO %s (consumer, tenant_id, held_since, attempts, next_attempt_at)
		 VALUES (:1, :2, SYSTIMESTAMP, 0, SYSTIMESTAMP)`,
		s.tenantTable,
	)

	// Two passes at most: the marker is there and we lock it, or it is not and we
	// insert it, or someone inserted it between the two and the second pass locks
	// theirs. Losing that race is not enough on its own — the winner's marker is a
	// row this transaction holds no lock on, and a release could delete it between
	// here and the held row's write, which is the race the marker-first order
	// exists to prevent.
	for range 2 {
		locked, err := markerLocked(ctx, tx, lock, row.Consumer, row.TenantID)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}

		_, err = tx.Exec(ctx, insert, row.Consumer, row.TenantID)
		if err == nil {
			return nil
		}
		if !database.IsUniqueViolation(err) {
			return fmt.Errorf("inbox oracle: mark tenant held failed: %w", err)
		}
	}

	return fmt.Errorf("inbox oracle: mark tenant held failed: tenant %q's marker vanished between the lock and the insert twice",
		row.TenantID)
}

func (s *oracleHoldStore) AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error) {
	// SECURITY: Manual SQL review completed - the only interpolated identifier is
	// the tenant table from the constructor-validated config; every value is bound.
	// Raw because the SET side assigns an EXPRESSION over the database clock,
	// which the builder's Set carries only as a bound value.
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = :1, lease_until = SYSTIMESTAMP + NUMTODSINTERVAL(:2, 'SECOND')
		 WHERE consumer = :3 AND tenant_id = :4
		   AND (lease_until IS NULL OR lease_until < SYSTIMESTAMP OR lease_owner = :1)`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "acquire lease", query, owner, lease.Seconds(), consumer, tenant)
}

func (s *oracleHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (bool, error) {
	// SECURITY: Manual SQL review completed - identifier from constructor-validated
	// config; the backoff, bounded error text, consumer, tenant and owner are bound.
	// Raw because the SET side increments a column and assigns a clock expression.
	query := fmt.Sprintf(
		`UPDATE %s SET attempts = attempts + 1,
		        next_attempt_at = SYSTIMESTAMP + NUMTODSINTERVAL(:1, 'SECOND'),
		        last_error = :2, lease_owner = NULL, lease_until = NULL
		 WHERE consumer = :3 AND tenant_id = :4 AND lease_owner = :5 AND lease_until > SYSTIMESTAMP`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "defer tenant", query, backoff.Seconds(), ledgererr.Bound(lastErr), consumer, tenant, owner)
}

func (s *oracleHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
	// SECURITY: Manual SQL review completed - both table names come from the
	// constructor-validated config, the consumer is bound three times. Raw because
	// this is three aggregate subqueries in one projection over dual, which the
	// builder's Select cannot compose.
	query := fmt.Sprintf(
		`SELECT (SELECT COUNT(*) FROM %s WHERE consumer = :1),
		        (SELECT COUNT(*) FROM %s WHERE consumer = :1),
		        (SELECT MIN(held_since) FROM %s WHERE consumer = :1)
		 FROM dual`,
		s.tenantTable, s.table, s.tenantTable,
	)
	return scanHoldStats(ctx, db, query, consumer)
}

func (s *oracleHoldStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	statements := []string{
		fmt.Sprintf(oracleCreateHoldTableSQL, s.table, s.table),
		fmt.Sprintf(oracleCreateHoldTenantTableSQL, s.tenantTable, s.tenantTable),
		fmt.Sprintf(oracleCreateHoldOrderIndexSQL, s.table, s.table),
		fmt.Sprintf(oracleCreateHoldDueIndexSQL, s.table, s.tenantTable),
	}
	// Every statement is attempted. Oracle has no IF NOT EXISTS, so an object that
	// already exists raises ORA-00955 — and returning on the first of those would
	// skip the tenant table and BOTH indexes whenever the row table happened to
	// exist, which the startup probe cannot catch: it reads a table, never an
	// index. Errors are collected instead, so a genuine failure still surfaces.
	var errs []error
	for _, stmt := range statements {
		if _, err := db.Exec(ctx, stmt); err != nil && !isOracleObjectExists(err) {
			errs = append(errs, fmt.Errorf("inbox oracle: create hold table failed: %w", err))
		}
	}
	return errors.Join(errs...)
}

// isOracleObjectExists reports whether err is Oracle's "name is already used by
// an existing object" (ORA-00955), which is what a re-run of this DDL raises for
// anything already created.
func isOracleObjectExists(err error) bool {
	var oraErr *oranet.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == oracleObjectExistsCode
	}
	return false
}

// markerLocked runs the FOR UPDATE probe and reports whether it found the marker.
// Its own function so the rows are closed and their error checked on one path,
// whatever the caller does next.
func markerLocked(ctx context.Context, tx dbtypes.Tx, query string, args ...any) (found bool, err error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("inbox oracle: lock tenant marker failed: %w", err)
	}

	// Deferred, and JOINED into the result rather than ranked behind a condition:
	// errors.Join drops nils, so a close failure surfaces whether or not the read
	// already failed, and there is no guard whose arms a test cannot tell apart.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("inbox oracle: close tenant marker probe failed: %w", closeErr))
		}
	}()

	found = rows.Next()
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, fmt.Errorf("inbox oracle: lock tenant marker failed: %w", rowsErr)
	}
	return found, nil
}
