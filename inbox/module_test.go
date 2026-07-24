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

func TestRegisterJobsFailsFastForDynamicMultitenant(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	}
	require.NoError(t, m.Init(deps), "Init succeeds — ProcessOnce works in multi-tenant mode")

	reg := &fakeRegistrar{}
	err := m.RegisterJobs(reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dynamic multi-tenant",
		"the cleanup job cannot enumerate dynamic tenants, so RegisterJobs must fail fast")
	assert.Contains(t, err.Error(), "inbox.tenancy=shared",
		"the error must hint at the shared-tenancy escape hatch")
	assert.Empty(t, reg.dailyJobs, "no cleanup job is registered for dynamic multi-tenant")
}

func TestRegisterJobsFailsFastForEmptyStaticMultitenant(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour},
		Multitenant: config.MultitenantConfig{Enabled: true}, // static (default) but no tenants configured
	}
	require.NoError(t, m.Init(deps), "Init succeeds; the guard is in RegisterJobs")

	reg := &fakeRegistrar{}
	err := m.RegisterJobs(reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no static multitenant.tenants",
		"the cleanup job would fan out across zero tenants and prune nothing, so RegisterJobs must fail fast")
	assert.Contains(t, err.Error(), "inbox.tenancy=shared",
		"the error must hint at the shared-tenancy escape hatch")
	assert.Empty(t, reg.dailyJobs, "no cleanup job is registered when static multi-tenant has no tenants")
}

func TestInitSucceedsForDynamicMultitenantProcessOnceOnly(t *testing.T) {
	// ProcessOnce resolves the tenant from the request context, so it works in dynamic
	// multi-tenant mode. Init must succeed; the dynamic-MT guard lives in RegisterJobs,
	// which only runs when the scheduler is present and the cleanup job would register.
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	}

	require.NoError(t, m.Init(deps))
	require.NotNil(t, m.InboxProcessor())
}

// stubSharedDB is a minimal non-nil resolver for shared-mode Init/RegisterJobs
// tests. The resolver-presence guard runs before any other tenancy logic, so
// it must be satisfied via SetSharedResolvers even when the test targets a
// later guard.
func stubSharedDB(context.Context) (dbtypes.Interface, error) {
	return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil
}

func TestRegisterJobsSharedTenancyDynamicMultitenantSucceeds(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	}
	require.NoError(t, m.Init(deps))

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	assert.Contains(t, reg.dailyJobs, "inbox-cleanup")
}

func TestModuleInitSharedTenancyRejectsStaticTenants(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true, Tenants: map[string]config.TenantEntry{"tenant-a": {}}},
		Source:      config.SourceConfig{Type: config.SourceTypeStatic},
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with static",
		"shared tenancy resolves the root \"\" key, which static tenant mode forbids")
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
