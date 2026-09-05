package inbox

import (
	"errors"
	"testing"
	"time"

	oranet "github.com/sijms/go-ora/v2/network"
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
		// No marker yet: the FOR UPDATE lock finds nothing and the marker is inserted.
		tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnRowsAffected(1)
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnRowsAffected(1)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		inserted, err := store.Park(t.Context(), dbtx, sampleHoldRow())

		require.NoError(t, err)
		assert.True(t, inserted)
	})

	t.Run("an_existing_marker_is_locked_not_reinserted", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		tx := db.ExpectTransaction()
		tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(
			dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnRowsAffected(1)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

		require.NoError(t, err)
		require.Len(t, tx.ExecLog(), 1, "the held tenant's marker is locked, not written again")
		assert.Contains(t, tx.ExecLog()[0].SQL, holdTable)
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
		tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(
			dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
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
	// The builder spells the row limit FETCH NEXT, Oracle's synonym for the
	// FETCH FIRST this store used to write by hand, and inlines the count rather
	// than binding it.
	t.Run("due_tenants_limits_the_rows", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		db.ExpectQuery(`FETCH NEXT`).WillReturnRows(tenantRowSet(time.Now()))

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

// TestOracleHoldStoreMarkerLockFailuresAreReported covers the Oracle-only marker
// probe, which has no PostgreSQL counterpart: PostgreSQL locks an existing marker
// with its upsert, while Oracle must SELECT ... FOR UPDATE first and decide from
// the result whether to insert. Both arms of that decision, and the probe's own
// failure, are Oracle's alone.
func TestOracleHoldStoreMarkerLockFailuresAreReported(t *testing.T) {
	t.Run("a_failed_lock_never_writes_anything", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		wantErr := errors.New("ORA-00054 resource busy")
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		tx := db.ExpectTransaction()
		tx.ExpectQuery(`FOR UPDATE`).WillReturnError(wantErr)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "lock tenant marker failed")
		assert.Empty(t, tx.ExecLog(), "neither the marker nor the row is written")
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("a_failed_marker_insert_never_writes_the_row", func(t *testing.T) {
		store := newOracleHoldTestStore(t)
		wantErr := errors.New("ORA-00600 internal error")
		db := dbtesting.NewTestDB(dbtypes.Oracle)
		tx := db.ExpectTransaction()
		// No marker found, so the insert runs — and fails.
		tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnError(wantErr)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

		require.Error(t, err)
		require.Len(t, tx.ExecLog(), 1, "the row is never written without its marker")
		assert.Contains(t, tx.ExecLog()[0].SQL, holdTenantTable)
		require.ErrorIs(t, err, wantErr)
	})
}

// TestOracleHoldStoreRelocksAfterLosingTheMarkerRace pins the Oracle-only hazard
// the insert's unique violation hides: the winner's marker is a row THIS
// transaction holds no lock on, so tolerating the violation and moving on would
// let a release delete it while the held row is still uncommitted. The probe must
// run AGAIN and take the lock.
//
// TestDB matches expectations first-registered-wins and never consumes one, so a
// test cannot script "empty, then present" for the same SQL. What it can pin —
// and what the fix is — is that losing the race sends the store back to the lock
// rather than onward to the row: two probes for one park, and no row written
// while the marker is unheld.
func TestOracleHoldStoreRelocksAfterLosingTheMarkerRace(t *testing.T) {
	store := newOracleHoldTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	tx := db.ExpectTransaction()
	tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
	tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnError(oracleUniqueViolation())

	dbtx, err := db.Begin(t.Context())
	require.NoError(t, err)

	_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

	require.Error(t, err, "a marker that never appears is not a park that proceeds")
	assert.Contains(t, err.Error(), "vanished between the lock and the insert")
	assert.Len(t, tx.QueryLog(), 2, "losing the insert race sends the store back to the lock")
	for _, exec := range tx.ExecLog() {
		assert.NotContains(t, exec.SQL, "INSERT INTO "+holdTable+" (consumer, stream",
			"the held row is never written while its marker is unheld")
	}
}

// oracleUniqueViolation is the driver's own ORA-00001, which is what
// database.IsUniqueViolation recognizes — a bare error carrying the text is not
// the same thing, and a test built on one would pass against a store that never
// tolerates a duplicate.
func oracleUniqueViolation() error {
	return &oranet.OracleError{ErrCode: 1}
}

// TestOracleHoldStoreCreatesEveryObjectEvenWhenSomeExist pins what "already
// exists" must not cost: Oracle has no IF NOT EXISTS, so a re-run raises
// ORA-00955 for whatever a previous run created. Returning on the first of those
// would skip the tenant table and both indexes whenever the row table existed —
// and the startup probe would never notice, because it reads a table, not an
// index.
func TestOracleHoldStoreCreatesEveryObjectEvenWhenSomeExist(t *testing.T) {
	store := newOracleHoldTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	// The row table is already there; everything else is not.
	db.ExpectExec(`CONSTRAINT pk_` + holdTable).WillReturnError(oracleObjectExists())
	db.ExpectExec(`CONSTRAINT pk_` + holdTenantTable).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_` + holdTable + `_tenant_order`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_` + holdTable + `_tenant_due`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
	assert.Len(t, db.ExecLog(), 4, "an existing object never stops the rest of the DDL")
}

// oracleObjectExists is the driver's ORA-00955.
func oracleObjectExists() error {
	return &oranet.OracleError{ErrCode: oracleObjectExistsCode}
}
