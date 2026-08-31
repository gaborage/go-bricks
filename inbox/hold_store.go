package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// holdTenantTableSuffix names the second table, derived from the configured one:
// the rows live in <tablename>, the per-tenant drain state in <tablename>_tenant.
const holdTenantTableSuffix = "_tenant"

// postgresMaxIdentifierLen is PostgreSQL's effective identifier limit. It is the
// binding one across both vendors — Oracle's is 128 — and PostgreSQL TRUNCATES
// past it rather than refusing, which would quietly point two deployments at one
// table.
const postgresMaxIdentifierLen = 63

// holdLongestDerivedAffix is the longest thing appended to the configured name:
// the order index, `idx_<name>_tenant_order`. It is longer than the tenant table's
// own suffix and longer than the `_pkey` PostgreSQL appends to that table, so
// budgeting for it covers every derived name.
const holdLongestDerivedAffix = len("idx_") + len("_tenant_order")

// maxHoldTableNameLen bounds the configured name so EVERY name derived from it
// fits the identifier limit. Budgeting only for the tenant table would leave the
// two index names to truncate — and they share the prefix `idx_<name>_tenant_`,
// so both would truncate to the same identifier, the second CREATE INDEX would
// quietly do nothing, and the drain's due-tenant query would run unindexed.
const maxHoldTableNameLen = postgresMaxIdentifierLen - holdLongestDerivedAffix

// validateHoldTableName checks that name is a safe, unqualified identifier short
// enough for its derived names.
func validateHoldTableName(name string) error {
	if err := validateTableName(name); err != nil {
		return fmt.Errorf("hold: %w", err)
	}
	if len(name) > maxHoldTableNameLen {
		return fmt.Errorf("hold: table name %q is too long: %d bytes, at most %d are allowed so the derived %q table fits",
			name, len(name), maxHoldTableNameLen, name+holdTenantTableSuffix)
	}
	return nil
}

// errHoldTenantRequired is returned for a row with no tenant. A hold is keyed by
// the tenant, so there is nothing to park a tenant-less delivery under — the lane
// skips such a delivery, and this is the store's backstop for that invariant on
// BOTH vendors. It also keeps Oracle's empty-string-is-NULL rule off the hold:
// the column is never handed one.
var errHoldTenantRequired = errors.New("inbox hold: a held row requires a tenant")

// validateHoldRow checks what every vendor's Park needs before it writes.
func validateHoldRow(row *HoldRow) error {
	if row.TenantID == "" {
		return errHoldTenantRequired
	}
	return nil
}

// HoldRow is one parked stream delivery. (Consumer, Stream, Offset) is its
// identity: a partition's offsets are unique within it, and a super stream's
// partition is its own stream.
type HoldRow struct {
	Consumer   string
	Stream     string
	Offset     int64
	TenantID   string
	Data       []byte
	Properties []byte
	HeldAt     time.Time
}

// HoldTenant is one held tenant's drain state.
type HoldTenant struct {
	Consumer      string
	TenantID      string
	HeldSince     time.Time
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
}

// HoldStats is what the gauges report for one consumer.
type HoldStats struct {
	Tenants         int64
	Rows            int64
	OldestHeldSince time.Time
}

// HoldStore is the hold ledger's persistence. Every method takes the resolved
// control-plane database: a hold lives there and nowhere else, because a tenant
// whose own database is down cannot hold its own messages.
//
// Database time is the clock throughout — NOW() on PostgreSQL, SYSTIMESTAMP on
// Oracle — so replicas with skewed clocks agree on when a lease expired and when
// a tenant is due.
type HoldStore interface {
	// Park inserts the row and marks its tenant held, in tx. It is idempotent on
	// the row's identity: a redelivery of an already-parked offset reports
	// inserted=false rather than failing.
	Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (inserted bool, err error)

	// HeldTenants lists the tenants currently held for a consumer.
	HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error)

	// ListTenants returns every held tenant's full drain state, oldest first.
	ListTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]HoldTenant, error)

	// DueTenants lists held tenants whose next attempt has come and whose lease is
	// free, oldest first.
	DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error)

	// AcquireLease takes or renews the drain lease for one tenant, reporting false
	// when another owner holds a live one.
	AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error)

	// ReleaseLease drops a lease this owner holds.
	ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error

	// NextRows returns the tenant's rows in (stream, offset) order.
	NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error)

	// DeleteRow removes one replayed row. Fenced by the lease: a write affecting no
	// rows means the lease was lost, and the caller discards the replay's outcome.
	DeleteRow(ctx context.Context, db dbtypes.Interface, consumer, stream string, offset int64, tenant, owner string) (deleted bool, err error)

	// Defer records a failed replay: one more attempt, the next one backed off,
	// the error bounded, the lease cleared. Fenced by the lease.
	Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (updated bool, err error)

	// Release deletes the tenant's marker, and only when no rows remain. Fenced by
	// the lease.
	Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (released bool, err error)

	// Stats reports what the gauges publish for one consumer.
	Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error)

	// CreateTable creates both tables and their indexes if they do not exist.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}

// scanHoldTenants runs a tenant-shaped query and reads its rows. Both vendors'
// stores select the same six columns in the same order, so the scanning is
// theirs to share and only the SQL differs.
func scanHoldTenants(ctx context.Context, db dbtypes.Interface, what, query string, args ...any) ([]HoldTenant, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox hold: %s failed: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []HoldTenant
	for rows.Next() {
		var t HoldTenant
		if err := rows.Scan(&t.Consumer, &t.TenantID, &t.HeldSince, &t.Attempts, &t.NextAttemptAt, &t.LastError); err != nil {
			return nil, fmt.Errorf("inbox hold: scan tenant failed: %w", err)
		}
		// Oracle folds the empty-string literal to NULL, so its NVL substitutes a
		// space where PostgreSQL's COALESCE substitutes "". Normalizing here keeps
		// the vendor workaround vendor-local: a caller asks whether there is an
		// error, not which database answered.
		t.LastError = strings.TrimSpace(t.LastError)
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox hold: %s failed: %w", what, err)
	}
	return tenants, nil
}

// scanHoldRows runs a row-shaped query and reads its rows. Both vendors select
// the same seven columns in the same order, so only their SQL differs.
func scanHoldRows(ctx context.Context, db dbtypes.Interface, query string, args ...any) ([]HoldRow, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox hold: next rows failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var held []HoldRow
	for rows.Next() {
		var row HoldRow
		if err := rows.Scan(&row.Consumer, &row.Stream, &row.Offset, &row.TenantID, &row.Data, &row.Properties, &row.HeldAt); err != nil {
			return nil, fmt.Errorf("inbox hold: scan held row failed: %w", err)
		}
		held = append(held, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox hold: next rows failed: %w", err)
	}
	return held, nil
}

// scanTenantIDs reads the one-column held-tenant listing a runner's held set
// is built from.
func scanTenantIDs(ctx context.Context, db dbtypes.Interface, query string, args ...any) ([]string, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox hold: list held tenants failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, fmt.Errorf("inbox hold: scan held tenant failed: %w", err)
		}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox hold: list held tenants failed: %w", err)
	}
	return tenants, nil
}

// scanHoldStats reads the three-column snapshot the gauges publish.
func scanHoldStats(ctx context.Context, db dbtypes.Interface, query string, args ...any) (HoldStats, error) {
	var stats HoldStats
	// Nothing held means no oldest, which is the zero time — substituting the
	// database's own clock would report an age of zero for a hold that does not
	// exist, and a gauge cannot tell that from a hold parked this instant.
	var oldest sql.NullTime

	row := db.QueryRow(ctx, query, args...)
	if err := row.Scan(&stats.Tenants, &stats.Rows, &oldest); err != nil {
		return HoldStats{}, fmt.Errorf("inbox hold: stats failed: %w", err)
	}
	if oldest.Valid {
		stats.OldestHeldSince = oldest.Time
	}
	return stats, nil
}

// affectedOne runs a fenced write and reports whether it changed a row. A write
// that changes none is not an error: for the lease-fenced statements it means the
// lease was lost, which the caller answers by discarding the replay's outcome
// rather than by failing the drain.
func affectedOne(ctx context.Context, db dbtypes.Interface, what, query string, args ...any) (bool, error) {
	res, err := db.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("inbox hold: %s failed: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inbox hold: rows affected failed: %w", err)
	}
	return n > 0, nil
}
