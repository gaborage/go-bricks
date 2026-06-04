package inbox

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRegistrar captures DailyAt registrations. Implements app.JobRegistrar.
type fakeRegistrar struct {
	dailyJobs map[string]any
}

func (r *fakeRegistrar) FixedRate(string, any, time.Duration) error { return nil }
func (r *fakeRegistrar) DailyAt(jobID string, job any, _ time.Time) error {
	if r.dailyJobs == nil {
		r.dailyJobs = map[string]any{}
	}
	r.dailyJobs[jobID] = job
	return nil
}
func (r *fakeRegistrar) WeeklyAt(string, any, time.Weekday, time.Time) error { return nil }
func (r *fakeRegistrar) HourlyAt(string, any, int) error                     { return nil }
func (r *fakeRegistrar) MonthlyAt(string, any, int, time.Time) error         { return nil }

func testDeps() *app.ModuleDeps {
	return &app.ModuleDeps{
		Logger: logger.New("info", false),
		DB: func(context.Context) (dbtypes.Interface, error) {
			return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil
		},
	}
}

func TestModuleNameIsInbox(t *testing.T) {
	assert.Equal(t, "inbox", NewModule().Name())
}

func TestModuleInitDisabledExposesNilProcessor(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: false}}

	require.NoError(t, m.Init(deps))
	assert.Nil(t, m.InboxProcessor(), "a disabled inbox must expose a nil InboxProcessor (no typed-nil interface)")
}

func TestModuleInitEnabledExposesProcessor(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}

	require.NoError(t, m.Init(deps))
	require.NotNil(t, m.InboxProcessor())
}

func TestModuleInitEnabledRequiresDB(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.DB = nil
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database resolver")
}

func TestModuleInitRejectsBadTableName(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true, TableName: "schema.inbox"}}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unqualified")
}

func TestRegisterJobsRegistersCleanup(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour}}
	require.NoError(t, m.Init(deps))

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	assert.Contains(t, reg.dailyJobs, "inbox-cleanup")
}

func TestRegisterJobsSkipsWhenDisabled(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: false}}
	require.NoError(t, m.Init(deps))

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	assert.Empty(t, reg.dailyJobs)
}
