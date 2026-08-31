package inbox

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
)

const (
	holdTable        = "gobricks_inbox_hold"
	holdTenantTable  = "gobricks_inbox_hold_tenant"
	testHoldConsumer = "orders-processor"
	testHoldTenant   = "tenant-a"
	otherHoldTenant  = "tenant-b"
	testHoldOwner    = "replica-1"
	testHoldStream   = "orders-0"
)

func newPostgresHoldTestStore(t *testing.T) HoldStore {
	t.Helper()
	store, err := NewPostgresHoldStore(holdTable)
	require.NoError(t, err)
	return store
}

func sampleHoldRow() *HoldRow {
	return &HoldRow{
		Consumer: testHoldConsumer,
		Stream:   testHoldStream,
		Offset:   41,
		TenantID: testHoldTenant,
		Data:     []byte(`{"id":1}`),
	}
}

// tenantRowSet is the six columns every tenant-shaped query selects, in order.
func tenantRowSet(heldSince time.Time) *dbtesting.RowSet {
	return dbtesting.NewRowSet("consumer", "tenant_id", "held_since", "attempts", "next_attempt_at", "last_error").
		AddRow(testHoldConsumer, testHoldTenant, heldSince, 2, heldSince, "boom")
}

// TestPostgresHoldStoreParkWritesRowAndTenantTogether pins that one park is two
// writes in the CALLER's transaction, marker FIRST: an existing marker is locked
// before the row is written, so a drain deciding to release this tenant waits for
// the row rather than deleting the marker out from under it. A row without its
// marker would be replayed by nothing.
func TestPostgresHoldStoreParkWritesRowAndTenantTogether(t *testing.T) {
	t.Run("first_park_reports_inserted", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnRowsAffected(1)
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnRowsAffected(1)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		inserted, err := store.Park(t.Context(), dbtx, sampleHoldRow())

		require.NoError(t, err)
		assert.True(t, inserted)
		require.Len(t, tx.ExecLog(), 2)
		assert.Contains(t, tx.ExecLog()[0].SQL, holdTenantTable, "the marker is taken first")
	})

	t.Run("a_redelivered_offset_reports_not_inserted", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnRowsAffected(1)
		// ON CONFLICT DO NOTHING: the row is already parked.
		tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnRowsAffected(0)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		inserted, err := store.Park(t.Context(), dbtx, sampleHoldRow())

		require.NoError(t, err)
		assert.False(t, inserted, "a re-park inserts nothing but must not fail")
	})

	t.Run("a_failed_marker_write_never_writes_the_row", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		wantErr := errors.New("connection reset")
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()
		tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnError(wantErr)

		dbtx, err := db.Begin(t.Context())
		require.NoError(t, err)

		_, err = store.Park(t.Context(), dbtx, sampleHoldRow())

		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
		assert.Contains(t, err.Error(), "mark tenant held failed")
		assert.Len(t, tx.ExecLog(), 1, "the row is never written without its marker")
	})
}

// TestPostgresHoldStoreReadsTheHeldSet pins the two reads a runner and a drain
// pass live on.
func TestPostgresHoldStoreReadsTheHeldSet(t *testing.T) {
	t.Run("held_tenants", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).
			WillReturnRows(dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant).AddRow("tenant-b"))

		tenants, err := store.HeldTenants(t.Context(), db, testHoldConsumer)

		require.NoError(t, err)
		assert.Equal(t, []string{testHoldTenant, "tenant-b"}, tenants)
	})

	t.Run("list_tenants_carries_the_drain_state", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		heldSince := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery(`FROM ` + holdTenantTable).WillReturnRows(tenantRowSet(heldSince))

		tenants, err := store.ListTenants(t.Context(), db, testHoldConsumer)

		require.NoError(t, err)
		require.Len(t, tenants, 1)
		assert.Equal(t, testHoldTenant, tenants[0].TenantID)
		assert.Equal(t, heldSince, tenants[0].HeldSince)
		assert.Equal(t, 2, tenants[0].Attempts)
		assert.Equal(t, "boom", tenants[0].LastError)
	})

	t.Run("due_tenants", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(tenantRowSet(time.Now()))

		tenants, err := store.DueTenants(t.Context(), db, testHoldConsumer, 10)

		require.NoError(t, err)
		require.Len(t, tenants, 1)
		assert.Equal(t, testHoldTenant, tenants[0].TenantID)
	})

	t.Run("next_rows_are_in_offset_order", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		heldAt := time.Now()
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(
			dbtesting.NewRowSet("consumer", "stream", "stream_offset", "tenant_id", "data", "properties", "held_at").
				AddRow(testHoldConsumer, testHoldStream, int64(41), testHoldTenant, []byte("a"), nil, heldAt).
				AddRow(testHoldConsumer, testHoldStream, int64(42), testHoldTenant, []byte("b"), nil, heldAt),
		)

		rows, err := store.NextRows(t.Context(), db, testHoldConsumer, testHoldTenant, 10)

		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, int64(41), rows[0].Offset)
		assert.Equal(t, int64(42), rows[1].Offset)
	})
}

// TestPostgresHoldStoreFencesEveryPostReplayWrite pins the property a lost lease
// depends on: each write after a replay carries the lease, and a write that
// changes NO row is lease loss rather than success.
func TestPostgresHoldStoreFencesEveryPostReplayWrite(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		affected int64
		call     func(HoldStore, dbtypes.Interface) (bool, error)
	}{
		{
			name:     "delete_row_under_the_lease",
			pattern:  `DELETE FROM ` + holdTable,
			affected: 1,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.DeleteRow(t.Context(), db, testHoldConsumer, testHoldStream, 41, testHoldTenant, testHoldOwner)
			},
		},
		{
			name:     "delete_row_without_the_lease",
			pattern:  `DELETE FROM ` + holdTable,
			affected: 0,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.DeleteRow(t.Context(), db, testHoldConsumer, testHoldStream, 41, testHoldTenant, testHoldOwner)
			},
		},
		{
			name:     "defer_under_the_lease",
			pattern:  `UPDATE ` + holdTenantTable,
			affected: 1,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.Defer(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner, time.Second, "boom")
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
			name:     "release_under_the_lease",
			pattern:  `DELETE FROM ` + holdTenantTable,
			affected: 1,
			call: func(s HoldStore, db dbtypes.Interface) (bool, error) {
				return s.Release(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner)
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
			store := newPostgresHoldTestStore(t)
			db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
			db.ExpectExec(tc.pattern).WillReturnRowsAffected(tc.affected)

			ok, err := tc.call(store, db)

			require.NoError(t, err, "a lost lease is not an error, it is a false")
			// The fence held exactly when the write changed a row: a zero-row write
			// is lease loss, which is what the caller reads this bool for.
			assert.Equal(t, tc.affected != 0, ok)
		})
	}
}

// TestPostgresHoldStoreBoundsThePersistedError pins that a handler's message
// reaches the ledger bounded, however long it was.
func TestPostgresHoldStoreBoundsThePersistedError(t *testing.T) {
	store := newPostgresHoldTestStore(t)
	oversized := strings.Repeat("handler exploded; ", 512)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)

	_, err := store.Defer(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner, time.Second, oversized)
	require.NoError(t, err)

	execs := db.ExecLog()
	require.Len(t, execs, 1)
	persisted, ok := execs[0].Args[1].(string)
	require.True(t, ok, "last_error is the second bound argument")
	assert.Greater(t, len(oversized), ledgererr.MaxBytes, "the fixture is actually oversized")
	assert.LessOrEqual(t, len(persisted), ledgererr.MaxBytes, "the ledger never receives more than the cap")
	assert.True(t, strings.HasSuffix(persisted, ledgererr.TruncationMarker),
		"a shortened error says so, or a reader cannot tell it from a short one")
}

// TestPostgresHoldStoreLeaseAndStats covers the two reads that are neither a
// held-set read nor a fenced write.
func TestPostgresHoldStoreLeaseAndStats(t *testing.T) {
	t.Run("acquire_lease_reports_whether_it_took_it", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(0)

		took, err := store.AcquireLease(t.Context(), db, testHoldConsumer, testHoldTenant, testHoldOwner, time.Minute)

		require.NoError(t, err)
		assert.False(t, took, "another owner's live lease is not an error")
	})

	t.Run("stats_snapshot", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		oldest := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
			dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(2), int64(7), oldest),
		)

		stats, err := store.Stats(t.Context(), db, testHoldConsumer)

		require.NoError(t, err)
		assert.Equal(t, int64(2), stats.Tenants)
		assert.Equal(t, int64(7), stats.Rows)
		assert.Equal(t, oldest, stats.OldestHeldSince)
	})

	t.Run("create_table_makes_both_tables_and_both_indexes", func(t *testing.T) {
		store := newPostgresHoldTestStore(t)
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + holdTable).WillReturnRowsAffected(0)
		db.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + holdTenantTable).WillReturnRowsAffected(0)
		db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_` + holdTable + `_tenant_order`).WillReturnRowsAffected(0)
		db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_` + holdTable + `_tenant_due`).WillReturnRowsAffected(0)

		require.NoError(t, store.CreateTable(t.Context(), db))
	})
}

// TestPostgresHoldStoreStatsReportsNoOldestWhenNothingIsHeld pins that an empty
// hold has no oldest entry: substituting the database's own clock would report an
// age of zero for a hold that does not exist, which a gauge cannot tell apart
// from a tenant parked this instant.
func TestPostgresHoldStoreStatsReportsNoOldestWhenNothingIsHeld(t *testing.T) {
	store := newPostgresHoldTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(0), int64(0), nil),
	)

	stats, err := store.Stats(t.Context(), db, testHoldConsumer)

	require.NoError(t, err)
	assert.Zero(t, stats.Tenants)
	assert.True(t, stats.OldestHeldSince.IsZero(), "no hold, no oldest")
}
