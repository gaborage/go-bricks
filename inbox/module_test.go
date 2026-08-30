package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
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

// probeReadyDB returns a fresh TestDB whose DELETE expectation satisfies Init's
// startup-database probe (verifyStartupDatabase's DeleteProcessed call).
func probeReadyDB() *dbtesting.TestDB {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("DELETE").WillReturnRowsAffected(0)
	return db
}

// testDeps' DB resolver mints a fresh, probe-ready TestDB on every call, so any
// test that reaches the startup-database probe passes it.
func testDeps() *app.ModuleDeps {
	return &app.ModuleDeps{
		Logger: logger.New("info", false),
		DB: func(context.Context) (dbtypes.Interface, error) {
			return probeReadyDB(), nil
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

// TestModuleInitFailsWhenDatabaseNotConfigured guards gap (a) from issue #876:
// an enabled inbox with no database configured must fail startup, not boot green.
func TestModuleInitFailsWhenDatabaseNotConfigured(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return nil, config.NewNotConfiguredError("database", "DATABASE_TYPE", "database")
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a database, but none is configured")
}

// TestModuleInitFailsWhenDatabaseUnreachable guards gap (b): a configured but
// unreachable database must fail startup rather than let every ProcessOnce call
// fail against a store that was never usable.
func TestModuleInitFailsWhenDatabaseUnreachable(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return nil, errors.New("dial tcp 10.0.0.1:5432: connect: connection refused")
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unreachable at startup")
}

// TestModuleInitFailsWhenTableMissing guards gap (c): a reachable database whose
// inbox table does not exist (or is unwritable) must fail startup — the fixture
// has no exec expectations, so the probe's DELETE goes unmatched.
func TestModuleInitFailsWhenTableMissing(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not usable")
}

// TestModuleInitPerTenantMultitenantSkipsStartupProbe pins
// startupDatabaseCheckApplies' Multitenant clause: the failing getDB must not fail Init.
func TestModuleInitPerTenantMultitenantSkipsStartupProbe(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{Enabled: true},
		Multitenant: config.MultitenantConfig{
			Enabled: true,
			Tenants: map[string]config.TenantEntry{"tenant-a": {}},
		},
	}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return nil, errors.New("would fail the probe if it ran")
	}

	err := m.Init(deps)
	require.NoError(t, err, "per-tenant fan-out resolves databases at runtime, not at Init")
}

// TestModuleInitDynamicSourceSkipsStartupProbe pins
// startupDatabaseCheckApplies' Source.Type clause: the failing getDB must not fail Init.
func TestModuleInitDynamicSourceSkipsStartupProbe(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:  config.InboxConfig{Enabled: true},
		Source: config.SourceConfig{Type: config.SourceTypeDynamic},
	}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return nil, errors.New("would fail the probe if it ran")
	}

	err := m.Init(deps)
	require.NoError(t, err, "a dynamic source resolves the \"\" key at runtime, not at Init")
}

// TestModuleInitSharedTenancyStaticSourceProbesControlPlaneDatabase closes gap
// (3): tenancy=shared with a static source resolves the "" key statically, so
// Init must now probe the control-plane database — previously this mode was
// never probed at startup at all.
func TestModuleInitSharedTenancyStaticSourceProbesControlPlaneDatabase(t *testing.T) {
	m := NewModule()
	failingDB := func(_ context.Context) (dbtypes.Interface, error) {
		return nil, errors.New("dial tcp control-plane-db: connection refused")
	}
	m.SetSharedResolvers(failingDB, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox:       config.InboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true}, // pins the sharedLedger() clause, not just !Multitenant.Enabled
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unreachable at startup",
		"tenancy=shared with a static source must probe the control-plane \"\" database at startup")
}

// TestModuleInitDisabledInboxSkipsStartupProbe pins that a disabled inbox never
// reaches the probe, regardless of how the DB resolver would behave.
func TestModuleInitDisabledInboxSkipsStartupProbe(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: false}}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) {
		return nil, errors.New("would fail the probe if it ran")
	}

	err := m.Init(deps)
	require.NoError(t, err)
}

// TestModuleInitDefaultsZeroStartupTimeoutToTenSeconds pins the timeout <= 0
// default: an unset App.Startup.Database must bound the probe's context at
// 10s, not 0 (an immediately-expired context) or another arithmetic result.
func TestModuleInitDefaultsZeroStartupTimeoutToTenSeconds(t *testing.T) {
	m := NewModule()
	var deadline time.Time
	var hasDeadline bool
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}} // App.Startup.Database left at zero
	deps.DB = func(ctx context.Context) (dbtypes.Interface, error) {
		deadline, hasDeadline = ctx.Deadline()
		return probeReadyDB(), nil
	}

	err := m.Init(deps)
	require.NoError(t, err)
	require.True(t, hasDeadline, "verifyStartupDatabase must derive a bounded context")
	assert.InDelta(t, float64(10*time.Second), float64(time.Until(deadline)), float64(2*time.Second),
		"a zero App.Startup.Database must default the probe timeout to 10s")
}

// TestModuleInitFailsWhenDatabaseResolverReturnsNilDatabase pins the nil-db
// contract-violation guard: without it, ensureStoreInitialized's
// db.DatabaseType() call would panic inside Init instead of erroring.
func TestModuleInitFailsWhenDatabaseResolverReturnsNilDatabase(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	deps.DB = func(_ context.Context) (dbtypes.Interface, error) { return nil, nil }

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned a nil database")
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

// TestModuleInitRefusesAHoldOnPerTenantLedgers pins the tenancy the hold needs:
// a tenant whose own database is down cannot hold its own messages, so the
// ledger must be the control-plane one.
func TestModuleInitRefusesAHoldOnPerTenantLedgers(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{
			Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyPerTenant,
			Hold: config.InboxHoldConfig{Enabled: true},
		},
	}

	err := m.Init(deps)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "inbox.tenancy: shared")
}

// TestModuleInitAcceptsAHoldOnTheSharedLedger is the other half: the same hold
// on the tenancy it requires reaches the rest of startup.
func TestModuleInitAcceptsAHoldOnTheSharedLedger(t *testing.T) {
	m := NewModule()
	// The shared resolver must be probe-ready: this Init reaches the startup
	// database check, which stubSharedDB's bare TestDB has no expectation for.
	m.SetSharedResolvers(func(context.Context) (dbtypes.Interface, error) {
		return probeReadyDB(), nil
	}, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{
			Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyShared,
			Hold: config.InboxHoldConfig{Enabled: true},
		},
	}

	require.NoError(t, m.Init(deps))
	assert.True(t, m.holdEnabled())
}

// TestModuleInitIgnoresAHoldOnADisabledInbox pins that a stray hold key on a
// disabled inbox stays a no-op rather than failing startup.
func TestModuleInitIgnoresAHoldOnADisabledInbox(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{
			Enabled: false, Tenancy: config.TenancyPerTenant,
			Hold: config.InboxHoldConfig{Enabled: true},
		},
	}

	require.NoError(t, m.Init(deps))
	assert.False(t, m.holdEnabled())
}
