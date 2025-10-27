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
	joinColumn         = "users.id"
	fromUsersClause    = "FROM users"
	fromAliasClause    = "FROM users u"
	testDate           = "2024-01-01"
	fromProductsClause = "FROM products"
	testExpr           = "DATE(created_at)"
	groupByClause      = "GROUP BY DATE(created_at)"
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
				f.Lt("deleted_at", testDate),
			)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "DELETE FROM users WHERE")
		assert.Contains(t, sql, "status = ")
		assert.Contains(t, sql, "deleted_at < ")
		assert.Equal(t, []any{"deleted", testDate}, args)
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
						jf.EqColumn(joinColumn, "profiles.user_id"),
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
				sql, _, _ := qb.Select("department", countClause).From("users").GroupBy("department").ToSQL()
				return sql
			},
			expectedSQL: `SELECT department, COUNT(*) FROM users GROUP BY department`,
		},
		{
			name: "Having",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("department", countClause).From("users").GroupBy("department").Having(countClause+" > ?", 5).ToSQL()
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
			LeftJoinOn("orders", jf.EqColumn(joinColumn, "orders.user_id")). // Valid join after error
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

// ========== Table Alias Tests ==========

func TestTableAliasFrom(t *testing.T) {
	t.Run("String table name backward compatibility", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").From("users")
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
	})

	t.Run("TableRef without alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").From(dbtypes.Table("users"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
	})

	t.Run("TableRef with alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").From(dbtypes.Table("users").As("u"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
	})

	t.Run("Oracle table with alias and reserved word", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		query := qb.Select("*").From(dbtypes.Table("LEVEL").As("lvl"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, `FROM "LEVEL" lvl`)
	})

	t.Run("Mixed string and TableRef in From", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").From("users", dbtypes.Table("profiles").As("p"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM users, profiles p")
	})

	t.Run("Multiple TableRef with aliases", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").From(dbtypes.Table("users").As("u"), dbtypes.Table("profiles").As("p"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM users u, profiles p")
	})
}

func TestTableAliasJoin(t *testing.T) {
	t.Run("JoinOn with table alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From(dbtypes.Table("users").As("u")).
			JoinOn(dbtypes.Table("profiles").As("p"), jf.EqColumn("u.id", "p.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
		assert.Contains(t, sql, "JOIN profiles p ON")
		assert.Contains(t, sql, "u.id = p.user_id")
	})

	t.Run("LeftJoinOn with table alias Oracle", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From(dbtypes.Table("customers").As("c")).
			LeftJoinOn(dbtypes.Table("orders").As("o"), jf.EqColumn("c.id", "o.customer_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM customers c")
		assert.Contains(t, sql, "LEFT JOIN orders o ON")
	})

	t.Run("RightJoinOn with string table name", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From("users").
			RightJoinOn("profiles", jf.EqColumn(joinColumn, "profiles.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
		assert.Contains(t, sql, "RIGHT JOIN profiles ON")
	})

	t.Run("InnerJoinOn with mixed string and TableRef", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select("*").
			From("users").
			InnerJoinOn(dbtypes.Table("profiles").As("p"), jf.EqColumn(joinColumn, "p.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
		assert.Contains(t, sql, "INNER JOIN profiles p ON")
	})

	t.Run("CrossJoinOn with table alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select("*").
			From(dbtypes.Table("users").As("u")).
			CrossJoinOn(dbtypes.Table("roles").As("r"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
		assert.Contains(t, sql, "CROSS JOIN roles r")
	})
}

func TestTableAliasMultipleJoins(t *testing.T) {
	t.Run("Multiple JOINs with aliases and Raw conditions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		f := qb.Filter()

		query := qb.Select("*").
			From(dbtypes.Table("orders").As("o")).
			JoinOn(dbtypes.Table("customers").As("c"),
				jf.Raw("c.id = TO_NUMBER(o.customer_id)")).
			JoinOn(dbtypes.Table("products").As("p"), jf.And(
				jf.Raw("p.sku = o.product_sku"),
				jf.Raw("p.status = ?", "active"),
			)).
			Where(f.Eq("o.id", 123))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM orders o")
		assert.Contains(t, sql, "JOIN customers c ON")
		assert.Contains(t, sql, "JOIN products p ON")
		assert.Len(t, args, 2) // "active" and 123
		assert.Equal(t, "active", args[0])
		assert.Equal(t, 123, args[1])
	})

	t.Run("Complex query with aliases, WHERE, GROUP BY, ORDER BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		f := qb.Filter()

		query := qb.Select("u.id", "u.name", "COUNT(o.id) AS order_count").
			From(dbtypes.Table("users").As("u")).
			LeftJoinOn(dbtypes.Table("orders").As("o"), jf.EqColumn("u.id", "o.user_id")).
			Where(f.Eq("u.status", "active")).
			GroupBy("u.id", "u.name").
			OrderBy("order_count DESC").
			Limit(10)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
		assert.Contains(t, sql, "LEFT JOIN orders o ON")
		assert.Contains(t, sql, "WHERE")
		assert.Contains(t, sql, "GROUP BY")
		assert.Contains(t, sql, "ORDER BY")
		assert.Contains(t, sql, "LIMIT 10")
		assert.Len(t, args, 1)
		assert.Equal(t, "active", args[0])
	})
}

func TestTableAliasOracleReservedWords(t *testing.T) {
	t.Run("Oracle reserved word table with alias in complex query", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		f := qb.Filter()

		query := qb.Select("l.id", "n.value").
			From(dbtypes.Table("LEVEL").As("l")).
			JoinOn(dbtypes.Table("NUMBER").As("n"), jf.EqColumn("l.id", "n.level_id")).
			Where(f.Eq("l.status", "active"))

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		// Reserved words should be quoted
		assert.Contains(t, sql, `FROM "LEVEL" l`)
		assert.Contains(t, sql, `JOIN "NUMBER" n ON`)
	})
}

func TestTableAliasInvalidTypes(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	t.Run("Invalid type in From panics with clear diagnostic", func(t *testing.T) {
		// This tests the fail-fast behavior - invalid type panics immediately
		assert.PanicsWithValue(t, "unsupported table reference type: int (must be string or *TableRef)", func() {
			qb.Select("*").From(123) // Invalid: int instead of string or TableRef
		})
	})

	t.Run("Invalid type in JoinOn panics with clear diagnostic", func(t *testing.T) {
		jf := qb.JoinFilter()
		assert.PanicsWithValue(t, "unsupported table reference type: int (must be string or *TableRef)", func() {
			qb.Select("*").From("users").JoinOn(123, jf.EqColumn("a", "b"))
		})
	})
}
func TestSelectExpressions(t *testing.T) {
	t.Run("Simple expression without alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select("id", qb.Expr(countClause)).From("products")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT id, COUNT(*) FROM products")
		assert.Empty(t, args)
	})

	t.Run("Expression with alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select("category", qb.Expr("SUM(amount)", "total_sales")).From("orders")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT category, SUM(amount) AS total_sales FROM orders")
		assert.Empty(t, args)
	})

	t.Run("Multiple expressions with aliases", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			"category",
			qb.Expr(countClause, "product_count"),
			qb.Expr("AVG(price)", "avg_price"),
			qb.Expr("SUM(stock)", "total_stock"),
		).From("products")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT category, COUNT(*) AS product_count, AVG(price) AS avg_price, SUM(stock) AS total_stock")
		assert.Contains(t, sql, fromProductsClause)
		assert.Empty(t, args)
	})

	t.Run("Mixed strings and expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			"id",
			"name",
			qb.Expr("UPPER(category)", "upper_category"),
			"status",
		).From("products")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT id, name, UPPER(category) AS upper_category, status")
		assert.Contains(t, sql, fromProductsClause)
		assert.Empty(t, args)
	})

	t.Run("Expression with complex SQL", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			"user_id",
			qb.Expr("COALESCE(email, phone, 'N/A')", "contact"),
		).From("users")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT user_id, COALESCE(email, phone, 'N/A') AS contact FROM users")
		assert.Empty(t, args)
	})

	t.Run("Window function expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			"product_id",
			"category",
			"price",
			qb.Expr("ROW_NUMBER() OVER (PARTITION BY category ORDER BY price DESC)", "rank"),
		).From("products")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT product_id, category, price, ROW_NUMBER() OVER (PARTITION BY category ORDER BY price DESC) AS rank")
		assert.Contains(t, sql, fromProductsClause)
		assert.Empty(t, args)
	})

	t.Run("Oracle reserved word column with expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			"level",
			qb.Expr(countClause, "total"),
		).From("categories")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "\"level\"") // Oracle quotes reserved word (lowercase)
		assert.Contains(t, sql, "COUNT(*) AS total")
		assert.Empty(t, args)
	})
}

func TestGroupByExpressions(t *testing.T) {
	t.Run("GROUP BY with raw expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		query := qb.Select(
			qb.Expr(testExpr, "date"),
			qb.Expr(countClause, "count"),
		).
			From("orders").
			GroupBy(qb.Expr(testExpr)).
			Where(f.Eq("status", "completed"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT DATE(created_at) AS date, COUNT(*) AS count FROM orders")
		assert.Contains(t, sql, "WHERE status = $1")
		assert.Contains(t, sql, groupByClause)
		assert.Equal(t, []any{"completed"}, args)
	})

	t.Run("Mixed column names and expressions in GROUP BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			"category",
			qb.Expr("YEAR(order_date)", "year"),
			qb.Expr("SUM(amount)", "total"),
		).
			From("orders").
			GroupBy("category", qb.Expr("YEAR(order_date)"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "GROUP BY category, YEAR(order_date)")
		assert.Empty(t, args)
	})

	t.Run("GROUP BY with expression and HAVING", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			qb.Expr(testExpr, "date"),
			qb.Expr(countClause, "order_count"),
		).
			From("orders").
			GroupBy(qb.Expr(testExpr)).
			Having("COUNT(*) > ?", 10)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, groupByClause)
		assert.Contains(t, sql, "HAVING COUNT(*) > $1")
		assert.Equal(t, []any{10}, args)
	})
}

func TestOrderByExpressions(t *testing.T) {
	t.Run("ORDER BY with raw expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select("*").
			From("products").
			OrderBy(qb.Expr("COUNT(*) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT * FROM products ORDER BY COUNT(*) DESC")
		assert.Empty(t, args)
	})

	t.Run("Mixed column names and expressions in ORDER BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select("id", "name", "price").
			From("products").
			OrderBy("name", qb.Expr("UPPER(category) ASC"), "price DESC")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "ORDER BY name, UPPER(category) ASC, price DESC")
		assert.Empty(t, args)
	})

	t.Run("ORDER BY with expression using function", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select("id", "created_at").
			From("orders").
			OrderBy(qb.Expr("DATE(created_at) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "ORDER BY DATE(created_at) DESC")
		assert.Empty(t, args)
	})
}

func TestExpressionErrorCases(t *testing.T) {
	t.Run("Unsupported column type in Select panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "unsupported column type in Select: int (must be string or RawExpression)", func() {
			qb.Select("id", 123, "name").From("users")
		})
	})

	t.Run("Unsupported type in GroupBy panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "unsupported groupBy type: int (must be string or RawExpression)", func() {
			qb.Select("*").From("users").GroupBy("id", 123)
		})
	})

	t.Run("Unsupported type in OrderBy panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "unsupported orderBy type: float64 (must be string or RawExpression)", func() {
			qb.Select("*").From("users").OrderBy("id", 3.14)
		})
	})

	t.Run("Empty expression SQL panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "expression SQL cannot be empty", func() {
			qb.Select(qb.Expr("")).From("users")
		})
	})

	t.Run("Multiple aliases panic", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "Expr accepts maximum 1 alias, got 2", func() {
			qb.Select(qb.Expr(countClause, "total", "count")).From("users")
		})
	})

	t.Run("Dangerous alias characters panic", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.Panics(t, func() {
			qb.Select(qb.Expr(countClause, "total;DROP TABLE users")).From("users")
		})
	})
}

func TestComplexExpressionQueries(t *testing.T) {
	t.Run("Aggregation query with GROUP BY and HAVING", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		query := qb.Select(
			"category",
			qb.Expr(countClause, "product_count"),
			qb.Expr("AVG(price)", "avg_price"),
			qb.Expr("MIN(price)", "min_price"),
			qb.Expr("MAX(price)", "max_price"),
		).
			From("products").
			Where(f.Eq("status", "active")).
			GroupBy("category").
			Having("COUNT(*) > ?", 5).
			OrderBy(qb.Expr("COUNT(*) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)

		// Verify SELECT clause
		assert.Contains(t, sql, "SELECT category, COUNT(*) AS product_count, AVG(price) AS avg_price")
		assert.Contains(t, sql, "MIN(price) AS min_price, MAX(price) AS max_price")

		// Verify WHERE, GROUP BY, HAVING, ORDER BY
		assert.Contains(t, sql, "WHERE status = $1")
		assert.Contains(t, sql, "GROUP BY category")
		assert.Contains(t, sql, "HAVING COUNT(*) > $2")
		assert.Contains(t, sql, "ORDER BY COUNT(*) DESC")

		assert.Equal(t, []any{"active", 5}, args)
	})

	t.Run("Complex query with subqueries and expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("category_id").
			From("featured_categories").
			Where(f.Eq("active", true))

		query := qb.Select(
			"id",
			"name",
			qb.Expr("price * 1.1", "price_with_tax"),
			qb.Expr("UPPER(category)", "upper_category"),
		).
			From("products").
			Where(f.And(
				f.InSubquery("category_id", subquery),
				f.Gt("stock", 0),
			)).
			OrderBy(qb.Expr("price * 1.1 DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)

		// Verify SELECT with expressions
		assert.Contains(t, sql, "price * 1.1 AS price_with_tax")
		assert.Contains(t, sql, "UPPER(category) AS upper_category")

		// Verify WHERE with subquery
		assert.Contains(t, sql, "category_id IN")
		assert.Contains(t, sql, "SELECT category_id FROM featured_categories")

		// Verify ORDER BY with expression
		assert.Contains(t, sql, "ORDER BY price * 1.1 DESC")

		// Verify args (true from subquery, 0 from stock check)
		assert.ElementsMatch(t, []any{true, 0}, args)
	})

	t.Run("Date aggregation with expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		query := qb.Select(
			qb.Expr(testExpr, "order_date"),
			qb.Expr(countClause, "order_count"),
			qb.Expr("SUM(total_amount)", "daily_revenue"),
		).
			From("orders").
			Where(f.Gte("created_at", testDate)).
			GroupBy(qb.Expr(testExpr)).
			Having("SUM(total_amount) > ?", 1000).
			OrderBy(qb.Expr("DATE(created_at) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)

		assert.Contains(t, sql, "SELECT DATE(created_at) AS order_date")
		assert.Contains(t, sql, "COUNT(*) AS order_count")
		assert.Contains(t, sql, "SUM(total_amount) AS daily_revenue")
		assert.Contains(t, sql, groupByClause)
		assert.Contains(t, sql, "HAVING SUM(total_amount) > $2")
		assert.Contains(t, sql, "ORDER BY DATE(created_at) DESC")
		assert.Equal(t, []any{testDate, 1000}, args)
	})
}
