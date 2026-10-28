package builder

import (
	dbsql "database/sql"
	"database/sql/driver"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/testing/mocks"
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
	// SECURITY: Manual SQL review completed - test fixture uses literal column "name" and a parameterized value, no user input concatenation
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
	// SECURITY: Manual SQL review completed - "number" is double-quoted for Oracle reserved-word handling; ROWNUM is a pseudo-column; both placeholders are parameterized
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

		subquery := qb.Select(qb.MustExpr("1")).
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
		// Placeholders must be contiguous across the subquery AND the outer filters:
		// region=$1 (subquery), quantity=$2, status=$3. A duplicate-numbering bug
		// (subquery $1 colliding with the outer filters' $1/$2) makes pgx Bind fail with
		// "bind message supplies 3 parameters, but prepared statement requires 2".
		assert.Contains(t, sql, "supplier_id IN (SELECT supplier_id FROM verified_suppliers WHERE region = $1)")
		assert.Contains(t, sql, "quantity > $2")
		assert.Contains(t, sql, "status = $3")
		assert.NotContains(t, sql, "quantity > $1", "outer filter must not reuse the subquery's $1")
		assert.Equal(t, []any{"EU", 0, "available"}, args)
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

	t.Run("Oracle subquery with additional filters uses contiguous placeholders", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
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
		// Oracle :N binds must be contiguous 1..3 with no duplicate :1 — go-ora treats :1
		// as a named parameter, so a duplicate either errors or silently binds one value to
		// two positions (filter corruption).
		assert.Contains(t, sql, "region = :1")
		assert.Contains(t, sql, "quantity > :2")
		assert.Contains(t, sql, "status = :3")
		assert.NotContains(t, sql, "quantity > :1", "outer filter must not reuse the subquery's :1")
		assert.Equal(t, []any{"EU", 0, "available"}, args)
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

		// SECURITY: Manual SQL review completed - both f.Raw calls below are correlated-subquery
		// joins on literal qualified column identifiers; no user input, no identifiers requiring
		// vendor quoting (PostgreSQL fixture).
		// Inner EXISTS: check for reviews
		reviewSubquery := qb.Select(qb.MustExpr("1")).
			From("reviews").
			Where(f.And(
				f.Raw("reviews.order_id = o.id"), // Column comparison in WHERE clause
				f.Eq("reviews.rating", 5),
			))

		// Middle EXISTS: check for orders with reviews
		orderSubquery := qb.Select(qb.MustExpr("1")).
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
		assert.ErrorIs(t, err, dbtypes.ErrNilSubquery)
	})

	t.Run("Nil subquery returns error on NotExists", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		filter := f.NotExists(nil)
		_, _, err := filter.ToSQL()
		require.Error(t, err)
		assert.ErrorIs(t, err, dbtypes.ErrNilSubquery)
	})

	t.Run("Nil subquery returns error on InSubquery", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		filter := f.InSubquery("user_id", nil)
		_, _, err := filter.ToSQL()
		require.Error(t, err)
		assert.ErrorIs(t, err, dbtypes.ErrNilSubquery)
	})
}

// TestFilterInSubqueryRefusesAForeignVendorPlaceholder pins #2122: the outer
// statement numbers only `?`, so a foreign subquery that already rendered $N or
// :N is refused on either vendor, as the EXISTS and SubqueryColumn doors do.
func TestFilterInSubqueryRefusesAForeignVendorPlaceholder(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		placeholder string
	}{
		{name: "postgresql_dollar", vendor: dbtypes.PostgreSQL, placeholder: "$1"},
		{name: "postgresql_colon", vendor: dbtypes.PostgreSQL, placeholder: ":1"},
		{name: "oracle_dollar", vendor: dbtypes.Oracle, placeholder: "$1"},
		{name: "oracle_colon", vendor: dbtypes.Oracle, placeholder: ":1"},
	}
	statements := []struct {
		name  string
		build func(qb *QueryBuilder, in dbtypes.Filter) (string, []any, error)
	}{
		{name: "select_beside_another_parameter", build: func(qb *QueryBuilder, in dbtypes.Filter) (string, []any, error) {
			f := qb.Filter()
			return qb.Select(colID).From(tableUsers).Where(f.And(f.Eq("x", 1), in)).ToSQL()
		}},
		{name: "select_alone", build: func(qb *QueryBuilder, in dbtypes.Filter) (string, []any, error) {
			return qb.Select(colID).From(tableUsers).Where(in).ToSQL()
		}},
		{name: "update", build: func(qb *QueryBuilder, in dbtypes.Filter) (string, []any, error) {
			return qb.Update(tableUsers).Set("x", 1).Where(in).ToSQL()
		}},
		{name: "delete", build: func(qb *QueryBuilder, in dbtypes.Filter) (string, []any, error) {
			return qb.Delete(tableUsers).Where(in).ToSQL()
		}},
	}
	for _, tt := range tests {
		for _, stmt := range statements {
			t.Run(tt.name+"_"+stmt.name, func(t *testing.T) {
				qb := NewQueryBuilder(tt.vendor)
				sub := vendorRenderedSubquery{sql: "SELECT id FROM held WHERE consumer = " + tt.placeholder, args: []any{7}}

				_, _, err := stmt.build(qb, qb.Filter().InSubquery(colID, sub))
				require.ErrorIs(t, err, errSubqueryPlaceholderFormat)
			})
		}
	}
}

// TestFilterInSubqueryNumbersAQuestionMarkSubquery pins the accepted side of
// #2122: a foreign subquery that renders `?`, like one this package built, is
// numbered by the outer statement, after the parameter that precedes it.
func TestFilterInSubqueryNumbersAQuestionMarkSubquery(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		expectedSQL string
	}{
		{
			name:        "postgresql",
			vendor:      dbtypes.PostgreSQL,
			expectedSQL: "SELECT id FROM users WHERE (x = $1 AND id IN (SELECT id FROM held WHERE consumer = $2))",
		},
		{
			name:        "oracle",
			vendor:      dbtypes.Oracle,
			expectedSQL: "SELECT id FROM users WHERE (x = :1 AND id IN (SELECT id FROM held WHERE consumer = :2))",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"_foreign", func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()
			sub := vendorRenderedSubquery{sql: "SELECT id FROM held WHERE consumer = ?", args: []any{7}}

			sql, args, err := qb.Select(colID).From(tableUsers).Where(f.And(f.Eq("x", 1), f.InSubquery(colID, sub))).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Equal(t, []any{1, 7}, args)
		})
		// A subquery this package built renders to the same statement (#2122 AC4).
		t.Run(tt.name+"_package", func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()
			sub := qb.Select(colID).From("held").Where(f.Eq("consumer", 7))

			sql, args, err := qb.Select(colID).From(tableUsers).Where(f.And(f.Eq("x", 1), f.InSubquery(colID, sub))).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Equal(t, []any{1, 7}, args)
		})
	}
}

// TestFilterInSubqueryRefusesAMockQueryBuilderVendorPlaceholder covers the
// shipped double: testing/mocks.MockQueryBuilder is a foreign implementation too.
func TestFilterInSubqueryRefusesAMockQueryBuilderVendorPlaceholder(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	sub := &mocks.MockQueryBuilder{}
	sub.On("ToSQL").Return("SELECT id FROM held WHERE consumer = $1", nil, nil)

	_, _, err := qb.Select(colID).From(tableUsers).Where(qb.Filter().InSubquery(colID, sub)).ToSQL()
	require.ErrorIs(t, err, errSubqueryPlaceholderFormat)
}

func TestFilterSubqueryCombinedWithOtherFilters(t *testing.T) {
	t.Run("EXISTS combined with AND", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select(qb.MustExpr("1")).
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

// ========== Regex Filter Tests ==========

func TestFilterRegex(t *testing.T) {
	const pattern = `^[a-z]+@example\.com$`

	tests := []struct {
		name        string
		vendor      string
		method      func(ff dbtypes.FilterFactory) dbtypes.Filter
		expectedSQL string
	}{
		// PostgreSQL: ~, ~*, !~, !~*
		{
			name:        "postgresql_regex_cs",
			vendor:      dbtypes.PostgreSQL,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Regex("email", pattern) },
			expectedSQL: "email ~ ?",
		},
		{
			name:        "postgresql_regex_ci",
			vendor:      dbtypes.PostgreSQL,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.RegexI("email", pattern) },
			expectedSQL: "email ~* ?",
		},
		{
			name:        "postgresql_not_regex_cs",
			vendor:      dbtypes.PostgreSQL,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotRegex("email", pattern) },
			expectedSQL: "email !~ ?",
		},
		{
			name:        "postgresql_not_regex_ci",
			vendor:      dbtypes.PostgreSQL,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotRegexI("email", pattern) },
			expectedSQL: "email !~* ?",
		},
		// Oracle: REGEXP_LIKE, optionally with 'i' flag and NOT prefix
		{
			name:        "oracle_regex_cs",
			vendor:      dbtypes.Oracle,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Regex("email", pattern) },
			expectedSQL: "REGEXP_LIKE(email, ?)",
		},
		{
			name:        "oracle_regex_ci",
			vendor:      dbtypes.Oracle,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.RegexI("email", pattern) },
			expectedSQL: "REGEXP_LIKE(email, ?, ?)",
		},
		{
			name:        "oracle_not_regex_cs",
			vendor:      dbtypes.Oracle,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotRegex("email", pattern) },
			expectedSQL: "NOT (REGEXP_LIKE(email, ?))",
		},
		{
			name:        "oracle_not_regex_ci",
			vendor:      dbtypes.Oracle,
			method:      func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotRegexI("email", pattern) },
			expectedSQL: "NOT (REGEXP_LIKE(email, ?, ?))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()

			sql, args, err := tt.method(f).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			require.NotEmpty(t, args)
			assert.Equal(t, pattern, args[0])
			// Oracle case-insensitive variants pass 'i' as the second arg
			if len(args) == 2 {
				assert.Equal(t, "i", args[1])
			}
		})
	}
}

func TestFilterRegexQuotesOracleReservedWords(t *testing.T) {
	// "number" is an Oracle reserved word and must be auto-quoted.
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	sql, args, err := f.Regex("number", `\d+`).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `REGEXP_LIKE("number", ?)`, sql)
	assert.Equal(t, []any{`\d+`}, args)
}

func TestFilterRegexUnsupportedVendor(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Vendor("mysql"))
	f := qb.Filter()

	_, _, err := f.Regex("col", "pat").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regex matching is not supported")
}

func TestFilterRegexInWhereClause(t *testing.T) {
	// End-to-end: regex composes with other filters and gets correct placeholders.
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	sql, args, err := qb.Select("id").
		From("users").
		Where(f.And(
			f.Eq("status", "active"),
			f.RegexI("email", `@example\.com$`),
		)).
		ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users WHERE (status = $1 AND email ~* $2)", sql)
	assert.Equal(t, []any{"active", `@example\.com$`}, args)
}

// ========== JSONContains Filter Tests ==========

func TestFilterJSONContainsPostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	type doc struct {
		Role string `json:"role"`
	}

	tests := []struct {
		name         string
		value        any
		expectedJSON string
	}{
		{name: "string_passthrough", value: `{"role":"admin"}`, expectedJSON: `{"role":"admin"}`},
		{name: "bytes_passthrough", value: []byte(`{"role":"admin"}`), expectedJSON: `{"role":"admin"}`},
		{name: "raw_message_passthrough", value: json.RawMessage(`{"role":"admin"}`), expectedJSON: `{"role":"admin"}`},
		{name: "struct_marshal", value: doc{Role: "admin"}, expectedJSON: `{"role":"admin"}`},
		{name: "map_marshal", value: map[string]any{"role": "admin"}, expectedJSON: `{"role":"admin"}`},
		{name: "slice_marshal", value: []string{"a", "b"}, expectedJSON: `["a","b"]`},
		{name: "nil_value", value: nil, expectedJSON: jsonLiteralNull},
		// Typed-nil byte slices map to the JSON literal "null" rather than
		// silently sending an empty parameter.
		{name: "nil_bytes", value: []byte(nil), expectedJSON: jsonLiteralNull},
		{name: "nil_raw_message", value: json.RawMessage(nil), expectedJSON: jsonLiteralNull},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := f.JSONContains("metadata", tt.value).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, "metadata @> ?::jsonb", sql)
			require.Len(t, args, 1)
			assert.Equal(t, tt.expectedJSON, args[0])
		})
	}
}

func TestFilterJSONContainsOracleNotImplemented(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	_, _, err := f.JSONContains("metadata", `{"role":"admin"}`).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Oracle support not implemented")
}

func TestFilterJSONContainsMarshalError(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	// channels are not JSON-marshallable
	_, _, err := f.JSONContains("metadata", make(chan int)).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSONContains:")
}

func TestFilterJSONContainsRejectsInvalidPreEncodedJSON(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	tests := []struct {
		name  string
		value any
	}{
		{name: "string_garbage", value: `{not json`},
		{name: "string_empty", value: ""},
		{name: "bytes_garbage", value: []byte(`{not json`)},
		{name: "raw_message_garbage", value: json.RawMessage(`{not json`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := f.JSONContains("metadata", tt.value).ToSQL()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid pre-encoded JSON")
		})
	}
}

func TestFilterJSONContainsInWhereClause(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	sql, args, err := qb.Select("id").
		From("users").
		Where(f.JSONContains("metadata", map[string]any{"role": "admin"})).
		ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users WHERE metadata @> $1::jsonb", sql)
	assert.Equal(t, []any{`{"role":"admin"}`}, args)
}

// TestFilterColumnsValidateIdentifiers closes ADR-082's last and widest stage.
// Until now a Filter column was interpolated with no grammar check — verbatim on
// PostgreSQL, where quoteColumnForQuery is the identity function, so a filter
// column was a live tautology-injection slot for any caller routing request text
// into one.
func TestFilterColumnsValidateIdentifiers(t *testing.T) {
	payloads := []struct{ name, column string }{
		{name: "boolean_bypass", column: `id = 1 OR 1=1 -- `},
		{name: "quote_breakout", column: `x" = 1 OR 1=1 --`},
		{name: "paren_breakout", column: `id) OR 1=1 --`},
	}

	for _, vendor := range []dbtypes.Vendor{dbtypes.PostgreSQL, dbtypes.Oracle} {
		qb := NewQueryBuilder(vendor)
		f := qb.Filter()
		jf := qb.JoinFilter()

		single := map[string]func(column string) dbtypes.Filter{
			"Eq":       func(c string) dbtypes.Filter { return f.Eq(c, 1) },
			"NotEq":    func(c string) dbtypes.Filter { return f.NotEq(c, 1) },
			"Lt":       func(c string) dbtypes.Filter { return f.Lt(c, 1) },
			"Lte":      func(c string) dbtypes.Filter { return f.Lte(c, 1) },
			"Gt":       func(c string) dbtypes.Filter { return f.Gt(c, 1) },
			"Gte":      func(c string) dbtypes.Filter { return f.Gte(c, 1) },
			"In":       func(c string) dbtypes.Filter { return f.In(c, []any{1, 2}) },
			"NotIn":    func(c string) dbtypes.Filter { return f.NotIn(c, []any{1, 2}) },
			"Like":     func(c string) dbtypes.Filter { return f.Like(c, "x%") },
			"Null":     func(c string) dbtypes.Filter { return f.Null(c) },
			"NotNull":  func(c string) dbtypes.Filter { return f.NotNull(c) },
			"Between":  func(c string) dbtypes.Filter { return f.Between(c, 1, 2) },
			"Regex":    func(c string) dbtypes.Filter { return f.Regex(c, "^x") },
			"RegexI":   func(c string) dbtypes.Filter { return f.RegexI(c, "^x") },
			"NotRegex": func(c string) dbtypes.Filter { return f.NotRegex(c, "^x") },
		}

		// Every column-taking JoinFilter door, and for the six two-column forms
		// BOTH sides — a pair door validating only its left argument would pass a
		// left-only matrix while leaving the right side wide open.
		joins := map[string]func(column string) dbtypes.JoinFilter{
			"EqColumn_left":     func(c string) dbtypes.JoinFilter { return jf.EqColumn(c, "b.id") },
			"EqColumn_right":    func(c string) dbtypes.JoinFilter { return jf.EqColumn("a.id", c) },
			"NotEqColumn_left":  func(c string) dbtypes.JoinFilter { return jf.NotEqColumn(c, "b.id") },
			"NotEqColumn_right": func(c string) dbtypes.JoinFilter { return jf.NotEqColumn("a.id", c) },
			"LtColumn_left":     func(c string) dbtypes.JoinFilter { return jf.LtColumn(c, "b.id") },
			"LtColumn_right":    func(c string) dbtypes.JoinFilter { return jf.LtColumn("a.id", c) },
			"LteColumn_left":    func(c string) dbtypes.JoinFilter { return jf.LteColumn(c, "b.id") },
			"LteColumn_right":   func(c string) dbtypes.JoinFilter { return jf.LteColumn("a.id", c) },
			"GtColumn_left":     func(c string) dbtypes.JoinFilter { return jf.GtColumn(c, "b.id") },
			"GtColumn_right":    func(c string) dbtypes.JoinFilter { return jf.GtColumn("a.id", c) },
			"GteColumn_left":    func(c string) dbtypes.JoinFilter { return jf.GteColumn(c, "b.id") },
			"GteColumn_right":   func(c string) dbtypes.JoinFilter { return jf.GteColumn("a.id", c) },
			"jf.Eq":             func(c string) dbtypes.JoinFilter { return jf.Eq(c, 1) },
			"jf.NotEq":          func(c string) dbtypes.JoinFilter { return jf.NotEq(c, 1) },
			"jf.Lt":             func(c string) dbtypes.JoinFilter { return jf.Lt(c, 1) },
			"jf.Lte":            func(c string) dbtypes.JoinFilter { return jf.Lte(c, 1) },
			"jf.Gt":             func(c string) dbtypes.JoinFilter { return jf.Gt(c, 1) },
			"jf.Gte":            func(c string) dbtypes.JoinFilter { return jf.Gte(c, 1) },
			"jf.In":             func(c string) dbtypes.JoinFilter { return jf.In(c, []any{1, 2}) },
			"jf.NotIn":          func(c string) dbtypes.JoinFilter { return jf.NotIn(c, []any{1, 2}) },
			"jf.Like":           func(c string) dbtypes.JoinFilter { return jf.Like(c, "x%") },
			"jf.Null":           func(c string) dbtypes.JoinFilter { return jf.Null(c) },
			"jf.NotNull":        func(c string) dbtypes.JoinFilter { return jf.NotNull(c) },
			"jf.Between":        func(c string) dbtypes.JoinFilter { return jf.Between(c, 1, 2) },
		}

		for _, p := range payloads {
			for name, build := range single {
				t.Run(vendor+"_"+name+"_rejects_"+p.name, func(t *testing.T) {
					_, _, err := qb.Select("id").From("users").Where(build(p.column)).ToSQL()
					require.Error(t, err)
					require.ErrorContains(t, err, "identifier",
						"rejected, but not because of the column identifier")
				})
			}
			for name, build := range joins {
				t.Run(vendor+"_"+name+"_rejects_"+p.name, func(t *testing.T) {
					_, _, err := qb.Select("id").From("a").JoinOn("b", build(p.column)).ToSQL()
					require.Error(t, err)
					require.ErrorContains(t, err, "identifier",
						"rejected, but not because of the column identifier")
				})
			}
		}

		t.Run(vendor+"_legitimate_columns_still_build", func(t *testing.T) {
			sql, _, err := qb.Select("id").From("users").
				Where(f.And(f.Eq("status", "active"), f.Gt("u.age", 18), f.Null(`"level"`))).ToSQL()
			require.NoError(t, err)
			require.NotEmpty(t, sql)
		})
	}
}

// TestFilterEqNilAndSliceRendering pins f.Eq's operand contract, which the
// package inherited from the underlying builder and never asserted. #1167 aligns
// jf.Eq to it, so it needs to be nailed down in its own right rather than only
// as the reference the JoinFilter tests compare against.
func TestFilterEqNilAndSliceRendering(t *testing.T) {
	tests := []struct {
		name    string
		vendor  dbtypes.Vendor
		filter  func(ff dbtypes.FilterFactory) dbtypes.Filter
		wantSQL string
		wantLen int
	}{
		{
			name:   "postgresql_eq_nil_is_null",
			vendor: dbtypes.PostgreSQL,
			filter: func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Eq("status", nil) },
			// nil is NOT a placeholder: `status = ?` bound to nil is never true.
			wantSQL: "status IS NULL",
		},
		{
			name:    "postgresql_not_eq_nil_is_not_null",
			vendor:  dbtypes.PostgreSQL,
			filter:  func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotEq("status", nil) },
			wantSQL: "status IS NOT NULL",
		},
		{
			name:    "postgresql_eq_slice_expands_to_in",
			vendor:  dbtypes.PostgreSQL,
			filter:  func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Eq("status", []int{1, 2}) },
			wantSQL: "status IN (?,?)",
			wantLen: 2,
		},
		{
			name:    "postgresql_eq_empty_slice_is_constant_false",
			vendor:  dbtypes.PostgreSQL,
			filter:  func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Eq("status", []int{}) },
			wantSQL: "(1=0)",
		},
		{
			name:    "postgresql_not_eq_empty_slice_is_constant_true",
			vendor:  dbtypes.PostgreSQL,
			filter:  func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.NotEq("status", []int{}) },
			wantSQL: "(1=1)",
		},
		{
			name:    "oracle_eq_nil_is_null_on_a_reserved_column",
			vendor:  dbtypes.Oracle,
			filter:  func(ff dbtypes.FilterFactory) dbtypes.Filter { return ff.Eq("level", nil) },
			wantSQL: `"level" IS NULL`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)

			sql, args, err := tt.filter(qb.Filter()).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Len(t, args, tt.wantLen)
		})
	}
}

// TestFilterEqNilPointerToValuer pins the reproducer from #1209: a nil pointer
// to a type whose Value() has a VALUE receiver satisfies driver.Valuer, so
// squirrel's Eq prologue asserts the interface and dereferences nil before it
// ever reaches its own pointer test. The door must settle the pointer first and
// render the NULL the JoinFilter door renders for the same operand.
func TestFilterEqNilPointerToValuer(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args, err := qb.Filter().Eq("u.id", (*dbsql.NullString)(nil)).ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "u.id IS NULL", sql)
	assert.Empty(t, args)
}

// TestFilterNotEqNilPointerToValuer is the NotEq half of #1209's reproducer.
func TestFilterNotEqNilPointerToValuer(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args, err := qb.Filter().NotEq("u.id", (*dbsql.NullString)(nil)).ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "u.id IS NOT NULL", sql)
	assert.Empty(t, args)
}

// TestFilterOrderingRefusesNilSlicesAndArrays is the FilterFactory mirror of the
// JoinFilter ordering contract (#1167, #1205): there is no rendering of
// `col < NULL` or of an ordering against a set, so the door fails closed with
// the shared sentinel instead of delegating to squirrel — which rendered
// `col < ?` for a typed nil pointer and PANICKED on a nil pointer to a Valuer.
func TestFilterOrderingRefusesNilSlicesAndArrays(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	doors := []struct {
		name string
		op   string
		fn   func(string, any) dbtypes.Filter
	}{
		{name: "lt", op: "<", fn: f.Lt},
		{name: "lte", op: "<=", fn: f.Lte},
		{name: "gt", op: ">", fn: f.Gt},
		{name: "gte", op: ">=", fn: f.Gte},
	}
	operands := map[string]any{
		"nil":              nil,
		"typed_slice":      []int{1},
		"empty_slice":      []int{},
		"typed_array":      [3]int{1, 2, 3},
		"empty_array":      [0]int{},
		"null_valuer":      dbsql.NullString{},
		"typed_nil_ptr":    (*int)(nil),
		"nil_valuer_ptr":   (*dbsql.NullString)(nil),
		"nil_ptr_receiver": (*panickingPtrValuer)(nil),
	}

	for _, door := range doors {
		for operandName, operand := range operands {
			t.Run(door.name+"_"+operandName, func(t *testing.T) {
				_, _, err := door.fn("u.id", operand).ToSQL()

				require.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
				assert.Contains(t, err.Error(), door.op)
			})
		}
	}

	t.Run("byte_slice_is_a_scalar_not_a_list", func(t *testing.T) {
		sql, args, err := f.Lt("u.id", []byte("raw")).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id < ?", sql)
		assert.Equal(t, []any{[]byte("raw")}, args)
	})
}

// TestBetweenRefusesNilAndSetBoundsAtBothFactories pins Between at BOTH
// families: a BETWEEN bound is an ordering operand, so nil, a set and a Valuer
// reporting NULL take the same sentinel the ordering doors take, and a nil
// pointer to a Valuer no longer panics (#1205, #1209). Each bound is checked in
// turn against a valid partner so neither position is left unpinned.
func TestBetweenRefusesNilAndSetBoundsAtBothFactories(t *testing.T) {
	operands := map[string]any{
		"nil":              nil,
		"typed_slice":      []int{1},
		"null_valuer":      dbsql.NullString{},
		"typed_nil_ptr":    (*int)(nil),
		"nil_valuer_ptr":   (*dbsql.NullString)(nil),
		"nil_ptr_receiver": (*panickingPtrValuer)(nil),
	}
	families := []struct {
		name string
		op   string
		call func(qb *QueryBuilder, lower, upper any) (string, []any, error)
	}{
		{
			name: "filter",
			call: func(qb *QueryBuilder, lower, upper any) (string, []any, error) {
				return qb.Filter().Between("u.id", lower, upper).ToSQL()
			},
		},
		{
			name: "join_filter",
			call: func(qb *QueryBuilder, lower, upper any) (string, []any, error) {
				return qb.JoinFilter().Between("u.id", lower, upper).ToSQL()
			},
		},
	}

	for _, family := range families {
		for operandName, operand := range operands {
			t.Run(family.name+"_lower_"+operandName, func(t *testing.T) {
				_, _, err := family.call(NewQueryBuilder(dbtypes.PostgreSQL), operand, 10)

				require.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
				assert.Contains(t, err.Error(), ">=")
			})

			t.Run(family.name+"_upper_"+operandName, func(t *testing.T) {
				_, _, err := family.call(NewQueryBuilder(dbtypes.PostgreSQL), 1, operand)

				require.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
				assert.Contains(t, err.Error(), "<=")
			})
		}

		t.Run(family.name+"_valid_bounds_bind_the_resolved_value", func(t *testing.T) {
			sql, args, err := family.call(
				NewQueryBuilder(dbtypes.PostgreSQL),
				dbsql.NullInt64{Int64: 5, Valid: true},
				10,
			)

			require.NoError(t, err)
			assert.Equal(t, "(u.id >= ? AND u.id <= ?)", sql)
			assert.Equal(t, []any{int64(5), 10}, args)
		})
	}
}

// TestInAndNotInResolveNilPointerElements pins the choice made for a nil-pointer
// element (#1209): the element is RESOLVED to NULL at build time rather than
// bound as a pointer. Binding the pointer left the crash to exec — database/sql
// asks a nil pointer-receiver Valuer for its value there — and rendered
// `IN (NULL)`, which matches nothing, without the door ever saying so. Both
// families take the same rule.
func TestInAndNotInResolveNilPointerElements(t *testing.T) {
	elements := map[string]any{
		"typed_nil_ptr":    (*int)(nil),
		"nil_valuer_ptr":   (*dbsql.NullString)(nil),
		"nil_ptr_receiver": (*panickingPtrValuer)(nil),
		"null_valuer":      dbsql.NullString{},
	}
	doors := []struct {
		name    string
		wantSQL string
		call    func(qb *QueryBuilder, values any) (string, []any, error)
	}{
		{
			name:    "filter_in",
			wantSQL: "u.id IN (?)",
			call: func(qb *QueryBuilder, values any) (string, []any, error) {
				return qb.Filter().In("u.id", values).ToSQL()
			},
		},
		{
			name:    "filter_not_in",
			wantSQL: "u.id NOT IN (?)",
			call: func(qb *QueryBuilder, values any) (string, []any, error) {
				return qb.Filter().NotIn("u.id", values).ToSQL()
			},
		},
		{
			name:    "join_filter_in",
			wantSQL: "u.id IN (?)",
			call: func(qb *QueryBuilder, values any) (string, []any, error) {
				return qb.JoinFilter().In("u.id", values).ToSQL()
			},
		},
		{
			name:    "join_filter_not_in",
			wantSQL: "u.id NOT IN (?)",
			call: func(qb *QueryBuilder, values any) (string, []any, error) {
				return qb.JoinFilter().NotIn("u.id", values).ToSQL()
			},
		},
	}

	for _, door := range doors {
		for elementName, element := range elements {
			t.Run(door.name+"_scalar_"+elementName, func(t *testing.T) {
				sql, args, err := door.call(NewQueryBuilder(dbtypes.PostgreSQL), element)

				require.NoError(t, err)
				assert.Equal(t, door.wantSQL, sql)
				assert.Equal(t, []any{nil}, args)
			})

			t.Run(door.name+"_inside_slice_"+elementName, func(t *testing.T) {
				sql, args, err := door.call(NewQueryBuilder(dbtypes.PostgreSQL), []any{element})

				require.NoError(t, err)
				assert.Equal(t, door.wantSQL, sql)
				assert.Equal(t, []any{nil}, args)
			})
		}

		t.Run(door.name+"_set_valuer_binds_the_resolved_value", func(t *testing.T) {
			sql, args, err := door.call(
				NewQueryBuilder(dbtypes.PostgreSQL),
				[]any{dbsql.NullInt64{Int64: 5, Valid: true}, 7},
			)

			require.NoError(t, err)
			assert.Contains(t, sql, "(?,?)")
			assert.Equal(t, []any{int64(5), 7}, args)
		})
	}
}

// TestInAndNotInWrapAByteSliceAsOneElement pins the one SQL text this atom
// moves at the list doors. A []byte is a driver.Value, so squirrel classified
// it as a SCALAR and rendered `col = ?` / `col <> ?` from an IN door — the
// door's own contract says a scalar is wrapped in a one-element list, and it
// now is, so the SQL says IN. The argument is unchanged: one []byte, not one
// placeholder per byte.
func TestInAndNotInWrapAByteSliceAsOneElement(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	raw := []byte("raw")

	tests := []struct {
		name    string
		filter  dbtypes.Filter
		wantSQL string
	}{
		{name: "filter_in", filter: qb.Filter().In("u.id", raw), wantSQL: "u.id IN (?)"},
		{name: "filter_not_in", filter: qb.Filter().NotIn("u.id", raw), wantSQL: "u.id NOT IN (?)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, []any{raw}, args)
		})
	}

	joinTests := []struct {
		name    string
		filter  dbtypes.JoinFilter
		wantSQL string
	}{
		{name: "join_filter_in", filter: qb.JoinFilter().In("u.id", raw), wantSQL: "u.id IN (?)"},
		{name: "join_filter_not_in", filter: qb.JoinFilter().NotIn("u.id", raw), wantSQL: "u.id NOT IN (?)"},
	}
	for _, tt := range joinTests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.filter.ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, []any{raw}, args)
		})
	}
}

// TestFilterCompareDoorsBindTheResolvedValue pins the argument half of the
// convergence: a Valuer HOLDING a value stays a scalar and keeps its SQL, but
// the door binds the value it already resolved (int64(5)) rather than the
// wrapper. Asserting on the SQL alone cannot see this — the text is `col op ?`
// either way — so it is the args that are pinned here.
func TestFilterCompareDoorsBindTheResolvedValue(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()
	held := dbsql.NullInt64{Int64: 5, Valid: true}
	pointed := 7

	tests := []struct {
		name    string
		filter  dbtypes.Filter
		wantSQL string
		wantArg any
	}{
		{name: "eq_valuer", filter: f.Eq("u.id", held), wantSQL: "u.id = ?", wantArg: int64(5)},
		{name: "not_eq_valuer", filter: f.NotEq("u.id", held), wantSQL: "u.id <> ?", wantArg: int64(5)},
		{name: "lt_valuer", filter: f.Lt("u.id", held), wantSQL: "u.id < ?", wantArg: int64(5)},
		{name: "lte_valuer", filter: f.Lte("u.id", held), wantSQL: "u.id <= ?", wantArg: int64(5)},
		{name: "gt_valuer", filter: f.Gt("u.id", held), wantSQL: "u.id > ?", wantArg: int64(5)},
		{name: "gte_valuer", filter: f.Gte("u.id", held), wantSQL: "u.id >= ?", wantArg: int64(5)},
		{name: "eq_pointer", filter: f.Eq("u.id", &pointed), wantSQL: "u.id = ?", wantArg: 7},
		{name: "lt_pointer", filter: f.Lt("u.id", &pointed), wantSQL: "u.id < ?", wantArg: 7},
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

// TestInResolvesTypedPointerSliceElements guards the fast path added for large
// lists: a slice whose ELEMENT TYPE can never be a nil pointer or a Valuer is
// passed through unresolved, because resolving it could not change any element.
// A []*int can, so it must still take the per-element path — this is the case
// that fails if the fast path's type test is widened by mistake.
func TestInResolvesTypedPointerSliceElements(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	seven := 7

	tests := []struct {
		name     string
		values   any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "typed_pointer_slice_with_a_nil_element",
			values:   []*int{nil, &seven},
			wantSQL:  "u.id IN (?,?)",
			wantArgs: []any{nil, 7},
		},
		{
			name:     "typed_valuer_slice_with_a_null_element",
			values:   []dbsql.NullInt64{{}, {Int64: 5, Valid: true}},
			wantSQL:  "u.id IN (?,?)",
			wantArgs: []any{nil, int64(5)},
		},
		{
			name:     "plain_scalar_slice_takes_the_fast_path_unchanged",
			values:   []int{1, 2},
			wantSQL:  "u.id IN (?,?)",
			wantArgs: []any{1, 2},
		},
		{
			// The constant carries no placeholder, so the argument list is empty
			// whichever path built it.
			name:     "empty_typed_slice_still_renders_the_constant",
			values:   []int{},
			wantSQL:  "(1=0)",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := qb.Filter().In("u.id", tt.values).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestOperandResolutionWalksThePointerChain pins the multi-level case: squirrel
// unwraps ONE pointer level, so a `**int` whose inner pointer is nil left a
// typed nil `*int` behind — neither `== nil` nor a list, therefore classified a
// SCALAR and handed back. Equality survived that by accident (squirrel's own
// prologue unwrapped the remaining level at render time), but an ordering door
// bound the nil pointer to `col < ?`, and a `**sql.NullString` reached the
// Valuer assertion and panicked. The chain is walked to the end at every door.
func TestOperandResolutionWalksThePointerChain(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	nilInner := (*int)(nil)
	nilValuerInner := (*dbsql.NullString)(nil)
	seven := 7
	pointedSeven := &seven

	t.Run("eq_double_pointer_to_nil_is_null", func(t *testing.T) {
		sql, args, err := f.Eq("u.id", &nilInner).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id IS NULL", sql)
		assert.Empty(t, args)
	})

	t.Run("eq_double_pointer_to_nil_valuer_is_null", func(t *testing.T) {
		require.NotPanics(t, func() {
			sql, args, err := f.Eq("u.id", &nilValuerInner).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, "u.id IS NULL", sql)
			assert.Empty(t, args)
		})
	})

	t.Run("lt_double_pointer_to_nil_returns_the_sentinel", func(t *testing.T) {
		_, _, err := f.Lt("u.id", &nilInner).ToSQL()

		require.Error(t, err)
		assert.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
	})

	t.Run("lt_double_pointer_to_nil_valuer_returns_the_sentinel", func(t *testing.T) {
		require.NotPanics(t, func() {
			_, _, err := f.Lt("u.id", &nilValuerInner).ToSQL()

			require.Error(t, err)
			assert.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
		})
	})

	t.Run("double_pointer_to_a_value_binds_the_value", func(t *testing.T) {
		sql, args, err := f.Eq("u.id", &pointedSeven).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id = ?", sql)
		assert.Equal(t, []any{7}, args)
	})

	t.Run("in_resolves_a_double_pointer_element", func(t *testing.T) {
		sql, args, err := f.In("u.id", []any{&nilInner, &pointedSeven}).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id IN (?,?)", sql)
		assert.Equal(t, []any{nil, 7}, args)
	})
}

// TestOperandResolutionRefusesACyclicPointer pins the case that made the
// pointer walk non-terminating. The loop's own comment argued a chain must end
// because Go types are finite — true for `**int`, false the moment an interface
// is in the chain: `var v any; v = &v` has type `*any`, dereferences to an `any`
// holding `*any`, and shortens nothing. The walk must detect the repeat and
// report it through the deferred-error channel rather than spin.
//
// Guarded by a timeout so a regression FAILS the suite instead of hanging it.
func TestOperandResolutionRefusesACyclicPointer(t *testing.T) {
	var cyclic any
	cyclic = &cyclic

	doors := map[string]func(*QueryBuilder) (string, []any, error){
		"f_eq": func(qb *QueryBuilder) (string, []any, error) {
			return qb.Filter().Eq("u.id", cyclic).ToSQL()
		},
		"f_lt": func(qb *QueryBuilder) (string, []any, error) {
			return qb.Filter().Lt("u.id", cyclic).ToSQL()
		},
		"f_in": func(qb *QueryBuilder) (string, []any, error) {
			return qb.Filter().In("u.id", cyclic).ToSQL()
		},
		"f_between": func(qb *QueryBuilder) (string, []any, error) {
			return qb.Filter().Between("u.id", cyclic, 10).ToSQL()
		},
		"jf_eq": func(qb *QueryBuilder) (string, []any, error) {
			return qb.JoinFilter().Eq("u.id", cyclic).ToSQL()
		},
		"jf_in": func(qb *QueryBuilder) (string, []any, error) {
			return qb.JoinFilter().In("u.id", cyclic).ToSQL()
		},
		"jf_between": func(qb *QueryBuilder) (string, []any, error) {
			return qb.JoinFilter().Between("u.id", cyclic, 10).ToSQL()
		},
	}

	for name, door := range doors {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, _, err := door(NewQueryBuilder(dbtypes.PostgreSQL))
				done <- err
			}()

			select {
			case err := <-done:
				require.Error(t, err, "a cyclic operand must surface an error")
				assert.Contains(t, err.Error(), "cyclic")
			case <-time.After(5 * time.Second):
				t.Fatal("resolving a cyclic pointer operand did not terminate")
			}
		})
	}
}

// TestInExpandsAPointerToASlice pins the second rendering bug: a pointer to a
// slice resolves to the slice, but the list door then wrapped that slice as ONE
// element, so squirrel expanded only the outer list and `IN` received a single
// argument holding a []int — which a driver rejects. Classification has to
// happen on the RESOLVED value, not on the operand's surface form.
func TestInExpandsAPointerToASlice(t *testing.T) {
	values := []int{1, 2}

	doors := []struct {
		name    string
		wantSQL string
		call    func(qb *QueryBuilder, v any) (string, []any, error)
	}{
		{
			name:    "filter_in",
			wantSQL: "u.id IN (?,?)",
			call: func(qb *QueryBuilder, v any) (string, []any, error) {
				return qb.Filter().In("u.id", v).ToSQL()
			},
		},
		{
			name:    "filter_not_in",
			wantSQL: "u.id NOT IN (?,?)",
			call: func(qb *QueryBuilder, v any) (string, []any, error) {
				return qb.Filter().NotIn("u.id", v).ToSQL()
			},
		},
		{
			name:    "join_filter_in",
			wantSQL: "u.id IN (?,?)",
			call: func(qb *QueryBuilder, v any) (string, []any, error) {
				return qb.JoinFilter().In("u.id", v).ToSQL()
			},
		},
	}

	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			sql, args, err := door.call(NewQueryBuilder(dbtypes.PostgreSQL), &values)

			require.NoError(t, err)
			assert.Equal(t, door.wantSQL, sql)
			assert.Equal(t, []any{1, 2}, args)
		})
	}

	t.Run("a_pointer_to_a_byte_slice_stays_one_operand", func(t *testing.T) {
		raw := []byte("raw")
		sql, args, err := NewQueryBuilder(dbtypes.PostgreSQL).Filter().In("u.id", &raw).ToSQL()

		require.NoError(t, err)
		assert.Equal(t, "u.id IN (?)", sql)
		assert.Equal(t, []any{raw}, args)
	})

	t.Run("a_pointer_to_a_nil_slice_matches_the_unpointered_form", func(t *testing.T) {
		// Pinned against the DIRECT nil slice rather than against a literal: the
		// point of resolving the pointer is that `&nilSlice` and `nilSlice` mean
		// the same thing at this door, and an empty list is the door's own
		// `(1=0)`, not a one-element list holding NULL.
		var nilSlice []int
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		wantSQL, wantArgs, err := qb.Filter().In("u.id", nilSlice).ToSQL()
		require.NoError(t, err)
		gotSQL, gotArgs, err := qb.Filter().In("u.id", &nilSlice).ToSQL()
		require.NoError(t, err)

		assert.Equal(t, wantSQL, gotSQL)
		assert.Equal(t, wantArgs, gotArgs)
		assert.Equal(t, "(1=0)", gotSQL)
	})
}

// inListDoor renders one of the four IN doors inside a full SELECT, so the
// vendor's own placeholder ($1 / :1) is what gets asserted.
type inListDoor struct {
	name       string
	clause     string
	predicate  string
	emptySet   string
	boundWhole string
	call       func(qb *QueryBuilder, values any) (string, []any, error)
}

func (d *inListDoor) render(predicate string) string {
	return d.clause + " " + predicate
}

// build renders the door's statement and fails the test on a panic.
func (d *inListDoor) build(t *testing.T, vendor string, values any) (sql string, args []any, err error) {
	t.Helper()
	require.NotPanics(t, func() {
		sql, args, err = d.call(NewQueryBuilder(vendor), values)
	})
	return sql, args, err
}

var inFilterDoors = []inListDoor{
	{
		name: "filter_in", clause: "SELECT * FROM t WHERE", predicate: "c IN", emptySet: "(1=0)", boundWhole: "c =",
		call: func(qb *QueryBuilder, values any) (string, []any, error) {
			return qb.Select("*").From("t").Where(qb.Filter().In("c", values)).ToSQL()
		},
	},
	{
		name: "filter_not_in", clause: "SELECT * FROM t WHERE", predicate: "c NOT IN", emptySet: "(1=1)", boundWhole: "c <>",
		call: func(qb *QueryBuilder, values any) (string, []any, error) {
			return qb.Select("*").From("t").Where(qb.Filter().NotIn("c", values)).ToSQL()
		},
	},
}

var inJoinFilterDoors = []inListDoor{
	{
		name: "join_filter_in", clause: "SELECT * FROM t JOIN u ON", predicate: "u.c IN", emptySet: "(1=0)", boundWhole: "u.c =",
		call: func(qb *QueryBuilder, values any) (string, []any, error) {
			return qb.Select("*").From("t").JoinOn("u", qb.JoinFilter().In("u.c", values)).ToSQL()
		},
	},
	{
		name: "join_filter_not_in", clause: "SELECT * FROM t JOIN u ON", predicate: "u.c NOT IN", emptySet: "(1=1)", boundWhole: "u.c <>",
		call: func(qb *QueryBuilder, values any) (string, []any, error) {
			return qb.Select("*").From("t").JoinOn("u", qb.JoinFilter().NotIn("u.c", values)).ToSQL()
		},
	},
}

var inListDoors = slices.Concat(inFilterDoors, inJoinFilterDoors)

var inListVendors = []struct {
	vendor            string
	placeholder       string
	secondPlaceholder string
}{
	{vendor: dbtypes.PostgreSQL, placeholder: "$1", secondPlaceholder: "$2"},
	{vendor: dbtypes.Oracle, placeholder: ":1", secondPlaceholder: ":2"},
}

type inScalarStatus string

type inScalarPoint struct{ X int }

// inValuerList is a slice that is also a driver.Valuer: squirrel asks it for
// its value and binds the answer as one operand.
type inValuerList []int

func (inValuerList) Value() (driver.Value, error) { return "asked", nil }

// TestInDoorsWrapEveryResolvedScalarType pins #2119: a scalar that is not a
// driver.Value is one bound element.
func TestInDoorsWrapEveryResolvedScalarType(t *testing.T) {
	five := 5
	scalars := []struct {
		name  string
		value any
		want  any
	}{
		{name: "int", value: 5, want: 5},
		{name: "int32", value: int32(5), want: int32(5)},
		{name: "uint", value: uint(5), want: uint(5)},
		{name: "float32", value: float32(1.5), want: float32(1.5)},
		{name: "pointer_to_int", value: &five, want: 5},
		{name: "defined_string_type", value: inScalarStatus("active"), want: inScalarStatus("active")},
		{name: "non_valuer_struct", value: inScalarPoint{X: 1}, want: inScalarPoint{X: 1}},
	}

	for _, v := range inListVendors {
		for _, door := range inListDoors {
			for _, sc := range scalars {
				t.Run(v.vendor+"_"+door.name+"_"+sc.name, func(t *testing.T) {
					sql, args, err := door.build(t, v.vendor, sc.value)

					require.NoError(t, err)
					assert.Equal(t, door.render(door.predicate+" ("+v.placeholder+")"), sql)
					assert.Equal(t, []any{sc.want}, args)
				})
			}
		}
	}
}

func TestJoinFilterInDoorsSpliceARawExpression(t *testing.T) {
	// SECURITY: Manual SQL review completed - literal CURRENT_DATE keyword, no user input
	expr := dbtypes.MustExpr("CURRENT_DATE")
	operands := map[string]any{"expression": expr, "pointer_to_expression": &expr}

	for _, v := range inListVendors {
		for _, door := range inJoinFilterDoors {
			for name, operand := range operands {
				t.Run(v.vendor+"_"+door.name+"_"+name, func(t *testing.T) {
					sql, args, err := door.build(t, v.vendor, operand)

					require.NoError(t, err)
					assert.Equal(t, door.render(door.predicate+" (CURRENT_DATE)"), sql)
					assert.Empty(t, args)
				})
			}
		}
	}
}

func TestFilterInDoorsRefuseARawExpressionAtToSQL(t *testing.T) {
	// SECURITY: Manual SQL review completed - literal CURRENT_DATE keyword and a blank literal, neither rendered
	expr := dbtypes.MustExpr("CURRENT_DATE")
	blank := dbtypes.RawExpression{SQL: "  "}
	operands := map[string]any{"expression": expr, "pointer_to_expression": &expr, "blank_expression": blank}

	for _, v := range inListVendors {
		for _, door := range inFilterDoors {
			for name, operand := range operands {
				t.Run(v.vendor+"_"+door.name+"_"+name, func(t *testing.T) {
					_, _, err := door.build(t, v.vendor, operand)

					require.ErrorIs(t, err, dbtypes.ErrExpressionInFilter)
				})
			}
		}
	}
}

func TestJoinFilterInDoorsRefuseABlankRawExpressionAtToSQL(t *testing.T) {
	// SECURITY: Manual SQL review completed - blank expression literal, never rendered
	blank := dbtypes.RawExpression{SQL: "  "}

	for _, v := range inListVendors {
		for _, door := range inJoinFilterDoors {
			t.Run(v.vendor+"_"+door.name, func(t *testing.T) {
				_, _, err := door.build(t, v.vendor, blank)

				require.ErrorIs(t, err, dbtypes.ErrEmptyExpressionSQL)
			})
		}
	}
}

// TestInDoorsKeepTheDriverValueAndListShapes pins the output the scalar wrap and
// the expression judgment must not move. width is the bound placeholder count; 0
// is the door's empty-set constant.
func TestInDoorsKeepTheDriverValueAndListShapes(t *testing.T) {
	raw := []byte("raw")
	ids := []int{1, 2}
	operands := []struct {
		name     string
		value    any
		width    int
		wantArgs []any
	}{
		{name: "int64", value: int64(5), width: 1, wantArgs: []any{int64(5)}},
		{name: "string", value: "a", width: 1, wantArgs: []any{"a"}},
		{name: "byte_slice", value: raw, width: 1, wantArgs: []any{raw}},
		{name: "pointer_to_byte_slice", value: &raw, width: 1, wantArgs: []any{raw}},
		{name: "untyped_nil", value: nil, width: 0, wantArgs: nil},
		{name: "typed_nil_pointer", value: (*int)(nil), width: 1, wantArgs: []any{nil}},
		{name: "nil_expression_pointer", value: (*dbtypes.RawExpression)(nil), width: 1, wantArgs: []any{nil}},
		{name: "slice", value: ids, width: 2, wantArgs: []any{1, 2}},
		{name: "pointer_to_slice", value: &ids, width: 2, wantArgs: []any{1, 2}},
		{name: "empty_slice", value: []int{}, width: 0, wantArgs: nil},
	}

	for _, v := range inListVendors {
		placeholders := map[int]string{
			1: "(" + v.placeholder + ")",
			2: "(" + v.placeholder + "," + v.secondPlaceholder + ")",
		}
		for _, door := range inListDoors {
			for _, op := range operands {
				t.Run(v.vendor+"_"+door.name+"_"+op.name, func(t *testing.T) {
					wantPred := door.emptySet
					if op.width > 0 {
						wantPred = door.predicate + " " + placeholders[op.width]
					}

					sql, args, err := door.build(t, v.vendor, op.value)

					require.NoError(t, err)
					assert.Equal(t, door.render(wantPred), sql)
					assert.Equal(t, op.wantArgs, args)
				})
			}
		}
	}
}

// TestInDoorsKeepTheWholeOperandRendering pins the pre-#2119 rendering of a map,
// a chan, or a slice that is a driver.Valuer: bound as one `=` / `<>` argument,
// or the empty-set constant when it holds nothing.
func TestInDoorsKeepTheWholeOperandRendering(t *testing.T) {
	var nilMap map[string]int
	values := map[string]int{"a": 1}
	operands := []struct {
		name     string
		value    any
		bound    bool
		wantArgs []any
	}{
		{name: "map", value: values, bound: true, wantArgs: []any{values}},
		{name: "empty_map", value: map[string]int{}, bound: false, wantArgs: nil},
		{name: "nil_map", value: nilMap, bound: false, wantArgs: nil},
		{name: "empty_chan", value: make(chan int), bound: false, wantArgs: nil},
		{name: "valuer_slice", value: inValuerList{1, 2}, bound: true, wantArgs: []any{"asked"}},
	}

	for _, v := range inListVendors {
		for _, door := range inListDoors {
			for _, op := range operands {
				t.Run(v.vendor+"_"+door.name+"_"+op.name, func(t *testing.T) {
					wantPred := door.emptySet
					if op.bound {
						wantPred = door.boundWhole + " " + v.placeholder
					}

					sql, args, err := door.build(t, v.vendor, op.value)

					require.NoError(t, err)
					assert.Equal(t, door.render(wantPred), sql)
					assert.Equal(t, op.wantArgs, args)
				})
			}
		}
	}
}

// TestInDoorsBindAMapOrChanWithResolvableValuesWhole pins #2119: a non-empty map
// or chan whose value type is an interface, a pointer or a driver.Valuer
// panicked walking its elements; it binds whole, like a concrete-valued one.
func TestInDoorsBindAMapOrChanWithResolvableValuesWhole(t *testing.T) {
	five := 5
	ch := make(chan any, 1)
	ch <- 1
	operands := map[string]any{
		"interface_valued_map":  map[string]any{"a": 1},
		"pointer_valued_map":    map[string]*int{"a": &five},
		"valuer_valued_map":     map[string]dbsql.NullString{"a": {String: "x", Valid: true}},
		"interface_valued_chan": ch,
	}

	for _, v := range inListVendors {
		for _, door := range inListDoors {
			for name, operand := range operands {
				t.Run(v.vendor+"_"+door.name+"_"+name, func(t *testing.T) {
					sql, args, err := door.build(t, v.vendor, operand)

					require.NoError(t, err)
					assert.Equal(t, door.render(door.boundWhole+" "+v.placeholder), sql)
					assert.Equal(t, []any{operand}, args)
				})
			}
		}
	}
}

// fValueDoors are the ten FilterFactory value-door positions (Between's two
// bounds counted apart), each on column "c".
var fValueDoors = map[string]func(f dbtypes.FilterFactory, v any) dbtypes.Filter{
	"eq":            func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Eq("c", v) },
	"not_eq":        func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.NotEq("c", v) },
	"lt":            func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Lt("c", v) },
	"lte":           func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Lte("c", v) },
	"gt":            func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Gt("c", v) },
	"gte":           func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Gte("c", v) },
	"between_lower": func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Between("c", v, 5) },
	"between_upper": func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.Between("c", 5, v) },
	"in":            func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.In("c", v) },
	"not_in":        func(f dbtypes.FilterFactory, v any) dbtypes.Filter { return f.NotIn("c", v) },
}

// TestFilterValueDoorsRefuseARawExpressionAtToSQL pins #2119 and #2120: a FilterFactory
// value door takes values, so an expression — or a non-nil pointer to one,
// blank or not — fails the statement instead of binding a struct the driver
// cannot encode.
func TestFilterValueDoorsRefuseARawExpressionAtToSQL(t *testing.T) {
	// SECURITY: Manual SQL review completed - literal CURRENT_DATE keyword, an aliased one and a blank literal, none rendered
	expr := dbtypes.MustExpr("CURRENT_DATE")
	aliased := dbtypes.MustExpr("CURRENT_DATE", "d")
	blank := dbtypes.RawExpression{SQL: "  "}
	operands := map[string]any{
		"expression": expr, "pointer_to_expression": &expr, "aliased_expression": aliased, "blank_expression": blank,
	}

	for _, v := range inListVendors {
		for doorName, door := range fValueDoors {
			for name, operand := range operands {
				t.Run(v.vendor+"_"+doorName+"_"+name, func(t *testing.T) {
					qb := NewQueryBuilder(v.vendor)
					var err error
					require.NotPanics(t, func() {
						_, _, err = qb.Select("id").From("t").Where(door(qb.Filter(), operand)).ToSQL()
					})

					require.ErrorIs(t, err, dbtypes.ErrExpressionInFilter)
				})
			}
		}
	}
}

// TestFilterValueDoorsTreatANilExpressionPointerAsNil pins the unchanged nil
// path: a nil *RawExpression resolves to nil before any expression judgment.
func TestFilterValueDoorsTreatANilExpressionPointerAsNil(t *testing.T) {
	var nilExpr *dbtypes.RawExpression

	for _, v := range inListVendors {
		t.Run(v.vendor+"_eq_renders_is_null", func(t *testing.T) {
			qb := NewQueryBuilder(v.vendor)

			sql, args, err := qb.Select("id").From("t").Where(qb.Filter().Eq("c", nilExpr)).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, "SELECT id FROM t WHERE c IS NULL", sql)
			assert.Empty(t, args)
		})
		t.Run(v.vendor+"_lt_refuses_an_ordering_against_nil", func(t *testing.T) {
			qb := NewQueryBuilder(v.vendor)

			_, _, err := qb.Select("id").From("t").Where(qb.Filter().Lt("c", nilExpr)).ToSQL()

			require.ErrorIs(t, err, dbtypes.ErrOrderingOperandNotComparable)
		})
	}
}
