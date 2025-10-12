package builder

import (
	"errors"
	"testing"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	joinFilterErrorMsg = "mock join filter error"
)

func TestBuildCaseInsensitiveLikePostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "name ILIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestBuildCaseInsensitiveLikeOracle(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "UPPER(name) LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%JOHN%", args[0])
}

func TestBuildCaseInsensitiveLikeDefault(t *testing.T) {
	qb := NewQueryBuilder("mysql")

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "name LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestPaginateSkipsZeroValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.Select("*").From("users").Paginate(0, 0)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "LIMIT")
	assert.NotContains(t, sql, "OFFSET")
}

func TestPaginateAppliesPositiveValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.Select("*").From("users").Paginate(5, 10)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 5")
	assert.Contains(t, sql, "OFFSET 10")
}

func TestPaginateOracleUsesSuffix(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	query := qb.Select("*").From("users").Paginate(5, 0)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "FETCH NEXT 5 ROWS ONLY")
	assert.NotContains(t, sql, "LIMIT")
}

func toSQL(t *testing.T, sqlizer squirrel.Sqlizer) (sql string, args []any) {
	t.Helper()

	sql, args, err := sqlizer.ToSql()
	require.NoError(t, err)

	return sql, args
}

func TestBuildCurrentTimestampByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, expected: "NOW()"},
		{vendor: dbtypes.Oracle, expected: "SYSDATE"},
		{vendor: "sqlite", expected: "NOW()"},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildCurrentTimestamp(); got != tt.expected {
			t.Fatalf("unexpected timestamp for %s: %s", tt.vendor, got)
		}
	}
}

func TestBuildUUIDGenerationByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, expected: "gen_random_uuid()"},
		{vendor: dbtypes.Oracle, expected: "SYS_GUID()"},
		{vendor: "mysql", expected: "UUID()"},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildUUIDGeneration(); got != tt.expected {
			t.Fatalf("unexpected UUID function for %s: %s", tt.vendor, got)
		}
	}
}

func TestBuildBooleanValueByVendor(t *testing.T) {
	cases := []struct {
		vendor string
		input  bool
		expect any
	}{
		{vendor: dbtypes.PostgreSQL, input: true, expect: true},
		{vendor: dbtypes.Oracle, input: true, expect: 1},
		{vendor: dbtypes.Oracle, input: false, expect: 0},
		{vendor: "sqlite", input: false, expect: false},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildBooleanValue(tt.input); got != tt.expect {
			t.Fatalf("unexpected boolean mapping for %s: %v", tt.vendor, got)
		}
	}
}

func TestEscapeIdentifierByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		name     string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, name: "foo", expected: `"foo"`},
		{vendor: dbtypes.Oracle, name: "bar", expected: `"bar"`},
		{vendor: "sqlite", name: "baz", expected: `"baz"`},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.EscapeIdentifier(tt.name); got != tt.expected {
			t.Fatalf("unexpected escaped identifier for %s: %s", tt.vendor, got)
		}
	}
}

func TestVendor(t *testing.T) {
	cases := []string{
		dbtypes.PostgreSQL,
		dbtypes.Oracle,
		"mysql",
		"sqlite",
	}

	for _, vendor := range cases {
		qb := NewQueryBuilder(vendor)
		assert.Equal(t, vendor, qb.Vendor())
	}
}

func TestInsertWithColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	builder := qb.InsertWithColumns("users", "name", "email").Values("John", "john@example.com")
	sql, _, err := builder.ToSql()
	require.NoError(t, err)
	assert.Contains(t, sql, `INSERT INTO users (name,email)`)
	assert.Contains(t, sql, `VALUES ($1,$2)`)
}

func TestUpdate(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	t.Run("Simple UPDATE with Set", func(t *testing.T) {
		builder := qb.Update("users")
		sql, args, err := builder.Set("name", "John").ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET name = $1")
		assert.Equal(t, []any{"John"}, args)
	})

	t.Run("UPDATE with WHERE filter", func(t *testing.T) {
		builder := qb.Update("users")
		sql, args, err := builder.
			Set("name", "John").
			Where(f.Eq("id", 123)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET name = $1")
		assert.Contains(t, sql, "WHERE id = $2")
		assert.Equal(t, []any{"John", 123}, args)
	})

	t.Run("UPDATE with SetMap", func(t *testing.T) {
		builder := qb.Update("users")
		sql, _, err := builder.
			SetMap(map[string]any{
				"name":   "John",
				"status": "active",
			}).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET")
		assert.Contains(t, sql, "name = ")
		assert.Contains(t, sql, "status = ")
	})
}

func TestDelete(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	t.Run("Simple DELETE", func(t *testing.T) {
		builder := qb.Delete("users")
		sql, _, err := builder.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "DELETE FROM users", sql)
	})

	t.Run("DELETE with WHERE filter", func(t *testing.T) {
		builder := qb.Delete("users")
		sql, args, err := builder.
			Where(f.Eq("status", "deleted")).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "DELETE FROM users WHERE status = $1")
		assert.Equal(t, []any{"deleted"}, args)
	})

	t.Run("DELETE with complex filter", func(t *testing.T) {
		builder := qb.Delete("users")
		sql, args, err := builder.
			Where(f.And(
				f.Eq("status", "deleted"),
				f.Lt("deleted_at", "2024-01-01"),
			)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "DELETE FROM users WHERE")
		assert.Contains(t, sql, "status = ")
		assert.Contains(t, sql, "deleted_at < ")
		assert.Equal(t, []any{"deleted", "2024-01-01"}, args)
	})
}

// Test WHERE clause methods
func TestWhereClauseMethods(t *testing.T) {
	tests := []struct {
		name         string
		setupQuery   func(*QueryBuilder) string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name: "WhereLte",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.Lte("age", 30)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age <= $1`,
		},
		{
			name: "WhereGte",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.Gte("age", 18)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age >= $1`,
		},
		{
			name: "WhereNotIn",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.NotIn("status", []string{"banned", "deleted"})).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE status NOT IN ($1,$2)`,
		},
		{
			name: "WhereNull",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.Null("deleted_at")).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE deleted_at IS NULL`,
		},
		{
			name: "WhereNotNull",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.NotNull("email")).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE email IS NOT NULL`,
		},
		{
			name: "WhereBetween",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select("*").From("users").Where(f.Between("age", 18, 65)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE (age >= $1 AND age <= $2)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql := tt.setupQuery(qb)
			assert.Equal(t, tt.expectedSQL, sql)
		})
	}
}

func TestWhereLike(t *testing.T) {
	tests := []struct {
		vendor      string
		expectedSQL string
	}{
		{
			vendor:      dbtypes.PostgreSQL,
			expectedSQL: `SELECT * FROM users WHERE name ILIKE $1`,
		},
		{
			vendor:      dbtypes.Oracle,
			expectedSQL: `SELECT * FROM users WHERE UPPER(name) LIKE :1`,
		},
		{
			vendor:      "mysql",
			expectedSQL: `SELECT * FROM users WHERE name LIKE ?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.vendor, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			f := qb.Filter()
			sql, args, err := qb.Select("*").From("users").Where(f.Like("name", "john")).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Len(t, args, 1)
		})
	}
}

// Test JOIN methods with type-safe JoinFilter (v2.0+)
func TestJoinMethods(t *testing.T) {
	tests := []struct {
		name        string
		setupQuery  func(*QueryBuilder) string
		expectedSQL string
	}{
		{
			name: "JoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select("*").From("users").
					JoinOn("profiles", jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "LeftJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select("*").From("users").
					LeftJoinOn("profiles", jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users LEFT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "RightJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select("*").From("users").
					RightJoinOn("profiles", jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users RIGHT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "InnerJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select("*").From("users").
					InnerJoinOn("profiles", jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users INNER JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "CrossJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").CrossJoinOn("roles").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users CROSS JOIN roles`,
		},
		{
			name: "JoinOn with complex condition",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select("*").From("users").
					JoinOn("profiles", jf.And(
						jf.EqColumn("users.id", "profiles.user_id"),
						jf.GtColumn("profiles.created_at", "users.created_at"),
					)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users JOIN profiles ON (users.id = profiles.user_id AND profiles.created_at > users.created_at)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql := tt.setupQuery(qb)
			assert.Equal(t, tt.expectedSQL, sql)
		})
	}
}

// Test query modifier methods
func TestQueryModifiers(t *testing.T) {
	tests := []struct {
		name        string
		setupQuery  func(*QueryBuilder) string
		expectedSQL string
	}{
		{
			name: "OrderBy",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").OrderBy("name", "age DESC").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users ORDER BY name, age DESC`,
		},
		{
			name: "GroupBy",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("department", "COUNT(*)").From("users").GroupBy("department").ToSQL()
				return sql
			},
			expectedSQL: `SELECT department, COUNT(*) FROM users GROUP BY department`,
		},
		{
			name: "Having",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("department", "COUNT(*)").From("users").GroupBy("department").Having("COUNT(*) > ?", 5).ToSQL()
				return sql
			},
			expectedSQL: `SELECT department, COUNT(*) FROM users GROUP BY department HAVING COUNT(*) > $1`,
		},
		{
			name: "Paginate with limit only",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").Paginate(10, 0).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users LIMIT 10`,
		},
		{
			name: "Paginate with offset only",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").Paginate(0, 20).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users OFFSET 20`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			sql := tt.setupQuery(qb)
			assert.Equal(t, tt.expectedSQL, sql)
		})
	}
}

// ========== JoinFilter Error Propagation Tests ==========

// mockErrorJoinFilter is a test helper that always returns an error from ToSQL()
type mockErrorJoinFilter struct{}

//nolint:revive // ToSql is required by squirrel.Sqlizer interface (lowercase 's')
func (m mockErrorJoinFilter) ToSql() (sql string, args []any, err error) {
	return "", nil, errors.New(joinFilterErrorMsg)
}

func (m mockErrorJoinFilter) ToSQL() (sql string, args []any, err error) {
	return "", nil, errors.New(joinFilterErrorMsg)
}

func TestJoinFilterErrorPropagation(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	errorFilter := mockErrorJoinFilter{}

	t.Run("JoinOn propagates error", func(t *testing.T) {
		query := qb.Select("*").
			From("users").
			JoinOn("profiles", errorFilter)

		sql, args, err := query.ToSQL()

		// Error should be propagated, not injected into SQL
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "JoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("LeftJoinOn propagates error", func(t *testing.T) {
		query := qb.Select("*").
			From("users").
			LeftJoinOn("profiles", errorFilter)

		sql, args, err := query.ToSQL()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "LeftJoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("RightJoinOn propagates error", func(t *testing.T) {
		query := qb.Select("*").
			From("users").
			RightJoinOn("profiles", errorFilter)

		sql, args, err := query.ToSQL()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "RightJoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("InnerJoinOn propagates error", func(t *testing.T) {
		query := qb.Select("*").
			From("users").
			InnerJoinOn("profiles", errorFilter)

		sql, args, err := query.ToSQL()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "InnerJoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("Error from first join prevents SQL generation", func(t *testing.T) {
		// Even with subsequent valid operations, the error should be preserved
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From("users").
			JoinOn("profiles", errorFilter).
			LeftJoinOn("orders", jf.EqColumn("users.id", "orders.user_id")). // Valid join after error
			Where(qb.Filter().Eq("status", "active"))                        // Valid where

		sql, args, err := query.ToSQL()

		// Original error should still be returned
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "JoinOn filter error")
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})
}

// TestJoinFilterNoErrorInjection verifies errors are NOT injected into SQL
func TestJoinFilterNoErrorInjection(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	errorFilter := mockErrorJoinFilter{}

	query := qb.Select("*").
		From("users").
		JoinOn("profiles", errorFilter)

	sql, _, _ := query.ToSQL()

	// SQL should be empty, NOT contain "WHERE ERROR:"
	assert.Empty(t, sql)
	assert.NotContains(t, sql, "ERROR:")
	assert.NotContains(t, sql, "WHERE ERROR:")
}
