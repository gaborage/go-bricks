package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

// Oracle DDL for the hold's two tables and their indexes. VARCHAR2 rather than
// VARCHAR, SYSTIMESTAMP rather than NOW(), named constraints, and no
// IF NOT EXISTS — ORA-00955 on an existing object is tolerated by the caller,
// which treats CreateTable errors as warnings.
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

// errHoldTenantRequired is returned for a row with no tenant. A hold is keyed by
// the tenant, so there is nothing to park a tenant-less delivery under — the lane
// skips such a delivery rather than parking it, and this is the store's backstop
// for that invariant. It also keeps Oracle's empty-string-is-NULL rule off the
// hold: the column is never handed one.
var errHoldTenantRequired = errors.New("inbox oracle: a held row requires a tenant")

// oracleHoldStore implements HoldStore for Oracle using :N placeholders.
type oracleHoldStore struct {
	table       string
	tenantTable string
}

// NewOracleHoldStore creates an Oracle hold store, refusing a table name whose
// derived names would not fit.
func NewOracleHoldStore(tableName string) (HoldStore, error) {
	if err := validateHoldTableName(tableName); err != nil {
		return nil, err
	}
	return &oracleHoldStore{
		table:       tableName,
		tenantTable: tableName + holdTenantTableSuffix,
	}, nil
}

// Park writes the row and marks the tenant held in the caller's transaction.
// Oracle has no ON CONFLICT: a unique violation is statement-level and leaves the
// transaction usable, so a redelivery of a parked offset is detected by catching
// ORA-00001 rather than by asking the statement to ignore it.
func (s *oracleHoldStore) Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (bool, error) {
	if row.TenantID == "" {
		return false, errHoldTenantRequired
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
	locked, err := markerLocked(ctx, tx, lock, row.Consumer, row.TenantID)
	if err != nil {
		return err
	}
	if locked {
		return nil
	}

	insert := fmt.Sprintf(
		`INSERT INTO %s (consumer, tenant_id, held_since, attempts, next_attempt_at)
		 VALUES (:1, :2, SYSTIMESTAMP, 0, SYSTIMESTAMP)`,
		s.tenantTable,
	)
	if _, err := tx.Exec(ctx, insert, row.Consumer, row.TenantID); err != nil && !database.IsUniqueViolation(err) {
		return fmt.Errorf("inbox oracle: mark tenant held failed: %w", err)
	}
	return nil
}

func (s *oracleHoldStore) HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error) {
	query := fmt.Sprintf(`SELECT tenant_id FROM %s WHERE consumer = :1`, s.tenantTable)
	return scanTenantIDs(ctx, db, query, consumer)
}

func (s *oracleHoldStore) ListTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]HoldTenant, error) {
	query := fmt.Sprintf(
		`SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, NVL(last_error, ' ')
		 FROM %s WHERE consumer = :1 ORDER BY held_since`,
		s.tenantTable,
	)
	return scanHoldTenants(ctx, db, "list tenants", query, consumer)
}

func (s *oracleHoldStore) DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error) {
	query := fmt.Sprintf(
		`SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, NVL(last_error, ' ')
		 FROM %s
		 WHERE consumer = :1 AND next_attempt_at <= SYSTIMESTAMP
		   AND (lease_until IS NULL OR lease_until < SYSTIMESTAMP)
		 ORDER BY held_since FETCH FIRST :2 ROWS ONLY`,
		s.tenantTable,
	)
	return scanHoldTenants(ctx, db, "list due tenants", query, consumer, limit)
}

func (s *oracleHoldStore) AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error) {
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = :1, lease_until = SYSTIMESTAMP + NUMTODSINTERVAL(:2, 'SECOND')
		 WHERE consumer = :3 AND tenant_id = :4
		   AND (lease_until IS NULL OR lease_until < SYSTIMESTAMP OR lease_owner = :1)`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "acquire lease", query, owner, lease.Seconds(), consumer, tenant)
}

func (s *oracleHoldStore) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	query := fmt.Sprintf(
		`UPDATE %s SET lease_owner = NULL, lease_until = NULL
		 WHERE consumer = :1 AND tenant_id = :2 AND lease_owner = :3`,
		s.tenantTable,
	)
	if _, err := db.Exec(ctx, query, consumer, tenant, owner); err != nil {
		return fmt.Errorf("inbox oracle: release lease failed: %w", err)
	}
	return nil
}

func (s *oracleHoldStore) NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error) {
	query := fmt.Sprintf(
		`SELECT consumer, stream, stream_offset, tenant_id, data, properties, held_at
		 FROM %s WHERE consumer = :1 AND tenant_id = :2
		 ORDER BY stream, stream_offset FETCH FIRST :3 ROWS ONLY`,
		s.table,
	)
	return scanHoldRows(ctx, db, query, consumer, tenant, limit)
}

func (s *oracleHoldStore) DeleteRow(ctx context.Context, db dbtypes.Interface, consumer, stream string, offset int64, tenant, owner string) (bool, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE consumer = :1 AND stream = :2 AND stream_offset = :3
		   AND EXISTS (SELECT 1 FROM %s t WHERE t.consumer = :1 AND t.tenant_id = :4
		               AND t.lease_owner = :5 AND t.lease_until > SYSTIMESTAMP)`,
		s.table, s.tenantTable,
	)
	return affectedOne(ctx, db, "delete held row", query, consumer, stream, offset, tenant, owner)
}

func (s *oracleHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (bool, error) {
	query := fmt.Sprintf(
		`UPDATE %s SET attempts = attempts + 1,
		        next_attempt_at = SYSTIMESTAMP + NUMTODSINTERVAL(:1, 'SECOND'),
		        last_error = :2, lease_owner = NULL, lease_until = NULL
		 WHERE consumer = :3 AND tenant_id = :4 AND lease_owner = :5 AND lease_until > SYSTIMESTAMP`,
		s.tenantTable,
	)
	return affectedOne(ctx, db, "defer tenant", query, backoff.Seconds(), ledgererr.Bound(lastErr), consumer, tenant, owner)
}

func (s *oracleHoldStore) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE consumer = :1 AND tenant_id = :2
		   AND lease_owner = :3 AND lease_until > SYSTIMESTAMP
		   AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.consumer = :1 AND r.tenant_id = :2)`,
		s.tenantTable, s.table,
	)
	return affectedOne(ctx, db, "release tenant", query, consumer, tenant, owner)
}

func (s *oracleHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
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
	for _, stmt := range statements {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("inbox oracle: create hold table failed: %w", err)
		}
	}
	return nil
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
