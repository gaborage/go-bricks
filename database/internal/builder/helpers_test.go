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

	t.Run("oracle_function_shaped_keys_pass_the_precondition_only", func(t *testing.T) {
		qb := NewQueryBuilder(dbtypes.Oracle)

		// Function-shaped keys are returned verbatim by the renderer and folded
		// for identity, so the membership check now accepts them. That is ALL
		// this proves: buildOracleMerge still renders them into a MERGE whose
		// USING alias is not a legal Oracle identifier, so such a call remains
		// broken at execution. Asserted separately from the valid cases above so
		// the precondition outcome is never read as an endorsement.
		sql, _, err := qb.BuildUpsert("users", []string{"count(*)"},
			map[string]any{"COUNT(*)": 1}, nil)

		require.NoError(t, err, "the precondition no longer rejects it")
		require.Contains(t, sql, "SELECT :1 AS COUNT(*)",
			"and the renderer still emits an alias Oracle cannot parse — see #997")
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
