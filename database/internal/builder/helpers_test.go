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
