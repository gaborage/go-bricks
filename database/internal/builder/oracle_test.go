package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	countClause = "COUNT(*)"
	sumClause   = "SUM(amount)"
)

func TestQuoteOracleColumnHandlesReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	if got := qb.quoteOracleColumn("number"); got != `"number"` {
		t.Fatalf("expected reserved word to be quoted, got %s", got)
	}
	if got := qb.quoteOracleColumn("name"); got != "name" {
		t.Fatalf("expected non-reserved word to remain unchanged")
	}
}

func TestQuoteOracleColumnsForDMLPreservesCase(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	cols := qb.quoteOracleColumnsForDML("id", "number")
	if cols[0] != "id" || cols[1] != `"number"` {
		t.Fatalf("unexpected quoting result: %v", cols)
	}
}

func TestBuildOraclePaginationClause(t *testing.T) {
	if clause := buildOraclePaginationClause(0, 0); clause != "" {
		t.Fatalf("expected empty clause, got %s", clause)
	}
	if clause := buildOraclePaginationClause(5, 0); clause != "FETCH NEXT 5 ROWS ONLY" {
		t.Fatalf("unexpected clause: %s", clause)
	}
	if clause := buildOraclePaginationClause(5, 10); clause != "OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY" {
		t.Fatalf("unexpected clause with offset: %s", clause)
	}
}

func TestBuildUpsertOracleGeneratesMergeStatement(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	insertColumns := map[string]any{
		"id":   1,
		"name": "alice",
	}
	updateColumns := map[string]any{
		"name": "bob",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)
	require.NoError(t, err)

	if !strings.HasPrefix(sql, "MERGE INTO users") {
		t.Fatalf("expected MERGE statement, got %s", sql)
	}
	if !strings.Contains(sql, "SELECT :1 AS \"id\", :2 AS \"name\" FROM dual") {
		t.Fatalf("expected using clause with positional binds, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN MATCHED THEN UPDATE SET \"name\" = :3") {
		t.Fatalf("expected update clause, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN NOT MATCHED THEN INSERT (\"id\", \"name\") VALUES (source.\"id\", source.\"name\")") {
		t.Fatalf("expected insert clause, got %s", sql)
	}

	require.Len(t, args, 3)
	if args[0] != 1 || args[1] != "alice" {
		t.Fatalf("unexpected using clause args: %v", args)
	}
}

func TestBuildUpsertOracleRequiresConflictColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	_, _, err := qb.BuildUpsert("users", nil, map[string]any{"id": 1}, nil)
	if err == nil {
		t.Fatalf("expected error when conflict columns missing")
	}
}

func TestBuildUpsertNonOracleFallsBack(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	sql, args, err := qb.BuildUpsert("users", []string{"id"}, map[string]any{"id": 1}, map[string]any{"name": "bob"})
	require.NoError(t, err)
	if !strings.Contains(sql, "ON CONFLICT") {
		t.Fatalf("expected PostgreSQL fallback, got %s", sql)
	}
	require.NotEmpty(t, args)
}

func TestOracleQuoteIdentifierCaseSensitivity(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercase_reserved_word",
			input:    "number",
			expected: `"number"`,
		},
		{
			name:     "uppercase_reserved_word",
			input:    "NUMBER",
			expected: `"NUMBER"`,
		},
		{
			name:     "mixed_case_reserved_word",
			input:    "Number",
			expected: `"Number"`,
		},
		{
			name:     "lowercase_non_reserved",
			input:    "name",
			expected: "name",
		},
		{
			name:     "uppercase_non_reserved",
			input:    "NAME",
			expected: "NAME",
		},
		{
			name:     "mixed_case_non_reserved",
			input:    "Name",
			expected: "Name",
		},
		{
			name:     "already_quoted_lowercase",
			input:    `"number"`,
			expected: `"number"`,
		},
		{
			name:     "already_quoted_uppercase",
			input:    `"NUMBER"`,
			expected: `"NUMBER"`,
		},
		{
			name:     "multiple_reserved_words",
			input:    "size",
			expected: `"size"`,
		},
		{
			name:     "dotted_identifier",
			input:    "table.number",
			expected: `"table"."number"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := qb.quoteOracleColumn(tt.input)
			if result != tt.expected {
				t.Fatalf("input: %s, expected: %s, got: %s", tt.input, tt.expected, result)
			}
		})
	}
}

func TestOracleSQLFunctionDetection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "count_star",
			input:    countClause,
			expected: true,
		},
		{
			name:     "sum_column",
			input:    sumClause,
			expected: true,
		},
		{
			name:     "avg_field",
			input:    "AVG(balance)",
			expected: true,
		},
		{
			name:     "max_function",
			input:    "MAX(created_at)",
			expected: true,
		},
		{
			name:     "lower_case_function",
			input:    countClause,
			expected: true,
		},
		{
			name:     "mixed_case_function",
			input:    countClause,
			expected: true,
		},
		{
			name:     "regular_column",
			input:    "name",
			expected: false,
		},
		{
			name:     "quoted_identifier",
			input:    `"number"`,
			expected: false,
		},
		{
			name:     "dotted_identifier",
			input:    "table.column",
			expected: false,
		},
		{
			name:     "column_with_parens_in_name",
			input:    `"column(test)"`,
			expected: false,
		},
		{
			name:     "empty_string",
			input:    "",
			expected: false,
		},
		{
			name:     "incomplete_function",
			input:    "COUNT(",
			expected: false,
		},
		{
			name:     "number_start",
			input:    "1COUNT(*)",
			expected: false,
		},
		{
			name:     "function_with_underscores",
			input:    "GET_USER_COUNT(*)",
			expected: true,
		},
		{
			name:     "quoted_with_parens",
			input:    `"column(test)"`,
			expected: false,
		},
		{
			name:     "invalid_chars_in_function_name",
			input:    "COUNT-STAR(*)",
			expected: false,
		},
		{
			name:     "no_closing_paren",
			input:    "COUNT(*",
			expected: false,
		},
		{
			name:     "empty_function_name",
			input:    "(*)",
			expected: false,
		},
		{
			name:     "function_with_numbers",
			input:    "FUNC123(arg)",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSQLFunction(tt.input)
			if result != tt.expected {
				t.Fatalf("input: %s, expected: %t, got: %t", tt.input, tt.expected, result)
			}
		})
	}
}

func TestOracleQuoteIdentifierWithSQLFunctions(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "count_star_not_quoted",
			input:    countClause,
			expected: countClause,
		},
		{
			name:     "sum_column_not_quoted",
			input:    sumClause,
			expected: sumClause,
		},
		{
			name:     "avg_reserved_column",
			input:    "AVG(number)",
			expected: "AVG(number)",
		},
		{
			name:     "lowercase_function",
			input:    countClause,
			expected: countClause,
		},
		{
			name:     "mixed_case_function",
			input:    "Count(Balance)",
			expected: "Count(Balance)",
		},
		{
			name:     "regular_reserved_word_still_quoted",
			input:    "number",
			expected: `"number"`,
		},
		{
			name:     "regular_column_not_quoted",
			input:    "name",
			expected: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := qb.quoteOracleColumn(tt.input)
			if result != tt.expected {
				t.Fatalf("input: %s, expected: %s, got: %s", tt.input, tt.expected, result)
			}
		})
	}
}

func TestTypeSafeWhereMethodsWithOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name         string
		method       string
		column       string
		value        any
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:         "WhereEq_with_reserved_word_number",
			method:       "WhereEq",
			column:       "number",
			value:        "12345",
			expectedSQL:  `SELECT id, name, "number" FROM accounts WHERE "number" = :1`,
			expectedArgs: []any{"12345"},
		},
		{
			name:         "WhereEq_with_reserved_word_level",
			method:       "WhereEq",
			column:       "level",
			value:        5,
			expectedSQL:  `SELECT id, "level" FROM users WHERE "level" = :1`,
			expectedArgs: []any{5},
		},
		{
			name:         "WhereNotEq_with_reserved_word_size",
			method:       "WhereNotEq",
			column:       "size",
			value:        100,
			expectedSQL:  `SELECT id, "size" FROM products WHERE "size" <> :1`,
			expectedArgs: []any{100},
		},
		{
			name:         "WhereGt_with_reserved_word_access",
			method:       "WhereGt",
			column:       "access",
			value:        10,
			expectedSQL:  `SELECT id, "access" FROM permissions WHERE "access" > :1`,
			expectedArgs: []any{10},
		},
		{
			name:         "WhereLt_with_reserved_word_order",
			method:       "WhereLt",
			column:       "order",
			value:        50,
			expectedSQL:  `SELECT id, "order" FROM items WHERE "order" < :1`,
			expectedArgs: []any{50},
		},
		{
			name:         "WhereIn_with_reserved_word_mode",
			method:       "WhereIn",
			column:       "mode",
			value:        []string{"read", "write"},
			expectedSQL:  `SELECT id, "mode" FROM settings WHERE "mode" IN (:1,:2)`,
			expectedArgs: []any{[]string{"read", "write"}},
		},
		{
			name:         "WhereNull_with_reserved_word_comment",
			method:       "WhereNull",
			column:       "comment",
			value:        nil,
			expectedSQL:  `SELECT id, "comment" FROM posts WHERE "comment" IS NULL`,
			expectedArgs: []any{nil},
		},
		{
			name:         "WhereEq_with_non_reserved_word",
			method:       "WhereEq",
			column:       "name",
			value:        "john",
			expectedSQL:  `SELECT id, name FROM users WHERE name = :1`,
			expectedArgs: []any{"john"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query *SelectQueryBuilder

			// Build query based on the test case
			switch tt.method {
			case "WhereEq":
				switch tt.column {
				case "number":
					query = qb.Select("id", "name", "number").From("accounts").WhereEq(tt.column, tt.value)
				case "level":
					query = qb.Select("id", "level").From("users").WhereEq(tt.column, tt.value)
				default:
					query = qb.Select("id", "name").From("users").WhereEq(tt.column, tt.value)
				}
			case "WhereNotEq":
				query = qb.Select("id", "size").From("products").WhereNotEq(tt.column, tt.value)
			case "WhereGt":
				query = qb.Select("id", "access").From("permissions").WhereGt(tt.column, tt.value)
			case "WhereLt":
				query = qb.Select("id", "order").From("items").WhereLt(tt.column, tt.value)
			case "WhereIn":
				query = qb.Select("id", "mode").From("settings").WhereIn(tt.column, tt.value)
			case "WhereNull":
				query = qb.Select("id", "comment").From("posts").WhereNull(tt.column)
			}

			sql, args, err := query.ToSQL()
			require.NoError(t, err)

			assert.Equal(t, tt.expectedSQL, sql, "SQL query should match expected")
			if tt.method != "WhereNull" && tt.method != "WhereIn" {
				assert.Equal(t, tt.expectedArgs, args, "SQL args should match expected")
			}
		})
	}
}

func TestWhereRawForComplexOracleQueries(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name        string
		condition   string
		args        []any
		expectedSQL string
	}{
		{
			name:        "oracle_specific_rownum",
			condition:   "ROWNUM <= ?",
			args:        []any{10},
			expectedSQL: `SELECT id, name FROM users WHERE ROWNUM <= :1`,
		},
		{
			name:        "manually_quoted_reserved_word",
			condition:   `"number" = ? AND "level" > ?`,
			args:        []any{"12345", 5},
			expectedSQL: `SELECT id, name FROM accounts WHERE "number" = :1 AND "level" > :2`,
		},
		{
			name:        "complex_oracle_function",
			condition:   `UPPER("name") LIKE ? AND "size" BETWEEN ? AND ?`,
			args:        []any{"%JOHN%", 10, 50},
			expectedSQL: `SELECT id, name FROM accounts WHERE UPPER("name") LIKE :1 AND "size" BETWEEN :2 AND :3`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query *SelectQueryBuilder
			if tt.name == "oracle_specific_rownum" {
				query = qb.Select("id", "name").From("users").WhereRaw(tt.condition, tt.args...)
			} else {
				query = qb.Select("id", "name").From("accounts").WhereRaw(tt.condition, tt.args...)
			}

			sql, args, err := query.ToSQL()
			require.NoError(t, err)

			assert.Equal(t, tt.expectedSQL, sql, "SQL query should match expected")
			assert.Equal(t, tt.args, args, "SQL args should match expected")
		})
	}
}

func TestFixesOriginalOracleIdentifierBug(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	// This test reproduces the exact scenario from the error log:
	// ORA-00936: missing expression at position 103
	// Query: "SELECT id, name, \"number\", balance, created_at, created_by, updated_at, updated_by FROM accounts WHERE number = :1"
	//                                                                                                               ^^^^^^ unquoted

	// Using type-safe WHERE method should properly quote the reserved word
	query := qb.Select("id", "name", "number", "balance", "created_at", "created_by", "updated_at", "updated_by").
		From("accounts").
		WhereEq("number", "54763470")

	sql, args, err := query.ToSQL()
	require.NoError(t, err)

	expectedSQL := `SELECT id, name, "number", balance, created_at, created_by, updated_at, updated_by FROM accounts WHERE "number" = :1`
	expectedArgs := []any{"54763470"}

	assert.Equal(t, expectedSQL, sql, "Generated SQL should have properly quoted 'number' in WHERE clause")
	assert.Equal(t, expectedArgs, args, "Arguments should match expected")

	// Verify that both SELECT and WHERE clauses properly quote the reserved word "number"
	assert.Contains(t, sql, `SELECT id, name, "number"`, "SELECT clause should quote reserved word")
	assert.Contains(t, sql, `WHERE "number" = :1`, "WHERE clause should quote reserved word")
	assert.NotContains(t, sql, `WHERE number = :1`, "WHERE clause should NOT contain unquoted reserved word")
}
