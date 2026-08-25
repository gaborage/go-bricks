package builder

import (
	dbsql "database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
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
		// SECURITY: Manual SQL review completed - test fixture string is a literal column-to-column comparison, no user input
		filter := jf.Raw(testExpectedJoinSQL)
		sql, args, err := filter.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, testExpectedJoinSQL, sql)
		assert.Empty(t, args)
	})

	t.Run("Raw with args (mixed column comparison + value)", func(t *testing.T) {
		// SECURITY: Manual SQL review completed - column comparison uses literal qualified identifiers; value side is parameterized via ?
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
		filter := jf.Eq("col1", qb.MustExpr("TO_NUMBER(o.field1)"))
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
			filter:      jf.NotEq("col1", qb.MustExpr("UPPER(o.field1)")),
			expectedSQL: "col1 != UPPER(o.field1)",
		},
		{
			name:        "Lt_expression",
			filter:      jf.Lt("amount", qb.MustExpr("TO_NUMBER(o.max_amount)")),
			expectedSQL: "amount < TO_NUMBER(o.max_amount)",
		},
		{
			name:        "Gte_expression",
			filter:      jf.Gte("date", qb.MustExpr("SYSDATE")),
			expectedSQL: `"date" >= SYSDATE`, // DATE is an Oracle reserved word (M10) — auto-quoted
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
		filter := jf.Between("age", qb.MustExpr("18"), qb.MustExpr("65"))
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "age >= 18")
		assert.Contains(t, sql, "age <= 65")
		assert.Empty(t, args)
	})

	t.Run("with_lower_expression_upper_value", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Between("price", qb.MustExpr("0"), 100.0)
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		assert.Contains(t, sql, "price >= 0")
		assert.Contains(t, sql, testExpectedSQL)
		assert.Equal(t, []any{100.0}, args)
	})

	t.Run("with_upper_expression_lower_value", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		filter := jf.Between("date", "2024-01-01", qb.MustExpr("SYSDATE"))
		sql, args, err := filter.ToSQL()

		require.NoError(t, err)
		// DATE is an Oracle reserved word (M10) — auto-quoted on the Oracle vendor.
		assert.Contains(t, sql, `"date" >= ?`)
		assert.Contains(t, sql, `"date" <= SYSDATE`)
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
			jf.Eq("emp.col1", qb.MustExpr("TO_NUMBER(o.field1)")),
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
			From(dbtypes.MustTable("orders").MustAs("o")).
			JoinOn(dbtypes.MustTable("customers").MustAs("c"), jf.And(
				jf.EqColumn("c.id", "o.customer_id"),
				jf.Eq("c.status", "active"),
			)).
			JoinOn(dbtypes.MustTable("products").MustAs("p"), jf.And(
				jf.EqColumn("p.id", "o.product_id"),
				jf.Eq("p.price", qb.MustExpr("TO_NUMBER(99.99)")),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "JOIN customers c ON (c.id = o.customer_id AND c.status = :1)")
		assert.Contains(t, sql, "JOIN products p ON (p.id = o.product_id AND p.price = TO_NUMBER(99.99))")
		// Note: The expression doesn't consume a placeholder, so args should only have "active"
		assert.Equal(t, []any{"active"}, args)
	})
}

// TestJoinFilterRejectsRawExpressionLiteral covers the #1153 class on the
// JoinFilter value doors: they interpolate expr.SQL verbatim, so a struct
// literal that never reached Expr() is validated here.
func TestJoinFilterRejectsRawExpressionLiteral(t *testing.T) {
	empty := dbtypes.RawExpression{SQL: "  "}

	tests := []struct {
		name   string
		filter func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter
	}{
		{name: "eq", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Eq("amount", empty) }},
		{name: "not_eq", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.NotEq("amount", empty) }},
		{name: "lt", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Lt("amount", empty) }},
		{name: "lte", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Lte("amount", empty) }},
		{name: "gt", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Gt("amount", empty) }},
		{name: "gte", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Gte("amount", empty) }},
		{name: "between_lower", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Between("age", empty, 65) }},
		{name: "between_upper", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Between("age", 18, empty) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql, args, err := tt.filter(qb.JoinFilter()).ToSQL()

			require.Error(t, err)
			assert.ErrorIs(t, err, dbtypes.ErrEmptyExpressionSQL)
			assert.Empty(t, sql)
			assert.Empty(t, args)
		})
	}
}

// These doors validate an expression but never render its Alias, so a bad alias
// is not a live sink here today. Pinned anyway: the guard is what makes that
// true, and a future change that started rendering the alias would otherwise
// have no regression test standing in its way.
func TestJoinFilterRejectsRawExpressionAlias(t *testing.T) {
	bad := dbtypes.RawExpression{SQL: "1", Alias: "x FROM users"}

	tests := []struct {
		name   string
		filter func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter
	}{
		{name: "eq", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Eq("amount", bad) }},
		{name: "gte", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Gte("amount", bad) }},
		{name: "between_lower", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Between("age", bad, 65) }},
		{name: "between_upper", filter: func(jf dbtypes.JoinFilterFactory) dbtypes.JoinFilter { return jf.Between("age", 18, bad) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql, args, err := tt.filter(qb.JoinFilter()).ToSQL()

			require.Error(t, err)
			assert.ErrorIs(t, err, dbtypes.ErrInvalidAlias)
			assert.Empty(t, sql)
			assert.Empty(t, args)
		})
	}
}

// TestJoinFilterEqOperandsMatchFilter pins the alignment #1167 asked for: a nil
// or slice operand means the same thing at both doors. Each case asserts jf's
// rendering against f's OWN rendering rather than a literal, so the two cannot
// drift apart again — which is the defect, not any particular spelling.
func TestJoinFilterEqOperandsMatchFilter(t *testing.T) {
	const column = "u.id"

	// Only nil and list operands are aligned. A SCALAR keeps this door's own
	// `col op ?` form — squirrel spells inequality `<>` where jf has always
	// emitted `!=` — so scalars are pinned separately, below, against the
	// historical spelling rather than against f.
	operands := map[string]any{
		"nil":               nil,
		"typed_slice":       []int{1, 2},
		"any_slice":         []any{"a", "b"},
		"empty_typed_slice": []int{},
		"empty_any_slice":   []any{},
		"single_item_slice": []int{7},
		// Resolved by squirrel BEFORE its nil/list test: a Valuer reporting NULL
		// and a typed nil pointer both mean NULL. A surface-level test calls them
		// scalars and renders `col = ?` — the bug this issue removes, reappearing
		// on the operands most likely to be nil in practice.
		"null_valuer":   dbsql.NullString{},
		"typed_nil_ptr": (*int)(nil),
	}

	for _, vendor := range []dbtypes.Vendor{dbtypes.PostgreSQL, dbtypes.Oracle} {
		for name, operand := range operands {
			t.Run(vendor+"_Eq_"+name, func(t *testing.T) {
				qb := NewQueryBuilder(vendor)

				wantSQL, wantArgs, wantErr := qb.Filter().Eq(column, operand).ToSQL()
				gotSQL, gotArgs, gotErr := qb.JoinFilter().Eq(column, operand).ToSQL()

				require.NoError(t, wantErr)
				require.NoError(t, gotErr)
				assert.Equal(t, wantSQL, gotSQL)
				assert.Equal(t, wantArgs, gotArgs)
			})

			t.Run(vendor+"_NotEq_"+name, func(t *testing.T) {
				qb := NewQueryBuilder(vendor)

				wantSQL, wantArgs, wantErr := qb.Filter().NotEq(column, operand).ToSQL()
				gotSQL, gotArgs, gotErr := qb.JoinFilter().NotEq(column, operand).ToSQL()

				require.NoError(t, wantErr)
				require.NoError(t, gotErr)
				assert.Equal(t, wantSQL, gotSQL)
				assert.Equal(t, wantArgs, gotArgs)
			})
		}
	}
}

// TestJoinFilterScalarRenderingUnchanged pins what the alignment must NOT move:
// a scalar keeps `= ?` / `!= ?`, and a []byte counts as a scalar rather than a
// list — squirrel's own rule, which is why it does not become an IN.
func TestJoinFilterScalarRenderingUnchanged(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	tests := []struct {
		name    string
		filter  dbtypes.JoinFilter
		wantSQL string
		wantArg any
	}{
		{name: "eq_scalar", filter: jf.Eq("u.id", 42), wantSQL: "u.id = ?", wantArg: 42},
		{name: "not_eq_scalar", filter: jf.NotEq("u.id", 42), wantSQL: "u.id != ?", wantArg: 42},
		{name: "eq_byte_slice", filter: jf.Eq("u.id", []byte("raw")), wantSQL: "u.id = ?", wantArg: []byte("raw")},
		// A Valuer that HOLDS a value is a scalar, so it takes the placeholder
		// path — and it binds the value the door ALREADY resolved to classify it,
		// so database/sql is never asked for a second, possibly different one.
		{
			name:    "eq_set_valuer_binds_resolved",
			filter:  jf.Eq("u.id", dbsql.NullInt64{Int64: 5, Valid: true}),
			wantSQL: "u.id = ?",
			wantArg: int64(5),
		},
		{name: "not_eq_byte_slice", filter: jf.NotEq("u.id", []byte("raw")), wantSQL: "u.id != ?", wantArg: []byte("raw")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, []any{tt.wantArg}, args)
		})
	}
}

// TestJoinFilterEqEmptySliceMatchesIn pins the empty-set spelling against the
// door that already had one, rather than against a literal.
func TestJoinFilterEqEmptySliceMatchesIn(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	eqSQL, _, err := jf.Eq("u.id", []int{}).ToSQL()
	require.NoError(t, err)
	inSQL, _, err := jf.In("u.id", []int{}).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, inSQL, eqSQL)

	notEqSQL, _, err := jf.NotEq("u.id", []int{}).ToSQL()
	require.NoError(t, err)
	notInSQL, _, err := jf.NotIn("u.id", []int{}).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, notInSQL, notEqSQL)
}

// TestJoinFilterOrderingRefusesNilAndSlices pins the fail-closed half: there is
// no rendering of `col < NULL` or of an ordering against a set.
func TestJoinFilterOrderingRefusesNilAndSlices(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	jf := qb.JoinFilter()

	doors := []struct {
		name string
		op   string
		fn   func(string, any) dbtypes.JoinFilter
	}{
		{name: "lt", op: "<", fn: jf.Lt},
		{name: "lte", op: "<=", fn: jf.Lte},
		{name: "gt", op: ">", fn: jf.Gt},
		{name: "gte", op: ">=", fn: jf.Gte},
	}
	operands := map[string]any{
		"nil":           nil,
		"typed_slice":   []int{1},
		"empty_slice":   []int{},
		"null_valuer":   dbsql.NullString{},
		"typed_nil_ptr": (*int)(nil),
	}

	for _, door := range doors {
		for operandName, operand := range operands {
			t.Run(door.name+"_"+operandName, func(t *testing.T) {
				_, _, err := door.fn("u.id", operand).ToSQL()

				require.Error(t, err)
				assert.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
				assert.Contains(t, err.Error(), door.op)
			})
		}
	}

	t.Run("byte_slice_is_a_scalar_not_a_list", func(t *testing.T) {
		sql, args, err := jf.Lt("u.id", []byte("raw")).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id < ?", sql)
		assert.Equal(t, []any{[]byte("raw")}, args)
	})
}

// countingValuer records how many times database/sql's Value() contract is
// exercised, and answers the same way every time.
type countingValuer struct {
	value any
	calls int
}

func (c *countingValuer) Value() (driver.Value, error) {
	c.calls++
	return c.value, nil
}

// erroringValuer refuses to produce a value, the way a Valuer over a corrupt or
// out-of-range field does.
type erroringValuer struct{ err error }

func (e erroringValuer) Value() (driver.Value, error) { return nil, e.err }

// statefulValuer answers NULL once and a value thereafter — the shape that makes
// a second resolution observable.
type statefulValuer struct{ calls int }

func (s *statefulValuer) Value() (driver.Value, error) {
	s.calls++
	if s.calls == 1 {
		return nil, nil
	}
	return int64(42), nil
}

// TestJoinFilterResolvesValuerExactlyOnce pins the single-resolution contract:
// the door classifies and binds ONE resolution, so nothing downstream — squirrel
// or database/sql at bind time — has to ask the operand again.
func TestJoinFilterResolvesValuerExactlyOnce(t *testing.T) {
	jf := NewQueryBuilder(dbtypes.PostgreSQL).JoinFilter()

	tests := []struct {
		name    string
		value   any
		door    func(string, any) dbtypes.JoinFilter
		wantSQL string
	}{
		{name: "eq_scalar_valuer", value: int64(5), door: jf.Eq, wantSQL: "u.id = ?"},
		{name: "eq_null_valuer", value: nil, door: jf.Eq, wantSQL: "u.id IS NULL"},
		{name: "not_eq_null_valuer", value: nil, door: jf.NotEq, wantSQL: "u.id IS NOT NULL"},
		{name: "lt_scalar_valuer", value: int64(5), door: jf.Lt, wantSQL: "u.id < ?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operand := &countingValuer{value: tt.value}

			sql, _, err := tt.door("u.id", operand).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, 1, operand.calls, "Value() must be resolved exactly once")
		})
	}
}

// TestJoinFilterValuerErrorSurfacesCause pins that a Valuer failure travels by
// identity instead of being flattened into the ordering sentinel, which is what
// discarded the cause: a Valuer that cannot answer says nothing about whether
// the operand is comparable.
func TestJoinFilterValuerErrorSurfacesCause(t *testing.T) {
	errBoom := errors.New("valuer exploded")
	operand := erroringValuer{err: errBoom}
	jf := NewQueryBuilder(dbtypes.PostgreSQL).JoinFilter()

	doors := []struct {
		name string
		fn   func(string, any) dbtypes.JoinFilter
	}{
		{name: "eq", fn: jf.Eq},
		{name: "not_eq", fn: jf.NotEq},
		{name: "lt", fn: jf.Lt},
		{name: "lte", fn: jf.Lte},
		{name: "gt", fn: jf.Gt},
		{name: "gte", fn: jf.Gte},
	}

	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			sql, args, err := door.fn("u.id", operand).ToSQL()

			require.Error(t, err)
			assert.ErrorIs(t, err, errBoom)
			assert.NotErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
			assert.Empty(t, sql)
			assert.Empty(t, args)
		})
	}
}

// TestJoinFilterRendersFirstResolutionOfStatefulValuer pins the half a counter
// cannot see: when a second resolution WOULD differ, the rendering is the one
// the door classified. Asking twice used to render `u.id = ?` bound to the
// SECOND answer under a classification made from the first.
func TestJoinFilterRendersFirstResolutionOfStatefulValuer(t *testing.T) {
	operand := &statefulValuer{}

	sql, args, err := NewQueryBuilder(dbtypes.PostgreSQL).JoinFilter().Eq("u.id", operand).ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "u.id IS NULL", sql)
	assert.Empty(t, args)
	assert.Equal(t, 1, operand.calls)
}
