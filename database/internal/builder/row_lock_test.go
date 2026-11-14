package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestForUpdateRendersAfterPaginationPerVendor pins the one vendor-sensitive
// fact about the lock clause: it must come AFTER pagination on both vendors —
// PostgreSQL's LIMIT/OFFSET and Oracle's OFFSET/FETCH — and it spells the same
// on both. The table VARIES the lock kind and the pagination so a mutant that
// dropped the ordering, the NOWAIT keyword, or the clause itself is caught.
func TestForUpdateRendersAfterPaginationPerVendor(t *testing.T) {
	cases := []struct {
		name    string
		vendor  dbtypes.Vendor
		build   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder
		wantSQL string
	}{
		{
			name:    "postgres_for_update",
			vendor:  dbtypes.PostgreSQL,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdate() },
			wantSQL: `SELECT id FROM users WHERE id = $1 FOR UPDATE`,
		},
		{
			name:    "postgres_for_update_nowait",
			vendor:  dbtypes.PostgreSQL,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateNoWait() },
			wantSQL: `SELECT id FROM users WHERE id = $1 FOR UPDATE NOWAIT`,
		},
		{
			name:   "postgres_after_limit_offset",
			vendor: dbtypes.PostgreSQL,
			build: func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder {
				return q.Paginate(5, 10).ForUpdateNoWait()
			},
			wantSQL: `SELECT id FROM users WHERE id = $1 LIMIT 5 OFFSET 10 FOR UPDATE NOWAIT`,
		},
		{
			name:    "oracle_for_update",
			vendor:  dbtypes.Oracle,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdate() },
			wantSQL: `SELECT id FROM users WHERE id = :1 FOR UPDATE`,
		},
		{
			name:    "oracle_for_update_nowait",
			vendor:  dbtypes.Oracle,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateNoWait() },
			wantSQL: `SELECT id FROM users WHERE id = :1 FOR UPDATE NOWAIT`,
		},
		{
			name:    "postgres_for_update_skip_locked",
			vendor:  dbtypes.PostgreSQL,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateSkipLocked() },
			wantSQL: `SELECT id FROM users WHERE id = $1 FOR UPDATE SKIP LOCKED`,
		},
		{
			name:   "postgres_skip_locked_after_limit",
			vendor: dbtypes.PostgreSQL,
			build: func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder {
				return q.OrderBy("id").Limit(1).ForUpdateSkipLocked()
			},
			wantSQL: `SELECT id FROM users WHERE id = $1 ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`,
		},
		{
			name:    "oracle_for_update_skip_locked",
			vendor:  dbtypes.Oracle,
			build:   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateSkipLocked() },
			wantSQL: `SELECT id FROM users WHERE id = :1 FOR UPDATE SKIP LOCKED`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qb := NewQueryBuilder(tc.vendor)
			f := qb.Filter()
			query := tc.build(qb.Select("id").From(tableUsers).Where(f.Eq("id", 1)))

			sql, args, err := query.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tc.wantSQL, sql)
			assert.Equal(t, []any{1}, args, "the lock clause binds nothing")
		})
	}
}

// TestRowLockLastCallWins pins that the three lock calls share one slot:
// every ordered combination of two and three calls renders only the last.
func TestRowLockLastCallWins(t *testing.T) {
	locks := map[string]func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder{
		forUpdate:           func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdate() },
		forUpdateNoWait:     func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateNoWait() },
		forUpdateSkipLocked: func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.ForUpdateSkipLocked() },
	}
	u, n, s := forUpdate, forUpdateNoWait, forUpdateSkipLocked
	sequences := [][]string{
		{u, n}, {u, s}, {n, u}, {n, s}, {s, u}, {s, n},
		{u, n, s}, {u, s, n}, {n, u, s}, {n, s, u}, {s, u, n}, {s, n, u},
	}
	for _, seq := range sequences {
		t.Run(strings.Join(seq, "_then_"), func(t *testing.T) {
			query := NewQueryBuilder(dbtypes.PostgreSQL).Select("id").From(tableUsers)
			for _, lock := range seq {
				query = locks[lock](query)
			}
			sql, _, err := query.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, "SELECT id FROM users "+seq[len(seq)-1], sql)
		})
	}
}

// TestOracleRefusesARowLockWithPagination pins the vendor restriction from the
// Oracle SQL Language Reference ("Restrictions on the row_limiting_clause: You
// cannot specify this clause with the for_update_clause"): Limit, Offset and
// Paginate each make a locked Oracle SELECT fail at ToSQL with the typed error,
// while the same statement is valid on PostgreSQL and a locked, unpaginated
// Oracle SELECT still renders.
func TestOracleRefusesARowLockWithPagination(t *testing.T) {
	paginations := map[string]func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder{
		"limit":    func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.Limit(5) },
		"offset":   func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.Offset(10) },
		"paginate": func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder { return q.Paginate(5, 10) },
	}
	for name, paginate := range paginations {
		t.Run("oracle_"+name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.Oracle)
			_, _, err := paginate(qb.Select("id").From(tableUsers)).ForUpdateNoWait().ToSQL()
			require.ErrorIs(t, err, dbtypes.ErrRowLockWithPagination)
		})
		t.Run("oracle_"+name+"_skip_locked", func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.Oracle)
			_, _, err := paginate(qb.Select("id").From(tableUsers)).ForUpdateSkipLocked().ToSQL()
			require.ErrorIs(t, err, dbtypes.ErrRowLockWithPagination)
		})
		t.Run("postgres_"+name+"_is_fine", func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql, _, err := paginate(qb.Select("id").From(tableUsers)).ForUpdateNoWait().ToSQL()
			require.NoError(t, err)
			assert.Contains(t, sql, "FOR UPDATE NOWAIT")
		})
	}

	qb := NewQueryBuilder(dbtypes.Oracle)
	sql, _, err := qb.Select("id").From(tableUsers).ForUpdate().ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users FOR UPDATE", sql)
}

// TestSelectWithoutLockRendersNoClause pins the zero value: a builder that never
// asked for a lock emits none, with and without pagination, on both vendors.
func TestSelectWithoutLockRendersNoClause(t *testing.T) {
	for _, vendor := range []dbtypes.Vendor{dbtypes.PostgreSQL, dbtypes.Oracle} {
		t.Run(vendor, func(t *testing.T) {
			qb := NewQueryBuilder(vendor)
			for _, paginated := range []bool{false, true} {
				query := qb.Select("id").From(tableUsers)
				if paginated {
					query = query.Limit(3)
				}
				sql, _, err := query.ToSQL()
				require.NoError(t, err)
				assert.NotContains(t, sql, "FOR UPDATE")
			}
		})
	}
}

// TestLockedSelectIsRefusedAsASubquery pins that the lock stays on the outer
// statement: a builder carrying ForUpdate is rejected by every subquery door
// before it can render FOR UPDATE inside EXISTS.
func TestLockedSelectIsRefusedAsASubquery(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	locked := qb.Select("id").From(tableUsers).ForUpdateNoWait()
	require.Error(t, locked.(*SelectQueryBuilder).ValidateForSubquery())

	_, _, err := qb.Select("id").From("orders").Where(f.Exists(locked)).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row lock")

	skipLocked := qb.Select("id").From(tableUsers).ForUpdateSkipLocked()
	err = skipLocked.(*SelectQueryBuilder).ValidateForSubquery()
	require.Error(t, err)
	assert.Equal(t, "subquery cannot carry a row lock (ForUpdate/ForUpdateNoWait/ForUpdateSkipLocked)", err.Error())

	_, _, err = qb.Select("id").From("orders").Where(f.Exists(skipLocked)).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row lock")

	unlocked := qb.Select("id").From(tableUsers)
	sql, _, err := qb.Select("id").From("orders").Where(f.Exists(unlocked)).ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "FOR UPDATE")
}
