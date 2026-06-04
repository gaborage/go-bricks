package inbox

import (
	"errors"
	"testing"
	"time"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgTestTable = "gobricks_inbox"

func newPostgresTestStore(t *testing.T) *postgresStore {
	t.Helper()
	store, err := NewPostgresStore(pgTestTable)
	require.NoError(t, err)
	return store.(*postgresStore)
}

func sampleRecord() Record {
	return Record{
		TenantID:    "tenant-a",
		EventID:     "evt-1",
		ProcessedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewPostgresStoreRejectsBadTableName(t *testing.T) {
	_, err := NewPostgresStore("bad; DROP")
	require.Error(t, err)
	_, err = NewPostgresStore("myschema.gobricks_inbox")
	require.Error(t, err, "qualified names must be rejected")
}

func TestPostgresStoreMarkProcessedInserted(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err)
	assert.True(t, inserted, "first insert reports inserted=true")
}

func TestPostgresStoreMarkProcessedDuplicate(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	// ON CONFLICT DO NOTHING affects 0 rows on a duplicate (no error).
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(0)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err)
	assert.False(t, inserted, "duplicate reports inserted=false")
}

func TestPostgresStoreMarkProcessedError(t *testing.T) {
	store := newPostgresTestStore(t)
	wantErr := errors.New("connection reset")
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
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

func TestPostgresStoreDeleteProcessed(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`DELETE FROM gobricks_inbox`).WillReturnRowsAffected(3)

	n, err := store.DeleteProcessed(t.Context(), db, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
}

func TestPostgresStoreCreateTable(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_inbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_inbox_processed`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
}
