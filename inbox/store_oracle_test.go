package inbox

import (
	"errors"
	"testing"
	"time"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	oranet "github.com/sijms/go-ora/v2/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oracleTestTable = "gobricks_inbox"

func newOracleTestStore(t *testing.T) *oracleStore {
	t.Helper()
	store, err := NewOracleStore(oracleTestTable)
	require.NoError(t, err)
	return store.(*oracleStore)
}

func TestOracleStoreMarkProcessedInserted(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err)
	assert.True(t, inserted)
}

// TestResolveOracleTenant guards issue #590: the empty/default tenant maps to the non-NULL
// sentinel, real tenant ids pass through, and a real tenant equal to the sentinel is rejected.
func TestResolveOracleTenant(t *testing.T) {
	got, err := resolveOracleTenant("")
	require.NoError(t, err)
	assert.Equal(t, oracleEmptyTenantSentinel, got, "empty/default tenant maps to the sentinel")

	got, err = resolveOracleTenant("tenant-a")
	require.NoError(t, err)
	assert.Equal(t, "tenant-a", got, "real tenant ids pass through unchanged")

	_, err = resolveOracleTenant(oracleEmptyTenantSentinel)
	require.Error(t, err, "a real tenant id colliding with the reserved sentinel must be rejected")
	assert.Contains(t, err.Error(), "reserved")
}

// TestOracleStoreMarkProcessedDefaultTenant guards issue #590: an empty tenant id (single-tenant
// mode) must insert successfully — the store substitutes the non-NULL sentinel rather than
// binding NULL, which Oracle would reject with ORA-01400 on the NOT NULL primary-key column.
func TestOracleStoreMarkProcessedDefaultTenant(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	rec := Record{TenantID: "", EventID: "evt-1", ProcessedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	inserted, err := store.MarkProcessed(t.Context(), tx, rec)
	require.NoError(t, err)
	assert.True(t, inserted)
}

// TestOracleStoreMarkProcessedRejectsReservedTenant asserts a real tenant id equal to the
// reserved sentinel is rejected rather than colliding with the default tenant in the ledger.
func TestOracleStoreMarkProcessedRejectsReservedTenant(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction() // tx is opened, but the guard rejects before any Exec

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	rec := Record{TenantID: oracleEmptyTenantSentinel, EventID: "evt-1", ProcessedAt: time.Now()}
	_, err = store.MarkProcessed(t.Context(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

// TestOracleCreateTableTenantNotNullNoEmptyDefault guards the fresh-table DDL: tenant_id stays
// NOT NULL but no longer carries the contradictory DEFAULT empty-string (which is NULL on Oracle).
func TestOracleCreateTableTenantNotNullNoEmptyDefault(t *testing.T) {
	assert.NotContains(t, oracleCreateTableSQL, "tenant_id     VARCHAR2(255) DEFAULT '' NOT NULL",
		"the contradictory DEFAULT '' (which is NULL on Oracle) must be gone")
	assert.Contains(t, oracleCreateTableSQL, "tenant_id     VARCHAR2(255) NOT NULL,")
}

func TestOracleStoreMarkProcessedDuplicate(t *testing.T) {
	store := newOracleTestStore(t)
	// ORA-00001 unique violation -> caught as a duplicate, not an error.
	oraDup := &oranet.OracleError{ErrCode: 1}
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnError(oraDup)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err, "ORA-00001 must be caught, not surfaced")
	assert.False(t, inserted, "duplicate reports inserted=false")
}

func TestOracleStoreMarkProcessedNonUniqueError(t *testing.T) {
	store := newOracleTestStore(t)
	wantErr := errors.New("ORA-12541 no listener")
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnError(wantErr)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	_, err = store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark processed failed")
}

func TestOracleStoreDeleteProcessed(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`DELETE FROM gobricks_inbox`).WillReturnRowsAffected(2)

	n, err := store.DeleteProcessed(t.Context(), db, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestOracleStoreCreateTable(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`CREATE TABLE gobricks_inbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_gobricks_inbox_processed`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
}
