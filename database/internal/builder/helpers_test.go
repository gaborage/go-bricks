package builder

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestBuildUpsertReportsMissingPreconditionsIdenticallyPerVendor pins the wording
// of both upsert preconditions to one string per precondition. The two builders
// used to phrase them per vendor ("for Oracle MERGE" / "for PostgreSQL upsert"),
// so a caller matching on the text had to know which vendor it had reached.
// Asserting the two vendors against each other, rather than against two literals,
// means the pairing cannot rot in only one direction.
func TestBuildUpsertReportsMissingPreconditionsIdenticallyPerVendor(t *testing.T) {
	insertColumns := map[string]any{"id": 1}

	pg := NewQueryBuilder(dbtypes.PostgreSQL)
	oracle := NewQueryBuilder(dbtypes.Oracle)

	_, _, pgEmptyErr := pg.BuildUpsert("users", nil, insertColumns, nil)
	_, _, oracleEmptyErr := oracle.BuildUpsert("users", nil, insertColumns, nil)
	require.EqualError(t, pgEmptyErr, "conflict columns required for upsert")
	require.EqualError(t, oracleEmptyErr, pgEmptyErr.Error(),
		"both vendors must report an empty conflict column set the same way")

	_, _, pgMissingErr := pg.BuildUpsert("users", []string{"tenant_id"}, insertColumns, nil)
	_, _, oracleMissingErr := oracle.BuildUpsert("users", []string{"tenant_id"}, insertColumns, nil)
	require.EqualError(t, pgMissingErr, `conflict column "tenant_id" must be present in insert columns for upsert`)
	require.EqualError(t, oracleMissingErr, pgMissingErr.Error(),
		"both vendors must report a conflict column absent from the insert set the same way")
}

// TestBuildUpsertMatchesConflictColumnsToInsertSetByVendorIdentity pins the
// insert-set precondition to the vendor's own notion of column identity, the
// same rule the checks around it use. It is the mirror of the overlap check:
// there identity matching widened the REJECTED set, here it widens the ACCEPTED
// one, so these cases prove the widening stops exactly where the vendor's own
// folding stops.
func TestBuildUpsertMatchesConflictColumnsToInsertSetByVendorIdentity(t *testing.T) {
	t.Run("oracle_folds_unquoted_case_variants_to_one_column", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// The full SQL is asserted because the ON clause keeps the caller's
		// spelling while the USING alias keeps the insert key's — the two
		// fragments differ textually and must still name one column.
		sql, args, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"ID": 1, "name": "alice"}, nil)

		require.NoError(t, err, "a case variant of an inserted column is the same Oracle column")
		require.Equal(t,
			"MERGE INTO users target USING (SELECT :1 AS ID, :2 AS name FROM dual) source "+
				"ON (target.id = source.id) "+
				"WHEN NOT MATCHED THEN INSERT (ID, name) VALUES (source.ID, source.name)",
			sql)
		require.Equal(t, []any{1, "alice"}, args)
	})

	t.Run("oracle_folds_whitespace_padded_keys", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// The renderer trims before folding, so " id " and "id" are one column.
		// This one emits valid SQL, so it genuinely builds and writes.
		sql, _, err := qb.BuildUpsert("users", []string{" id "},
			map[string]any{"id": 1, "name": "alice"}, nil)

		require.NoError(t, err)
		require.Contains(t, sql, "ON (target.id = source.id)",
			"the padded spelling must render as the trimmed column")
	})

	t.Run("oracle_function_shaped_keys_are_rejected_outright", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// This pairing once satisfied the membership check and then rendered a
		// MERGE whose USING alias Oracle cannot parse. The build-time rejection
		// (#997) replaces that: the identity fold that made the two spellings
		// match is no longer reached, because neither key can name a column here.
		_, _, err := qb.BuildUpsert("users", []string{"count(*)"},
			map[string]any{"COUNT(*)": 1}, nil)

		require.EqualError(t, err, `conflict column "count(*)" is not a single column name for upsert`)
	})

	t.Run("oracle_accepts_the_reverse_spelling_direction", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// An implementation applying identity to only one side of the
		// comparison would pass one direction and fail the other.
		_, _, err := qb.BuildUpsert("users", []string{"ID"}, map[string]any{"id": 1}, nil)

		require.NoError(t, err)
	})

	t.Run("exact_spellings_still_match_for_every_rendering", func(t *testing.T) {
		// The other half of the claim: identity matching only ever ACCEPTS more,
		// never less, so every spelling that matched itself before still does.
		// Reserved words matter most, because they render quoted and so take
		// columnIdentity's other branch.
		for _, col := range []string{"id", "level", "number", "MixedCase", "col$"} {
			for _, vendor := range []string{dbtypes.Oracle, dbtypes.PostgreSQL} {
				qb := NewQueryBuilder(vendor)

				_, _, err := qb.BuildUpsert("users", []string{col}, map[string]any{col: 1}, nil)

				require.NoErrorf(t, err, "%s: an exact spelling must still match itself (%q)", vendor, col)
			}
		}
	})

	t.Run("oracle_keeps_quoted_reserved_word_case_variants_distinct", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// Reserved words are quoted, and quoted identifiers stay case-sensitive,
		// so these are two columns and the rejection must survive.
		_, _, err := qb.BuildUpsert("users", []string{"level"}, map[string]any{"LEVEL": 1}, nil)

		require.ErrorContains(t, err, `conflict column "level" must be present in insert columns`)
	})

	t.Run("postgresql_keeps_case_and_whitespace_variants_distinct", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// PostgreSQL quotes every identifier, so folding here would accept calls
		// the database rejects.
		_, _, caseErr := qb.BuildUpsert("users", []string{"id"}, map[string]any{"ID": 1}, nil)
		_, _, spaceErr := qb.BuildUpsert("users", []string{" id "}, map[string]any{"id": 1}, nil)

		require.ErrorContains(t, caseErr, `conflict column "id" must be present in insert columns`)
		require.ErrorContains(t, spaceErr, `conflict column " id " must be present in insert columns`)
	})
}

// TestBuildUpsertRejectsDuplicateConflictColumnsByVendorIdentity pins the
// uniqueness precondition to the vendor's own notion of column identity. A
// duplicate conflict target is meaningless in both dialects, but only
// PostgreSQL fails on it today — Oracle's ON clause merely repeats a tautology
// and executes fine, so the rejection is what makes one call mean one thing.
func TestBuildUpsertRejectsDuplicateConflictColumnsByVendorIdentity(t *testing.T) {
	t.Run("exact_duplicate_rejected_on_every_vendor", func(t *testing.T) {
		insertColumns := map[string]any{"id": 1, "name": "a"}

		_, _, pgErr := NewQueryBuilder(dbtypes.PostgreSQL).
			BuildUpsert("users", []string{"id", "id"}, insertColumns, nil)
		_, _, oracleErr := NewQueryBuilder(dbtypes.Oracle).
			BuildUpsert("users", []string{"id", "id"}, insertColumns, nil)

		require.EqualError(t, pgErr,
			`conflict columns must be distinct: "id" and "id" name the same column for upsert`)
		require.EqualError(t, oracleErr, pgErr.Error(),
			"both vendors must report a duplicated conflict column the same way")
	})

	t.Run("oracle_case_variant_is_a_duplicate", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// Both render unquoted and fold to ID, so this names one column twice.
		// The full message is pinned because it reports BOTH spellings: naming
		// only the second would leave the caller hunting for the other half.
		_, _, err := qb.BuildUpsert("users", []string{"id", "ID"},
			map[string]any{"id": 1, "ID": 2, "name": "a"}, nil)

		require.EqualError(t, err,
			`conflict columns must be distinct: "id" and "ID" name the same column for upsert`)
	})

	t.Run("postgresql_case_variant_is_a_composite_target", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// PostgreSQL quotes every identifier, so "id" and "ID" are two columns
		// and conflicting on both is a legitimate composite target.
		sql, _, err := qb.BuildUpsert("users", []string{"id", "ID"},
			map[string]any{"id": 1, "ID": 2, "name": "a"}, nil)

		require.NoError(t, err)
		require.Contains(t, sql, `ON CONFLICT ("ID", "id")`)
	})

	t.Run("oracle_quoted_reserved_word_case_variant_is_not_a_duplicate", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// Quoted identifiers stay case-sensitive on Oracle too, so these are two
		// distinct columns and the pair must survive the uniqueness check.
		_, _, err := qb.BuildUpsert("users", []string{"level", "LEVEL"},
			map[string]any{"level": 1, "LEVEL": 2, "name": "a"}, nil)

		require.NoError(t, err)
	})
}

// TestBuildUpsertEnforcesPreconditionsForEveryVendor guards the hoist: the three
// preconditions live in BuildUpsert, not in either vendor builder, so neither
// vendor can grow a precondition the other lacks. An unsupported vendor must
// still be reported as unsupported rather than as a precondition failure for an
// upsert it cannot build at all.
func TestBuildUpsertEnforcesPreconditionsForEveryVendor(t *testing.T) {
	for _, vendor := range []string{dbtypes.PostgreSQL, dbtypes.Oracle} {
		t.Run(vendor, func(t *testing.T) {
			qb := NewQueryBuilder(vendor)

			_, _, emptyErr := qb.BuildUpsert("users", nil, map[string]any{"id": 1}, nil)
			require.EqualError(t, emptyErr, "conflict columns required for upsert")

			_, _, missingErr := qb.BuildUpsert("users", []string{"tenant_id"}, map[string]any{"id": 1}, nil)
			require.ErrorContains(t, missingErr, "must be present in insert columns for upsert")

			_, _, overlapErr := qb.BuildUpsert("users",
				[]string{"id"}, map[string]any{"id": 1, "name": "a"}, map[string]any{"id": 2})
			require.ErrorContains(t, overlapErr, "collides with conflict column")
		})
	}

	// The vendor check runs before the preconditions, so a call that violates
	// both is reported as the unsupported vendor it is.
	unknown := NewQueryBuilder("unknown")
	_, _, err := unknown.BuildUpsert("users", nil, nil, nil)
	require.EqualError(t, err, "upsert not supported for database vendor: unknown")
}

// TestBuildUpsertRejectsIdentityCollidingColumnSets closes the gap C59.10 named
// and left open: conflictColumns is deduplicated by vendor identity, but two
// INSERT or UPDATE keys that fold to one Oracle column were not. Such a call
// built a MERGE declaring one alias twice — ORA-00957 at parse — and naming it
// twice in the INSERT list, with no error from the builder (#997).
func TestBuildUpsertRejectsIdentityCollidingColumnSets(t *testing.T) {
	tests := []struct {
		name            string
		conflictColumns []string
		insertColumns   map[string]any
		updateColumns   map[string]any
		wantErr         string
	}{
		{
			name:            "insert_keys_folding_to_one_column",
			conflictColumns: []string{"k"},
			insertColumns:   map[string]any{"k": 0, "id": 1, "ID": 2},
			wantErr:         `insert columns must be distinct: "ID" and "id" name the same column for upsert`,
		},
		{
			// The colliding pair is also the conflict column, which the ON clause
			// names as source.Id — ambiguous once the USING clause declares it twice.
			name:            "insert_keys_folding_onto_the_conflict_column",
			conflictColumns: []string{"Id"},
			insertColumns:   map[string]any{"ID": 1, "id": 2},
			updateColumns:   map[string]any{"name": "x"},
			wantErr:         `insert columns must be distinct: "ID" and "id" name the same column for upsert`,
		},
		{
			// The shape rendering-comparison misses: `id` renders unquoted and
			// Oracle folds it to ID, `"ID"` renders quoted and IS ID. Two
			// renderings, one column, and the MERGE declared it twice.
			name:            "unquoted_key_folding_onto_a_quoted_upper_key",
			conflictColumns: []string{"k"},
			insertColumns:   map[string]any{"k": 0, "id": 1, `"ID"`: 2},
			wantErr:         `insert columns must be distinct: "\"ID\"" and "id" name the same column for upsert`,
		},
		{
			name:            "update_key_folding_onto_a_quoted_upper_key",
			conflictColumns: []string{"k"},
			insertColumns:   map[string]any{"k": 0},
			updateColumns:   map[string]any{"id": 1, `"ID"`: 2},
			wantErr:         `update columns must be distinct: "\"ID\"" and "id" name the same column for upsert`,
		},
		{
			// rejectConflictColumnUpdates already folded update keys, but only to
			// keep its own error deterministic; it never rejected the collision.
			name:            "update_keys_folding_to_one_column",
			conflictColumns: []string{"k"},
			insertColumns:   map[string]any{"k": 0},
			updateColumns:   map[string]any{"ID": 1, "id": 2},
			wantErr:         `update columns must be distinct: "ID" and "id" name the same column for upsert`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.Oracle)

			sql, args, err := qb.BuildUpsert("users", tt.conflictColumns, tt.insertColumns, tt.updateColumns)

			require.EqualError(t, err, tt.wantErr)
			require.Empty(t, sql, "a rejected call emits no SQL")
			require.Empty(t, args, "a rejected call binds no arguments")
		})
	}
}

// TestBuildUpsertKeepsDistinctIdentitiesBuildable is the other half of the
// precondition: the check must reject only what the vendor itself folds. A
// caller-quoted key keeps its case on Oracle, so it is a second column and the
// pairing still builds — the same residual C59.7 and C59.9 carry.
func TestBuildUpsertKeepsDistinctIdentitiesBuildable(t *testing.T) {
	t.Run("oracle_keeps_a_quoted_lowercase_name_distinct", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// The other half of the fold: `"id"` names a lowercase column Oracle
		// keeps distinct from the ID that `id` folds to, so this pairing is two
		// columns and must still build. Same for a reserved word, which renders
		// quoted in whatever case the caller wrote.
		_, _, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, `"id"`: 2}, nil)
		require.NoError(t, err)

		_, _, levelErr := qb.BuildUpsert("users", []string{"level"},
			map[string]any{"level": 1, "LEVEL": 2}, nil)
		require.NoError(t, levelErr)
	})

	t.Run("oracle_keeps_a_doubled_quote_inside_a_quoted_name", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// A doubled quote is how Oracle spells a quote inside an identifier, so
		// this names one column and must survive the check that refuses the
		// undoubled ones.
		sql, _, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, `a""b`: 2}, nil)

		require.NoError(t, err)
		require.Contains(t, sql, `:1 AS "a""b"`)
	})

	t.Run("oracle_quoted_and_unquoted_spellings_are_two_columns", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		sql, args, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, `"id"`: 2}, nil)

		require.NoError(t, err)
		require.Contains(t, sql, `SELECT :1 AS "id", :2 AS id`)
		require.Len(t, args, 2)
	})

	t.Run("postgresql_case_variants_stay_two_columns", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// PostgreSQL quotes every identifier, so nothing folds and the pairing
		// this rejects on Oracle is a legitimate two-column insert here.
		sql, _, err := qb.BuildUpsert("users", []string{"k"},
			map[string]any{"k": 0, "id": 1, "ID": 2}, map[string]any{"ID": 3, "id": 4})

		require.NoError(t, err)
		require.Contains(t, sql, `"ID"`)
		require.Contains(t, sql, `"id"`)
	})
}

// TestBuildUpsertRejectsColumnsOracleMergeCannotName pins the second half of
// #997. Conflict and insert keys become column aliases in Oracle's MERGE — in its
// USING clause and its INSERT list — which admit neither a qualifier nor a
// function call, so those keys could only ever produce SQL Oracle refuses to
// parse. Update keys are held to the same rule by choice. Rejecting them at build
// time also makes columnIdentity's quote guard unreachable from BuildUpsert:
// every key that survives renders as one whole token, so the guard's
// HasPrefix test can no longer upper-case a rendering through its own quotes.
func TestBuildUpsertRejectsColumnsOracleMergeCannotName(t *testing.T) {
	tests := []struct {
		name            string
		conflictColumns []string
		insertColumns   map[string]any
		updateColumns   map[string]any
		wantErr         string
	}{
		{
			name:            "function_shaped_conflict_column",
			conflictColumns: []string{"count(*)"},
			insertColumns:   map[string]any{"COUNT(*)": 1},
			wantErr:         `conflict column "count(*)" is not a single column name for upsert`,
		},
		{
			name:            "qualified_insert_key",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "t.name": 2},
			wantErr:         `insert column "t.name" is not a single column name for upsert`,
		},
		{
			// The rendering the identity guard mishandles — quoted, but not at
			// position 0, so HasPrefix reads it as unquoted and upper-cases it
			// through its own quotes. It never gets that far now: the dot in the
			// rendering is refused here, which is how the fold stays unreachable.
			name:            "partially_quoted_insert_key",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, `t."level"`: 2},
			wantErr:         `insert column "t.\"level\"" is not a single column name for upsert`,
		},
		{
			name:            "function_shaped_update_key",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1},
			updateColumns:   map[string]any{`MAX("a")`: 2},
			wantErr:         `update column "MAX(\"a\")" is not a single column name for upsert`,
		},
		{
			// The one shape that did build legal SQL: Oracle accepts an
			// alias-qualified SET target, and `target` is the alias
			// buildOracleMerge hardcodes. Refused anyway — that spelling depends
			// on an internal alias the caller has no contract for, and naming the
			// column alone means the same thing.
			name:            "alias_qualified_update_key",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "n"},
			updateColumns:   map[string]any{"target.name": "v"},
			wantErr:         `update column "target.name" is not a single column name for upsert`,
		},
		{
			// The rendering that is not a column at all: oracleQuoteIdentifier
			// wraps this key without doubling the quotes inside it, producing
			// `"role" = 'admin', "name"` — a second SET assignment, in a position
			// no bind parameter guards. Refusing it is what makes "single column
			// name" true rather than aspirational.
			name:            "update_key_whose_rendering_escapes_the_identifier",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "name": "n"},
			updateColumns:   map[string]any{`role" = 'admin', "name`: "n2"},
			wantErr:         `update column "role\" = 'admin', \"name" is not a single column name for upsert`,
		},
		{
			name:            "whitespace_only_insert_key",
			conflictColumns: []string{"id"},
			insertColumns:   map[string]any{"id": 1, "  ": 2},
			wantErr:         `insert column "  " is not a single column name for upsert`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.Oracle)

			sql, args, err := qb.BuildUpsert("users", tt.conflictColumns, tt.insertColumns, tt.updateColumns)

			require.EqualError(t, err, tt.wantErr)
			require.Empty(t, sql, "a rejected call emits no SQL")
			require.Empty(t, args, "a rejected call binds no arguments")
		})
	}

	t.Run("postgresql_refuses_a_key_that_escapes_the_identifier", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// EscapeIdentifier wraps without doubling the quotes inside, exactly as
		// oracleQuoteIdentifier does, so the same key becomes a second SET
		// assignment here too. This one clause of the check is not Oracle
		// grammar and does not stop at Oracle; the qualifier and function rules
		// still do.
		_, _, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, "name": "n"},
			map[string]any{`role" = 'admin', "name`: "n2"})

		require.EqualError(t, err,
			`update column "role\" = 'admin', \"name" is not a single column name for upsert`)
	})

	t.Run("postgresql_keeps_a_doubled_quote_key", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// A doubled quote is the legal spelling on this vendor too, so the rule
		// refuses only what the escaper cannot render faithfully.
		_, _, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, `a""b`: 2}, nil)

		require.NoError(t, err)
	})

	t.Run("postgresql_still_builds_a_dotted_key", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.PostgreSQL)

		// PostgreSQL is unchanged, which is the claim under test — not that the
		// key is sensible there. Its escaper splits on the dot and quotes each
		// part, so the key renders as a qualified reference rather than a column
		// name; refusing it there would be a second breaking change, out of scope.
		_, _, err := qb.BuildUpsert("users", []string{"id"},
			map[string]any{"id": 1, "t.name": 2}, nil)

		require.NoError(t, err)
	})
}
