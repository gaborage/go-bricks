package builder

import (
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testLeftJoinColumn  = "users.id"
	testRightJoinColumn = "profiles.user_id"
	testExpectedJoinSQL = "users.id = profiles.user_id"
)

func TestJoinFilterEqColumn(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		left        string
		right       string
		expectedSQL string
	}{
		{
			name:        "postgresql_simple",
			vendor:      dbtypes.PostgreSQL,
			left:        testLeftJoinColumn,
			right:       testRightJoinColumn,
			expectedSQL: testExpectedJoinSQL,
		},
		{
			name:        "oracle_simple",
			vendor:      dbtypes.Oracle,
			left:        testLeftJoinColumn,
			right:       testRightJoinColumn,
			expectedSQL: testExpectedJoinSQL,
		},
		{
			name:        "oracle_reserved_word",
			vendor:      dbtypes.Oracle,
			left:        "accounts.number",
			right:       "transactions.account_number",
			expectedSQL: `accounts."number" = transactions.account_number`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			jf := qb.JoinFilter()

			filter := jf.EqColumn(tt.left, tt.right)
			sql, args, err := filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Empty(t, args, "JOIN filters should not have placeholder args")
		})
	}
}

func TestJoinFilterComparisons(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	tests := []struct {
		name        string
		filter      dbtypes.JoinFilter
		expectedSQL string
	}{
		{
			name:        "NotEqColumn",
			filter:      jf.NotEqColumn("a.id", "b.id"),
			expectedSQL: "a.id != b.id",
		},
		{
			name:        "LtColumn",
			filter:      jf.LtColumn("a.created_at", "b.updated_at"),
			expectedSQL: "a.created_at < b.updated_at",
		},
		{
			name:        "LteColumn",
			filter:      jf.LteColumn("a.price", "b.max_price"),
			expectedSQL: "a.price <= b.max_price",
		},
		{
			name:        "GtColumn",
			filter:      jf.GtColumn("a.score", "b.threshold"),
			expectedSQL: "a.score > b.threshold",
		},
		{
			name:        "GteColumn",
			filter:      jf.GteColumn("a.balance", "b.minimum"),
			expectedSQL: "a.balance >= b.minimum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.filter.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Empty(t, args)
		})
	}
}

func TestJoinFilterAnd(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	filter := jf.And(
		jf.EqColumn(testLeftJoinColumn, testRightJoinColumn),
		jf.GtColumn("profiles.created_at", "users.created_at"),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(users.id = profiles.user_id AND profiles.created_at > users.created_at)", sql)
	assert.Empty(t, args)
}

func TestJoinFilterOr(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	filter := jf.Or(
		jf.EqColumn("users.primary_email", "contacts.email"),
		jf.EqColumn("users.secondary_email", "contacts.email"),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(users.primary_email = contacts.email OR users.secondary_email = contacts.email)", sql)
	assert.Empty(t, args)
}

func TestJoinFilterRaw(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	t.Run("Raw without args", func(t *testing.T) {
		filter := jf.Raw(testExpectedJoinSQL)
		sql, args, err := filter.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, testExpectedJoinSQL, sql)
		assert.Empty(t, args)
	})

	t.Run("Raw with args (mixed column comparison + value)", func(t *testing.T) {
		filter := jf.Raw(`users.id = profiles.user_id AND profiles.type = ?`, "primary")
		sql, args, err := filter.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, `users.id = profiles.user_id AND profiles.type = ?`, sql)
		assert.Equal(t, []any{"primary"}, args)
	})
}

func TestJoinFilterEmptyAnd(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	// Empty AND should produce tautology (always true)
	filter := jf.And()
	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(1=1)", sql)
	assert.Empty(t, args)
}

func TestJoinFilterEmptyOr(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	// Empty OR should produce contradiction (always false)
	filter := jf.Or()
	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(1=0)", sql)
	assert.Empty(t, args)
}
