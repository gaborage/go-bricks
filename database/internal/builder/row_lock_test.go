package builder

import (
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
			name:   "oracle_after_offset_fetch",
			vendor: dbtypes.Oracle,
			build: func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder {
				return q.Paginate(5, 10).ForUpdateNoWait()
			},
			wantSQL: `SELECT id FROM users WHERE id = :1 OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY FOR UPDATE NOWAIT`,
		},
		{
			name:   "last_call_wins",
			vendor: dbtypes.PostgreSQL,
			build: func(q dbtypes.SelectQueryBuilder) dbtypes.SelectQueryBuilder {
				return q.ForUpdateNoWait().ForUpdate()
			},
			wantSQL: `SELECT id FROM users WHERE id = $1 FOR UPDATE`,
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

	unlocked := qb.Select("id").From(tableUsers)
	sql, _, err := qb.Select("id").From("orders").Where(f.Exists(unlocked)).ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "FOR UPDATE")
}
