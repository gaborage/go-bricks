package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeJobCtx is a minimal scheduler.JobContext backed by a test DB.
type fakeJobCtx struct {
	context.Context
	db dbtypes.Interface
}

func (c fakeJobCtx) JobID() string               { return "inbox-cleanup" }
func (c fakeJobCtx) TriggerType() string         { return "scheduled" }
func (c fakeJobCtx) Logger() logger.Logger       { return logger.New("info", false) }
func (c fakeJobCtx) DB() dbtypes.Interface       { return c.db }
func (c fakeJobCtx) Messaging() messaging.Client { return nil }
func (c fakeJobCtx) Config() *config.Config      { return nil }

func newCoverageModule(db dbtypes.Interface, cfg config.InboxConfig) *Module {
	return &Module{
		logger: logger.New("info", false),
		cfg:    cfg,
		getDB:  func(context.Context) (dbtypes.Interface, error) { return db, nil },
	}
}

func TestEnsureStoreInitializedOracleWithAutoCreate(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`CREATE TABLE gobricks_inbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_gobricks_inbox_processed`).WillReturnRowsAffected(0)
	m := newCoverageModule(db, config.InboxConfig{Enabled: true, TableName: "gobricks_inbox", AutoCreateTable: true})

	require.NoError(t, m.ensureStoreInitialized(t.Context()))
	assert.True(t, m.tableCreated)
	assert.NotNil(t, m.store)
}

func TestLazyStoreDelegates(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
	db.ExpectExec(`CREATE TABLE`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX`).WillReturnRowsAffected(0)
	m := newCoverageModule(db, config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"})
	ls := &lazyStore{module: m}

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	inserted, err := ls.MarkProcessed(t.Context(), tx, Record{EventID: "e", ProcessedAt: time.Now()})
	require.NoError(t, err)
	assert.True(t, inserted)

	require.NoError(t, ls.CreateTable(t.Context(), db))
}

func TestCleanupExecute(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`DELETE FROM gobricks_inbox`).WillReturnRowsAffected(5)
	m := newCoverageModule(db, config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"})

	c := &Cleanup{store: &lazyStore{module: m}, retentionPeriod: time.Hour}
	ctx := fakeJobCtx{Context: context.Background(), db: db}
	require.NoError(t, c.Execute(ctx))
}

func TestCleanupExecuteErrorsWithoutDB(t *testing.T) {
	c := &Cleanup{store: &lazyStore{module: &Module{}}, retentionPeriod: time.Hour}
	ctx := fakeJobCtx{Context: context.Background(), db: nil}
	err := c.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}

func TestModuleShutdown(t *testing.T) {
	m := newCoverageModule(dbtesting.NewTestDB(dbtypes.PostgreSQL), config.InboxConfig{})
	require.NoError(t, m.Shutdown())
}

func TestLazyStorePropagatesInitError(t *testing.T) {
	m := &Module{
		cfg:   config.InboxConfig{TableName: "gobricks_inbox"},
		getDB: func(context.Context) (dbtypes.Interface, error) { return nil, errInitFailed },
	}
	ls := &lazyStore{module: m}

	_, err := ls.MarkProcessed(context.Background(), nil, Record{})
	require.ErrorIs(t, err, errInitFailed)
	_, err = ls.DeleteProcessed(context.Background(), nil, time.Now())
	require.ErrorIs(t, err, errInitFailed)
	err = ls.CreateTable(context.Background(), nil)
	require.ErrorIs(t, err, errInitFailed)
}

var errInitFailed = errors.New("db unavailable")
