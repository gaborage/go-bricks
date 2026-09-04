package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

// The hold's column names, shared by both vendors' statements so a rename moves
// in one place rather than across thirty string literals.
const (
	colConsumer      = "consumer"
	colTenantID      = "tenant_id"
	colStream        = "stream"
	colStreamOffset  = "stream_offset"
	colData          = "data"
	colProperties    = "properties"
	colHeldAt        = "held_at"
	colHeldSince     = "held_since"
	colAttempts      = "attempts"
	colNextAttemptAt = "next_attempt_at"
	colLastError     = "last_error"
	colLeaseOwner    = "lease_owner"
	colLeaseUntil    = "lease_until"
)

// boundedLimit renders a caller's row limit for the builder. A limit is a count,
// and a negative one would wrap through uint64 into a number no query should
// carry; anything below one asks for nothing, which the smallest legal limit
// expresses honestly.
// Written as a clamp rather than a branch: a comparison here has no observable
// boundary — the only input the two spellings could disagree on is 1, which both
// answer with 1 — so there would be nothing for a test to falsify.
func boundedLimit(limit int) uint64 {
	return uint64(max(limit, 1))
}

// holdTenantColumns is the six-column tenant projection both vendors select, in
// the order scanHoldTenants reads them. lastError is the vendor's own expression
// for "no error" — COALESCE on PostgreSQL, NVL on Oracle, which differ because
// Oracle folds an empty string to NULL — and it arrives as a RawExpression
// because the builder accepts only identifiers in a projection.
func holdTenantColumns(lastError dbtypes.RawExpression) []any {
	return []any{colConsumer, colTenantID, colHeldSince, colAttempts, colNextAttemptAt, lastError}
}

// holdRowColumns is the seven-column row projection, in scanHoldRows' order.
func holdRowColumns() []any {
	return []any{colConsumer, colStream, colStreamOffset, colTenantID, colData, colProperties, colHeldAt}
}

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

// holdQueries builds the statements whose SQL the query builder makes identical
// across vendors. What differs between PostgreSQL and Oracle is two expressions —
// the clock and the "no error" rendering — so they are fields rather than two
// copies of every method.
type holdQueries struct {
	qb          *database.QueryBuilder
	vendor      string
	table       string
	tenantTable string
	// now is the database's own clock: NOW() or SYSTIMESTAMP. Every lease and due
	// decision is made in database time so replicas with skewed clocks agree.
	now string
	// secondsFromNow is the clock plus a bound number of seconds — the vendor's
	// interval arithmetic with one ? — composed from now at construction so the two
	// spellings of the clock cannot drift apart.
	secondsFromNow string
	// noError renders an absent last_error as the empty string. Oracle needs a
	// space where PostgreSQL can use '', because it folds an empty literal to NULL;
	// the shared scanner trims it back.
	noError string
}

func (q *holdQueries) ListTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]HoldTenant, error) {
	// SECURITY: Manual SQL review completed - a constant expression over a fixed
	// column name, no interpolation and no caller input; an Expr only because the
	// builder's projection accepts identifiers alone.
	noError, err := q.qb.Expr(q.noError)
	if err != nil {
		return nil, q.wrap("build list tenants query failed", err)
	}

	f := q.qb.Filter()
	query, args, err := q.qb.Select(holdTenantColumns(noError)...).From(q.tenantTable).
		Where(f.Eq(colConsumer, consumer)).
		OrderBy(colHeldSince).
		ToSQL()
	if err != nil {
		return nil, q.wrap("build list tenants query failed", err)
	}
	return scanHoldTenants(ctx, db, "list tenants", query, args...)
}

func (q *holdQueries) DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error) {
	// SECURITY: Manual SQL review completed - a constant expression over a fixed
	// column, as in ListTenants; no interpolation and no caller value.
	noError, err := q.qb.Expr(q.noError)
	if err != nil {
		return nil, q.wrap("build due tenants query failed", err)
	}

	f := q.qb.Filter()
	// SECURITY: Manual SQL review completed - both predicates are constant text
	// over fixed column names and the database's own clock; no identifier is
	// interpolated and no caller value concatenated.
	due := f.Raw(colNextAttemptAt + " <= " + q.now)
	free := f.Raw("(" + colLeaseUntil + " IS NULL OR " + colLeaseUntil + " < " + q.now + ")")

	query, args, err := q.qb.Select(holdTenantColumns(noError)...).From(q.tenantTable).
		Where(f.And(f.Eq(colConsumer, consumer), due, free)).
		OrderBy(colHeldSince).
		Limit(boundedLimit(limit)).
		ToSQL()
	if err != nil {
		return nil, q.wrap("build due tenants query failed", err)
	}
	return scanHoldTenants(ctx, db, "list due tenants", query, args...)
}

func (q *holdQueries) HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error) {
	f := q.qb.Filter()
	query, args, err := q.qb.Select(colTenantID).From(q.tenantTable).
		Where(f.Eq(colConsumer, consumer)).
		ToSQL()
	if err != nil {
		return nil, q.wrap("build held tenants query failed", err)
	}
	return scanTenantIDs(ctx, db, query, args...)
}

func (q *holdQueries) NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error) {
	f := q.qb.Filter()
	query, args, err := q.qb.Select(holdRowColumns()...).From(q.table).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant))).
		OrderBy(colStream, colStreamOffset).
		Limit(boundedLimit(limit)).
		ToSQL()
	if err != nil {
		return nil, q.wrap("build next rows query failed", err)
	}
	return scanHoldRows(ctx, db, query, args...)
}

// deleteRow removes a replayed row while this owner still holds the lease. The
// fence is a subquery in the SAME statement: checking the lease first and
// deleting second would reopen the window the lease exists to close.
func (q *holdQueries) DeleteRow(ctx context.Context, db dbtypes.Interface,
	consumer, stream string, offset int64, tenant, owner string,
) (bool, error) {
	f := q.qb.Filter()
	lease, err := q.leaseHeldBy(f, consumer, tenant, owner)
	if err != nil {
		return false, q.wrap("build delete held row query failed", err)
	}

	query, args, err := q.qb.Delete(q.table).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colStream, stream),
			f.Eq(colStreamOffset, offset),
			f.Exists(lease),
		)).
		ToSQL()
	if err != nil {
		return false, q.wrap("build delete held row query failed", err)
	}
	return affectedOne(ctx, db, "delete held row", query, args...)
}

// release drops the tenant's marker once its last row is gone, under the lease
// and in one statement — a tenant whose rows a concurrent park just added must
// stay held, which a check-then-delete would miss.
func (q *holdQueries) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	f := q.qb.Filter()
	one, err := q.constantOne()
	if err != nil {
		return false, q.wrap("build release tenant query failed", err)
	}

	rowsRemain := q.qb.Select(one).From(q.table).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant)))

	// SECURITY: Manual SQL review completed - constant text over a fixed column and
	// the vendor's own clock keyword, which the constructor sets; no identifier is
	// interpolated and every caller value (consumer, tenant, owner) is bound by the
	// typed Eq predicates beside it.
	leaseHeld := f.Raw(colLeaseUntil + " > " + q.now)

	query, args, err := q.qb.Delete(q.tenantTable).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			f.Eq(colLeaseOwner, owner),
			leaseHeld,
			f.NotExists(rowsRemain),
		)).
		ToSQL()
	if err != nil {
		return false, q.wrap("build release tenant query failed", err)
	}
	return affectedOne(ctx, db, "release tenant", query, args...)
}

// releaseLease drops a lease this owner holds.
func (q *holdQueries) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	f := q.qb.Filter()
	query, args, err := q.qb.Update(q.tenantTable).
		SetMap(map[string]any{colLeaseOwner: nil, colLeaseUntil: nil}).
		Where(f.And(f.Eq(colConsumer, consumer), f.Eq(colTenantID, tenant), f.Eq(colLeaseOwner, owner))).
		ToSQL()
	if err != nil {
		return q.wrap("build release lease query failed", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return q.wrap("release lease failed", err)
	}
	return nil
}

// leaseHeldBy is the fence's subquery: this owner holds a live lease on the
// tenant.
func (q *holdQueries) leaseHeldBy(f dbtypes.FilterFactory, consumer, tenant, owner string) (dbtypes.SelectQueryBuilder, error) {
	one, err := q.constantOne()
	if err != nil {
		return nil, err
	}

	return q.qb.Select(one).From(q.tenantTable).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			f.Eq(colLeaseOwner, owner),
			// SECURITY: Manual SQL review completed - constant text over a fixed column
			// and the database's own clock.
			f.Raw(colLeaseUntil+" > "+q.now),
		)), nil
}

// constantOne is the projection an EXISTS ignores.
//
// SECURITY: Manual SQL review completed - a literal, no interpolation; an Expr
// only because the builder's projection accepts identifiers alone.
func (q *holdQueries) constantOne() (dbtypes.RawExpression, error) {
	return q.qb.Expr("1")
}

// wrap names the vendor in an error the way each store's own messages do.
func (q *holdQueries) wrap(what string, err error) error {
	return fmt.Errorf("inbox %s: %s: %w", q.vendor, what, err)
}

// stats is the one-round-trip snapshot: three scalar subqueries in one
// projection, which SubqueryColumn composes and the vendor renders (FROM dual
// on Oracle).
func (q *holdQueries) stats(consumer string) dbtypes.SelectQueryBuilder {
	f := q.qb.Filter()
	tenants := q.qb.Select(q.qb.MustExpr("COUNT(*)")).From(q.tenantTable).Where(f.Eq(colConsumer, consumer))
	rows := q.qb.Select(q.qb.MustExpr("COUNT(*)")).From(q.table).Where(f.Eq(colConsumer, consumer))
	oldest := q.qb.Select(q.qb.MustExpr("MIN(" + colHeldSince + ")")).From(q.tenantTable).Where(f.Eq(colConsumer, consumer))
	return q.qb.Select().
		SubqueryColumn(tenants, "tenants").
		SubqueryColumn(rows, "held_rows").
		SubqueryColumn(oldest, "oldest")
}

// acquireLease takes the lease when it is free, expired on the database clock,
// or already this owner's. secondsFromNow is the vendor's spelling of
// "now plus ? seconds" with one placeholder for the seconds.
func (q *holdQueries) acquireLease(consumer, tenant, owner string, lease time.Duration) dbtypes.UpdateQueryBuilder {
	f := q.qb.Filter()
	// SECURITY: Manual SQL review completed - secondsFromNow is composed at construction
	// from the vendor's clock (now + ? seconds, one placeholder); owner, seconds, consumer
	// and tenant are bound
	return q.qb.Update(q.tenantTable).
		Set(colLeaseOwner, owner).
		SetExpr(colLeaseUntil, q.qb.MustExpr(q.secondsFromNow), lease.Seconds()).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			// SECURITY: Manual SQL review completed - constant text over a fixed column and
			// the database's own clock, the same predicate the drain's due query uses.
			f.Or(f.Null(colLeaseUntil), f.Raw(colLeaseUntil+" < "+q.now), f.Eq(colLeaseOwner, owner)),
		))
}

// deferTenant pushes the tenant's next attempt out, records the bounded error
// and drops the lease — only while this owner still holds it.
func (q *holdQueries) deferTenant(consumer, tenant, owner string, backoff time.Duration, lastErr string) dbtypes.UpdateQueryBuilder {
	f := q.qb.Filter()
	// SECURITY: Manual SQL review completed - secondsFromNow is composed at construction
	// from the vendor's clock; attempts + 1 and the NULLs are static text; every value bound
	return q.qb.Update(q.tenantTable).
		Set(colAttempts, q.qb.MustExpr(colAttempts+" + 1")).
		SetExpr(colNextAttemptAt, q.qb.MustExpr(q.secondsFromNow), backoff.Seconds()).
		Set(colLastError, ledgererr.Bound(lastErr)).
		// Literal NULLs, as the hand-written statement spelled them: a bound nil
		// would be equivalent but would move the placeholder numbering.
		Set(colLeaseOwner, q.qb.MustExpr("NULL")).
		Set(colLeaseUntil, q.qb.MustExpr("NULL")).
		Where(f.And(
			f.Eq(colConsumer, consumer),
			f.Eq(colTenantID, tenant),
			f.Eq(colLeaseOwner, owner),
			// SECURITY: Manual SQL review completed - constant text over a fixed column and
			// the database's own clock.
			f.Raw(colLeaseUntil+" > "+q.now),
		))
}

// execAffectedOne runs q through the framework helper and reports whether a
// row was touched; the error carries "inbox hold: <what> failed" as its label.
func execAffectedOne(ctx context.Context, db dbtypes.Interface, what string, q dbtypes.UpdateQueryBuilder) (bool, error) {
	n, err := database.ExecuteUpdate(ctx, db, q, "inbox hold: "+what+" failed")
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
