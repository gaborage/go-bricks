package builder

import (
	"testing"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testJoinClause = "profiles ON users.id = profiles.user_id"
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

	builder := qb.Update("users")
	sql, _, err := builder.Set("name", "John").ToSql()
	require.NoError(t, err)
	assert.Contains(t, sql, "UPDATE users SET name = $1")
}

func TestDelete(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	builder := qb.Delete("users")
	sql, _, err := builder.ToSql()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users", sql)
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
				sql, _, _ := qb.Select("*").From("users").WhereLte("age", 30).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age <= $1`,
		},
		{
			name: "WhereGte",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").WhereGte("age", 18).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE age >= $1`,
		},
		{
			name: "WhereNotIn",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").WhereNotIn("status", []string{"banned", "deleted"}).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE status NOT IN ($1,$2)`,
		},
		{
			name: "WhereNull",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").WhereNull("deleted_at").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE deleted_at IS NULL`,
		},
		{
			name: "WhereNotNull",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").WhereNotNull("email").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users WHERE email IS NOT NULL`,
		},
		{
			name: "WhereBetween",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").WhereBetween("age", 18, 65).ToSQL()
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
			sql, args, err := qb.Select("*").From("users").WhereLike("name", "john").ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql)
			assert.Len(t, args, 1)
		})
	}
}

// Test JOIN methods
func TestJoinMethods(t *testing.T) {
	tests := []struct {
		name        string
		setupQuery  func(*QueryBuilder) string
		expectedSQL string
	}{
		{
			name: "Join",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").Join(testJoinClause).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "LeftJoin",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").LeftJoin(testJoinClause).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users LEFT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "RightJoin",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").RightJoin(testJoinClause).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users RIGHT JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "InnerJoin",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").InnerJoin(testJoinClause).ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users INNER JOIN profiles ON users.id = profiles.user_id`,
		},
		{
			name: "CrossJoin",
			setupQuery: func(qb *QueryBuilder) string {
				sql, _, _ := qb.Select("*").From("users").CrossJoin("roles").ToSQL()
				return sql
			},
			expectedSQL: `SELECT * FROM users CROSS JOIN roles`,
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
