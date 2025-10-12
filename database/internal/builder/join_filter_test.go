package builder

import (
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testLeftJoinColumn   = "users.id"
	testRightJoinColumn  = "profiles.user_id"
	testExpectedJoinSQL  = "users.id = profiles.user_id"
	testContactsEmail    = "contacts.email"
	testRightJoinColumnB = "b.a_id"
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
		jf.EqColumn("users.primary_email", testContactsEmail),
		jf.EqColumn("users.secondary_email", testContactsEmail),
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

// ========== Nil JoinFilter Handling Tests ==========

func TestJoinFilterAndOrNilHandling(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	t.Run("And with nil join filters", func(t *testing.T) {
		// Mix of nil and valid join filters
		validFilter := jf.EqColumn("users.id", "profiles.user_id")
		combinedFilter := jf.And(
			nil,         // nil should be skipped
			validFilter, // valid filter
			nil,         // another nil
			jf.GtColumn("profiles.created_at", "users.created_at"),
		)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "users.id = profiles.user_id")
		assert.Contains(t, sql, "profiles.created_at > users.created_at")
		assert.Contains(t, sql, "AND")
		assert.Empty(t, args) // No placeholders in column comparisons
	})

	t.Run("And with all nil filters", func(t *testing.T) {
		// All nil filters should produce empty And
		combinedFilter := jf.And(nil, nil, nil)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		// Empty And() produces (1=1)
		assert.Equal(t, "(1=1)", sql)
		assert.Empty(t, args)
	})

	t.Run("Or with nil join filters", func(t *testing.T) {
		// Mix of nil and valid join filters
		combinedFilter := jf.Or(
			nil,
			jf.EqColumn("users.primary_email", testContactsEmail),
			nil,
			jf.EqColumn("users.secondary_email", testContactsEmail),
		)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "users.primary_email = contacts.email")
		assert.Contains(t, sql, "users.secondary_email = contacts.email")
		assert.Contains(t, sql, "OR")
		assert.Empty(t, args)
	})

	t.Run("Or with all nil filters", func(t *testing.T) {
		// All nil filters should produce empty Or
		combinedFilter := jf.Or(nil, nil)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		// Empty Or() produces (1=0)
		assert.Equal(t, "(1=0)", sql)
		assert.Empty(t, args)
	})

	t.Run("Nested And/Or with nil filters", func(t *testing.T) {
		// Complex nesting with nils mixed in
		complexFilter := jf.And(
			nil,
			jf.Or(
				nil,
				jf.EqColumn("a.id", testRightJoinColumnB),
				nil,
			),
			nil,
			jf.GtColumn("a.created_at", "b.created_at"),
		)

		sql, args, err := complexFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "a.id = b.a_id")
		assert.Contains(t, sql, "a.created_at > b.created_at")
		assert.Empty(t, args)
	})
}

// TestJoinFilterNilDoesNotPanic ensures nil join filters don't cause panics
func TestJoinFilterNilDoesNotPanic(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	// This should not panic (was the bug before the fix)
	assert.NotPanics(t, func() {
		filter := jf.And(nil, jf.EqColumn("a.id", testRightJoinColumnB))
		_, _, _ = filter.ToSQL()
	})

	assert.NotPanics(t, func() {
		filter := jf.Or(nil, jf.EqColumn("a.id", testRightJoinColumnB), nil)
		_, _, _ = filter.ToSQL()
	})
}
