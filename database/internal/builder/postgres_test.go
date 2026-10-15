package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestBuildPostgreSQLUpsertProducesDeterministicSql(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	insertColumns := map[string]any{
		"name":       "alice",
		"id":         42,
		"tenant_id":  "acme",
		"updated_at": "2023-05-01",
	}
	updateColumns := map[string]any{
		"updated_at": "2023-06-01",
		"name":       "bob",
	}
	conflictColumns := []string{"tenant_id", "id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)
	require.NoError(t, err)

	// The on-conflict UPDATE must bind the caller's update values ("bob"/"2023-06-01") as
	// parameters — NOT reuse EXCLUDED (the insert values), which silently ignored them and
	// diverged from Oracle's MERGE. Update placeholders follow the insert placeholders.
	expectedSQL := "INSERT INTO users (\"id\",\"name\",\"tenant_id\",\"updated_at\") VALUES ($1,$2,$3,$4) ON CONFLICT (\"id\", \"tenant_id\") DO UPDATE SET \"name\" = $5, \"updated_at\" = $6"
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL generated: %s", sql)
	}

	// Insert values first (sorted id,name,tenant_id,updated_at), then update values (sorted name,updated_at).
	require.Len(t, args, 6)
	if args[0] != 42 || args[1] != "alice" || args[2] != "acme" || args[3] != "2023-05-01" || args[4] != "bob" || args[5] != "2023-06-01" {
		t.Fatalf("unexpected argument ordering: %v", args)
	}
}

func TestBuildPostgreSQLUpsertBindsUpdateOnlyColumnAbsentFromInsert(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	// "version" is updated but NOT inserted. Under the old EXCLUDED behavior this produced
	// EXCLUDED."version", referencing a column absent from the insert (a runtime SQL error).
	// Binding the value makes it valid.
	insertColumns := map[string]any{"id": 1, "name": "a"}
	updateColumns := map[string]any{"version": 7}

	sql, args, err := qb.BuildUpsert("t", []string{"id"}, insertColumns, updateColumns)
	require.NoError(t, err)
	require.NotContains(t, sql, "EXCLUDED")
	require.Contains(t, sql, "\"version\" = $3")
	require.Equal(t, []any{1, "a", 7}, args)
}

func TestBuildPostgreSQLUpsertDoNothingWhenNoUpdateColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args, err := qb.BuildUpsert("t", []string{"id"}, map[string]any{"id": 1}, map[string]any{})
	require.NoError(t, err)
	require.Contains(t, sql, "DO NOTHING")
	require.Equal(t, []any{1}, args, "no update values appended for DO NOTHING")
}

// TestBuildPostgreSQLUpsertRejectsConflictColumnAbsentFromInsertSet verifies
// PostgreSQL refuses the conflict target Oracle's MERGE cannot express at all.
// PostgreSQL would emit valid SQL that leaves the conflict column to a table
// default and so matches only when that default collides; rejecting it keeps one
// BuildUpsert call meaning one thing on both vendors.
func TestBuildPostgreSQLUpsertRejectsConflictColumnAbsentFromInsertSet(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	_, _, err := qb.BuildUpsert("users", []string{"id", "tenant_id"}, map[string]any{"id": 1}, nil)

	require.Error(t, err, "conflict column missing from insert columns must be rejected")
	require.Contains(t, err.Error(), "tenant_id",
		"error must name the offending conflict column, not merely the first one")
}

// TestBuildPostgreSQLUpsertRejectsConflictColumnInUpdateSet verifies PostgreSQL
// refuses the same overlap Oracle's MERGE refuses. PostgreSQL would happily emit
// ON CONFLICT ("id") DO UPDATE SET "id" = $N; rejecting it here keeps one
// BuildUpsert call meaning one thing on both vendors.
func TestBuildPostgreSQLUpsertRejectsConflictColumnInUpdateSet(t *testing.T) {
	tests := []struct {
		name            string
		conflictColumns []string
		insertColumns   map[string]any
		updateColumns   map[string]any
		wantErrColumn   string
		wantSQLContains string
		wantDoNothing   bool
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
			name:            "empty_update_set_builds_without_error",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{},
			wantDoNothing:   true,
		},
		{
			// The mirror of the Oracle case_variant_of_conflict_column_rejected
			// case, and it must come out the OTHER way: PostgreSQL quotes every
			// identifier, so "id" and "ID" are two different columns and updating
			// "ID" on a conflict against "id" is legal, meaningful SQL. Folding
			// case here would reject a call PostgreSQL executes correctly.
			name:            "case_variant_of_conflict_column_is_a_distinct_column",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "alice"},
			updateColumns:   map[string]any{"ID": 2},
			wantSQLContains: `DO UPDATE SET "ID" =`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)

			sql, _, err := qb.BuildUpsert("users", tt.conflictColumns, tt.insertColumns, tt.updateColumns)

			if tt.wantErrColumn != "" {
				require.ErrorIs(t, err, dbtypes.ErrUpsertConflictColumnInUpdateSet)
				require.Contains(t, err.Error(), tt.wantErrColumn,
					"error must name the overlapping column, not merely the first conflict column")
				return
			}

			require.NoError(t, err)
			require.Contains(t, sql, "ON CONFLICT")
			if tt.wantSQLContains != "" {
				require.Contains(t, sql, tt.wantSQLContains)
			}
			if tt.wantDoNothing {
				require.Contains(t, sql, "DO NOTHING",
					"an empty update set must still build, emitting DO NOTHING")
			}
		})
	}
}

// TestBuildPostgreSQLUpsertSplicesARawExpression pins #2120: an expression in
// either map is spliced through valueCell, and only the bound values number
// placeholders, contiguously across the insert and the update.
func TestBuildPostgreSQLUpsertSplicesARawExpression(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	// SECURITY: Manual SQL review completed - literal CURRENT_TIMESTAMP keyword, no user input
	now := dbtypes.MustExpr("CURRENT_TIMESTAMP")

	sql, args, err := qb.BuildUpsert("t", []string{"id"},
		map[string]any{"id": 1, "created": now, "name": "a"},
		map[string]any{"name": "b", "updated": now})

	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO t ("created","id","name") VALUES (CURRENT_TIMESTAMP,$1,$2) ON CONFLICT ("id") DO UPDATE SET "name" = $3, "updated" = CURRENT_TIMESTAMP`, sql)
	assert.Equal(t, []any{1, "a", "b"}, args)
}

func TestBuildPostgreSQLUpsertRefusesAnAliasedRawExpression(t *testing.T) {
	// SECURITY: Manual SQL review completed - literal CURRENT_TIMESTAMP keyword with an alias, never rendered
	aliased := dbtypes.MustExpr("CURRENT_TIMESTAMP", "ts")
	cases := map[string]struct{ insert, update map[string]any }{
		"in_insert_columns": {insert: map[string]any{"id": 1, "created": aliased}, update: map[string]any{"name": "b"}},
		"in_update_columns": {insert: map[string]any{"id": 1}, update: map[string]any{"updated": aliased}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewQueryBuilder(dbtypes.PostgreSQL).BuildUpsert("t", []string{"id"}, tc.insert, tc.update)

			require.ErrorIs(t, err, dbtypes.ErrAliasInValue)
		})
	}
}

// TestBuildPostgreSQLUpsertKeepsAQuestionMarkInAnUpdateOnlyQuotedKey pins that
// squirrel's placeholder pass leaves a `?` in a quoted update column as written.
func TestBuildPostgreSQLUpsertKeepsAQuestionMarkInAnUpdateOnlyQuotedKey(t *testing.T) {
	cases := map[string]string{
		"single_question_mark":  `"a?b"`,
		"doubled_question_mark": `"a??b"`,
	}

	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			sql, args, err := NewQueryBuilder(dbtypes.PostgreSQL).BuildUpsert("t", []string{"id"},
				map[string]any{"id": 1}, map[string]any{key: 2})

			require.NoError(t, err)
			assert.Equal(t, `INSERT INTO t ("id") VALUES ($1) ON CONFLICT ("id") DO UPDATE SET `+key+` = $2`, sql)
			assert.Equal(t, []any{1, 2}, args)
		})
	}
}

// TestBuildPostgreSQLUpsertBindsAPointerToARawExpression pins that a pointer to
// an expression stays a bound value in either map, as Set and Values bind one.
func TestBuildPostgreSQLUpsertBindsAPointerToARawExpression(t *testing.T) {
	// SECURITY: Manual SQL review completed - literal CURRENT_TIMESTAMP keyword, bound not rendered
	now := dbtypes.MustExpr("CURRENT_TIMESTAMP")

	sql, args, err := NewQueryBuilder(dbtypes.PostgreSQL).BuildUpsert("t", []string{"id"},
		map[string]any{"id": 1, "created": &now}, map[string]any{"updated": &now})

	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO t ("created","id") VALUES ($1,$2) ON CONFLICT ("id") DO UPDATE SET "updated" = $3`, sql)
	assert.Equal(t, []any{&now, 1, &now}, args)
}

func TestBuildPostgreSQLUpsertRefusesABlankRawExpression(t *testing.T) {
	// SECURITY: Manual SQL review completed - blank expression literal, never rendered
	blank := dbtypes.RawExpression{SQL: "  "}
	cases := map[string]struct{ insert, update map[string]any }{
		"in_insert_columns": {insert: map[string]any{"id": 1, "created": blank}, update: map[string]any{"name": "b"}},
		"in_update_columns": {insert: map[string]any{"id": 1}, update: map[string]any{"updated": blank}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewQueryBuilder(dbtypes.PostgreSQL).BuildUpsert("t", []string{"id"}, tc.insert, tc.update)

			require.ErrorIs(t, err, dbtypes.ErrEmptyExpressionSQL)
		})
	}
}

// TestBuildPostgreSQLUpsertKeepsAQuestionMarkInAQuotedConflictKey pins that the
// ON CONFLICT target keeps a `?` in a quoted key as written, and that the SET
// placeholder continues the statement's own numbering after it. The INSERT
// column list is not pinned: squirrel's pass rewrites a `?` there, as it did
// before #2120.
func TestBuildPostgreSQLUpsertKeepsAQuestionMarkInAQuotedConflictKey(t *testing.T) {
	cases := map[string]struct {
		update     map[string]any
		wantSuffix string
	}{
		"do_update":  {update: map[string]any{"n": "y"}, wantSuffix: ` ON CONFLICT ("a?b") DO UPDATE SET "n" = $4`},
		"do_nothing": {update: nil, wantSuffix: ` ON CONFLICT ("a?b") DO NOTHING`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sql, _, err := NewQueryBuilder(dbtypes.PostgreSQL).BuildUpsert("t", []string{`"a?b"`},
				map[string]any{`"a?b"`: 1, "n": "x"}, tc.update)

			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(sql, tc.wantSuffix), "got %s", sql)
		})
	}
}

// TestBuildPostgreSQLUpsertResolvesTheEscapedPlaceholderLikeSet pins that an
// update expression renders squirrel's `??` escape the way Set does, beside an
// ordinary key and beside a quoted key that holds a `?` itself.
func TestBuildPostgreSQLUpsertResolvesTheEscapedPlaceholderLikeSet(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	// SECURITY: Manual SQL review completed - literal expression with an escaped question mark, no user input
	escaped := dbtypes.RawExpression{SQL: "NVL(x,'??')"}

	setSQL, _, err := qb.Update("t").Set("b", escaped).Where(qb.Filter().Eq("id", 1)).ToSQL()
	require.NoError(t, err)
	require.Equal(t, "UPDATE t SET b = NVL(x,'?') WHERE id = $1", setSQL)

	cases := map[string]string{
		"ordinary_key":           `"b"`,
		"question_mark_in_a_key": `"a?b"`,
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			sql, args, err := qb.BuildUpsert("t", []string{"id"}, map[string]any{"id": 1}, map[string]any{key: escaped})

			require.NoError(t, err)
			assert.Equal(t, `INSERT INTO t ("id") VALUES ($1) ON CONFLICT ("id") DO UPDATE SET `+key+` = NVL(x,'?')`, sql)
			assert.Equal(t, []any{1}, args)
		})
	}
}
