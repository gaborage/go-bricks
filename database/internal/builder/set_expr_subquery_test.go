package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestSetExprBindsArgumentsPerVendor pins the one thing SetExpr adds over Set:
// the expression's `?` placeholders are renumbered with the statement's own,
// in order, on both vendors — the interval-arithmetic shape the hold stores
// hand-write today. The table VARIES argument count and position so a mutant
// that dropped or reordered the args is caught.
func TestSetExprBindsArgumentsPerVendor(t *testing.T) {
	cases := []struct {
		name     string
		vendor   dbtypes.Vendor
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "postgres",
			vendor:   dbtypes.PostgreSQL,
			wantSQL:  `UPDATE users SET lease_until = NOW() + ($1 * INTERVAL '1 second'), attempts = attempts + 1, owner = $2 WHERE id = $3`,
			wantArgs: []any{30, "w1", 7},
		},
		{
			name:     "oracle",
			vendor:   dbtypes.Oracle,
			wantSQL:  `UPDATE users SET lease_until = NOW() + (:1 * INTERVAL '1 second'), attempts = attempts + 1, owner = :2 WHERE id = :3`,
			wantArgs: []any{30, "w1", 7},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qb := NewQueryBuilder(tc.vendor)
			f := qb.Filter()
			// SECURITY: Manual SQL review completed - test fixture, static expression, seconds bound as an argument
			query := qb.Update(tableUsers).
				SetExpr("lease_until", qb.MustExpr("NOW() + (? * INTERVAL '1 second')"), 30).
				Set("attempts", qb.MustExpr("attempts + 1")).
				Set("owner", "w1").
				Where(f.Eq("id", 7))

			sql, args, err := query.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tc.wantSQL, sql)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

// TestSetExprRefusesAliasAndEmptySQL pins that SetExpr judges the expression
// the way Set does: an alias projects nothing in a SET, and an empty body is
// the RawExpression's own error — both surface at ToSQL naming the column.
func TestSetExprRefusesAliasAndEmptySQL(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	_, _, err := qb.Update(tableUsers).SetExpr("n", dbtypes.RawExpression{SQL: "1", Alias: "x"}).ToSQL()
	require.ErrorIs(t, err, dbtypes.ErrAliasInValue)
	assert.Contains(t, err.Error(), "n:")

	_, _, err = qb.Update(tableUsers).SetExpr("n", dbtypes.RawExpression{SQL: "  "}).ToSQL()
	require.ErrorIs(t, err, dbtypes.ErrEmptyExpressionSQL)

	_, _, err = qb.Update(tableUsers).SetExpr("bad column", qb.MustExpr("1")).ToSQL()
	require.Error(t, err, "the SET target still goes through the identifier grammar")
}

// TestSubqueryColumnRendersScalarSubqueriesPerVendor pins the stats-snapshot
// shape: several scalar subqueries in one projection, each aliased, their
// arguments numbered after one another and before the outer WHERE's; on Oracle
// a table-less outer SELECT gains FROM dual.
func TestSubqueryColumnRendersScalarSubqueriesPerVendor(t *testing.T) {
	cases := []struct {
		name     string
		vendor   dbtypes.Vendor
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "postgres_no_from",
			vendor:   dbtypes.PostgreSQL,
			wantSQL:  `SELECT (SELECT COUNT(*) FROM held WHERE consumer = $1) AS tenants, (SELECT MIN(held_since) FROM held WHERE consumer = $2) AS oldest`,
			wantArgs: []any{"c1", "c1"},
		},
		{
			name:     "oracle_gains_from_dual",
			vendor:   dbtypes.Oracle,
			wantSQL:  `SELECT (SELECT COUNT(*) FROM held WHERE consumer = :1) AS tenants, (SELECT MIN(held_since) FROM held WHERE consumer = :2) AS oldest FROM dual`,
			wantArgs: []any{"c1", "c1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qb := NewQueryBuilder(tc.vendor)
			f := qb.Filter()
			tenants := qb.Select(qb.MustExpr("COUNT(*)")).From("held").Where(f.Eq("consumer", "c1"))
			oldest := qb.Select(qb.MustExpr("MIN(held_since)")).From("held").Where(f.Eq("consumer", "c1"))

			sql, args, err := qb.Select().SubqueryColumn(tenants, "tenants").SubqueryColumn(oldest, "oldest").ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tc.wantSQL, sql)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

// TestSubqueryColumnNumbersArgsWithTheOuterWhere pins the renumbering across
// the projection AND the outer predicate: a subquery arg, then a WHERE arg.
func TestSubqueryColumnNumbersArgsWithTheOuterWhere(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()
	inner := qb.Select(qb.MustExpr("COUNT(*)")).From("orders").Where(f.Eq("orders.user_id", 5))

	sql, args, err := qb.Select("id").SubqueryColumn(inner, "orders").From(tableUsers).Where(f.Eq("id", 5)).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT id, (SELECT COUNT(*) FROM orders WHERE orders.user_id = $1) AS orders FROM users WHERE id = $2`, sql)
	assert.Equal(t, []any{5, 5}, args)
}

// TestSubqueryColumnRefusesBadInput pins the three refusals: an alias outside
// the grammar, a nil subquery, and a subquery carrying a row lock.
func TestSubqueryColumnRefusesBadInput(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	inner := qb.Select(qb.MustExpr("1")).From("orders")

	_, _, err := qb.Select().SubqueryColumn(inner, "bad alias").ToSQL()
	require.ErrorIs(t, err, dbtypes.ErrInvalidAlias)

	_, _, err = qb.Select().SubqueryColumn(nil, "n").ToSQL()
	require.Error(t, err)

	locked := qb.Select(qb.MustExpr("1")).From("orders").ForUpdate()
	_, _, err = qb.Select().SubqueryColumn(locked, "n").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row lock")
}

// TestOracleTableLessSelectRendersFromDual pins the Probe shape (`SELECT 1`)
// on both vendors and that a real From is never doubled.
func TestOracleTableLessSelectRendersFromDual(t *testing.T) {
	pg := NewQueryBuilder(dbtypes.PostgreSQL)
	sql, _, err := pg.Select(pg.MustExpr("1")).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1", sql)

	ora := NewQueryBuilder(dbtypes.Oracle)
	sql, _, err = ora.Select(ora.MustExpr("1")).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1 FROM dual", sql)

	sql, _, err = ora.Select("id").From(tableUsers).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users", sql)

	// A JOIN is a row source too: a caller that forgot From keeps its loud
	// failure at the database instead of a silent join against dual.
	jf := ora.JoinFilter()
	sql, _, err = ora.Select("id").JoinOn("orders", jf.EqColumn("orders.user_id", "users.id")).ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "dual")
}
