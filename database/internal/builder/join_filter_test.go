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
	testExpectedSQL      = "price <= ?"
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

// ========== Column-to-Value Comparison Tests ==========

func TestJoinFilterEq(t *testing.T) {
	t.Run("with_simple_value_postgresql", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.Eq("status", "active")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "status = ?", sql)
		assert.Equal(t, []any{"active"}, args)
	})

	t.Run("with_simple_value_oracle", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Eq("status", "active")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "status = ?", sql)
		assert.Equal(t, []any{"active"}, args)
	})

	t.Run("oracle_reserved_word", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Eq("number", "12345")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, `"number" = ?`, sql)
		assert.Equal(t, []any{"12345"}, args)
	})

	t.Run("with_expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Eq("col1", qb.Expr("TO_NUMBER(o.field1)"))
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "col1 = TO_NUMBER(o.field1)", sql)
		assert.Empty(t, args, "Expression should not generate placeholders")
	})

	t.Run("in_full_join_query", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From("orders").
			JoinOn("customers", jf.Eq("customers.status", "active"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "customers.status = $1")
		assert.Equal(t, []any{"active"}, args)
	})
}

func TestJoinFilterComparisonOperators(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	tests := []struct {
		name         string
		filter       dbtypes.JoinFilter
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:         "NotEq",
			filter:       jf.NotEq("status", "deleted"),
			expectedSQL:  "status != ?",
			expectedArgs: []any{"deleted"},
		},
		{
			name:         "Lt",
			filter:       jf.Lt("age", 18),
			expectedSQL:  "age < ?",
			expectedArgs: []any{18},
		},
		{
			name:         "Lte",
			filter:       jf.Lte("price", 100.0),
			expectedSQL:  testExpectedSQL,
			expectedArgs: []any{100.0},
		},
		{
			name:         "Gt",
			filter:       jf.Gt("score", 50),
			expectedSQL:  "score > ?",
			expectedArgs: []any{50},
		},
		{
			name:         "Gte",
			filter:       jf.Gte("balance", 0.0),
			expectedSQL:  "balance >= ?",
			expectedArgs: []any{0.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.filter.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Equal(t, tt.expectedArgs, args)
		})
	}
}

func TestJoinFilterComparisonWithExpressions(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	jf := qb.JoinFilter()

	tests := []struct {
		name        string
		filter      dbtypes.JoinFilter
		expectedSQL string
	}{
		{
			name:        "NotEq_expression",
			filter:      jf.NotEq("col1", qb.Expr("UPPER(o.field1)")),
			expectedSQL: "col1 != UPPER(o.field1)",
		},
		{
			name:        "Lt_expression",
			filter:      jf.Lt("amount", qb.Expr("TO_NUMBER(o.max_amount)")),
			expectedSQL: "amount < TO_NUMBER(o.max_amount)",
		},
		{
			name:        "Gte_expression",
			filter:      jf.Gte("date", qb.Expr("SYSDATE")),
			expectedSQL: "date >= SYSDATE",
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

func TestJoinFilterIn(t *testing.T) {
	t.Run("with_multiple_values", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.In("status", []string{"active", "pending"})
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "status IN (?,?)", sql)
		assert.Equal(t, []any{"active", "pending"}, args)
	})

	t.Run("with_single_value", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.In("status", "active")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "status IN (?)", sql)
		assert.Equal(t, []any{"active"}, args)
	})

	t.Run("with_empty_slice", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.In("status", []string{})
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "(1=0)", sql) // Always false for empty IN
		assert.Empty(t, args)
	})

	t.Run("oracle_placeholder_format", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From("orders").
			JoinOn("customers", jf.In("customers.tier", []string{"gold", "platinum"}))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "customers.tier IN (:1,:2)")
		assert.Equal(t, []any{"gold", "platinum"}, args)
	})
}

func TestJoinFilterNotIn(t *testing.T) {
	t.Run("with_multiple_values", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.NotIn("status", []string{"deleted", "banned"})
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "status NOT IN (?,?)", sql)
		assert.Equal(t, []any{"deleted", "banned"}, args)
	})

	t.Run("with_empty_slice", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.NotIn("status", []string{})
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "(1=1)", sql) // Always true for empty NOT IN
		assert.Empty(t, args)
	})
}

func TestJoinFilterLike(t *testing.T) {
	t.Run("simple_pattern", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.Like("name", "%Smith%")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "name LIKE ?", sql)
		assert.Equal(t, []any{"%Smith%"}, args)
	})

	t.Run("oracle_reserved_word", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Like("comment", "%test%")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, `"comment" LIKE ?`, sql)
		assert.Equal(t, []any{"%test%"}, args)
	})
}

func TestJoinFilterNull(t *testing.T) {
	t.Run("simple_column", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.Null("deleted_at")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "deleted_at IS NULL", sql)
		assert.Empty(t, args)
	})

	t.Run("oracle_reserved_word", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Null("level")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, `"level" IS NULL`, sql)
		assert.Empty(t, args)
	})
}

func TestJoinFilterNotNull(t *testing.T) {
	t.Run("simple_column", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.NotNull("email")
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "email IS NOT NULL", sql)
		assert.Empty(t, args)
	})
}

func TestJoinFilterBetween(t *testing.T) {
	t.Run("with_simple_values", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.Between("price", 10.0, 20.0)
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "price >= ?")
		assert.Contains(t, sql, testExpectedSQL)
		assert.Contains(t, sql, "AND")
		assert.Equal(t, []any{10.0, 20.0}, args)
	})

	t.Run("with_both_expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Between("age", qb.Expr("18"), qb.Expr("65"))
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "age >= 18")
		assert.Contains(t, sql, "age <= 65")
		assert.Empty(t, args)
	})

	t.Run("with_lower_expression_upper_value", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Between("price", qb.Expr("0"), 100.0)
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "price >= 0")
		assert.Contains(t, sql, testExpectedSQL)
		assert.Equal(t, []any{100.0}, args)
	})

	t.Run("with_upper_expression_lower_value", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Between("date", "2024-01-01", qb.Expr("SYSDATE"))
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "date >= ?")
		assert.Contains(t, sql, "date <= SYSDATE")
		assert.Equal(t, []any{"2024-01-01"}, args)
	})
}

func TestJoinFilterMixedConditions(t *testing.T) {
	t.Run("mixed_column_and_value_comparisons", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		filter := jf.And(
			jf.EqColumn("c.id", "o.customer_id"),          // Column-to-column
			jf.Eq("c.status", "active"),                   // Column-to-value
			jf.In("c.tier", []string{"gold", "platinum"}), // IN clause
		)

		sql, args, err := filter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "c.id = o.customer_id")
		assert.Contains(t, sql, "c.status = ?")
		assert.Contains(t, sql, "c.tier IN (?,?)")
		assert.Equal(t, []any{"active", "gold", "platinum"}, args)
	})

	t.Run("complex_join_with_expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.And(
			jf.EqColumn("emp.id", "o.emp_id"),
			jf.Eq("emp.col1", qb.Expr("TO_NUMBER(o.field1)")),
			jf.Eq("emp.status", "3"),
		)

		sql, args, err := filter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "emp.id = o.emp_id")
		assert.Contains(t, sql, "emp.col1 = TO_NUMBER(o.field1)")
		assert.Contains(t, sql, "emp.status = ?")
		assert.Equal(t, []any{"3"}, args)
	})
}

func TestJoinFilterInFullQuery(t *testing.T) {
	t.Run("complex_join_query", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()

		query := qb.Select("*").
			From(dbtypes.Table("orders").As("o")).
			JoinOn(dbtypes.Table("customers").As("c"), jf.And(
				jf.EqColumn("c.id", "o.customer_id"),
				jf.Eq("c.status", "active"),
			)).
			JoinOn(dbtypes.Table("products").As("p"), jf.And(
				jf.EqColumn("p.id", "o.product_id"),
				jf.Eq("p.price", qb.Expr("TO_NUMBER(99.99)")),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "JOIN customers c ON (c.id = o.customer_id AND c.status = :1)")
		assert.Contains(t, sql, "JOIN products p ON (p.id = o.product_id AND p.price = TO_NUMBER(99.99))")
		// Note: The expression doesn't consume a placeholder, so args should only have "active"
		assert.Equal(t, []any{"active"}, args)
	})
}
