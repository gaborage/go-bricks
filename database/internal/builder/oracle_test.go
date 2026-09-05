package builder

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	countClause   = "COUNT(*)"
	sumClause     = "SUM(amount)"
	assertFormat  = "input: %s"
	testTableName = "schema.number"
)

func TestQuoteOracleColumnHandlesReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	assert.Equal(t, `"number"`, qb.renderer.QuoteColumn("number"), "reserved word should be quoted")
	assert.Equal(t, "name", qb.renderer.QuoteColumn("name"), "non-reserved word should remain unchanged")
}

func TestQuoteOracleColumnsForDMLPreservesCase(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	cols := qb.quoteColumnsForDML("id", "number")
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

	// Boundary: math.MaxInt still fits a signed int, so it survived the old
	// int() narrowing too.
	maxInt := uint64(math.MaxInt)
	want := fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", maxInt, maxInt)
	if clause := buildOraclePaginationClause(maxInt, maxInt); clause != want {
		t.Fatalf("math.MaxInt clause dropped or altered: %s", clause)
	}

	// Boundary: math.MaxInt+1 wrapped negative under the old int() narrowing,
	// which made both guards fall through and silently returned no clause --
	// an unpaginated query returning every row.
	overflow := uint64(math.MaxInt) + 1
	want = fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", overflow, overflow)
	if clause := buildOraclePaginationClause(overflow, overflow); clause != want {
		t.Fatalf("math.MaxInt+1 clause dropped or altered: %s", clause)
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
	// Non-reserved identifiers stay unquoted so Oracle folds them to the uppercase
	// form created by standard DDL (reserved-word-only quoting).
	if !strings.Contains(sql, "SELECT :1 AS id, :2 AS name FROM dual") {
		t.Fatalf("expected using clause with positional binds, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN MATCHED THEN UPDATE SET name = :3") {
		t.Fatalf("expected update clause, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN NOT MATCHED THEN INSERT (id, name) VALUES (source.id, source.name)") {
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

// TestRendererQuoteColumnDelegatesToSqllex is a smoke test only: the exhaustive
// case-sensitivity / reserved-word / quoted-form / dotted-name matrix lives in
// sqllex.TestQuoteOracleIdentifierCaseSensitivity now that oracleRenderer's
// QuoteColumn is a one-line delegate to sqllex.QuoteOracleIdentifier. This keeps
// just enough cases here to prove the delegation wiring itself, plus the
// PostgreSQL branch — which now pins the SEAM rather than a vendor guard: a
// PostgreSQL builder holds postgresRenderer, whose QuoteColumn is the identity.
func TestRendererQuoteColumnDelegatesToSqllex(t *testing.T) {
	oracle := NewQueryBuilder(dbtypes.Oracle)
	postgres := NewQueryBuilder(dbtypes.PostgreSQL)

	tests := []struct {
		name     string
		qb       *QueryBuilder
		input    string
		expected string
	}{
		{
			name:     "oracle_reserved_word_quoted",
			qb:       oracle,
			input:    "number",
			expected: `"number"`,
		},
		{
			name:     "oracle_non_reserved_unchanged",
			qb:       oracle,
			input:    "name",
			expected: "name",
		},
		{
			name:     "oracle_already_quoted_unchanged",
			qb:       oracle,
			input:    `"number"`,
			expected: `"number"`,
		},
		{
			name:     "postgres_passthrough_unchanged",
			qb:       postgres,
			input:    "number",
			expected: "number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.qb.renderer.QuoteColumn(tt.input)
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

	// SECURITY: Manual SQL review completed - all `tt.condition` values are literal strings from the
	// static fixture above; identifier quoting verified for Oracle reserved words ("number", "level",
	// "size", "name") and ROWNUM is a pseudo-column; placeholders are parameterized.
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
			tables:      []string{testTableName},
			expectedSQL: `SELECT * FROM schema."number"`,
			shouldQuote: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert []string to []any for From() call
			tables := make([]any, len(tt.tables))
			for i, t := range tt.tables {
				tables[i] = t
			}
			query := qb.Select("*").From(tables...)
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
		orderBy     []any
		groupBy     []any
		expectedSQL []string
	}{
		{
			name:        "reserved_word_column",
			orderBy:     []any{"number"},
			groupBy:     []any{"level"},
			expectedSQL: []string{`ORDER BY "number"`, `GROUP BY "level"`},
		},
		{
			name:        "column_with_direction",
			orderBy:     []any{"number ASC", "size DESC"},
			groupBy:     []any{"access"},
			expectedSQL: []string{`ORDER BY "number" ASC, "size" DESC`, `GROUP BY "access"`},
		},
		{
			name:        "mixed_reserved_and_normal",
			orderBy:     []any{"name", "number DESC"},
			groupBy:     []any{"category", "level"},
			expectedSQL: []string{`ORDER BY name, "number" DESC`, `GROUP BY category, "level"`},
		},
		{
			// HARDEN (ADR-031): SQL function expressions are no longer accepted as
			// plain strings through OrderBy/GroupBy — they must go through qb.Expr()
			// (the developer-controlled raw-SQL escape hatch). Plain strings are
			// validated as bare identifiers to close the M9 injection vector.
			name:        "sql_functions_via_expr_preserved",
			orderBy:     []any{qb.MustExpr(countClause), qb.MustExpr(sumClause)},
			groupBy:     []any{qb.MustExpr("COUNT(items)"), "user_id"},
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

		// Qualified identifiers
		{"qualified_reserved", testTableName, `schema."number"`},
		{"qualified_normal", "schema.name", "schema.name"},

		// The four-token grammar ADR-031 documents. The renderer understood only
		// `col DIR` and quoted the whole string, which Oracle rejects (#1156).
		{"reserved_with_desc_nulls_last", "level DESC NULLS LAST", `"level" DESC NULLS LAST`},
		{"reserved_with_nulls_first_only", "level NULLS FIRST", `"level" NULLS FIRST`},
		{"normal_with_asc_nulls_last", "created_at ASC NULLS LAST", "created_at ASC NULLS LAST"},
		{"lowercase_direction_uppercases", "name desc", "name DESC"},
		{"qualified_reserved_lowercase_nulls", "t.level desc nulls first", `t."level" DESC NULLS FIRST`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := qb.renderer.QuoteIdentifierForClause(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestOracleUpdateTableQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	tests := []struct {
		name           string
		table          string
		setColumn      string
		whereColumn    string
		expectedSQL    string
		expectSetQuote bool
	}{
		{
			name:           "reserved_word_table",
			table:          "number",
			setColumn:      "level", // reserved word
			whereColumn:    "size",  // reserved word
			expectedSQL:    `UPDATE "number" SET "level" = :1 WHERE "size" = :2`,
			expectSetQuote: true,
		},
		{
			name:           "regular_table",
			table:          "users",
			setColumn:      "name",
			whereColumn:    "id",
			expectedSQL:    `UPDATE users SET name = :1 WHERE id = :2`,
			expectSetQuote: false,
		},
		{
			name:           "schema_qualified_reserved_word",
			table:          testTableName,
			setColumn:      "level",
			whereColumn:    "order",
			expectedSQL:    `UPDATE schema."number" SET "level" = :1 WHERE "order" = :2`,
			expectSetQuote: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := qb.Update(tt.table).
				Set(tt.setColumn, "active").
				Where(f.Eq(tt.whereColumn, 123)).
				ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql, "SQL should match expected UPDATE statement")
			assert.Equal(t, []any{"active", 123}, args)
		})
	}
}

// TestOracleUpdateSetMapQuoting tests that SetMap properly quotes columns in UPDATE statements
func TestOracleUpdateSetMapQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	sql, _, err := qb.Update("number").
		SetMap(map[string]any{
			"level": "active", // reserved word
			"size":  42,       // reserved word
		}).
		ToSQL()

	require.NoError(t, err)
	assert.Contains(t, sql, `UPDATE "number" SET`, "Table should be quoted")
	assert.Contains(t, sql, `"level" = `, "Reserved word column should be quoted")
	assert.Contains(t, sql, `"size" = `, "Reserved word column should be quoted")
}

// TestOracleDeleteTableQuoting tests that DELETE statements properly quote table names
// for reserved words and mixed-case identifiers, preventing Oracle syntax errors.
func TestOracleDeleteTableQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	f := qb.Filter()

	tests := []struct {
		name        string
		table       string
		whereColumn string
		expectedSQL string
	}{
		{
			name:        "reserved_word_table",
			table:       "number",
			whereColumn: "level", // reserved word
			expectedSQL: `DELETE FROM "number" WHERE "level" = :1`,
		},
		{
			name:        "regular_table",
			table:       "users",
			whereColumn: "id",
			expectedSQL: `DELETE FROM users WHERE id = :1`,
		},
		{
			name:        "schema_qualified_reserved_word",
			table:       testTableName,
			whereColumn: "level",
			expectedSQL: `DELETE FROM schema."number" WHERE "level" = :1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := qb.Delete(tt.table).
				Where(f.Eq(tt.whereColumn, 123)).
				ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql, "SQL should match expected DELETE statement")
			assert.Equal(t, []any{123}, args)
		})
	}
}

// TestOracleDeleteWithoutWhereQuoting tests that DELETE without WHERE quotes table name
func TestOracleDeleteWithoutWhereQuoting(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	tests := []struct {
		name        string
		table       string
		expectedSQL string
	}{
		{
			name:        "reserved_word_table",
			table:       "number",
			expectedSQL: `DELETE FROM "number"`,
		},
		{
			name:        "regular_table",
			table:       "users",
			expectedSQL: `DELETE FROM users`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _, err := qb.Delete(tt.table).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSQL, sql, "SQL should match expected DELETE FROM clause")
		})
	}
}

// TestBuildUpsertOracleUsesReservedWordOnlyQuoting verifies the Oracle MERGE
// statement applies reserved-word-only quoting (preserving Oracle's case-folding
// semantics) rather than unconditionally double-quoting lowercase identifiers.
// Standard DDL creates uppercase ID/NAME columns, so emitting quoted lowercase
// "id"/"name" would fail at runtime with ORA-00904 (M7 bug #1).
func TestBuildUpsertOracleUsesReservedWordOnlyQuoting(t *testing.T) {
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

	// Non-reserved identifiers must stay unquoted so Oracle folds them to the
	// uppercase form created by standard DDL.
	assert.Equal(t,
		"MERGE INTO users target USING (SELECT :1 AS id, :2 AS name FROM dual) source "+
			"ON (target.id = source.id) "+
			"WHEN MATCHED THEN UPDATE SET name = :3 "+
			"WHEN NOT MATCHED THEN INSERT (id, name) VALUES (source.id, source.name)",
		sql,
		"Oracle MERGE must use reserved-word-only quoting for non-reserved identifiers")

	require.Len(t, args, 3)
	assert.Equal(t, 1, args[0])
	assert.Equal(t, "alice", args[1])
	assert.Equal(t, "bob", args[2])
}

// TestBuildUpsertOracleQuotesReservedWordColumnsAndTable verifies reserved words
// used as column or table names ARE quoted (preserving case) while the table name
// is routed through the same quoting the DML paths use. The "level" table is a
// reserved word and must become "level"; an unquoted MERGE INTO level fails with
// ORA-00905 (M7 bug #2). Reserved-word columns ("number") must be quoted too.
func TestBuildUpsertOracleQuotesReservedWordColumnsAndTable(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	insertColumns := map[string]any{
		"id":     1,
		"number": 7,
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("level", conflictColumns, insertColumns, nil)
	require.NoError(t, err)

	assert.Equal(t,
		`MERGE INTO "level" target USING (SELECT :1 AS id, :2 AS "number" FROM dual) source `+
			`ON (target.id = source.id) `+
			`WHEN NOT MATCHED THEN INSERT (id, "number") VALUES (source.id, source."number")`,
		sql,
		"reserved-word table and column names must be quoted while preserving case")

	require.Len(t, args, 2)
}

// TestBuildUpsertOracleRejectsUnknownConflictColumns verifies the MERGE builder
// fails fast when a conflict column is absent from the insert columns. Otherwise
// the generated ON clause references source.<missing> which does not exist in the
// USING SELECT, producing invalid SQL with no error (M7 bug #3).
func TestBuildUpsertOracleRejectsUnknownConflictColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	_, _, err := qb.BuildUpsert("users", []string{"id", "tenant_id"}, map[string]any{"id": 1}, nil)
	require.Error(t, err, "conflict column missing from insert columns must be rejected")
	assert.Contains(t, err.Error(), "tenant_id",
		"error should name the offending conflict column")
}

// TestBuildUpsertOracleRejectsConflictColumnInUpdateSet verifies a column present
// in both conflictColumns and updateColumns is rejected at build time. Oracle's
// MERGE cannot update a column referenced in the ON clause and fails at execution
// with ORA-38104; rejecting here turns that into a build-time error.
func TestBuildUpsertOracleRejectsConflictColumnInUpdateSet(t *testing.T) {
	tests := []struct {
		name               string
		conflictColumns    []string
		insertColumns      map[string]any
		updateColumns      map[string]any
		wantErrColumn      string
		wantSQLContains    string
		wantNoUpdateClause bool
	}{
		{
			name:            "conflict_column_only_in_conflict_target",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{"name": "bob"},
		},
		{
			name:            "column_only_in_update_set",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{"name": "bob", "version": 7},
		},
		{
			name:            "conflict_column_in_both_rejected",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{"id": 2, "name": "bob"},
			wantErrColumn:   "id",
		},
		{
			name:            "one_of_several_conflict_columns_overlaps",
			conflictColumns: []string{"id", "tenant_id"},
			insertColumns:   map[string]any{"id": 1, "tenant_id": "acme", "name": "alice"},
			updateColumns:   map[string]any{"tenant_id": "globex"},
			wantErrColumn:   "tenant_id",
		},
		{
			name:               "empty_update_set_builds_without_error",
			conflictColumns:    []string{"id"},
			insertColumns:      map[string]any{"id": 1, "name": "alice"},
			updateColumns:      map[string]any{},
			wantNoUpdateClause: true,
		},
		{
			// Oracle leaves non-reserved identifiers unquoted and folds them to
			// upper case, so id and ID are the same column: an exact-string check
			// would emit ON (target.id = source.id) ... SET ID = :3 and still die
			// with ORA-38104 at execution.
			name:            "case_variant_of_conflict_column_rejected",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{"ID": 2},
			wantErrColumn:   "ID",
		},
		{
			// Reserved words ARE quoted on Oracle, and quoted identifiers stay
			// case-sensitive, so "number" and "NUMBER" are genuinely two columns.
			name:            "quoted_reserved_word_case_variant_stays_distinct",
			conflictColumns: []string{"number"},
			insertColumns:   map[string]any{"number": 1, "name": "alice"},
			updateColumns:   map[string]any{"NUMBER": 2},
			wantSQLContains: `SET "NUMBER" =`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.Oracle)

			sql, _, err := qb.BuildUpsert("users", tt.conflictColumns, tt.insertColumns, tt.updateColumns)

			if tt.wantErrColumn != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "collides with conflict column")
				assert.Contains(t, err.Error(), "ORA-38104")
				assert.Contains(t, err.Error(), tt.wantErrColumn,
					"error must name the overlapping column, not merely the first conflict column")
				return
			}

			require.NoError(t, err)
			assert.Contains(t, sql, "MERGE INTO")
			if tt.wantSQLContains != "" {
				assert.Contains(t, sql, tt.wantSQLContains)
			}
			if tt.wantNoUpdateClause {
				assert.NotContains(t, sql, "WHEN MATCHED",
					"an empty update set must still build, emitting no UPDATE arm")
			}
		})
	}
}

// TestOracleRenderingDoublesInteriorQuotes drives the Oracle renderer directly.
//
// It used to go through qb.Select, then through f.Eq when Select began validating
// its columns. Both doors validate now, so neither will carry an injection-shaped
// identifier to the renderer — which is the whole point of ADR-082, and leaves
// EscapeIdentifier as the renderer's last public observation point. The escaping
// itself is still worth pinning: validation is what stops these reaching the
// renderer, and the escape is what makes the renderer safe if one ever does.
func TestOracleRenderingDoublesInteriorQuotes(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	for _, tt := range identifierEscapeCases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.escaped, qb.EscapeIdentifier(tt.identifier))
		})
	}

	t.Run("the_validating_doors_refuse_these_outright", func(t *testing.T) {
		f := qb.Filter()
		_, _, err := qb.Select("id").From("users").
			Where(f.Eq(`role" = 'admin', "name`, 1)).ToSQL()
		require.Error(t, err, "a Filter column must not reach the renderer at all")

		_, _, err = qb.Select(`role" = 'admin', "name`).From("users").ToSQL()
		require.Error(t, err, "a Select column must not reach the renderer at all")
	})
}

// TestBuildUpsertValidatesTable covers the argument #1104 calls the worse half:
// the table sits first in both vendors' templates, so a trailing comment takes
// the rest of the statement and no column precondition ever runs.
func TestBuildUpsertValidatesTable(t *testing.T) {
	const mergeTakeover = `users" target USING (SELECT 1 AS "x" FROM dual) source ON (1=1) ` +
		`WHEN NOT MATCHED THEN INSERT ("a") VALUES (1) --`

	rejected := []struct{ name, table string }{
		{name: "merge_takeover", table: mergeTakeover},
		{name: "stacked_statement", table: `users; DROP TABLE users--`},
		{name: "trailing_comment", table: `users--`},
	}

	accepted := []struct{ name, table string }{
		{name: "bare_name", table: "users"},
		{name: "qualified_name", table: "app.users"},
	}

	for _, vendor := range []dbtypes.Vendor{dbtypes.PostgreSQL, dbtypes.Oracle} {
		qb := NewQueryBuilder(vendor)
		for _, tt := range rejected {
			t.Run(vendor+"_rejects_"+tt.name, func(t *testing.T) {
				sql, args, err := qb.BuildUpsert(tt.table, []string{"id"},
					map[string]any{"id": 1, "name": "n"}, map[string]any{"name": "n2"})

				require.Error(t, err)
				// Attribution: the call must fail at the table door, not because
				// some column precondition happened to trip on the same input.
				require.ErrorContains(t, err, "invalid table identifier",
					"rejected the call, but not because of the table identifier")
				require.Empty(t, sql, "a rejected call emits no SQL")
				require.Empty(t, args, "a rejected call binds no arguments")
			})
		}
		for _, tt := range accepted {
			t.Run(vendor+"_accepts_"+tt.name, func(t *testing.T) {
				sql, _, err := qb.BuildUpsert(tt.table, []string{"id"},
					map[string]any{"id": 1, "name": "n"}, map[string]any{"name": "n2"})

				require.NoError(t, err)
				require.NotEmpty(t, sql)
			})
		}
	}
}

func TestOracleFromAliasQuoting(t *testing.T) {
	tests := []struct {
		name   string
		vendor dbtypes.Vendor
		table  string
		want   string
	}{
		{name: "oracle_non_reserved_table_with_alias", vendor: dbtypes.Oracle, table: "users u", want: "SELECT name FROM users u"},
		{name: "oracle_reserved_table_with_alias", vendor: dbtypes.Oracle, table: "level l", want: `SELECT name FROM "level" l`},
		{name: "postgresql_table_with_alias", vendor: dbtypes.PostgreSQL, table: "users u", want: "SELECT name FROM users u"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _, err := NewQueryBuilder(tt.vendor).Select(colName).From(tt.table).ToSQL()

			require.NoError(t, err)
			assert.Equal(t, tt.want, sql)
		})
	}
}

// TestBuildUpsertVendorSegmentGrammar covers the third map-shaped door, which
// reaches its columns through normalizeUpsertColumns rather than the identifier
// funnels — so it needs its own pair or a `#` key would keep rendering on
// PostgreSQL after every other door stopped (#1202).
func TestBuildUpsertVendorSegmentGrammar(t *testing.T) {
	tests := []struct {
		name      string
		vendor    string
		table     string
		column    string
		wantError bool
	}{
		{name: "postgresql_refuses_hashed_column", vendor: dbtypes.PostgreSQL, table: tableAccounts, column: "a#b", wantError: true},
		{name: "postgresql_refuses_hashed_table", vendor: dbtypes.PostgreSQL, table: "t#x", column: colName, wantError: true},
		{name: "oracle_accepts_hashed_column", vendor: dbtypes.Oracle, table: tableAccounts, column: "a#b"},
		{name: "oracle_accepts_hashed_table", vendor: dbtypes.Oracle, table: "t#x", column: colName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.vendor)

			sql, _, err := qb.BuildUpsert(
				tt.table,
				[]string{colID},
				map[string]any{colID: 1, tt.column: 2},
				map[string]any{tt.column: 2},
			)

			if tt.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "#")
				assert.Empty(t, sql)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, sql)
		})
	}
}

// TestStructDoorsDisagreeOnDbTagGrammar RECORDS a known residual rather than
// asserting desired behavior, and pins WHICH doors it covers: the INSERT struct
// doors render db-tag columns through quoteColumnsForDML without a validator, so
// a `db:"a#b"` tag reaches PostgreSQL unquoted and fails at execution, while
// SetStruct routes columns through the column funnel and refuses it like any
// other column. The rows vary only the door, which is what makes the
// disagreement the visible fact. Struct tags are developer constants judged by
// the columns package against the union alphabet, which is why the INSERT half
// was left out of #1202's sweep (ADR-100). If an INSERT row starts failing
// because the statement is refused, the residual has been closed — delete the
// row, do not "fix" it.
func TestStructDoorsDisagreeOnDbTagGrammar(t *testing.T) {
	type hashTagged struct {
		ID   int64  `db:"id"`
		Name string `db:"a#b"`
	}

	tests := []struct {
		name    string
		build   func(qb *QueryBuilder) (string, []any, error)
		wantErr bool
		wantSQL string
	}{
		{
			name: "insert_struct_still_renders_the_tag",
			build: func(qb *QueryBuilder) (string, []any, error) {
				return qb.InsertStruct(tableAccounts, &hashTagged{ID: 1, Name: "x"}).ToSQL()
			},
			wantSQL: "a#b",
		},
		{
			name: "insert_fields_still_renders_the_tag",
			build: func(qb *QueryBuilder) (string, []any, error) {
				return qb.InsertFields(tableAccounts, &hashTagged{ID: 1, Name: "x"}, "Name").ToSQL()
			},
			wantSQL: "a#b",
		},
		{
			name: "set_struct_refuses_the_tag",
			build: func(qb *QueryBuilder) (string, []any, error) {
				return qb.Update(tableAccounts).SetStruct(&hashTagged{ID: 1, Name: "x"}).ToSQL()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)

			sql, _, err := tt.build(qb)

			if tt.wantErr {
				require.Error(t, err, "SetStruct judges db-tag columns through the column funnel")
				return
			}
			require.NoError(t, err, "the INSERT struct doors do not consult the vendor grammar today")
			assert.Contains(t, sql, tt.wantSQL, "the tag name reaches the statement unquoted")
		})
	}
}
