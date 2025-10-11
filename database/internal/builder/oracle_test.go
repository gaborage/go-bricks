package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	countClause            = "COUNT(*)"
	sumClause              = "SUM(amount)"
	assertFormat           = "input: %s"
	testIncompleteFunction = "COUNT("
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
			input:    strings.ToLower(countClause),
			expected: true,
		},
		{
			name:     "mixed_case_function",
			input:    "CoUnT(*)",
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
			input:    testIncompleteFunction,
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
		{
			name:     "qualified_function_schema_pkg_func",
			input:    "SCHEMA.PKG.FUNC(table.column)",
			expected: true,
		},
		{
			name:     "qualified_function_schema_func",
			input:    "SCHEMA.GET_USER_COUNT(*)",
			expected: true,
		},
		{
			name:     "qualified_function_with_underscores",
			input:    "MY_SCHEMA.MY_PKG.MY_FUNC(arg)",
			expected: true,
		},
		{
			name:     "qualified_function_empty_segment",
			input:    "SCHEMA..FUNC(arg)",
			expected: false,
		},
		{
			name:     "qualified_function_invalid_start",
			input:    "SCHEMA.1PKG.FUNC(arg)",
			expected: false,
		},
		{
			name:     "qualified_function_invalid_chars",
			input:    "SCHEMA.PKG-NAME.FUNC(arg)",
			expected: false,
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
			var query dbtypes.SelectQueryBuilder
			f := qb.Filter()

			// Build query based on the test case
			switch tt.method {
			case "WhereEq":
				switch tt.column {
				case "number":
					query = qb.Select("id", "name", "number").From("accounts").Where(f.Eq(tt.column, tt.value))
				case "level":
					query = qb.Select("id", "level").From("users").Where(f.Eq(tt.column, tt.value))
				default:
					query = qb.Select("id", "name").From("users").Where(f.Eq(tt.column, tt.value))
				}
			case "WhereNotEq":
				query = qb.Select("id", "size").From("products").Where(f.NotEq(tt.column, tt.value))
			case "WhereGt":
				query = qb.Select("id", "access").From("permissions").Where(f.Gt(tt.column, tt.value))
			case "WhereLt":
				query = qb.Select("id", "order").From("items").Where(f.Lt(tt.column, tt.value))
			case "WhereIn":
				query = qb.Select("id", "mode").From("settings").Where(f.In(tt.column, tt.value))
			case "WhereNull":
				query = qb.Select("id", "comment").From("posts").Where(f.Null(tt.column))
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
			var query dbtypes.SelectQueryBuilder
			f := qb.Filter()
			if tt.name == "oracle_specific_rownum" {
				query = qb.Select("id", "name").From("users").Where(f.Raw(tt.condition, tt.args...))
			} else {
				query = qb.Select("id", "name").From("accounts").Where(f.Raw(tt.condition, tt.args...))
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
	f := qb.Filter()
	query := qb.Select("id", "name", "number", "balance", "created_at", "created_by", "updated_at", "updated_by").
		From("accounts").
		Where(f.Eq("number", "54763470"))

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

func TestIsQuotedString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty", "", false},
		{"properly_quoted", `"test"`, true},
		{"unquoted", "test", false},
		{"only_start_quote", `"test`, false},
		{"only_end_quote", `test"`, false},
		{"single_quote", `"`, false},
		{"just_quotes", `""`, true},
		{"quotes_inside", `"test"more"`, true}, // Full string is quoted
		{"spaces_quoted", `"  spaces  "`, true},
		{"single_char", "a", false},
		{"two_chars", "ab", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isQuotedString(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestExtractFunctionNameAndArgs(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedName string
		expectedOk   bool
	}{
		{"simple_function", countClause, "COUNT", true},
		{"with_spaces", "  FUNC  ( args ) ", "FUNC", true},
		{"no_closing", "FUNC(", "", false},
		{"no_opening", "FUNC)", "", false},
		{"empty_name", "()", "", false},
		{"no_parens", "FUNC", "", false},
		{"complex_args", "SUM(table.column)", "SUM", true},
		{"qualified_name", "SCHEMA.PKG.FUNC(arg)", "SCHEMA.PKG.FUNC", true},
		{"nested_parens", "FUNC(INNER())", "FUNC", true},
		{"starts_with_paren", "(FUNC)", "", false},
		{"multiple_parens", "FUNC()MORE()", "FUNC", true}, // Only cares about first
		{"whitespace_name", "  TRIM_FUNC  (arg)", "TRIM_FUNC", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ok := extractFunctionNameAndArgs(tt.input)
			assert.Equal(t, tt.expectedOk, ok, assertFormat, tt.input)
			if tt.expectedOk {
				assert.Equal(t, tt.expectedName, name, assertFormat, tt.input)
			}
		})
	}
}

func TestIsValidOracleIdentifierStart(t *testing.T) {
	tests := []struct {
		name     string
		input    byte
		expected bool
	}{
		// Letters should return true
		{"uppercase_A", 'A', true},
		{"uppercase_Z", 'Z', true},
		{"lowercase_a", 'a', true},
		{"lowercase_z", 'z', true},
		{"middle_letter", 'M', true},

		// Numbers should return false
		{"digit_0", '0', false},
		{"digit_9", '9', false},
		{"digit_5", '5', false},

		// Special characters should return false
		{"underscore", '_', false},
		{"dollar", '$', false},
		{"hash", '#', false},
		{"hyphen", '-', false},
		{"space", ' ', false},
		{"dot", '.', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidOracleIdentifierStart(tt.input)
			assert.Equal(t, tt.expected, result, "input: %c", tt.input)
		})
	}
}

func TestIsValidIdentifierSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid_simple", "TABLE", true},
		{"valid_lowercase", "table", true},
		{"with_underscore", "MY_TABLE", true},
		{"with_dollar", "SYS$TABLE", true},
		{"with_hash", "MY#TABLE", true},
		{"with_numbers", "TABLE123", true},
		{"mixed_case", "MyTable", true},

		// Invalid cases - should start with letter
		{"starts_with_digit", "1TABLE", false},
		{"starts_with_underscore", "_TABLE", false},
		{"starts_with_dollar", "$TABLE", false},
		{"starts_with_hash", "#TABLE", false},

		// Invalid characters
		{"with_dash", "MY-TABLE", false},
		{"with_space", "MY TABLE", false},
		{"with_dot", "MY.TABLE", false},
		{"with_special", "MY@TABLE", false},

		// Edge cases
		{"empty", "", false},
		{"single_letter", "A", true},
		{"single_digit", "1", false},
		{"all_digits_start_letter", "A123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidIdentifierSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestIsValidQuotedSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid_quoted", `"test"`, true},
		{"quoted_with_spaces", `"test with spaces"`, true},
		{"quoted_with_special", `"test@#$%"`, true},
		{"quoted_with_numbers", `"123test"`, true},
		{"quoted_empty_content", `""`, false}, // Empty content inside quotes

		// Invalid cases
		{"not_quoted", "test", false},
		{"only_start_quote", `"test`, false},
		{"only_end_quote", `test"`, false},
		{"single_quote", `"`, false},
		{"empty", "", false},
		{"short_string", "a", false},

		// Edge cases
		{"quotes_in_content", `"test"inside"`, true}, // Valid quoted string
		{"single_char_quoted", `"a"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidQuotedSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestParseQualifiedIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		valid    bool
	}{
		{"simple", "FUNC", []string{"FUNC"}, true},
		{"schema_qualified", "SCHEMA.FUNC", []string{"SCHEMA", "FUNC"}, true},
		{"fully_qualified", "SCHEMA.PKG.FUNC", []string{"SCHEMA", "PKG", "FUNC"}, true},
		{"with_spaces", " SCHEMA . PKG . FUNC ", []string{"SCHEMA", "PKG", "FUNC"}, true},

		// Invalid cases
		{"empty_segment", "SCHEMA..FUNC", nil, false},
		{"starts_with_dot", ".FUNC", nil, false},
		{"ends_with_dot", "FUNC.", nil, false},
		{"only_dots", "...", nil, false},

		// Mixed quoted and unquoted
		{"mixed_quoted", `"SCHEMA".PKG."FUNC"`, []string{`"SCHEMA"`, "PKG", `"FUNC"`}, true},
		{"all_quoted", `"SCHEMA"."PKG"."FUNC"`, []string{`"SCHEMA"`, `"PKG"`, `"FUNC"`}, true},

		// Edge cases
		{"single_dot", ".", nil, false},
		{"empty_string", "", []string{""}, false},   // Will have empty segment
		{"just_spaces", "   ", []string{""}, false}, // Trimmed to empty
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments, valid := parseQualifiedIdentifier(tt.input)
			assert.Equal(t, tt.valid, valid, assertFormat, tt.input)
			if tt.valid {
				assert.Equal(t, tt.expected, segments, assertFormat, tt.input)
			} else {
				assert.Nil(t, segments, "input: %s should return nil segments", tt.input)
			}
		})
	}
}

func TestValidateSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid unquoted segments
		{"valid_unquoted", "TABLE", true},
		{"valid_with_numbers", "TABLE123", true},
		{"valid_with_underscore", "MY_TABLE", true},

		// Invalid unquoted segments
		{"invalid_starts_digit", "1TABLE", false},
		{"invalid_special_char", "MY-TABLE", false},

		// Valid quoted segments
		{"valid_quoted", `"table"`, true},
		{"valid_quoted_special", `"my-table"`, true},
		{"valid_quoted_numbers", `"123table"`, true},

		// Invalid quoted segments
		{"invalid_quoted_empty", `""`, false},
		{"invalid_unclosed_quote", `"table`, false},

		// Edge cases
		{"single_letter", "A", true},
		{"single_quoted", `"A"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

// Integration test to verify the helper functions work together correctly
func TestHelperFunctionsIntegration(t *testing.T) {
	// Test cases that verify the interaction between helper functions
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Cases that previously failed due to bugs
		{"number_start_bug_fix", "1COUNT(*)", false},
		{"qualified_invalid_start_bug_fix", "SCHEMA.1PKG.FUNC(arg)", false},

		// Valid cases
		{"simple_function", countClause, true},
		{"qualified_function", "SCHEMA.PKG.FUNC(arg)", true},
		{"mixed_quoted_qualified", `"SCHEMA".PKG.FUNC(arg)`, true},

		// Invalid cases
		{"quoted_identifier", `"column"`, false},
		{"no_parentheses", "NOTAFUNC", false},
		{"empty_segments", "SCHEMA..FUNC()", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSQLFunction(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

// TestIsBalancedParentheses tests the enhanced parentheses balancing logic
func TestIsBalancedParentheses(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Balanced cases
		{"simple_balanced", countClause, true},
		{"nested_balanced", "FUNC(arg, OTHER(inner))", true},
		{"multiple_balanced", "FUNC() + OTHER()", true},
		{"empty_string", "", true},
		{"no_parentheses", "column", true},

		// Unbalanced cases
		{"extra_opening", "COUNT((", false},
		{"extra_closing", "COUNT())", false},
		{"missing_opening", "COUNT)", false},
		{"missing_closing", testIncompleteFunction, false},
		{"wrong_order", ")COUNT(", false},
		{"nested_unbalanced", "FUNC(OTHER()", false},
		{"multiple_unbalanced", "FUNC()) + OTHER(", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBalancedParentheses(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

// TestOracleFromClauseQuoting tests the new FROM clause identifier quoting
func TestOracleFromClauseQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name        string
		tables      []string
		expectedSQL string
		shouldQuote bool
	}{
		{
			name:        "reserved_word_table",
			tables:      []string{"number"},
			expectedSQL: `SELECT * FROM "number"`,
			shouldQuote: true,
		},
		{
			name:        "single_reserved_table",
			tables:      []string{"number"},
			expectedSQL: `FROM "number"`,
			shouldQuote: true,
		},
		{
			name:        "regular_table",
			tables:      []string{"users"},
			expectedSQL: `SELECT * FROM users`,
			shouldQuote: false,
		},
		{
			name:        "qualified_table_name",
			tables:      []string{"schema.number"},
			expectedSQL: `SELECT * FROM schema."number"`,
			shouldQuote: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := qb.Select("*").From(tt.tables...)
			sql, _, err := query.ToSQL()
			assert.NoError(t, err)
			assert.Contains(t, sql, tt.expectedSQL, "SQL should contain expected FROM clause")
		})
	}
}

// TestOracleOrderByGroupByQuoting tests the new ORDER BY and GROUP BY identifier quoting
func TestOracleOrderByGroupByQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name        string
		orderBy     []string
		groupBy     []string
		expectedSQL []string
	}{
		{
			name:        "reserved_word_column",
			orderBy:     []string{"number"},
			groupBy:     []string{"level"},
			expectedSQL: []string{`ORDER BY "number"`, `GROUP BY "level"`},
		},
		{
			name:        "column_with_direction",
			orderBy:     []string{"number ASC", "size DESC"},
			groupBy:     []string{"access"},
			expectedSQL: []string{`ORDER BY "number" ASC, "size" DESC`, `GROUP BY "access"`},
		},
		{
			name:        "mixed_reserved_and_normal",
			orderBy:     []string{"name", "number DESC"},
			groupBy:     []string{"category", "level"},
			expectedSQL: []string{`ORDER BY name, "number" DESC`, `GROUP BY category, "level"`},
		},
		{
			name:        "sql_functions_preserved",
			orderBy:     []string{countClause, sumClause},
			groupBy:     []string{"COUNT(items)", "user_id"},
			expectedSQL: []string{`ORDER BY COUNT(*), SUM(amount)`, `GROUP BY COUNT(items), user_id`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := qb.Select("*").From("users")

			if len(tt.groupBy) > 0 {
				query = query.GroupBy(tt.groupBy...)
			}
			if len(tt.orderBy) > 0 {
				query = query.OrderBy(tt.orderBy...)
			}

			sql, _, err := query.ToSQL()
			assert.NoError(t, err)

			for _, expected := range tt.expectedSQL {
				assert.Contains(t, sql, expected, "SQL should contain expected clause: %s", expected)
			}
		})
	}
}

// TestQuoteOracleIdentifierForClause tests the new clause-specific identifier quoting
func TestQuoteOracleIdentifierForClause(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Simple cases
		{"reserved_word", "number", `"number"`},
		{"normal_column", "name", "name"},
		{"empty_string", "", ""},

		// Direction keywords
		{"reserved_with_asc", "number ASC", `"number" ASC`},
		{"reserved_with_desc", "level DESC", `"level" DESC`},
		{"normal_with_asc", "name ASC", "name ASC"},
		{"normal_with_desc", "created_at DESC", "created_at DESC"},

		// SQL functions (should not be quoted)
		{"count_function", countClause, countClause},
		{"sum_function", sumClause, sumClause},
		{"qualified_function", "SCHEMA.FUNC(arg)", "SCHEMA.FUNC(arg)"},

		// Qualified identifiers
		{"qualified_reserved", "schema.number", `schema."number"`},
		{"qualified_normal", "schema.name", "schema.name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := qb.quoteOracleIdentifierForClause(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

// TestEnhancedSQLFunctionDetectionWithBalancedParentheses tests the stricter parentheses validation
func TestEnhancedSQLFunctionDetectionWithBalancedParentheses(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid balanced cases
		{"simple_function", countClause, true},
		{"function_with_args", sumClause, true},
		{"nested_function", "UPPER(TRIM(name))", true},
		{"multiple_args", "SUBSTR(name, 1, 10)", true},

		// Invalid unbalanced cases (now properly detected)
		{"extra_opening_paren", "COUNT((", false},
		{"extra_closing_paren", "COUNT())", false},
		{"missing_closing_paren", testIncompleteFunction, false},
		{"wrong_paren_order", ")COUNT(", false},
		{"nested_unbalanced", "UPPER(TRIM(name)", false},

		// Edge cases
		{"empty_function", "FUNC()", true},
		{"quoted_identifier", `"column"`, false},
		{"no_parentheses", "column", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSQLFunction(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}
