package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
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
			input:    "COUNT(*)",
			expected: true,
		},
		{
			name:     "sum_column",
			input:    "SUM(amount)",
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
			input:    "count(*)",
			expected: true,
		},
		{
			name:     "mixed_case_function",
			input:    "Count(*)",
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
			input:    "COUNT(*)",
			expected: "COUNT(*)",
		},
		{
			name:     "sum_column_not_quoted",
			input:    "SUM(amount)",
			expected: "SUM(amount)",
		},
		{
			name:     "avg_reserved_column",
			input:    "AVG(number)",
			expected: "AVG(number)",
		},
		{
			name:     "lowercase_function",
			input:    "count(*)",
			expected: "count(*)",
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
