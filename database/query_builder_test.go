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

// Helper function to convert []interface{} to []driver.Value for sqlmock
func convertToDriverValues(args []interface{}) []driver.Value {
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
			expected: "INSERT INTO users",
		},
		{
			name:     "oracle",
			vendor:   Oracle,
			table:    "users",
			expected: "INSERT INTO users",
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
	assert.Contains(t, sql, `"NUMBER"`)
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
		name         string
		vendor       string
		limit        int
		offset       int
		expectLimit  bool
		expectOffset bool
	}{
		{
			name:         "postgresql_limit_offset",
			vendor:       PostgreSQL,
			limit:        10,
			offset:       5,
			expectLimit:  true,
			expectOffset: true,
		},
		{
			name:         "postgresql_limit_only",
			vendor:       PostgreSQL,
			limit:        10,
			offset:       0,
			expectLimit:  true,
			expectOffset: false,
		},
		{
			name:         "oracle_no_limit_offset",
			vendor:       Oracle,
			limit:        10,
			offset:       5,
			expectLimit:  false, // Oracle doesn't use LIMIT
			expectOffset: false,
		},
		{
			name:         "zero_values",
			vendor:       PostgreSQL,
			limit:        0,
			offset:       0,
			expectLimit:  false,
			expectOffset: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)
			query := qb.Select("*").From("table")
			query = qb.BuildLimitOffset(query, tt.limit, tt.offset)

			sql, _, err := query.ToSql()
			require.NoError(t, err)

			if tt.expectLimit {
				assert.Contains(t, sql, "LIMIT")
			} else {
				assert.NotContains(t, sql, "LIMIT")
			}

			if tt.expectOffset {
				assert.Contains(t, sql, "OFFSET")
			} else {
				assert.NotContains(t, sql, "OFFSET")
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
		expected interface{}
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
			expected:   `"TABLE_NAME"`,
		},
		{
			name:       "oracle_mixed_case",
			vendor:     Oracle,
			identifier: "TableName",
			expected:   `"TABLENAME"`,
		},
		{
			name:       "unknown_vendor",
			vendor:     "unknown",
			identifier: "table_name",
			expected:   "table_name",
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

func TestQueryBuilderQuoteOracleColumn(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	tests := []struct {
		name     string
		column   string
		expected string
	}{
		{
			name:     "reserved_word_number",
			column:   "number",
			expected: `"number"`,
		},
		{
			name:     "regular_column",
			column:   "id",
			expected: "id",
		},
		{
			name:     "name_column",
			column:   "name",
			expected: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := qb.quoteOracleColumn(tt.column)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryBuilderQuoteOracleColumns(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	columns := []string{"id", "number", "name"}
	result := qb.quoteOracleColumns(columns...)

	expected := []string{"id", `"number"`, "name"}
	assert.Equal(t, expected, result)
}

func TestQueryBuilderQuoteOracleColumnsNonOracle(t *testing.T) {
	qb := NewQueryBuilder(PostgreSQL)

	columns := []string{"id", "number", "name"}
	result := qb.quoteOracleColumns(columns...)

	// Should return original columns for non-Oracle vendors
	assert.Equal(t, columns, result)
}

func TestQueryBuilderBuildUpsertPostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(PostgreSQL)

	insertColumns := map[string]interface{}{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]interface{}{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	require.NoError(t, err)
	assert.NotEmpty(t, sql)
	assert.NotEmpty(t, args)
	assert.Contains(t, sql, "INSERT INTO users")
	assert.Contains(t, sql, "ON CONFLICT")
	assert.Contains(t, sql, "DO UPDATE SET")
}

func TestQueryBuilderBuildUpsertOracle(t *testing.T) {
	qb := NewQueryBuilder(Oracle)

	insertColumns := map[string]interface{}{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]interface{}{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	// Oracle implementation is not fully implemented (returns empty)
	assert.NoError(t, err)
	assert.Empty(t, sql)
	assert.Empty(t, args)
}

func TestQueryBuilderBuildUpsertUnknown(t *testing.T) {
	qb := NewQueryBuilder("unknown")

	insertColumns := map[string]interface{}{
		"id":   1,
		"name": "test",
	}
	updateColumns := map[string]interface{}{
		"name": "updated",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)

	assert.NoError(t, err)
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
	var results []map[string]interface{}
	for resultRows.Next() {
		var id int
		var name, title string
		err = resultRows.Scan(&id, &name, &title)
		require.NoError(t, err)
		results = append(results, map[string]interface{}{
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
