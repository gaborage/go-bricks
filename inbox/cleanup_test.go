package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

// captureStore records the tenant present in the context of each DeleteProcessed call,
// so cleanup fan-out tests can assert per-tenant resolution without a real database.
type captureStore struct {
	tenants      []string
	deleteErrFor string
}

func (s *captureStore) MarkProcessed(context.Context, dbtypes.Tx, Record) (bool, error) {
	return false, nil
}

func (s *captureStore) DeleteProcessed(ctx context.Context, _ dbtypes.Interface, _ time.Time) (int64, error) {
	tid, _ := multitenant.GetTenant(ctx)
	s.tenants = append(s.tenants, tid)
	if tid == s.deleteErrFor {
		return 0, errors.New("delete boom")
	}
	return 0, nil
}

func (s *captureStore) CreateTable(context.Context, dbtypes.Interface) error { return nil }

func TestInboxCleanupFansOutAcrossStaticTenants(t *testing.T) {
	store := &captureStore{}
	c := &Cleanup{
		store:           store,
		retentionPeriod: time.Hour,
		getDB:           func(context.Context) (dbtypes.Interface, error) { return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil },
		tenants:         []string{"tenant-a", "tenant-b"},
	}
	require.NoError(t, c.Execute(fakeJobCtx{Context: context.Background()}))
	assert.Equal(t, []string{"tenant-a", "tenant-b"}, store.tenants, "cleanup must run once per configured tenant, in order")
}

func TestInboxCleanupIsolatesPerTenantFailures(t *testing.T) {
	store := &captureStore{deleteErrFor: "bad"}
	c := &Cleanup{
		store:           store,
		retentionPeriod: time.Hour,
		getDB:           func(context.Context) (dbtypes.Interface, error) { return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil },
		tenants:         []string{"good", "bad"},
	}
	err := c.Execute(fakeJobCtx{Context: context.Background()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "bad"`)
	assert.Equal(t, []string{"good", "bad"}, store.tenants, "both tenants are attempted despite one failing")
}

// fakeJobCtx is a minimal scheduler.JobContext backed by a test DB.
type fakeJobCtx struct {
	context.Context
	db dbtypes.Interface
}

func (c fakeJobCtx) JobID() string                   { return "inbox-cleanup" }
func (c fakeJobCtx) TriggerType() string             { return "scheduled" }
func (c fakeJobCtx) Logger() logger.Logger           { return logger.New("info", false) }
func (c fakeJobCtx) DB() dbtypes.Interface           { return c.db }
func (c fakeJobCtx) Messaging() messaging.AMQPClient { return nil }
func (c fakeJobCtx) Config() *config.Config          { return nil }

func TestCleanupExecute(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`DELETE FROM gobricks_inbox`).WillReturnRowsAffected(5)
	m := newCoverageModule(db, &config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"})

	c := &Cleanup{
		store:           &lazyStore{module: m},
		retentionPeriod: time.Hour,
		getDB:           func(context.Context) (dbtypes.Interface, error) { return db, nil },
		tenants:         []string{""},
	}
	ctx := fakeJobCtx{Context: context.Background(), db: db}
	require.NoError(t, c.Execute(ctx))
}

func TestCleanupExecuteErrorsWithoutDB(t *testing.T) {
	c := &Cleanup{
		store:           &lazyStore{module: &Module{}},
		retentionPeriod: time.Hour,
		getDB:           func(context.Context) (dbtypes.Interface, error) { return nil, nil },
		tenants:         []string{""},
	}
	ctx := fakeJobCtx{Context: context.Background(), db: nil}
	err := c.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}
