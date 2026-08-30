package inbox

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func newOracleHoldTestStore(t *testing.T) HoldStore {
	t.Helper()
	store, err := NewOracleHoldStore(holdTable)
	require.NoError(t, err)
	return store
}

// TestOracleHoldStoreParkDetectsADuplicateByTheViolation pins the dialect
// difference that matters: Oracle has no ON CONFLICT, so a re-park is a
// unique violation the store catches rather than a statement that ignores it.
func TestOracleHoldStoreParkDetectsADuplicateByTheViolation(t *testing.T) {
	t.Run("first_park_reports_inserted", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		tx := db.ExpectTransaction()
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnRowsAffected(1)
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnRowsAffected(1)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		inserted, err := store.Park(t.Context(), dbtx, sampleHoldRow())

		require.NoError(t, err)
		assert.True(t, inserted)
	})

	t.Run("a_tenantless_row_is_refused_before_any_write", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectTransaction()

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		row := sampleHoldRow()
		row.TenantID = ""
		_, err = store.Park(t.Context(), dbtx, row)

		require.ErrorIs(t, err, errHoldTenantRequired,
			"a hold is keyed by the tenant, and Oracle stores an empty string as NULL")
	})

	t.Run("a_real_write_failure_still_fails", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		wantErr := errors.New("ORA-00600 internal error")
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		tx := db.ExpectTransaction()
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnError(wantErr)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
	})
}

// TestOracleHoldStoreUsesItsOwnDialect pins the spellings that differ from
// PostgreSQL's: SYSTIMESTAMP, FETCH FIRST, NUMTODSINTERVAL and dual.
func TestOracleHoldStoreUsesItsOwnDialect(t *testing.T) {
	t.Run("due_tenants_fetch_first", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectQuery(`FETCH FIRST`).WillReturnRows(tenantRowSet(time.Now()))

		tenants, err := store.DueTenants(t.Context(), db, testHoldConsumer, 10)

		require.NoError(t, err)
		require.Len(t, tenants, 1)
		assert.Equal(t, testHoldTenant, tenants[0].TenantID)
	})

	t.Run("acquire_lease_uses_numtodsinterval", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectExec(`NUMTODSINTERVAL`).WillReturnRowsAffected(1)

		took, err := store.AcquireLease(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner, time.Minute)

		require.NoError(t, err)
		assert.True(t, took)
	})

	// Oracle folds the empty-string literal to NULL, so its NVL substitutes a
	// SPACE where PostgreSQL's COALESCE substitutes "". A caller asking whether a
	// tenant has an error must not have to know which vendor answered.
	t.Run("an_absent_last_error_reads_empty_on_both_vendors", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectQuery(`FROM ` + holdTenantTable).WillReturnRows(
			dbtesting.NewRowSet("consumer", "tenant_id", "held_since", "attempts", "next_attempt_at", "last_error").
				AddRow(testHoldConsumer, testHoldTenant, time.Now(), 0, time.Now(), " "),
		)

		tenants, err := store.ListTenants(t.Context(), db, testHoldConsumer)

		require.NoError(t, err)
		require.Len(t, tenants, 1)
		assert.Empty(t, tenants[0].LastError, "the space Oracle substitutes is not an error")
	})

	t.Run("stats_selects_from_dual", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		oldest := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectQuery(`FROM dual`).WillReturnRows(
			dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(3), oldest),
		)

		stats, err := store.Stats(t.Context(), db, testHoldConsumer)

		require.NoError(t, err)
		assert.Equal(t, int64(1), stats.Tenants)
		assert.Equal(t, oldest, stats.OldestHeldSince)
	})

	t.Run("create_table_names_its_constraints", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectExec(`CONSTRAINT pk_` + holdTable).WillReturnRowsAffected(0)
		db.ExpectExec(`CONSTRAINT pk_` + holdTenantTable).WillReturnRowsAffected(0)
		db.ExpectExec(`CREATE INDEX idx_` + holdTable + `_tenant_order`).WillReturnRowsAffected(0)
		db.ExpectExec(`CREATE INDEX idx_` + holdTable + `_tenant_due`).WillReturnRowsAffected(0)

		require.NoError(t, store.CreateTable(t.Context(), db))
	})
}

// TestOracleHoldStoreFencesEveryPostReplayWrite mirrors the PostgreSQL fencing
// test: the lease is in every post-replay statement on this vendor too, and a
// zero-row result means the lease was lost.
func TestOracleHoldStoreFencesEveryPostReplayWrite(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		affected int64
		call     func(HoldStore, dbtypes.Interface) (bool, error)
	}{
		{
			name:     "delete_row_without_the_lease",
			pattern:  `DELETE FROM ` + holdTable,
			affected: 0,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.DeleteRow(t.Context(), db, testHoldConsumer, testHoldStream, 41, testHoldTenant, testHoldOwner)
			},
		},
		{
			name:     "defer_without_the_lease",
			pattern:  `UPDATE ` + holdTenantTable,
			affected: 0,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.Defer(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner, time.Second, "boom")
			},
		},
		{
			name:     "release_without_the_lease",
			pattern:  `DELETE FROM ` + holdTenantTable,
			affected: 0,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.Release(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newOracleHoldTestStore(t)
			db := dbtesting.NewTestDB(dbtypes.Oracle)
			db.ExpectExec(tc.pattern).WillReturnRowsAffected(tc.affected)

			ok, err := tc.call(store, db)

			require.NoError(t, err)
			// The fence held exactly when the write changed a row: a zero-row write
			// is lease loss, which is what the caller reads this bool for.
			assert.Equal(t, tc.affected != 0, ok)
		})
	}
}
