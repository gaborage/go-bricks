package builder

import (
	"errors"
	"testing"

	"github.com/Masterminds/squirrel"
	"github.com/gaborage/go-bricks/database/internal/columns"
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
	testEmail          = "john@example.com"
	testWhereClause    = "WHERE status = $1"
	testAccountID      = "ACC-001"
	testUser           = "Test User"
	allFieldsTest      = "All Fields Test"

	aliasU           = "u"
	aliasA           = "a"
	colUserID        = "u.id"
	colUserName      = "u.name"
	colUserEmail     = "u.email"
	colUserStatus    = "u.status"
	colAccountNumber = "a.number"

	// Common test data values
	statusActive   = "active"
	statusInactive = "inactive"
	statusPending  = "pending"
	statusPaid     = "paid"
	statusDeleted  = "deleted"

	// Table names (heavily duplicated across tests)
	tableUsers    = "users"
	tableProfiles = "profiles"
	tableProducts = "products"
	tableOrders   = "orders"
	tableAccounts = "accounts"

	// Column names (commonly used)
	colID       = "id"
	colName     = "name"
	colEmail    = "email"
	colStatus   = "status"
	colLevel    = "level"
	colPrice    = "price"
	colCategory = "category"

	// Struct field names (for Columns tests)
	fieldID     = "ID"
	fieldName   = "Name"
	fieldEmail  = "Email"
	fieldStatus = "Status"
	fieldNumber = "Number"
	fieldLevel  = "Level"

	// Test values
	testJohn   = "John"
	testVideo  = "video"
	testSQLite = "sqlite"
	selectAll  = "*"
)

// Test structs for columns feature
type IntegrationUser struct {
	ID        int64  `db:"id"`
	Name      string `db:"name"`
	Email     string `db:"email"`
	Status    string `db:"status"`
	CreatedAt string `db:"created_at"`
}

type IntegrationAccount struct {
	ID     int64  `db:"id"`
	Number string `db:"number"` // Oracle reserved word
	Level  int    `db:"level"`  // Oracle reserved word
	Size   string `db:"size"`   // Oracle reserved word
}

type IntegrationProduct struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Price string `db:"price"`
}

func TestBuildCaseInsensitiveLikePostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike(colName, "john"))

	assert.Equal(t, "name ILIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestBuildCaseInsensitiveLikeOracle(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike(colName, "john"))

	assert.Equal(t, "UPPER(name) LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%JOHN%", args[0])
}

func TestBuildCaseInsensitiveLikeDefault(t *testing.T) {
	qb := NewQueryBuilder("mysql")

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike(colName, "john"))

	assert.Equal(t, "name LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestPaginateSkipsZeroValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.Select(selectAll).From(tableUsers).Paginate(0, 0)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "LIMIT")
	assert.NotContains(t, sql, "OFFSET")
}

func TestPaginateAppliesPositiveValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.Select(selectAll).From(tableUsers).Paginate(5, 10)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 5")
	assert.Contains(t, sql, "OFFSET 10")
}

func TestPaginateOracleUsesSuffix(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	query := qb.Select(selectAll).From(tableUsers).Paginate(5, 0)

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

// toStrings converts []any to []string for INSERT operations

func TestBuildCurrentTimestampByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, expected: "NOW()"},
		{vendor: dbtypes.Oracle, expected: "SYSDATE"},
		{vendor: testSQLite, expected: "NOW()"},
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
		{vendor: testSQLite, input: false, expect: false},
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
		{vendor: testSQLite, name: "baz", expected: `"baz"`},
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
		testSQLite,
	}

	for _, vendor := range cases {
		qb := NewQueryBuilder(vendor)
		assert.Equal(t, vendor, qb.Vendor())
	}
}

func TestInsertWithColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	builder := qb.InsertWithColumns(tableUsers, colName, colEmail).Values(testJohn, testEmail)
	sql, _, err := builder.ToSql()
	require.NoError(t, err)
	assert.Contains(t, sql, `INSERT INTO users (name,email)`)
	assert.Contains(t, sql, `VALUES ($1,$2)`)
}

func TestUpdate(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	t.Run("Simple UPDATE with Set", func(t *testing.T) {
		builder := qb.Update(tableUsers)
		sql, args, err := builder.Set(colName, testJohn).ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET name = $1")
		assert.Equal(t, []any{testJohn}, args)
	})

	t.Run("UPDATE with WHERE filter", func(t *testing.T) {
		builder := qb.Update(tableUsers)
		sql, args, err := builder.
			Set(colName, testJohn).
			Where(f.Eq(colID, 123)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET name = $1")
		assert.Contains(t, sql, "WHERE id = $2")
		assert.Equal(t, []any{testJohn, 123}, args)
	})

	t.Run("UPDATE with SetMap", func(t *testing.T) {
		builder := qb.Update(tableUsers)
		sql, _, err := builder.
			SetMap(map[string]any{
				colName:   testJohn,
				colStatus: statusActive,
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
		builder := qb.Delete(tableUsers)
		sql, _, err := builder.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "DELETE FROM users", sql)
	})

	t.Run("DELETE with WHERE filter", func(t *testing.T) {
		builder := qb.Delete(tableUsers)
		sql, args, err := builder.
			Where(f.Eq(colStatus, statusDeleted)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "DELETE FROM users WHERE status = $1")
		assert.Equal(t, []any{statusDeleted}, args)
	})

	t.Run("DELETE with complex filter", func(t *testing.T) {
		builder := qb.Delete(tableUsers)
		sql, args, err := builder.
			Where(f.And(
				f.Eq(colStatus, statusDeleted),
				f.Lt("deleted_at", testDate),
			)).
			ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "DELETE FROM users WHERE")
		assert.Contains(t, sql, "status = ")
		assert.Contains(t, sql, "deleted_at < ")
		assert.Equal(t, []any{statusDeleted, testDate}, args)
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
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.Lte("age", 30)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age <= $1`,
		},
		{
			name: "WhereGte",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.Gte("age", 18)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age >= $1`,
		},
		{
			name: "WhereNotIn",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.NotIn(colStatus, []string{"banned", statusDeleted})).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE status NOT IN ($1,$2)`,
		},
		{
			name: "WhereNull",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.Null("deleted_at")).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE deleted_at IS NULL`,
		},
		{
			name: "WhereNotNull",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.NotNull(colEmail)).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE email IS NOT NULL`,
		},
		{
			name: "WhereBetween",
			setupQuery: func(qb *QueryBuilder) string {
				f := qb.Filter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Where(f.Between("age", 18, 65)).ToSQL()
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
			sql, args, err := qb.Select(selectAll).From(tableUsers).Where(f.Like(colName, testJohn)).ToSQL()
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
				sql, _, _ := qb.Select(selectAll).From(tableUsers).
					JoinOn(tableProfiles, jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "LeftJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).
					LeftJoinOn(tableProfiles, jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users LEFT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "RightJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).
					RightJoinOn(tableProfiles, jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users RIGHT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "InnerJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).
					InnerJoinOn(tableProfiles, jf.EqColumn(testLeftJoinColumn, testRightJoinColumn)).
					ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users INNER JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "CrossJoinOn",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select(selectAll).From(tableUsers).CrossJoinOn("roles").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users CROSS JOIN roles`,
		},
		{
			name: "JoinOn with complex condition",
			setupQuery: func(qb *QueryBuilder) string {
				jf := qb.JoinFilter()
				sql, _, _ := qb.Select(selectAll).From(tableUsers).
					JoinOn(tableProfiles, jf.And(
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
				sql, _, _ := qb.Select(selectAll).From(tableUsers).OrderBy(colName, "age DESC").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users ORDER BY name, age DESC`,
		},
		{
			name: "GroupBy",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("department", countClause).From(tableUsers).GroupBy("department").ToSQL()
				return sql
			},
			expectedSQL: `SELECT department, COUNT(*) FROM users GROUP BY department`,
		},
		{
			name: "Having",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("department", countClause).From(tableUsers).GroupBy("department").Having(countClause+" > ?", 5).ToSQL()
				return sql
			},
			expectedSQL: `SELECT department, COUNT(*) FROM users GROUP BY department HAVING COUNT(*) > $1`,
		},
		{
			name: "Paginate with limit only",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Paginate(10, 0).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users LIMIT 10`,
		},
		{
			name: "Paginate with offset only",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select(selectAll).From(tableUsers).Paginate(0, 20).ToSQL()
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
		query := qb.Select(selectAll).
			From(tableUsers).
			JoinOn(tableProfiles, errorFilter)

		sql, args, err := query.ToSQL()

		// Error should be propagated, not injected into SQL
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "JoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("LeftJoinOn propagates error", func(t *testing.T) {
		query := qb.Select(selectAll).
			From(tableUsers).
			LeftJoinOn(tableProfiles, errorFilter)

		sql, args, err := query.ToSQL()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "LeftJoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("RightJoinOn propagates error", func(t *testing.T) {
		query := qb.Select(selectAll).
			From(tableUsers).
			RightJoinOn(tableProfiles, errorFilter)

		sql, args, err := query.ToSQL()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "RightJoinOn filter error")
		assert.Contains(t, err.Error(), joinFilterErrorMsg)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	})

	t.Run("InnerJoinOn propagates error", func(t *testing.T) {
		query := qb.Select(selectAll).
			From(tableUsers).
			InnerJoinOn(tableProfiles, errorFilter)

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
		query := qb.Select(selectAll).
			From(tableUsers).
			JoinOn(tableProfiles, errorFilter).
			LeftJoinOn(tableOrders, jf.EqColumn(joinColumn, "orders.user_id")). // Valid join after error
			Where(qb.Filter().Eq(colStatus, statusActive))                      // Valid where

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

	query := qb.Select(selectAll).
		From(tableUsers).
		JoinOn(tableProfiles, errorFilter)

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
		query := qb.Select(selectAll).From(tableUsers)
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
	})

	t.Run("TableRef without alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select(selectAll).From(dbtypes.Table(tableUsers))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
	})

	t.Run("TableRef with alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select(selectAll).From(dbtypes.Table(tableUsers).As(aliasU))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
	})

	t.Run("Oracle table with alias and reserved word", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		query := qb.Select(selectAll).From(dbtypes.Table("LEVEL").As("lvl"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, `FROM "LEVEL" lvl`)
	})

	t.Run("Mixed string and TableRef in From", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select(selectAll).From(tableUsers, dbtypes.Table(tableProfiles).As("p"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM users, profiles p")
	})

	t.Run("Multiple TableRef with aliases", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select(selectAll).From(dbtypes.Table(tableUsers).As(aliasU), dbtypes.Table(tableProfiles).As("p"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM users u, profiles p")
	})
}

func TestTableAliasJoin(t *testing.T) {
	t.Run("JoinOn with table alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select(selectAll).
			From(dbtypes.Table(tableUsers).As(aliasU)).
			JoinOn(dbtypes.Table(tableProfiles).As("p"), jf.EqColumn(colUserID, "p.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromAliasClause)
		assert.Contains(t, sql, "JOIN profiles p ON")
		assert.Contains(t, sql, "u.id = p.user_id")
	})

	t.Run("LeftJoinOn with table alias Oracle", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		jf := qb.JoinFilter()
		query := qb.Select(selectAll).
			From(dbtypes.Table("customers").As("c")).
			LeftJoinOn(dbtypes.Table(tableOrders).As("o"), jf.EqColumn("c.id", "o.customer_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM customers c")
		assert.Contains(t, sql, "LEFT JOIN orders o ON")
	})

	t.Run("RightJoinOn with string table name", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select(selectAll).
			From(tableUsers).
			RightJoinOn(tableProfiles, jf.EqColumn(joinColumn, "profiles.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
		assert.Contains(t, sql, "RIGHT JOIN profiles ON")
	})

	t.Run("InnerJoinOn with mixed string and TableRef", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		query := qb.Select(selectAll).
			From(tableUsers).
			InnerJoinOn(dbtypes.Table(tableProfiles).As("p"), jf.EqColumn(joinColumn, "p.user_id"))
		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, fromUsersClause)
		assert.Contains(t, sql, "INNER JOIN profiles p ON")
	})

	t.Run("CrossJoinOn with table alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		query := qb.Select(selectAll).
			From(dbtypes.Table(tableUsers).As(aliasU)).
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

		query := qb.Select(selectAll).
			From(dbtypes.Table(tableOrders).As("o")).
			JoinOn(dbtypes.Table("customers").As("c"),
				jf.Raw("c.id = TO_NUMBER(o.customer_id)")).
			JoinOn(dbtypes.Table(tableProducts).As("p"), jf.And(
				jf.Raw("p.sku = o.product_sku"),
				jf.Raw("p.status = ?", statusActive),
			)).
			Where(f.Eq("o.id", 123))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "FROM orders o")
		assert.Contains(t, sql, "JOIN customers c ON")
		assert.Contains(t, sql, "JOIN products p ON")
		assert.Len(t, args, 2) // "active" and 123
		assert.Equal(t, statusActive, args[0])
		assert.Equal(t, 123, args[1])
	})

	t.Run("Complex query with aliases, WHERE, GROUP BY, ORDER BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		jf := qb.JoinFilter()
		f := qb.Filter()

		query := qb.Select(colUserID, colUserName, "COUNT(o.id) AS order_count").
			From(dbtypes.Table(tableUsers).As(aliasU)).
			LeftJoinOn(dbtypes.Table(tableOrders).As("o"), jf.EqColumn(colUserID, "o.user_id")).
			Where(f.Eq(colUserStatus, statusActive)).
			GroupBy(colUserID, colUserName).
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
		assert.Equal(t, statusActive, args[0])
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
			Where(f.Eq("l.status", statusActive))

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

		query := qb.Select(colID, qb.Expr(countClause)).From(tableProducts)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT id, COUNT(*) FROM products")
		assert.Empty(t, args)
	})

	t.Run("Expression with alias", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(colCategory, qb.Expr("SUM(amount)", "total_sales")).From(tableOrders)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT category, SUM(amount) AS total_sales FROM orders")
		assert.Empty(t, args)
	})

	t.Run("Multiple expressions with aliases", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			colCategory,
			qb.Expr(countClause, "product_count"),
			qb.Expr("AVG(price)", "avg_price"),
			qb.Expr("SUM(stock)", "total_stock"),
		).From(tableProducts)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT category, COUNT(*) AS product_count, AVG(price) AS avg_price, SUM(stock) AS total_stock")
		assert.Contains(t, sql, fromProductsClause)
		assert.Empty(t, args)
	})

	t.Run("Mixed strings and expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			colID,
			colName,
			qb.Expr("UPPER(category)", "upper_category"),
			colStatus,
		).From(tableProducts)

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
		).From(tableUsers)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT user_id, COALESCE(email, phone, 'N/A') AS contact FROM users")
		assert.Empty(t, args)
	})

	t.Run("Window function expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(
			"product_id",
			colCategory,
			colPrice,
			qb.Expr("ROW_NUMBER() OVER (PARTITION BY category ORDER BY price DESC)", "rank"),
		).From(tableProducts)

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT product_id, category, price, ROW_NUMBER() OVER (PARTITION BY category ORDER BY price DESC) AS rank")
		assert.Contains(t, sql, fromProductsClause)
		assert.Empty(t, args)
	})

	t.Run("Oracle reserved word column with expression", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			colLevel,
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
			From(tableOrders).
			GroupBy(qb.Expr(testExpr)).
			Where(f.Eq(colStatus, "completed"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT DATE(created_at) AS date, COUNT(*) AS count FROM orders")
		assert.Contains(t, sql, testWhereClause)
		assert.Contains(t, sql, groupByClause)
		assert.Equal(t, []any{"completed"}, args)
	})

	t.Run("Mixed column names and expressions in GROUP BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(
			colCategory,
			qb.Expr("YEAR(order_date)", "year"),
			qb.Expr("SUM(amount)", "total"),
		).
			From(tableOrders).
			GroupBy(colCategory, qb.Expr("YEAR(order_date)"))

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
			From(tableOrders).
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

		query := qb.Select(selectAll).
			From(tableProducts).
			OrderBy(qb.Expr("COUNT(*) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT * FROM products ORDER BY COUNT(*) DESC")
		assert.Empty(t, args)
	})

	t.Run("Mixed column names and expressions in ORDER BY", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		query := qb.Select(colID, colName, colPrice).
			From(tableProducts).
			OrderBy(colName, qb.Expr("UPPER(category) ASC"), "price DESC")

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "ORDER BY name, UPPER(category) ASC, price DESC")
		assert.Empty(t, args)
	})

	t.Run("ORDER BY with expression using function", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		query := qb.Select(colID, "created_at").
			From(tableOrders).
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
			qb.Select(colID, 123, colName).From(tableUsers)
		})
	})

	t.Run("Unsupported type in GroupBy panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "unsupported groupBy type: int (must be string or RawExpression)", func() {
			qb.Select(selectAll).From(tableUsers).GroupBy(colID, 123)
		})
	})

	t.Run("Unsupported type in OrderBy panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "unsupported orderBy type: float64 (must be string or RawExpression)", func() {
			qb.Select(selectAll).From(tableUsers).OrderBy(colID, 3.14)
		})
	})

	t.Run("Empty expression SQL panics", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "expression SQL cannot be empty", func() {
			qb.Select(qb.Expr("")).From(tableUsers)
		})
	})

	t.Run("Multiple aliases panic", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.PanicsWithValue(t, "Expr accepts maximum 1 alias, got 2", func() {
			qb.Select(qb.Expr(countClause, "total", "count")).From(tableUsers)
		})
	})

	t.Run("Dangerous alias characters panic", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		assert.Panics(t, func() {
			qb.Select(qb.Expr(countClause, "total;DROP TABLE users")).From(tableUsers)
		})
	})
}

func TestComplexExpressionQueries(t *testing.T) {
	t.Run("Aggregation query with GROUP BY and HAVING", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		f := qb.Filter()

		query := qb.Select(
			colCategory,
			qb.Expr(countClause, "product_count"),
			qb.Expr("AVG(price)", "avg_price"),
			qb.Expr("MIN(price)", "min_price"),
			qb.Expr("MAX(price)", "max_price"),
		).
			From(tableProducts).
			Where(f.Eq(colStatus, statusActive)).
			GroupBy(colCategory).
			Having("COUNT(*) > ?", 5).
			OrderBy(qb.Expr("COUNT(*) DESC"))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)

		// Verify SELECT clause
		assert.Contains(t, sql, "SELECT category, COUNT(*) AS product_count, AVG(price) AS avg_price")
		assert.Contains(t, sql, "MIN(price) AS min_price, MAX(price) AS max_price")

		// Verify WHERE, GROUP BY, HAVING, ORDER BY
		assert.Contains(t, sql, testWhereClause)
		assert.Contains(t, sql, "GROUP BY category")
		assert.Contains(t, sql, "HAVING COUNT(*) > $2")
		assert.Contains(t, sql, "ORDER BY COUNT(*) DESC")

		assert.Equal(t, []any{statusActive, 5}, args)
	})

	t.Run("Complex query with subqueries and expressions", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		f := qb.Filter()

		subquery := qb.Select("category_id").
			From("featured_categories").
			Where(f.Eq(statusActive, true))

		query := qb.Select(
			colID,
			colName,
			qb.Expr("price * 1.1", "price_with_tax"),
			qb.Expr("UPPER(category)", "upper_category"),
		).
			From(tableProducts).
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
			From(tableOrders).
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

// ========== Struct-Based Column Extraction Tests (v2.3+) ==========

// TestColumnsSelect tests struct-based columns with SELECT queries
func TestColumnsSelect(t *testing.T) {
	t.Run("PostgreSQL - Simple SELECT", func(t *testing.T) {
		// Clear global registry before test
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})

		query := qb.Select(cols.Col(fieldID), cols.Col(fieldName), cols.Col(fieldEmail)).
			From(tableUsers)

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT id, name, email FROM users", sql)
	})

	t.Run("Oracle - SELECT with reserved words", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.Oracle)
		cols := qb.Columns(&IntegrationAccount{})

		query := qb.Select(cols.Col(fieldID), cols.Col(fieldNumber), cols.Col(fieldLevel)).
			From(tableAccounts)

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		// Oracle should quote reserved words
		assert.Contains(t, sql, `"number"`)
		assert.Contains(t, sql, `"level"`)
	})

	t.Run("SELECT with Fields() helper", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})

		query := qb.Select(toAny(cols.Cols(fieldID, fieldName, fieldEmail))...).
			From(tableUsers)

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT id, name, email FROM users", sql)
	})

	t.Run("SELECT with All() helper", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})

		query := qb.Select(toAny(cols.All())...).
			From(tableUsers)

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT id, name, email, status, created_at FROM users", sql)
	})
}

// TestColumnsWhere tests struct-based columns with WHERE clauses
func TestColumnsWhere(t *testing.T) {
	t.Run("PostgreSQL - WHERE with struct columns", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})
		f := qb.Filter()

		query := qb.Select(toAny(cols.All())...).
			From(tableUsers).
			Where(f.Eq(cols.Col(fieldStatus), statusActive))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, testWhereClause)
		assert.Equal(t, []any{statusActive}, args)
	})

	t.Run("Oracle - WHERE with reserved word", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.Oracle)
		cols := qb.Columns(&IntegrationAccount{})
		f := qb.Filter()

		query := qb.Select(selectAll).
			From(tableAccounts).
			Where(f.Eq(cols.Col(fieldLevel), 5))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, `WHERE "level" = :1`)
		assert.Equal(t, []any{5}, args)
	})

	t.Run("Complex WHERE with multiple struct fields", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})
		f := qb.Filter()

		query := qb.Select(toAny(cols.Cols(fieldID, fieldName))...).
			From(tableUsers).
			Where(f.And(
				f.Eq(cols.Col(fieldStatus), statusActive),
				f.NotNull(cols.Col(fieldEmail)),
			))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "status = $1")
		assert.Contains(t, sql, "email IS NOT NULL")
		assert.Equal(t, []any{statusActive}, args)
	})
}

// TestColumnsInsert tests struct-based columns with INSERT queries
func TestColumnsInsert(t *testing.T) {
	t.Run("PostgreSQL - INSERT with struct columns", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})

		query := qb.InsertWithColumns(tableUsers, cols.Cols(fieldName, fieldEmail, fieldStatus)...).
			Values("John Doe", testEmail, statusActive)

		sql, args, err := query.ToSql()
		require.NoError(t, err)
		assert.Equal(t, "INSERT INTO users (name,email,status) VALUES ($1,$2,$3)", sql)
		assert.Equal(t, []any{"John Doe", testEmail, statusActive}, args)
	})

	t.Run("Oracle - INSERT with reserved words", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.Oracle)
		cols := qb.Columns(&IntegrationAccount{})

		query := qb.InsertWithColumns(tableAccounts, cols.Cols(fieldNumber, fieldLevel, "Size")...).
			Values(testAccountID, 3, "large")

		sql, args, err := query.ToSql()
		require.NoError(t, err)
		// Oracle should quote reserved words in column list
		assert.Contains(t, sql, `"number"`)
		assert.Contains(t, sql, `"level"`)
		assert.Contains(t, sql, `"size"`)
		assert.Equal(t, []any{testAccountID, 3, "large"}, args)
	})

	t.Run("INSERT with All() helper", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationProduct{})

		query := qb.InsertWithColumns(tableProducts, cols.All()...).
			Values(1, "Widget", "9.99")

		sql, args, err := query.ToSql()
		require.NoError(t, err)
		assert.Equal(t, "INSERT INTO products (id,name,price) VALUES ($1,$2,$3)", sql)
		assert.Equal(t, []any{1, "Widget", "9.99"}, args)
	})
}

// TestColumnsUpdate tests struct-based columns with UPDATE queries
func TestColumnsUpdate(t *testing.T) {
	t.Run("PostgreSQL - UPDATE with struct columns", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})
		f := qb.Filter()

		query := qb.Update(tableUsers).
			Set(cols.Col(fieldName), "Jane Doe").
			Set(cols.Col(fieldStatus), statusInactive).
			Where(f.Eq(cols.Col(fieldID), 123))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "UPDATE users SET name = $1, status = $2")
		assert.Contains(t, sql, "WHERE id = $3")
		assert.Equal(t, []any{"Jane Doe", statusInactive, 123}, args)
	})

	t.Run("Oracle - UPDATE with reserved words", func(t *testing.T) {
		columns.ClearGlobalRegistry()
		defer columns.ClearGlobalRegistry()

		qb := NewQueryBuilder(dbtypes.Oracle)
		cols := qb.Columns(&IntegrationAccount{})
		f := qb.Filter()

		query := qb.Update(tableAccounts).
			Set(cols.Col(fieldLevel), 5).
			Where(f.Eq(cols.Col(fieldNumber), testAccountID))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, `UPDATE accounts SET "level" = :1`)
		assert.Contains(t, sql, `WHERE "number" = :2`)
		assert.Equal(t, []any{5, testAccountID}, args)
	})
}

// TestColumnsCaching tests that metadata is cached across queries
func TestColumnsCaching(t *testing.T) {
	columns.ClearGlobalRegistry()
	defer columns.ClearGlobalRegistry()

	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	// First access: should parse struct
	cols1 := qb.Columns(&IntegrationUser{})
	require.NotNil(t, cols1)

	// Second access: should return cached instance
	cols2 := qb.Columns(&IntegrationUser{})
	require.NotNil(t, cols2)

	// Should be the same instance
	assert.Same(t, cols1, cols2, "Cached column metadata should return same instance")
}

// TestColumnsVendorIsolation tests that different vendors have separate caches
func TestColumnsVendorIsolation(t *testing.T) {
	columns.ClearGlobalRegistry()
	defer columns.ClearGlobalRegistry()

	oracleQb := NewQueryBuilder(dbtypes.Oracle)
	pgQb := NewQueryBuilder(dbtypes.PostgreSQL)

	// Same struct, different vendors
	oracleCols := oracleQb.Columns(&IntegrationAccount{})
	pgCols := pgQb.Columns(&IntegrationAccount{})

	// Oracle should quote reserved word "number"
	assert.Contains(t, oracleCols.Col("Number"), `"`)

	// PostgreSQL should not quote "number"
	assert.NotContains(t, pgCols.Col("Number"), `"`)

	// Different instances due to different vendors
	assert.NotSame(t, oracleCols, pgCols)
}

// TestColumnsPanic tests panic behavior for invalid usage
func TestColumnsPanic(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	t.Run("Panic on invalid field name", func(t *testing.T) {
		cols := qb.Columns(&IntegrationUser{})

		assert.Panics(t, func() {
			cols.Col("NonExistentField")
		}, "Should panic on non-existent field name")
	})

	t.Run("Panic on struct with no db tags", func(t *testing.T) {
		type NoTags struct {
			ID   int64
			Name string
		}

		assert.Panics(t, func() {
			qb.Columns(&NoTags{})
		}, "Should panic on struct with no db tags")
	})
}

// TestColumnsComplexQuery tests realistic complex query patterns
func TestColumnsComplexQuery(t *testing.T) {
	columns.ClearGlobalRegistry()
	defer columns.ClearGlobalRegistry()

	t.Run("Complex query with JOINs and struct columns", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)
		userCols := qb.Columns(&IntegrationUser{})
		acctCols := qb.Columns(&IntegrationAccount{})
		jf := qb.JoinFilter()
		f := qb.Filter()

		query := qb.Select(
			"u."+userCols.Col(fieldID),
			"u."+userCols.Col(fieldName),
			"a."+acctCols.Col(fieldNumber),
		).
			From(dbtypes.Table(tableUsers).As(aliasU)).
			JoinOn(dbtypes.Table(tableAccounts).As(aliasA), jf.EqColumn(
				"u."+userCols.Col(fieldID),
				"a."+acctCols.Col(fieldID),
			)).
			Where(f.Eq("u."+userCols.Col(fieldStatus), statusActive))

		sql, args, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT u.id, u.name, a")
		assert.Contains(t, sql, `a."number"`) // Oracle quotes reserved word
		assert.Contains(t, sql, "JOIN accounts a ON u.id = a.id")
		assert.Contains(t, sql, "WHERE u.status = :1")
		assert.Equal(t, []any{statusActive}, args)
	})

	t.Run("Query with ORDER BY and GROUP BY using struct columns", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)
		cols := qb.Columns(&IntegrationUser{})

		query := qb.Select(cols.Col(fieldStatus), qb.Expr("COUNT(*)", "user_count")).
			From(tableUsers).
			GroupBy(cols.Col(fieldStatus)).
			OrderBy(cols.Col(fieldStatus))

		sql, _, err := query.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT status, COUNT(*) AS user_count")
		assert.Contains(t, sql, "FROM users")
		assert.Contains(t, sql, "GROUP BY status")
		assert.Contains(t, sql, "ORDER BY status")
	})
}

// TestColumnsMultipleTypes tests using multiple struct types in same query
func TestColumnsMultipleTypes(t *testing.T) {
	columns.ClearGlobalRegistry()
	defer columns.ClearGlobalRegistry()

	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	// Register multiple types
	userCols := qb.Columns(&IntegrationUser{})
	productCols := qb.Columns(&IntegrationProduct{})

	// Both should be cached and independently accessible
	assert.NotNil(t, userCols)
	assert.NotNil(t, productCols)

	// Verify correct columns from each type
	assert.Equal(t, colID, userCols.Col(fieldID))
	assert.Equal(t, colName, userCols.Col(fieldName))
	assert.Equal(t, colEmail, userCols.Col(fieldEmail))

	assert.Equal(t, colID, productCols.Col(fieldID))
	assert.Equal(t, colName, productCols.Col(fieldName))
	assert.Equal(t, colPrice, productCols.Col("Price"))
}

// toAny converts []string to []any for SELECT operations with spread operator
func toAny(vals []string) []any {
	result := make([]any, len(vals))
	for i, v := range vals {
		result[i] = v
	}
	return result
}

// ========== v2.4 Aliased Columns Tests ==========

func TestColumnAliasingUnaliased(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	cols := qb.Columns(&IntegrationUser{})

	// Unaliased columns should return bare column names
	assert.Equal(t, colID, cols.Col(fieldID))
	assert.Equal(t, colName, cols.Col(fieldName))
	assert.Equal(t, colEmail, cols.Col(fieldEmail))
	assert.Equal(t, "", cols.Alias())

	// Cols should return bare column names
	result := cols.Cols(fieldID, fieldName)
	assert.Equal(t, []string{colID, colName}, result)

	// All should return all bare column names
	allCols := cols.All()
	assert.Contains(t, allCols, colID)
	assert.Contains(t, allCols, colName)
	assert.Contains(t, allCols, colEmail)
}

func TestColumnAliasingWithAlias(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	cols := qb.Columns(&IntegrationUser{})

	// Create aliased instance
	u := cols.As(aliasU)

	// Aliased columns should return qualified names
	assert.Equal(t, colUserID, u.Col(fieldID))
	assert.Equal(t, colUserName, u.Col(fieldName))
	assert.Equal(t, colUserEmail, u.Col(fieldEmail))
	assert.Equal(t, aliasU, u.Alias())

	// Cols should return qualified names
	result := u.Cols(fieldID, fieldName)
	assert.Equal(t, []string{colUserID, colUserName}, result)

	// All should return all qualified names
	allCols := u.All()
	assert.Contains(t, allCols, colUserID)
	assert.Contains(t, allCols, colUserName)
	assert.Contains(t, allCols, colUserEmail)

	// Original should remain unaliased
	assert.Equal(t, colID, cols.Col(fieldID))
	assert.Equal(t, "", cols.Alias())
}

func TestColumnAliasingOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	cols := qb.Columns(&IntegrationAccount{})

	// Oracle reserved words should be quoted
	assert.Contains(t, cols.Col(fieldNumber), `"`) // "NUMBER"
	assert.Contains(t, cols.Col(fieldLevel), `"`)  // "LEVEL"
	assert.NotContains(t, cols.Col(fieldID), `"`)  // ID not reserved

	// With alias, qualified and quoted
	a := cols.As(aliasA)
	assert.Contains(t, a.Col(fieldNumber), "a.")
	assert.Contains(t, a.Col(fieldNumber), `"`) // a."NUMBER"
	assert.Equal(t, aliasA, a.Alias())
}

func TestColumnAliasingMultipleAliases(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	cols := qb.Columns(&IntegrationUser{})

	// Create multiple aliases from same base
	u := cols.As(aliasU)
	u2 := cols.As("u2")
	u3 := u.As("u3") // Alias from already-aliased instance

	// Each should have independent alias
	assert.Equal(t, colUserID, u.Col(fieldID))
	assert.Equal(t, "u2.id", u2.Col(fieldID))
	assert.Equal(t, "u3.id", u3.Col(fieldID))

	// Original remains unchanged
	assert.Equal(t, colID, cols.Col(fieldID))
}

func TestColumnAliasingInSelectQuery(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	userCols := qb.Columns(&IntegrationUser{})
	acctCols := qb.Columns(&IntegrationAccount{})

	u := userCols.As(aliasU)
	a := acctCols.As(aliasA)

	jf := qb.JoinFilter()
	f := qb.Filter()

	query := qb.Select(
		u.Col(fieldID),
		u.Col(fieldName),
		a.Col(fieldNumber),
	).
		From(dbtypes.Table(tableUsers).As(aliasU)).
		JoinOn(dbtypes.Table(tableAccounts).As(aliasA), jf.EqColumn(
			u.Col(fieldID),
			a.Col(fieldID),
		)).
		Where(f.Eq(u.Col(fieldStatus), statusActive))

	sql, args, err := query.ToSQL()
	require.NoError(t, err)

	assert.Contains(t, sql, colUserID)
	assert.Contains(t, sql, colUserName)
	assert.Contains(t, sql, colAccountNumber)
	assert.Contains(t, sql, "u.status = $")
	assert.Equal(t, []any{statusActive}, args)
}

// ========== v2.4 Struct-to-Query Tests ==========

func TestInsertStructAllFields(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	user := IntegrationUser{
		ID:     0, // Zero ID should be excluded
		Name:   "Alice",
		Email:  testEmail,
		Status: statusActive,
	}

	query := qb.InsertStruct(tableUsers, &user)
	sql, args, err := query.ToSql()

	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO users")
	assert.Contains(t, sql, colName)
	assert.Contains(t, sql, colEmail)
	assert.Contains(t, sql, colStatus)
	assert.NotContains(t, sql, colID) // Zero ID excluded
	assert.Contains(t, args, "Alice")
	assert.Contains(t, args, testEmail)
	assert.Contains(t, args, statusActive)
}

func TestInsertStructWithNonZeroID(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	user := IntegrationUser{
		ID:     123, // Non-zero ID should be included
		Name:   "Bob",
		Email:  testEmail,
		Status: statusInactive,
	}

	query := qb.InsertStruct(tableUsers, &user)
	sql, args, err := query.ToSql()

	require.NoError(t, err)
	assert.Contains(t, sql, colID) // Non-zero ID included
	assert.Contains(t, args, int64(123))
}

// TestIsZeroValueIDFieldExactMatching verifies that only columns exactly named "id"
// are treated as ID columns, not columns containing "id" as a substring.
func TestIsZeroValueIDFieldExactMatching(t *testing.T) {
	tests := []struct {
		name        string
		column      string
		value       any
		shouldBeID  bool
		description string
	}{
		// Exact "id" matching (should be treated as ID)
		{"exact id lowercase", "id", int64(0), true, "Plain 'id' column"},
		{"exact id uppercase", "ID", int64(0), true, "Uppercase 'ID' column"},
		{"exact id mixed case", "Id", int64(0), true, "Mixed case 'Id' column"},
		{"exact id double quoted", `"id"`, int64(0), true, "Double-quoted 'id' column"},
		{"exact id backticks", "`id`", int64(0), true, "Backtick-quoted 'id' column"},
		{"exact id square brackets", "[id]", int64(0), true, "Square bracket-quoted 'id' column"},
		{"qualified id simple", "users.id", int64(0), true, "Qualified 'users.id' column"},
		{"qualified id quoted", `"users"."id"`, int64(0), true, "Quoted qualified column"},
		{"qualified id schema", `"schema"."table"."id"`, int64(0), true, "Fully qualified with schema"},

		// Columns containing "id" substring (should NOT be treated as ID)
		{"substring paid", statusPaid, int64(0), false, "Column 'paid' (payment status)"},
		{"substring video", testVideo, string(""), false, "Column 'video' (media URL)"},
		{"substring provider", "provider", string(""), false, "Column 'provider' (service name)"},
		{"substring validate", "validate", int64(0), false, "Column 'validate' (validation flag)"},
		{"substring liquidate", "liquidate", int64(0), false, "Column 'liquidate' (action)"},
		{"substring holiday", "holiday", string(""), false, "Column 'holiday' (date)"},
		{"substring confidence", "confidence", int64(0), false, "Column 'confidence' (score)"},
		{"substring identifier", "identifier", string(""), false, "Column 'identifier' (some ID)"},

		// Foreign keys (should NOT be treated as ID per exact-match requirement)
		{"foreign key user_id", "user_id", int64(0), false, "Foreign key 'user_id'"},
		{"foreign key order_id", "order_id", int64(0), false, "Foreign key 'order_id'"},
		{"foreign key product_id", "product_id", int64(0), false, "Foreign key 'product_id'"},

		// Non-zero values (should NOT trigger zero-value skip)
		{"non-zero id", colID, int64(123), false, "Non-zero ID should be included"},
		{"non-zero paid", statusPaid, int64(1), false, "Non-zero paid status"},

		// Non-matching types (should return false)
		{"wrong type float", "id", float64(0.0), false, "Float type not supported"},
		{"wrong type bool", "id", false, false, "Bool type not supported"},
		{"wrong type struct", "id", struct{}{}, false, "Struct type not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)
			result := qb.isZeroValueIDField(tt.column, tt.value)

			if tt.shouldBeID {
				assert.True(t, result, "Expected %s to be treated as zero-value ID field: %s", tt.column, tt.description)
			} else {
				assert.False(t, result, "Expected %s NOT to be treated as zero-value ID field: %s", tt.column, tt.description)
			}
		})
	}
}

// TestInsertStructColumnsWithIDSubstring verifies that columns containing "id"
// as a substring (like "paid", "video") are correctly included in INSERT statements.
func TestInsertStructColumnsWithIDSubstring(t *testing.T) {
	// Define a test struct with columns containing "id" substring
	type Payment struct {
		ID       int64  `db:"id"`
		Amount   int    `db:"amount"`
		Paid     int    `db:"paid"`     // Contains "id" substring
		Video    string `db:"video"`    // Contains "id" substring
		Provider string `db:"provider"` // Contains "id" substring
	}

	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	payment := Payment{
		ID:       0, // Zero - should be excluded (auto-increment)
		Amount:   100,
		Paid:     0,  // Zero but NOT an ID - should be included
		Video:    "", // Empty but NOT an ID - should be included
		Provider: "", // Empty but NOT an ID - should be included
	}

	query := qb.InsertStruct("payments", &payment)
	sql, args, err := query.ToSql()

	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO payments")

	// All columns except "id" should be included
	assert.Contains(t, sql, "amount", "amount column should be included")
	assert.Contains(t, sql, statusPaid, "paid column should be included (not treated as ID)")
	assert.Contains(t, sql, testVideo, "video column should be included (not treated as ID)")
	assert.Contains(t, sql, "provider", "provider column should be included (not treated as ID)")

	// Only the actual "id" column should be excluded (check all possible positions)
	assert.NotContains(t, sql, "(id,", "id column should be excluded at start")
	assert.NotContains(t, sql, ",id,", "id column should be excluded in middle")
	assert.NotContains(t, sql, ",id)", "id column should be excluded at end")

	// Verify all values are present in args
	assert.Contains(t, args, 100, "amount value should be in args")
	assert.Contains(t, args, 0, "paid value (0) should be in args")
	assert.Contains(t, args, "", "video value (empty string) should be in args")
}

func TestInsertFieldsSelectiveFields(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	user := IntegrationUser{
		ID:     456,
		Name:   "Charlie",
		Email:  testEmail,
		Status: statusPending,
	}

	// Insert only Name and Email
	query := qb.InsertFields(tableUsers, &user, fieldName, fieldEmail)
	sql, args, err := query.ToSql()

	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO users")
	assert.Contains(t, sql, colName)
	assert.Contains(t, sql, colEmail)
	assert.NotContains(t, sql, colStatus) // Not requested
	assert.NotContains(t, sql, colID)     // Not requested
	assert.Equal(t, []any{"Charlie", testEmail}, args)
}

func TestInsertFieldsInvalidField(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	user := IntegrationUser{}

	assert.Panics(t, func() {
		qb.InsertFields("users", &user, "InvalidField")
	}, "Should panic on invalid field name")
}

func TestInsertStructOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	account := IntegrationAccount{
		ID:     0,
		Number: testAccountID,
		Level:  5,
		Size:   "large",
	}

	query := qb.InsertStruct(tableAccounts, &account)
	sql, args, err := query.ToSql()

	require.NoError(t, err)
	// Oracle should quote reserved words
	assert.Contains(t, sql, `"number"`)
	assert.Contains(t, sql, `"level"`)
	assert.Contains(t, sql, `"size"`)
	assert.Contains(t, args, testAccountID)
	assert.Contains(t, args, 5)
	assert.Contains(t, args, "large")
}

func TestSetStructAllFields(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	user := IntegrationUser{
		ID:     999, // ID included in SET (no filtering for UPDATE)
		Name:   "Updated Name",
		Email:  "updated@example.com",
		Status: statusInactive,
	}

	query := qb.Update(tableUsers).
		SetStruct(&user).
		Where(f.Eq(colID, 123))

	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, "UPDATE users")
	assert.Contains(t, sql, colName+" =")
	assert.Contains(t, sql, colEmail+" =")
	assert.Contains(t, sql, colStatus+" =")
	assert.Contains(t, args, "Updated Name")
	assert.Contains(t, args, "updated@example.com")
	assert.Contains(t, args, statusInactive)
}

func TestSetStructSelectiveFields(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	f := qb.Filter()

	user := IntegrationUser{
		ID:     999,
		Name:   "Selective Update",
		Email:  "selective@example.com",
		Status: statusPending,
	}

	// Update only Name and Status
	query := qb.Update(tableUsers).
		SetStruct(&user, fieldName, fieldStatus).
		Where(f.Eq(colID, 456))

	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, colName+" =")
	assert.Contains(t, sql, colStatus+" =")
	assert.NotContains(t, sql, colEmail) // Not requested
	assert.Contains(t, args, "Selective Update")
	assert.Contains(t, args, statusPending)
	assert.NotContains(t, args, "selective@example.com")
}

func TestSetStructInvalidField(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	user := IntegrationUser{}

	assert.Panics(t, func() {
		qb.Update("users").SetStruct(&user, "NonExistentField")
	}, "Should panic on invalid field name")
}

func TestSetStructOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	account := IntegrationAccount{
		ID:     789,
		Number: "ACC-999",
		Level:  10,
		Size:   "xlarge",
	}

	query := qb.Update(tableAccounts).
		SetStruct(&account).
		Where(f.Eq(colID, 789))

	sql, args, err := query.ToSQL()

	require.NoError(t, err)
	// Oracle should quote reserved words
	assert.Contains(t, sql, `"number"`)
	assert.Contains(t, sql, `"level"`)
	assert.Contains(t, sql, `"size"`)
	assert.Contains(t, args, "ACC-999")
	assert.Contains(t, args, 10)
	assert.Contains(t, args, "xlarge")
}

func TestFieldMapWithAlias(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	cols := qb.Columns(&IntegrationUser{})

	user := IntegrationUser{
		ID:     123,
		Name:   testUser,
		Email:  testEmail,
		Status: statusActive,
	}

	// Unaliased FieldMap
	fieldMap := cols.FieldMap(&user)
	assert.Equal(t, int64(123), fieldMap[colID])
	assert.Equal(t, testUser, fieldMap[colName])
	assert.Equal(t, testEmail, fieldMap[colEmail])

	// Aliased FieldMap
	u := cols.As(aliasU)
	aliasedMap := u.FieldMap(&user)
	assert.Equal(t, int64(123), aliasedMap[colUserID])
	assert.Equal(t, testUser, aliasedMap[colUserName])
	assert.Equal(t, testEmail, aliasedMap[colUserEmail])
}

func TestAllFieldsWithAlias(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	cols := qb.Columns(&IntegrationUser{})

	user := IntegrationUser{
		ID:     456,
		Name:   allFieldsTest,
		Email:  testEmail,
		Status: statusPending,
	}

	// Unaliased AllFields
	colNames, values := cols.AllFields(&user)
	assert.Contains(t, colNames, colID)
	assert.Contains(t, colNames, colName)
	assert.Contains(t, values, int64(456))
	assert.Contains(t, values, allFieldsTest)

	// Aliased AllFields
	u := cols.As(aliasU)
	aliasedCols, aliasedVals := u.AllFields(&user)
	assert.Contains(t, aliasedCols, colUserID)
	assert.Contains(t, aliasedCols, colUserName)
	assert.Contains(t, aliasedVals, int64(456))
	assert.Contains(t, aliasedVals, allFieldsTest)
}
