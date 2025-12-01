package builder

import (
	"errors"
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testStatusFilterSQL = "status = ?"
)

// ========== Basic Filter Tests ==========

func TestFilterEq(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		column      string
		value       any
		expectedSQL string
	}{
		{
			name:        "postgresql_eq",
			vendor:      dbtypes.PostgreSQL,
			column:      "status",
			value:       "active",
			expectedSQL: testStatusFilterSQL,
		},
		{
			name:        "oracle_eq_reserved",
			vendor:      dbtypes.Oracle,
			column:      "number",
			value:       "12345",
			expectedSQL: `"number" = ?`,
		},
		{
			name:        "oracle_eq_normal",
			vendor:      dbtypes.Oracle,
			column:      "name",
			value:       "John",
			expectedSQL: "name = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()

			filter := f.Eq(tt.column, tt.value)
			sql, args, err := filter.ToSQL()

			require.NoError(t, err)
			assert.Contains(t, sql, tt.expectedSQL)
			assert.Equal(t, []any{tt.value}, args)
		})
	}
}

func TestFilterComparisonOperators(t *testing.T) {
	tests := []struct {
		name        string
		setupFilter func(dbtypes.FilterFactory) dbtypes.Filter
		expectedSQL string
		expectedArg any
	}{
		{
			name:        "NotEq",
			setupFilter: func(f dbtypes.FilterFactory) dbtypes.Filter { return f.NotEq("status", "deleted") },
			expectedSQL: "status <> ?",
			expectedArg: "deleted",
		},
		{
			name:        "Lt",
			setupFilter: func(f dbtypes.FilterFactory) dbtypes.Filter { return f.Lt("age", 18) },
			expectedSQL: "age < ?",
			expectedArg: 18,
		},
		{
			name:        "Lte",
			setupFilter: func(f dbtypes.FilterFactory) dbtypes.Filter { return f.Lte("price", 100.0) },
			expectedSQL: "price <= ?",
			expectedArg: 100.0,
		},
		{
			name:        "Gt",
			setupFilter: func(f dbtypes.FilterFactory) dbtypes.Filter { return f.Gt("score", 50) },
			expectedSQL: "score > ?",
			expectedArg: 50,
		},
		{
			name:        "Gte",
			setupFilter: func(f dbtypes.FilterFactory) dbtypes.Filter { return f.Gte("balance", 1000) },
			expectedSQL: "balance >= ?",
			expectedArg: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			f := qb.Filter()

			filter := tt.setupFilter(f)
			sql, args, err := filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Len(t, args, 1)
			assert.Equal(t, tt.expectedArg, args[0])
		})
	}
}

func TestFilterInNotIn(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	t.Run("In with slice", func(t *testing.T) {
		filterIn := f.In("status", []string{"active", "pending"})
		sql, args, err := filterIn.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "status IN (?,?)", sql)
		assert.Equal(t, []any{"active", "pending"}, args)
	})

	t.Run("In with scalar (normalized to slice)", func(t *testing.T) {
		filterIn := f.In("status", "active")
		sql, args, err := filterIn.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "status IN (?)", sql)
		assert.Equal(t, []any{"active"}, args)
	})

	t.Run("In with nil (empty slice - always false)", func(t *testing.T) {
		filterIn := f.In("status", nil)
		sql, args, err := filterIn.ToSQL()
		require.NoError(t, err)
		// Squirrel generates (1=0) for empty IN - logically "always false"
		assert.Equal(t, "(1=0)", sql)
		assert.Empty(t, args)
	})

	t.Run("NotIn with slice", func(t *testing.T) {
		filterNotIn := f.NotIn("status", []string{"deleted", "banned"})
		sql, args, err := filterNotIn.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "status NOT IN (?,?)", sql)
		assert.Equal(t, []any{"deleted", "banned"}, args)
	})

	t.Run("NotIn with scalar (normalized to slice)", func(t *testing.T) {
		filterNotIn := f.NotIn("status", "deleted")
		sql, args, err := filterNotIn.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "status NOT IN (?)", sql)
		assert.Equal(t, []any{"deleted"}, args)
	})

	t.Run("NotIn with nil (empty slice - always true)", func(t *testing.T) {
		filterNotIn := f.NotIn("status", nil)
		sql, args, err := filterNotIn.ToSQL()
		require.NoError(t, err)
		// Squirrel generates (1=1) for empty NOT IN - logically "always true"
		assert.Equal(t, "(1=1)", sql)
		assert.Empty(t, args)
	})
}

func TestFilterLike(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		expectedSQL string
	}{
		{
			name:        "postgresql_ilike",
			vendor:      dbtypes.PostgreSQL,
			expectedSQL: "name ILIKE ?",
		},
		{
			name:        "oracle_upper",
			vendor:      dbtypes.Oracle,
			expectedSQL: "UPPER(name) LIKE ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()

			filter := f.Like("name", "john")
			sql, args, err := filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Len(t, args, 1)
		})
	}
}

func TestFilterNullNotNull(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// Test NULL
	filterNull := f.Null("deleted_at")
	sql, args, err := filterNull.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "deleted_at IS NULL", sql)
	assert.Empty(t, args)

	// Test NOT NULL
	filterNotNull := f.NotNull("email")
	sql, args, err = filterNotNull.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "email IS NOT NULL", sql)
	assert.Empty(t, args)
}

func TestFilterBetween(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	filter := f.Between("age", 18, 65)
	sql, args, err := filter.ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "(age >= ? AND age <= ?)", sql)
	assert.Equal(t, []any{18, 65}, args)
}

// ========== Logical Operator Tests ==========

func TestFilterAnd(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	filter := f.And(
		f.Eq("status", "active"),
		f.Gt("age", 18),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(status = ? AND age > ?)", sql)
	assert.Equal(t, []any{"active", 18}, args)
}

func TestFilterOr(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	filter := f.Or(
		f.Eq("status", "active"),
		f.Eq("role", "admin"),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(status = ? OR role = ?)", sql)
	assert.Equal(t, []any{"active", "admin"}, args)
}

func TestFilterNot(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	filter := f.Not(f.Eq("status", "deleted"))
	sql, args, err := filter.ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "NOT (status = ?)", sql)
	assert.Equal(t, []any{"deleted"}, args)
}

// ========== Complex Nested Logic Tests ==========

func TestFilterNestedAndOr(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// (status = 'active' OR status = 'pending') AND age > 18
	filter := f.And(
		f.Or(
			f.Eq("status", "active"),
			f.Eq("status", "pending"),
		),
		f.Gt("age", 18),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "((status = ? OR status = ?) AND age > ?)", sql)
	assert.Equal(t, []any{"active", "pending", 18}, args)
}

func TestFilterComplexNestedLogic(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// ((status = 'active' OR role = 'admin') AND balance > 1000) OR vip = true
	filter := f.Or(
		f.And(
			f.Or(
				f.Eq("status", "active"),
				f.Eq("role", "admin"),
			),
			f.Gt("balance", 1000),
		),
		f.Eq("vip", true),
	)

	sql, args, err := filter.ToSQL()
	require.NoError(t, err)
	// Verify structure (exact SQL may vary slightly) - filters use ? placeholders
	assert.Contains(t, sql, testStatusFilterSQL)
	assert.Contains(t, sql, "role = ?")
	assert.Contains(t, sql, "balance > ?")
	assert.Contains(t, sql, "vip = ?")
	assert.Equal(t, []any{"active", "admin", 1000, true}, args)
}

// ========== Integration with SelectQueryBuilder ==========

func TestFilterWithSelectQueryBuilder(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	query := qb.Select("*").From("users").Where(f.Eq("status", "active"))
	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM users WHERE status = $1", sql)
	assert.Equal(t, []any{"active"}, args)
}

func TestFilterMultipleWhereCallsWithAnd(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// Multiple Where() calls should be ANDed together
	query := qb.Select("*").From("users").
		Where(f.Eq("status", "active")).
		Where(f.Gt("age", 18))

	sql, args, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "WHERE")
	assert.Contains(t, sql, "status = $1")
	assert.Contains(t, sql, "age > $2")
	assert.Equal(t, []any{"active", 18}, args)
}

func TestFilterOrWithSelectQueryBuilder(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	query := qb.Select("*").From("users").Where(f.Or(
		f.Eq("status", "active"),
		f.Eq("role", "admin"),
	))

	sql, args, err := query.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM users WHERE (status = $1 OR role = $2)", sql)
	assert.Equal(t, []any{"active", "admin"}, args)
}

// ========== Oracle-Specific Tests ==========

func TestFilterOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	tests := []struct {
		name   string
		column string
	}{
		{"number", "number"},
		{"level", "level"},
		{"size", "size"},
		{"access", "access"},
		{"order", "order"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := f.Eq(tt.column, "value")
			sql, args, err := filter.ToSQL()

			require.NoError(t, err)
			// Verify quoting
			assert.Contains(t, sql, `"`+tt.column+`"`)
			assert.Equal(t, []any{"value"}, args)
		})
	}
}

func TestFilterOraclePlaceholders(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	// Test Oracle placeholder format in a full query (not just the filter)
	query := qb.Select("*").From("users").Where(f.And(
		f.Eq("id", 1),
		f.Eq("name", "test"),
	))

	sql, args, err := query.ToSQL()
	require.NoError(t, err)
	// Oracle uses :1, :2 placeholders
	assert.Contains(t, sql, ":1")
	assert.Contains(t, sql, ":2")
	assert.Equal(t, []any{1, "test"}, args)
}

// ========== Raw Filter Tests ==========

func TestFilterRaw(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// Test Raw filter in a full query to verify placeholder formatting
	query := qb.Select("*").From("users").Where(f.Raw("UPPER(name) = ?", "JOHN"))
	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, "UPPER(name) = $1")
	assert.Equal(t, []any{"JOHN"}, args)
}

func TestFilterRawOracleReservedWord(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	// Test Raw filter with Oracle reserved words in a full query
	query := qb.Select("*").From("accounts").Where(f.Raw(`"number" = ? AND ROWNUM <= ?`, "12345", 10))
	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, `"number"`)
	assert.Contains(t, sql, ":1")
	assert.Contains(t, sql, ":2")
	assert.Equal(t, []any{"12345", 10}, args)
}

// ========== Dynamic Filter Building Tests ==========

func TestFilterDynamicBuilding(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// Simulate dynamic filter building (like search filters)
	filters := []dbtypes.Filter{}

	name := "John"
	minAge := 25
	statuses := []string{"active", "pending"}

	if name != "" {
		filters = append(filters, f.Eq("name", name))
	}
	if minAge > 0 {
		filters = append(filters, f.Gt("age", minAge))
	}
	if len(statuses) > 0 {
		filters = append(filters, f.In("status", statuses))
	}

	// Combine all filters with AND and test in a full query
	query := qb.Select("*").From("users").Where(f.And(filters...))
	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, "name = $1")
	assert.Contains(t, sql, "age > $2")
	assert.Contains(t, sql, "status IN")
	assert.Equal(t, []any{"John", 25, "active", "pending"}, args)
}

func TestFilterEmptyDynamicBuilding(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// Empty filters slice
	filters := []dbtypes.Filter{}

	// And() with no filters should work gracefully
	// Squirrel produces (1=1) for empty And(), which is logically "always true"
	finalFilter := f.And(filters...)
	sql, args, err := finalFilter.ToSQL()

	require.NoError(t, err)
	// Squirrel's And() with no conditions produces a tautology
	assert.Equal(t, "(1=1)", sql)
	assert.Empty(t, args)
}

// ========== Nil Filter Handling Tests ==========

func TestFilterAndOrNilHandling(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	t.Run("And with nil filters", func(t *testing.T) {
		// Mix of nil and valid filters
		validFilter := f.Eq("status", "active")
		combinedFilter := f.And(
			nil,                  // nil should be skipped
			validFilter,          // valid filter
			nil,                  // another nil
			f.Gt("balance", 100), // another valid filter
		)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, testStatusFilterSQL)
		assert.Contains(t, sql, "balance > ?")
		assert.Contains(t, sql, "AND")
		assert.Equal(t, []any{"active", 100}, args)
	})

	t.Run("And with all nil filters", func(t *testing.T) {
		// All nil filters should produce empty And
		combinedFilter := f.And(nil, nil, nil)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		// Empty And() produces (1=1)
		assert.Equal(t, "(1=1)", sql)
		assert.Empty(t, args)
	})

	t.Run("Or with nil filters", func(t *testing.T) {
		// Mix of nil and valid filters
		combinedFilter := f.Or(
			nil,
			f.Eq("status", "pending"),
			nil,
			f.Eq("status", "active"),
		)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, testStatusFilterSQL)
		assert.Contains(t, sql, "OR")
		assert.Equal(t, []any{"pending", "active"}, args)
	})

	t.Run("Or with all nil filters", func(t *testing.T) {
		// All nil filters should produce empty Or
		combinedFilter := f.Or(nil, nil)

		sql, args, err := combinedFilter.ToSQL()
		require.NoError(t, err)
		// Empty Or() produces (1=0)
		assert.Equal(t, "(1=0)", sql)
		assert.Empty(t, args)
	})

	t.Run("Nested And/Or with nil filters", func(t *testing.T) {
		// Complex nesting with nils mixed in
		complexFilter := f.And(
			nil,
			f.Or(
				nil,
				f.Eq("type", "premium"),
				nil,
			),
			nil,
			f.Gt("age", 18),
		)

		sql, args, err := complexFilter.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "type = ?")
		assert.Contains(t, sql, "age > ?")
		assert.Equal(t, []any{"premium", 18}, args)
	})
}

// TestFilterNilDoesNotPanic ensures nil filters don't cause panics when ToSQL is called
func TestFilterNilDoesNotPanic(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// This should not panic (was the bug before the fix)
	assert.NotPanics(t, func() {
		filter := f.And(nil, f.Eq("id", 1))
		_, _, _ = filter.ToSQL()
	})

	assert.NotPanics(t, func() {
		filter := f.Or(nil, f.Eq("status", "active"), nil)
		_, _, _ = filter.ToSQL()
	})
}

// ========== Subquery Support Tests ==========

func TestFilterExists(t *testing.T) {
	t.Run("Simple EXISTS with basic subquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("id").
			From("categories").
			Where(f.Eq("status", "active"))

		query := qb.Select("*").
			From("products").
			Where(f.Exists(subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "WHERE EXISTS (SELECT id FROM categories WHERE status = :1)")
		assert.Equal(t, []any{"active"}, args)
	})

	t.Run("EXISTS with correlated subquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()
		jf := qb.JoinFilter()

		subquery := qb.Select("id"). // Use regular column instead of literal "1"
						From("reviews").
						Where(jf.And(
				jf.EqColumn("reviews.product_id", "p.id"),
				f.Eq("reviews.rating", 5),
			))

		query := qb.Select("p.name").
			From(dbtypes.MustTable("products").MustAs("p")).
			Where(f.Exists(subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "EXISTS (SELECT id FROM reviews WHERE")
		assert.Contains(t, sql, "reviews.product_id = p.id")
		assert.Contains(t, sql, "reviews.rating = :1")
		assert.Equal(t, []any{5}, args)
	})

	t.Run("PostgreSQL placeholder numbering", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		subquery := qb.Select("category_id").
			From("featured").
			Where(f.Eq("status", "active"))

		query := qb.Select("*").
			From("products").
			Where(f.And(
				f.Exists(subquery),
				f.Eq("in_stock", "yes"),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		// PostgreSQL uses $1, $2, etc placeholders
		assert.Contains(t, sql, "EXISTS")
		assert.Contains(t, sql, "SELECT category_id FROM featured WHERE status =")
		assert.Contains(t, sql, "in_stock =")
		// Args should contain both values
		assert.ElementsMatch(t, []any{"active", "yes"}, args)
	})

	t.Run("Oracle placeholder numbering", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("category_id").
			From("featured").
			Where(f.Eq("status", "active"))

		query := qb.Select("*").
			From("products").
			Where(f.And(
				f.Exists(subquery),
				f.Eq("in_stock", "yes"),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		// Oracle uses :1, :2, etc placeholders
		assert.Contains(t, sql, "EXISTS")
		assert.Contains(t, sql, "SELECT category_id FROM featured WHERE status =")
		assert.Contains(t, sql, "in_stock =")
		// Args should contain both values
		assert.ElementsMatch(t, []any{"active", "yes"}, args)
	})
}

func TestFilterNotExists(t *testing.T) {
	t.Run("Simple NOT EXISTS", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		subquery := qb.Select("1").
			From("orders").
			Where(f.Eq("orders.status", "pending"))

		query := qb.Select("*").
			From("customers").
			Where(f.NotExists(subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "WHERE NOT EXISTS (SELECT 1 FROM orders WHERE orders.status = $1)")
		assert.Equal(t, []any{"pending"}, args)
	})

	t.Run("NOT EXISTS with complex WHERE clause", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("id").
			From("transactions").
			Where(f.And(
				f.Eq("status", "failed"),
				f.Gt("amount", 1000),
			))

		query := qb.Select("*").
			From("accounts").
			Where(f.NotExists(subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "NOT EXISTS")
		assert.Contains(t, sql, "status = :1")
		assert.Contains(t, sql, "amount > :2")
		assert.Equal(t, []any{"failed", 1000}, args)
	})
}

func TestFilterInSubquery(t *testing.T) {
	t.Run("Simple IN with subquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		subquery := qb.Select("category_id").
			From("featured_categories").
			Where(f.Eq("active", true))

		query := qb.Select("*").
			From("products").
			Where(f.InSubquery("category_id", subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "WHERE category_id IN (SELECT category_id FROM featured_categories WHERE active = $1)")
		assert.Equal(t, []any{true}, args)
	})

	t.Run("IN with correlated subquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()
		jf := qb.JoinFilter()

		subquery := qb.Select("max_price").
			From("price_history").
			Where(jf.EqColumn("price_history.product_id", "p.id"))

		query := qb.Select("*").
			From(dbtypes.MustTable("products").MustAs("p")).
			Where(f.InSubquery("p.current_price", subquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "p.current_price IN (SELECT max_price FROM price_history WHERE price_history.product_id = p.id)")
		assert.Empty(t, args) // Correlated subquery has no args
	})

	t.Run("IN with subquery and additional WHERE filters", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		subquery := qb.Select("supplier_id").
			From("verified_suppliers").
			Where(f.Eq("region", "EU"))

		query := qb.Select("*").
			From("inventory").
			Where(f.And(
				f.InSubquery("supplier_id", subquery),
				f.Gt("quantity", 0),
				f.Eq("status", "available"),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "supplier_id IN (SELECT supplier_id FROM verified_suppliers WHERE region = $1)")
		assert.Contains(t, sql, "quantity >")
		assert.Contains(t, sql, "status =")
		// Squirrel may optimize placeholders, so just verify args content
		assert.ElementsMatch(t, []any{"EU", 0, "available"}, args)
	})

	t.Run("PostgreSQL vs Oracle placeholder handling", func(t *testing.T) {
		// PostgreSQL
		qbPG := NewQueryBuilder(dbtypes.PostgreSQL)
		fPG := qbPG.Filter()
		subqueryPG := qbPG.Select("id").From("active_users").Where(fPG.Eq("status", "verified"))
		queryPG := qbPG.Select("*").From("orders").Where(fPG.InSubquery("user_id", subqueryPG))
		sqlPG, argsPG, errPG := queryPG.ToSQL()
		require.NoError(t, errPG)
		assert.Contains(t, sqlPG, "$1")

		// Oracle
		qbOracle := NewQueryBuilder(dbtypes.Oracle)
		fOracle := qbOracle.Filter()
		subqueryOracle := qbOracle.Select("id").From("active_users").Where(fOracle.Eq("status", "verified"))
		queryOracle := qbOracle.Select("*").From("orders").Where(fOracle.InSubquery("user_id", subqueryOracle))
		sqlOracle, argsOracle, errOracle := queryOracle.ToSQL()
		require.NoError(t, errOracle)
		assert.Contains(t, sqlOracle, ":1")

		// Both should have same args
		assert.Equal(t, argsPG, argsOracle)
	})
}

func TestFilterNestedSubqueries(t *testing.T) {
	t.Run("Subquery containing another subquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		// Inner subquery: featured categories
		innerSubquery := qb.Select("category_id").
			From("trending").
			Where(f.Gte("score", 100))

		// Outer subquery: products in featured categories
		outerSubquery := qb.Select("product_id").
			From("catalog").
			Where(f.InSubquery("category_id", innerSubquery))

		// Main query: inventory for featured products
		query := qb.Select("*").
			From("inventory").
			Where(f.InSubquery("product_id", outerSubquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "product_id IN (SELECT product_id FROM catalog WHERE category_id IN (SELECT category_id FROM trending WHERE score >= :1))")
		assert.Equal(t, []any{100}, args)
	})

	t.Run("Multiple levels of nesting with EXISTS", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		// Inner EXISTS: check for reviews
		reviewSubquery := qb.Select("1").
			From("reviews").
			Where(f.And(
				f.Raw("reviews.order_id = o.id"), // Column comparison in WHERE clause
				f.Eq("reviews.rating", 5),
			))

		// Middle EXISTS: check for orders with reviews
		orderSubquery := qb.Select("1").
			From(dbtypes.MustTable("orders").MustAs("o")).
			Where(f.And(
				f.Raw("o.customer_id = c.id"), // Column comparison in WHERE clause
				f.Exists(reviewSubquery),
			))

		// Main query: customers with orders that have 5-star reviews
		query := qb.Select("c.name").
			From(dbtypes.MustTable("customers").MustAs("c")).
			Where(f.Exists(orderSubquery))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "EXISTS (SELECT 1 FROM orders o WHERE")
		assert.Contains(t, sql, "EXISTS (SELECT 1 FROM reviews WHERE")
		assert.Contains(t, sql, "reviews.rating = $1")
		assert.Equal(t, []any{5}, args)
	})
}

func TestFilterSubqueryErrorCases(t *testing.T) {
	t.Run("Nil subquery returns error on Exists", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		filter := f.Exists(nil)
		_, _, err := filter.ToSQL()
		require.Error(t, err)
		assert.True(t, errors.Is(err, dbtypes.ErrNilSubquery))
	})

	t.Run("Nil subquery returns error on NotExists", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		filter := f.NotExists(nil)
		_, _, err := filter.ToSQL()
		require.Error(t, err)
		assert.True(t, errors.Is(err, dbtypes.ErrNilSubquery))
	})

	t.Run("Nil subquery returns error on InSubquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		filter := f.InSubquery("user_id", nil)
		_, _, err := filter.ToSQL()
		require.Error(t, err)
		assert.True(t, errors.Is(err, dbtypes.ErrNilSubquery))
	})
}

func TestFilterSubqueryCombinedWithOtherFilters(t *testing.T) {
	t.Run("EXISTS combined with AND", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("1").
			From("premium_users").
			Where(f.Eq("tier", "gold"))

		query := qb.Select("*").
			From("orders").
			Where(f.And(
				f.Exists(subquery),
				f.Gt("total", 500),
				f.Eq("status", "completed"),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "EXISTS")
		assert.Contains(t, sql, "total >")
		assert.Contains(t, sql, "status =")
		// Squirrel may optimize placeholders, so just verify args content
		assert.ElementsMatch(t, []any{"gold", 500, "completed"}, args)
	})

	t.Run("NOT EXISTS combined with OR", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		subquery := qb.Select("id").
			From("blacklist").
			Where(f.Eq("active", true))

		query := qb.Select("*").
			From("applicants").
			Where(f.Or(
				f.NotExists(subquery),
				f.Eq("override", false), // Use different value to avoid placeholder reuse
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "NOT EXISTS")
		assert.Contains(t, sql, "OR")
		assert.Contains(t, sql, "override =")
		assert.Equal(t, []any{true, false}, args)
	})
}
