package database

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test SQL constants to avoid duplication
const (
	insertIntoUsers = "INSERT INTO users"
)

// Helper function to convert []any to []driver.Value for sqlmock
func convertToDriverValues(args []any) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	return values
}

func TestNewQueryBuilder(t *testing.T) {
	tests := []struct {
		name   string
		vendor string
	}{
		{
			name:   "postgresql",
			vendor: PostgreSQL,
		},
		{
			name:   "oracle",
			vendor: Oracle,
		},
		{
			name:   "unknown",
			vendor: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			assert.NotNil(t, qb)
			assert.Equal(t, tt.vendor, qb.Vendor())
		})
	}
}

func TestQueryBuilderVendor(t *testing.T) {
	pgBuilder := NewQueryBuilder(PostgreSQL)
	assert.Equal(t, PostgreSQL, pgBuilder.Vendor())

	oracleBuilder := NewQueryBuilder(Oracle)
	assert.Equal(t, Oracle, oracleBuilder.Vendor())
}

func TestQueryBuilderSelect(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		columns  []string
		expected string
	}{
		{
			name:     "postgresql_basic",
			vendor:   PostgreSQL,
			columns:  []string{"id", "name"},
			expected: "SELECT id, name",
		},
		{
			name:     "oracle_basic",
			vendor:   Oracle,
			columns:  []string{"id", "name"},
			expected: "SELECT id, name",
		},
		{
			name:     "oracle_reserved_word",
			vendor:   Oracle,
			columns:  []string{"id", "number"},
			expected: `SELECT id, "number"`,
		},
		{
			name:     "oracle_count_function",
			vendor:   Oracle,
			columns:  []string{"COUNT(*)"},
			expected: "SELECT COUNT(*)",
		},
		{
			name:     "oracle_mixed_columns_and_functions",
			vendor:   Oracle,
			columns:  []string{"id", "COUNT(*)", "number", "SUM(balance)"},
			expected: `SELECT id, COUNT(*), "number", SUM(balance)`,
		},
		{
			name:     "postgresql_single_column",
			vendor:   PostgreSQL,
			columns:  []string{"*"},
			expected: "SELECT *",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Select(tt.columns...)

			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expected)
		})
	}
}

func TestQueryBuilderInsert(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		table    string
		expected string
	}{
		{
			name:     "postgresql",
			vendor:   PostgreSQL,
			table:    "users",
			expected: insertIntoUsers,
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			table:    "users",
			expected: insertIntoUsers,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Insert(tt.table).Columns("name").Values("test")

			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expected)
		})
	}
}

func TestQueryBuilderUpdate(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		table    string
		expected string
	}{
		{
			name:     "postgresql",
			vendor:   PostgreSQL,
			table:    "users",
			expected: "UPDATE users",
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			table:    "users",
			expected: "UPDATE users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Update(tt.table).Set("name", "test")

			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expected)
		})
	}
}

func TestQueryBuilderInsertWithColumnsOracleReserved(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	// Build an INSERT with a reserved column name for Oracle
	query := qb.InsertWithColumns("accounts", "id", "number", "name").Values(1, "123", "John")
	sql, _, err := query.ToSql()
	require.NoError(t, err)

	// Should quote the reserved column and use Oracle-style placeholders
	assert.Contains(t, sql, "INSERT INTO accounts")
	assert.Contains(t, sql, `"number"`)
}

// Removed test for exported QuoteColumns helper (API pruned).

func TestQueryBuilderDelete(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		table    string
		expected string
	}{
		{
			name:     "postgresql",
			vendor:   PostgreSQL,
			table:    "users",
			expected: "DELETE FROM users",
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			table:    "users",
			expected: "DELETE FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Delete(tt.table).Where("id = ?", 1)

			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expected)
		})
	}
}

func TestQueryBuilderBuildCaseInsensitiveLike(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		column   string
		value    string
		expected string
	}{
		{
			name:     "postgresql_ilike",
			vendor:   PostgreSQL,
			column:   "name",
			value:    "john",
			expected: "name ILIKE",
		},
		{
			name:     "oracle_upper",
			vendor:   Oracle,
			column:   "name",
			value:    "john",
			expected: "UPPER(name) LIKE",
		},
		{
			name:     "oracle_reserved_word",
			vendor:   Oracle,
			column:   "number",
			value:    "123",
			expected: `UPPER("number") LIKE`,
		},
		{
			name:     "unknown_vendor",
			vendor:   "unknown",
			column:   "name",
			value:    "john",
			expected: "name LIKE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			condition := qb.BuildCaseInsensitiveLike(tt.column, tt.value)

			// Use the condition in a query to generate SQL
			query := qb.Select("*").From("table").Where(condition)
			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expected)
		})
	}
}

func TestQueryBuilderBuildLimitOffset(t *testing.T) {
	tests := []struct {
		name        string
		vendor      string
		limit       int
		offset      int
		expected    []string
		notExpected []string
	}{
		{
			name:     "postgresql_limit_offset",
			vendor:   PostgreSQL,
			limit:    10,
			offset:   5,
			expected: []string{"LIMIT 10", "OFFSET 5"},
		},
		{
			name:        "postgresql_limit_only",
			vendor:      PostgreSQL,
			limit:       10,
			offset:      0,
			expected:    []string{"LIMIT 10"},
			notExpected: []string{"OFFSET"},
		},
		{
			name:        "oracle_offset_fetch",
			vendor:      Oracle,
			limit:       10,
			offset:      5,
			expected:    []string{"OFFSET 5 ROWS", "FETCH NEXT 10 ROWS ONLY"},
			notExpected: []string{"LIMIT"},
		},
		{
			name:        "zero_values",
			vendor:      PostgreSQL,
			limit:       0,
			offset:      0,
			notExpected: []string{"LIMIT", "OFFSET", "FETCH NEXT"},
		},
		{
			name:        "oracle_offset_only",
			vendor:      Oracle,
			limit:       0,
			offset:      7,
			expected:    []string{"OFFSET 7 ROWS"},
			notExpected: []string{"FETCH NEXT", "LIMIT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Select("*").From("table")
			query = qb.BuildLimitOffset(query, tt.limit, tt.offset)

			sql, _, err := query.ToSql()
			require.NoError(t, err)

			for _, fragment := range tt.expected {
				assert.Contains(t, sql, fragment)
			}
			for _, fragment := range tt.notExpected {
				assert.NotContains(t, sql, fragment)
			}
		})
	}
}

func TestQueryBuilderBuildLimitOffsetDefaultVendor(t *testing.T) {
	qb := NewQueryBuilder("sqlite")
	query := qb.Select("*").From("items")

	// Apply only offset to verify it is respected without LIMIT
	query = qb.BuildLimitOffset(query, 0, 3)
	sqlText, _, err := query.ToSql()
	require.NoError(t, err)
	assert.NotContains(t, sqlText, "LIMIT")
	assert.Contains(t, sqlText, "OFFSET")

	// Apply limit and offset together
	query = qb.BuildLimitOffset(qb.Select("*").From("items"), 4, 2)
	sqlText, _, err = query.ToSql()
	require.NoError(t, err)
	assert.Contains(t, sqlText, "LIMIT 4")
	assert.Contains(t, sqlText, "OFFSET 2")
}

func TestQueryBuilderBuildCurrentTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		expected string
	}{
		{
			name:     "postgresql",
			vendor:   PostgreSQL,
			expected: "NOW()",
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			expected: "SYSDATE",
		},
		{
			name:     "unknown",
			vendor:   "unknown",
			expected: "NOW()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			result := qb.BuildCurrentTimestamp()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryBuilderBuildUUIDGeneration(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		expected string
	}{
		{
			name:     "postgresql",
			vendor:   PostgreSQL,
			expected: "gen_random_uuid()",
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			expected: "SYS_GUID()",
		},
		{
			name:     "unknown",
			vendor:   "unknown",
			expected: "UUID()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			result := qb.BuildUUIDGeneration()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryBuilderBuildBooleanValue(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		value    bool
		expected any
	}{
		{
			name:     "postgresql_true",
			vendor:   PostgreSQL,
			value:    true,
			expected: true,
		},
		{
			name:     "postgresql_false",
			vendor:   PostgreSQL,
			value:    false,
			expected: false,
		},
		{
			name:     "oracle_true",
			vendor:   Oracle,
			value:    true,
			expected: 1,
		},
		{
			name:     "oracle_false",
			vendor:   Oracle,
			value:    false,
			expected: 0,
		},
		{
			name:     "unknown_true",
			vendor:   "unknown",
			value:    true,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			result := qb.BuildBooleanValue(tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryBuilderEscapeIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		vendor     string
		identifier string
		expected   string
	}{
		{
			name:       "postgresql_basic",
			vendor:     PostgreSQL,
			identifier: "table_name",
			expected:   `"table_name"`,
		},
		{
			name:       "oracle_basic",
			vendor:     Oracle,
			identifier: "table_name",
			expected:   `"table_name"`,
		},
		{
			name:       "oracle_mixed_case",
			vendor:     Oracle,
			identifier: "TableName",
			expected:   `"TableName"`,
		},
		{
			name:       "unknown_vendor",
			vendor:     "unknown",
			identifier: "table_name",
			expected:   `"table_name"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			result := qb.EscapeIdentifier(tt.identifier)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryBuilderWhereClauseHelpers(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		column   string
		value    any
		method   string
		expected map[string]any
	}{
		{
			name:     "oracle_eq_reserved_word",
			vendor:   Oracle,
			column:   "number",
			value:    "12345",
			method:   "Eq",
			expected: map[string]any{`"number"`: "12345"},
		},
		{
			name:     "oracle_eq_normal_column",
			vendor:   Oracle,
			column:   "name",
			value:    "John",
			method:   "Eq",
			expected: map[string]any{"name": "John"},
		},
		{
			name:     "postgresql_eq_reserved_word",
			vendor:   PostgreSQL,
			column:   "number",
			value:    123,
			method:   "Eq",
			expected: map[string]any{"number": 123},
		},
		{
			name:     "oracle_noteq_reserved_word",
			vendor:   Oracle,
			column:   "size",
			value:    100,
			method:   "NotEq",
			expected: map[string]any{`"size"`: 100},
		},
		{
			name:     "oracle_gt_reserved_word",
			vendor:   Oracle,
			column:   "level",
			value:    5,
			method:   "Gt",
			expected: map[string]any{`"level"`: 5},
		},
		{
			name:     "oracle_lt_reserved_word",
			vendor:   Oracle,
			column:   "access",
			value:    10,
			method:   "Lt",
			expected: map[string]any{`"access"`: 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)

			var condition map[string]any
			switch tt.method {
			case "Eq":
				condition = map[string]any(qb.Eq(tt.column, tt.value))
			case "NotEq":
				condition = map[string]any(qb.NotEq(tt.column, tt.value))
			case "Gt":
				condition = map[string]any(qb.Gt(tt.column, tt.value))
			case "Lt":
				condition = map[string]any(qb.Lt(tt.column, tt.value))
			case "GtOrEq":
				condition = map[string]any(qb.GtOrEq(tt.column, tt.value))
			case "LtOrEq":
				condition = map[string]any(qb.LtOrEq(tt.column, tt.value))
			}

			assert.Equal(t, tt.expected, condition)
		})
	}
}

func TestQueryBuilderOracleWhereClauseInQuery(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	// Test that the WHERE clause helper creates properly quoted SQL
	query := qb.Select("id", "name", "number").
		From("accounts").
		Where(qb.Eq("number", "12345"))

	sql, args, err := query.ToSql()
	require.NoError(t, err)

	expectedSQL := `SELECT id, name, "number" FROM accounts WHERE "number" = :1`
	assert.Equal(t, expectedSQL, sql)
	assert.Equal(t, []any{"12345"}, args)
}

func TestQueryBuilderBuildUpsertPostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(PostgreSQL)

	insertColumns := map[string]any{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]any{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	require.NoError(t, err)
	assert.NotEmpty(t, sql)
	assert.NotEmpty(t, args)
	assert.Contains(t, sql, insertIntoUsers)
	assert.Contains(t, sql, "ON CONFLICT")
	assert.Contains(t, sql, "DO UPDATE SET")
}

func TestQueryBuilderBuildUpsertOracle(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	insertColumns := map[string]any{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]any{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	// Oracle implementation now provides working MERGE statement
	assert.NoError(t, err)
	assert.Contains(t, sql, "MERGE INTO users")
	assert.Contains(t, sql, "WHEN MATCHED THEN UPDATE")
	assert.Contains(t, sql, "WHEN NOT MATCHED THEN INSERT")
	assert.NotEmpty(t, args)
}

func TestQueryBuilderBuildUpsertUnknown(t *testing.T) {
	qb := NewQueryBuilder("unknown")

	insertColumns := map[string]any{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]any{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	// Unknown vendor now returns proper error instead of empty results
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upsert not supported for database vendor")
	assert.Empty(t, sql)
	assert.Empty(t, args)
}

func TestQueryBuilderPlaceholderFormat(t *testing.T) {
	tests := []struct {
		name      string
		vendor    string
		expectSQL string
	}{
		{
			name:      "postgresql_dollar_placeholders",
			vendor:    PostgreSQL,
			expectSQL: "$1",
		},
		{
			name:      "oracle_colon_placeholders",
			vendor:    Oracle,
			expectSQL: ":1",
		},
		{
			name:      "unknown_question_placeholders",
			vendor:    "unknown",
			expectSQL: "?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Select("*").From("table").Where("id = ?", 1)

			sql, _, err := query.ToSql()
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expectSQL)
		})
	}
}

func TestQueryBuilderIntegrationTest(t *testing.T) {
	// Test a complete query building scenario
	qb := NewQueryBuilder(PostgreSQL)

	// Build a complex SELECT query
	query := qb.Select("id", "name", "email").
		From("users").
		Where("active = ?", true).
		Where(qb.BuildCaseInsensitiveLike("name", "john")).
		OrderBy("name ASC")

	query = qb.BuildLimitOffset(query, 10, 5)

	sql, args, err := query.ToSql()
	require.NoError(t, err)
	assert.NotEmpty(t, sql)
	assert.NotEmpty(t, args)

	// Verify SQL contains expected elements
	assert.Contains(t, sql, "SELECT id, name, email")
	assert.Contains(t, sql, "FROM users")
	assert.Contains(t, sql, "WHERE")
	assert.Contains(t, sql, "ILIKE") // PostgreSQL case-insensitive
	assert.Contains(t, sql, "ORDER BY")
	assert.Contains(t, sql, "LIMIT")
	assert.Contains(t, sql, "OFFSET")
}

// Test query builder with actual SQL execution using sqlmock
func TestQueryBuilderWithSqlmock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	qb := NewQueryBuilder(PostgreSQL)

	// Build a SELECT query
	query := qb.Select("id", "name").From("users").Where("active = ?", true)
	sql, args, err := query.ToSql()
	require.NoError(t, err)

	// Set up mock expectation
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta(sql)).WithArgs(convertToDriverValues(args)...).WillReturnRows(rows)

	// Execute query
	resultRows, err := db.QueryContext(context.Background(), sql, args...)
	require.NoError(t, err)
	defer resultRows.Close()

	// Verify we got expected data
	require.True(t, resultRows.Next())
	var id int
	var name string
	err = resultRows.Scan(&id, &name)
	require.NoError(t, err)
	assert.Equal(t, 1, id)
	assert.Equal(t, "John", name)

	require.NoError(t, mock.ExpectationsWereMet())
}

// Test INSERT with sqlmock
func TestQueryBuilderInsertWithSqlmock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	qb := NewQueryBuilder(PostgreSQL)

	// Build an INSERT query
	query := qb.Insert("users").Columns("name", "email").Values("John", "john@example.com")
	sql, args, err := query.ToSql()
	require.NoError(t, err)

	// Set up mock expectation
	mock.ExpectExec(regexp.QuoteMeta(sql)).WithArgs(convertToDriverValues(args)...).WillReturnResult(sqlmock.NewResult(1, 1))

	// Execute query
	result, err := db.ExecContext(context.Background(), sql, args...)
	require.NoError(t, err)

	// Verify result
	lastID, err := result.LastInsertId()
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastID)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)

	require.NoError(t, mock.ExpectationsWereMet())
}

// Test complex query with joins and subqueries
func TestQueryBuilderComplexQueryWithSqlmock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	qb := NewQueryBuilder(PostgreSQL)

	// Build a complex query with joins
	query := qb.Select("u.id", "u.name", "p.title").
		From("users u").
		Join("posts p ON u.id = p.user_id").
		Where("u.active = ?", true).
		Where(qb.BuildCaseInsensitiveLike("p.title", "go")).
		OrderBy("u.name ASC")

	query = qb.BuildLimitOffset(query, 5, 0)

	sql, args, err := query.ToSql()
	require.NoError(t, err)

	// Set up mock expectation
	rows := sqlmock.NewRows([]string{"id", "name", "title"}).
		AddRow(1, "John", "Learning Go").
		AddRow(2, "Jane", "Go Best Practices")

	mock.ExpectQuery(regexp.QuoteMeta(sql)).WithArgs(convertToDriverValues(args)...).WillReturnRows(rows)

	// Execute query
	resultRows, err := db.QueryContext(context.Background(), sql, args...)
	require.NoError(t, err)
	defer resultRows.Close()

	// Verify we got expected data
	var results []map[string]any
	for resultRows.Next() {
		var id int
		var name, title string
		err = resultRows.Scan(&id, &name, &title)
		require.NoError(t, err)
		results = append(results, map[string]any{
			"id":    id,
			"name":  name,
			"title": title,
		})
	}

	assert.Len(t, results, 2)
	assert.Equal(t, "John", results[0]["name"])
	assert.Equal(t, "Learning Go", results[0]["title"])

	require.NoError(t, mock.ExpectationsWereMet())
}
