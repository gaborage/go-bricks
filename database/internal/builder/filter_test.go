package builder

import (
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			expectedSQL: "status = ?",
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
	assert.Contains(t, sql, "status = ?")
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
