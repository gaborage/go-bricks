package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRegistrar captures FixedRate / DailyAt registrations and returns
// configurable errors. Implements app.JobRegistrar.
type fakeRegistrar struct {
	FixedRateCalls []struct {
		JobID    string
		Job      any
		Interval time.Duration
	}
	DailyAtCalls []struct {
		JobID     string
		LocalTime time.Time
	}
	FixedRateErr error
	DailyAtErr   error
}

func (r *fakeRegistrar) FixedRate(jobID string, job any, interval time.Duration) error {
	r.FixedRateCalls = append(r.FixedRateCalls, struct {
		JobID    string
		Job      any
		Interval time.Duration
	}{jobID, job, interval})
	return r.FixedRateErr
}

func (r *fakeRegistrar) DailyAt(jobID string, _ any, localTime time.Time) error {
	r.DailyAtCalls = append(r.DailyAtCalls, struct {
		JobID     string
		LocalTime time.Time
	}{jobID, localTime})
	return r.DailyAtErr
}

// WeeklyAt / HourlyAt / MonthlyAt are part of the JobRegistrar interface but
// unused by the outbox module. Defined as no-op stubs to satisfy the
// interface contract.
func (r *fakeRegistrar) WeeklyAt(_ string, _ any, _ time.Weekday, _ time.Time) error {
	return nil
}
func (r *fakeRegistrar) HourlyAt(_ string, _ any, _ int) error               { return nil }
func (r *fakeRegistrar) MonthlyAt(_ string, _ any, _ int, _ time.Time) error { return nil }

func TestModuleName(t *testing.T) {
	m := NewModule()
	assert.Equal(t, "outbox", m.Name())
}

func TestModuleInitDisabled(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: false},
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.Nil(t, m.publisher, "Publisher should be nil when outbox is disabled")
}

func TestModuleInitEnabledWithNilDB(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
		},
		DB: nil,
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database resolver")
}

func TestModuleInitEnabledWithNilMessaging(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: nil,
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging resolver")
}

func TestModuleInitEnabledWithBothResolvers(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://localhost"},
			},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.publisher, "Publisher should be initialized when outbox is enabled")
}

func TestModuleInitDisabledAllowsNilResolvers(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: false},
		},
		DB:        nil,
		Messaging: nil,
	}

	err := m.Init(deps)
	require.NoError(t, err, "Nil resolvers should be allowed when outbox is disabled")
}

// TestModuleInitEnabledMessagingUnconfiguredSingleTenant guards issue #366:
// outbox.enabled=true with no messaging.broker.url must fail at startup instead of
// letting the relay advance every event's retry_count each poll without delivering.
func TestModuleInitEnabledMessagingUnconfiguredSingleTenant(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
			// Messaging.Broker.URL intentionally empty.
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging is not configured")
}

// TestModuleInitRejectsPublishTimeoutBelowConnectionTimeout guards the fail-fast: a
// publishtimeout shorter than the broker confirmation wait would truncate every publish
// into a connectivity failure (an unbounded duplicate-delivery loop), so Init rejects it.
func TestModuleInitRejectsPublishTimeoutBelowConnectionTimeout(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 10 * time.Second},
			Messaging: config.MessagingConfig{
				Broker:    config.BrokerConfig{URL: "amqp://localhost"},
				Reconnect: config.ReconnectConfig{ConnectionTimeout: 30 * time.Second},
			},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishtimeout")
	assert.Contains(t, err.Error(), "connectiontimeout")
}

// TestModuleInitRejectsPublishTimeoutBelowReadyTimeout guards the companion fail-fast: a
// publishtimeout shorter than the client's readiness pre-flight wait expires INSIDE that
// wait, so a not-ready broker surfaces as context.DeadlineExceeded instead of
// ErrNotConnected — silently defeating the relay's mid-batch broker-drop detection.
func TestModuleInitRejectsPublishTimeoutBelowReadyTimeout(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 3 * time.Second},
			Messaging: config.MessagingConfig{
				Broker:    config.BrokerConfig{URL: "amqp://localhost"},
				Reconnect: config.ReconnectConfig{ReadyTimeout: 5 * time.Second},
			},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishtimeout")
	assert.Contains(t, err.Error(), "readytimeout")
}

// TestModuleInitRejectsPublishTimeoutBelowResendDelay guards the third fail-fast: a
// resenddelay at or above publishtimeout makes each retryable event burn its whole
// per-record deadline inside a single publish-retry wait, delivering nothing.
func TestModuleInitRejectsPublishTimeoutBelowResendDelay(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 30 * time.Second},
			Messaging: config.MessagingConfig{
				Broker:    config.BrokerConfig{URL: "amqp://localhost"},
				Reconnect: config.ReconnectConfig{ResendDelay: 45 * time.Second},
			},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishtimeout")
	assert.Contains(t, err.Error(), "resenddelay")
}

// TestModuleInitAllowsShortPublishTimeoutWhenNoRetries pins the guard's exemption:
// with maxpublishattempts == 1 the retry loop's attempt ceiling fires before any
// resenddelay wait, so the no-retry setup is valid despite the inverted pair.
func TestModuleInitAllowsShortPublishTimeoutWhenNoRetries(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 30 * time.Second},
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://localhost"},
				Reconnect: config.ReconnectConfig{
					ResendDelay:        45 * time.Second,
					MaxPublishAttempts: 1,
				},
			},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	require.NoError(t, m.Init(deps))
}

// TestModuleInitMessagingUnconfiguredErrorPrecedesTimeoutGuard pins Init's error
// ordering: with no broker URL AND a publishtimeout below connectiontimeout, the
// actionable root cause ("messaging is not configured") must surface, not the derived
// timeout complaint (see the validatePublishTimeout call order in Init).
func TestModuleInitMessagingUnconfiguredErrorPrecedesTimeoutGuard(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 10 * time.Second},
			Messaging: config.MessagingConfig{
				// Broker.URL intentionally empty; connectiontimeout mirrors the value
				// config.Validate now defaults in every deployment mode.
				Reconnect: config.ReconnectConfig{ConnectionTimeout: 30 * time.Second},
			},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging is not configured")
	assert.NotContains(t, err.Error(), "publishtimeout")
}

// TestModuleInitMultiTenantGuardFiresOnValidatedDefaults composes the #659 fix with
// the outbox guard end-to-end: a multi-tenant static config (root broker URL empty)
// run through config.Validate() picks up the defaulted 30s connectiontimeout, so an
// explicit outbox.publishtimeout below it must fail Init — pinning that the guard can
// no longer be silently bypassed by an un-defaulted zero.
func TestModuleInitMultiTenantGuardFiresOnValidatedDefaults(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Name: "outbox-test", Version: "1.0.0", Env: "development"},
		Server: config.ServerConfig{
			Port: 8080,
			Timeout: config.TimeoutConfig{
				Read:       15 * time.Second,
				Write:      30 * time.Second,
				Middleware: 5 * time.Second,
				Shutdown:   10 * time.Second,
			},
		},
		Log: config.LogConfig{Level: "info"},
		Multitenant: config.MultitenantConfig{
			Enabled:  true,
			Resolver: config.ResolverConfig{Type: config.ResolverTypeHeader, Header: "X-Tenant-ID"},
			Tenants: map[string]config.TenantEntry{
				"acme": {
					Database: config.DatabaseConfig{
						Type:     config.PostgreSQL,
						Host:     "acme.db",
						Port:     5432,
						Database: "acme",
						Username: "acme_user",
					},
					Messaging: config.TenantMessagingConfig{URL: "amqp://localhost:5672/"},
				},
			},
		},
		Source: config.SourceConfig{Type: config.SourceTypeStatic},
		Outbox: config.OutboxConfig{Enabled: true, PublishTimeout: 10 * time.Second},
		// Root broker URL intentionally empty — Validate() must still default
		// messaging.reconnect.connectiontimeout to 30s (#659).
	}
	require.NoError(t, config.Validate(cfg))

	m := NewModule()
	deps := &app.ModuleDeps{
		Logger:    logger.New("info", false),
		Config:    cfg,
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishtimeout")
	assert.Contains(t, err.Error(), "connectiontimeout")
}

// TestModuleInitEnabledMessagingUnconfiguredMultiTenant verifies the static
// check is skipped when multitenant.enabled=true — each tenant supplies its
// own broker URL via the resource source, so a global check would be wrong.
func TestModuleInitEnabledMessagingUnconfiguredMultiTenant(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
			Multitenant: config.MultitenantConfig{
				Enabled: true,
				// Static tenants are required for the relay to fan out; messaging is
				// resolved per-tenant, so the global broker URL stays intentionally empty.
				Tenants: map[string]config.TenantEntry{"tenant-a": {}},
			},
			// Messaging.Broker.URL intentionally empty.
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.publisher, "Publisher should be initialized in static multi-tenant mode even with empty global broker URL")
}

func TestModuleInitFailsFastForDynamicMultitenant(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{
			Outbox:      config.OutboxConfig{Enabled: true},
			Multitenant: config.MultitenantConfig{Enabled: true},
			Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dynamic multi-tenant",
		"the relay cannot enumerate dynamic tenants, so Init must fail fast rather than silently never relaying")
	assert.Contains(t, err.Error(), "outbox.tenancy=shared",
		"the error must hint at the shared-tenancy escape hatch")
}

func TestModuleInitFailsFastForEmptyStaticMultitenant(t *testing.T) {
	// multitenant enabled + static source (the default) but no tenants configured: the
	// relay would fan out across zero tenants and silently deliver nothing, so Init must
	// fail fast rather than register a no-op relay.
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{
			Outbox:      config.OutboxConfig{Enabled: true},
			Multitenant: config.MultitenantConfig{Enabled: true}, // Tenants intentionally omitted
		},
		DB:        func(_ context.Context) (dbtypes.Interface, error) { return nil, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no static multitenant.tenants")
	assert.Contains(t, err.Error(), "set outbox.tenancy=shared",
		"the error must hint at the shared-tenancy escape hatch")
}

// stubSharedDB / stubSharedMsg are minimal non-nil resolvers for shared-mode
// Init tests. The resolver-presence guard (Step 4.2a) runs before any other
// tenancy logic, so it must be satisfied via SetSharedResolvers even when the
// test targets a later guard.
func stubSharedDB(_ context.Context) (dbtypes.Interface, error) { return nil, nil }
func stubSharedMsg(_ context.Context) (messaging.AMQPClient, error) {
	return nil, nil
}

// sharedTenancyDeps builds the ModuleDeps used by the shared-tenancy tests;
// only Config varies between them.
func sharedTenancyDeps(cfg *config.Config) *app.ModuleDeps {
	return &app.ModuleDeps{
		Logger:    logger.New("disabled", true),
		Config:    cfg,
		DB:        stubSharedDB,
		Messaging: stubSharedMsg,
	}
}

func TestModuleInitSharedTenancyDynamicMultitenantSucceeds(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, stubSharedMsg)
	deps := sharedTenancyDeps(&config.Config{
		Outbox:      config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	})

	err := m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.publisher, "publisher must be initialized for shared tenancy with a dynamic source")
}

func TestModuleInitSharedTenancyRequiresInjectedResolvers(t *testing.T) {
	m := NewModule()
	// SetSharedResolvers deliberately NOT called.
	deps := sharedTenancyDeps(&config.Config{
		Outbox:      config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	})

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app.RegisterModule",
		"a module constructed without SetSharedResolvers must be told how to fix it")
}

func TestModuleInitSharedTenancyRejectsStaticTenants(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, stubSharedMsg)
	deps := sharedTenancyDeps(&config.Config{
		Outbox:      config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true, Tenants: map[string]config.TenantEntry{"tenant-a": {}}},
		Source:      config.SourceConfig{Type: config.SourceTypeStatic},
	})

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with static",
		"shared tenancy resolves the root \"\" key, which static tenant mode forbids")
}

func TestModuleInitSharedTenancyStaticSourceRequiresRootMessaging(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, stubSharedMsg)
	deps := sharedTenancyDeps(&config.Config{
		Outbox: config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		// Multitenant intentionally disabled — a static source is the default in
		// single-tenant mode, and the root messaging.broker.url is still empty.
	})

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root",
		"shared tenancy with a static source must require the root messaging.broker.url")
}

func TestRegisterJobsSharedTenancySinglePass(t *testing.T) {
	m := NewModule()
	m.SetSharedResolvers(stubSharedDB, stubSharedMsg)
	deps := sharedTenancyDeps(&config.Config{
		Outbox:      config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared},
		Multitenant: config.MultitenantConfig{Enabled: true},
		Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
	})
	require.NoError(t, m.Init(deps))

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	require.Len(t, reg.FixedRateCalls, 1, "relay registered exactly once")
	assert.Equal(t, "outbox-relay", reg.FixedRateCalls[0].JobID)
	require.NotNil(t, reg.FixedRateCalls[0].Job, "the registrar fake must capture the constructed relay")
	relay, ok := reg.FixedRateCalls[0].Job.(*Relay)
	require.True(t, ok, "the registered job must be a *Relay")
	assert.Equal(t, []string{""}, relay.tenants, "shared tenancy runs a single control-plane pass, not a fan-out")
	assert.Equal(t, config.TenancyShared, relay.config.Tenancy,
		"logCycle derives the tenancy stamp from the relay's config copy")
}

// initEnabledModule returns an outbox Module that has gone through Init
// against the supplied dbVendor (no auto-create-table). getDB is wired to
// return a TestDB of that vendor.
func initEnabledModule(t *testing.T, dbVendor string, retention time.Duration) (*Module, *dbtesting.TestDB) {
	t.Helper()
	db := dbtesting.NewTestDB(dbVendor)
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{
			Outbox: config.OutboxConfig{
				Enabled:         true,
				RetentionPeriod: retention,
			},
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://localhost"},
			},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) { return db, nil },
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}
	require.NoError(t, m.Init(deps))
	return m, db
}

func TestModuleEnsureStoreInitializedPostgres(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 0)
	store, err := m.ensureStoreInitialized(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, store, "store created for postgres vendor")
}

func TestModuleEnsureStoreInitializedOracle(t *testing.T) {
	m, _ := initEnabledModule(t, "oracle", 0)
	store, err := m.ensureStoreInitialized(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, store, "store created for oracle vendor")
}

func TestModuleEnsureStoreInitializedUnknownVendor(t *testing.T) {
	m, _ := initEnabledModule(t, "mysql", 0)
	store, err := m.ensureStoreInitialized(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported database vendor")
	assert.Nil(t, store, "store must remain nil on unsupported vendor")
}

func TestModuleEnsureStoreInitializedDBResolverError(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{
			Outbox:    config.OutboxConfig{Enabled: true},
			Messaging: config.MessagingConfig{Broker: config.BrokerConfig{URL: "amqp://x"}},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, errors.New("connection refused")
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) { return nil, nil },
	}
	require.NoError(t, m.Init(deps))

	store, err := m.ensureStoreInitialized(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unavailable")
	assert.Contains(t, err.Error(), "connection refused")
	assert.Nil(t, store)
}

func TestModuleEnsureStoreInitializedIdempotent(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 0)
	first, err := m.ensureStoreInitialized(context.Background())
	require.NoError(t, err)

	// Second call must not re-create the store — assert via identity since
	// TestDB.DatabaseType() doesn't log call counts.
	second, err := m.ensureStoreInitialized(context.Background())
	require.NoError(t, err)
	assert.Same(t, first, second, "store identity must be preserved on subsequent calls")
}

func TestModuleOutboxPublisherReturnsLazyPublisher(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 0)
	pub := m.OutboxPublisher()
	require.NotNil(t, pub)
	assert.IsType(t, &lazyPublisher{}, pub)
}

func TestModuleShutdownReturnsNil(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 0)
	assert.NoError(t, m.Shutdown())
}

func TestModuleRegisterJobsNoOpWhenDisabled(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{Outbox: config.OutboxConfig{Enabled: false}},
	}
	require.NoError(t, m.Init(deps))

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	assert.Empty(t, reg.FixedRateCalls, "no relay job when outbox disabled")
	assert.Empty(t, reg.DailyAtCalls, "no cleanup job when outbox disabled")
}

func TestModuleRegisterJobsRegistersRelayAndCleanup(t *testing.T) {
	// applyDefaults() forces RetentionPeriod to 72h when zero, so cleanup
	// is always registered alongside the relay. Pass an explicit retention
	// to keep the test intent clear.
	m, _ := initEnabledModule(t, "postgresql", 24*time.Hour)

	reg := &fakeRegistrar{}
	require.NoError(t, m.RegisterJobs(reg))
	require.Len(t, reg.FixedRateCalls, 1, "relay registered exactly once")
	assert.Equal(t, "outbox-relay", reg.FixedRateCalls[0].JobID)
	require.Len(t, reg.DailyAtCalls, 1, "cleanup registered when retention > 0")
	assert.Equal(t, "outbox-cleanup", reg.DailyAtCalls[0].JobID)
	// Default cleanup time is 04:00 local.
	assert.Equal(t, 4, reg.DailyAtCalls[0].LocalTime.Hour())
}

func TestModuleRegisterJobsPropagatesRelayError(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 24*time.Hour)

	reg := &fakeRegistrar{FixedRateErr: errors.New("scheduler unavailable")}
	err := m.RegisterJobs(reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to register relay job")
	assert.Empty(t, reg.DailyAtCalls, "cleanup not attempted after relay registration failure")
}

func TestModuleRegisterJobsPropagatesCleanupError(t *testing.T) {
	m, _ := initEnabledModule(t, "postgresql", 24*time.Hour)

	reg := &fakeRegistrar{DailyAtErr: errors.New("scheduler unavailable")}
	err := m.RegisterJobs(reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to register cleanup job")
	assert.Len(t, reg.FixedRateCalls, 1, "relay registered before cleanup failure")
}

func TestLazyPublisherPublishLazilyInitializesStore(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	_, ok := m.stores.Cached("")
	require.False(t, ok, "store is nil before first Publish")

	// Wire the tx's INSERT expectation matching postgresStore.Insert.
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_outbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(context.Background())
	require.NoError(t, err)

	event := &app.OutboxEvent{
		EventType:   "test.event",
		AggregateID: "agg-1",
		Payload:     []byte(`{"k":"v"}`),
		Exchange:    "ex",
	}
	id, err := m.OutboxPublisher().Publish(context.Background(), tx, event)
	require.NoError(t, err)
	assert.NotEmpty(t, id, "Publish returns a generated event ID")
	_, ok = m.stores.Cached("")
	assert.True(t, ok, "store initialized lazily on first Publish")
}

func TestLazyStoreDelegatesAllMethodsAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	// Each lazyStore method must initialize the store on first call then
	// delegate to the concrete implementation. The exact SQL for the
	// concrete store is tested elsewhere — here we just need the lazy
	// init path to fire. Match the postgresStore DELETE SQL.
	db.ExpectExec(`DELETE FROM gobricks_outbox`).WillReturnRowsAffected(0)

	_, err := ls.DeletePublished(context.Background(), db, time.Now())
	require.NoError(t, err)
	_, ok := m.stores.Cached("")
	assert.True(t, ok, "lazyStore.DeletePublished triggered lazy init")
}

func TestLazyStoreInsertDelegatesAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_outbox`).
		WillReturnRowsAffected(1)
	tx, err := db.Begin(context.Background())
	require.NoError(t, err)

	rec := &Record{ID: "evt", EventType: "x", Payload: []byte("{}"), Exchange: "ex", RoutingKey: "rk"}
	require.NoError(t, ls.Insert(context.Background(), tx, rec))
	_, ok := m.stores.Cached("")
	assert.True(t, ok)
}

func TestLazyStoreMarkPublishedDelegatesAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	db.ExpectExec(`UPDATE gobricks_outbox SET status`).WillReturnRowsAffected(1)
	require.NoError(t, ls.MarkPublished(context.Background(), db, "evt-id"))
	_, ok := m.stores.Cached("")
	assert.True(t, ok)
}

func TestLazyStoreMarkFailedDelegatesAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	db.ExpectExec(`UPDATE gobricks_outbox SET retry_count`).WillReturnRowsAffected(1)
	require.NoError(t, ls.MarkFailed(context.Background(), db, "evt-id", "boom"))
	_, ok := m.stores.Cached("")
	assert.True(t, ok)
}

func TestLazyStoreFetchPendingDelegatesAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	// postgresStore.FetchPending issues a SELECT; we just need the lazy
	// init path to fire. Returning a closed rows iterator is sufficient.
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers", "exchange",
		"routing_key", "status", "retry_count", "created_at",
	))

	_, err := ls.FetchPending(context.Background(), db, 10)
	require.NoError(t, err)
	_, ok := m.stores.Cached("")
	assert.True(t, ok, "lazyStore.FetchPending triggered lazy init")
}

func TestLazyStoreCreateTableDelegatesAfterInit(t *testing.T) {
	m, db := initEnabledModule(t, "postgresql", 0)
	ls := &lazyStore{module: m}

	// postgresStore.CreateTable issues three sequential Exec calls (table
	// then two indexes). Distinct patterns prevent first-match-wins
	// ambiguity between the two `CREATE INDEX` statements.
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)

	require.NoError(t, ls.CreateTable(context.Background(), db))
	_, ok := m.stores.Cached("")
	assert.True(t, ok, "lazyStore.CreateTable triggered lazy init")
}

func TestOutboxStorePerTenant(t *testing.T) {
	tenants := dbtesting.NewTenantDBMap()
	dbA := tenants.ForTenant("tenant-a")
	dbA.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	dbA.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	dbA.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)

	dbB := tenants.ForTenant("tenant-b")
	dbB.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	dbB.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	dbB.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)

	m := &Module{
		logger: logger.New("info", false),
		cfg:    config.OutboxConfig{Enabled: true, TableName: "gobricks_outbox", AutoCreateTable: true},
		getDB:  tenants.AsGetDBFunc(),
	}

	storeA, err := m.ensureStoreInitialized(multitenant.SetTenant(context.Background(), "tenant-a"))
	require.NoError(t, err)
	storeB, err := m.ensureStoreInitialized(multitenant.SetTenant(context.Background(), "tenant-b"))
	require.NoError(t, err)
	storeAAgain, err := m.ensureStoreInitialized(multitenant.SetTenant(context.Background(), "tenant-a"))
	require.NoError(t, err)

	assert.Same(t, storeA, storeAAgain, "a tenant reuses its cached store")
	assert.NotSame(t, storeA, storeB, "different tenants receive isolated stores")

	dbtesting.AssertExecExecuted(t, dbA, "CREATE TABLE")
	dbtesting.AssertExecExecuted(t, dbB, "CREATE TABLE")
}

func TestLazyPublisherUsesCallersTenantStore(t *testing.T) {
	tenants := dbtesting.NewTenantDBMap()
	dbA := tenants.ForTenantWithVendor("tenant-a", dbtypes.PostgreSQL)
	dbA.ExpectTransaction().ExpectExec(`VALUES ($1, $2`).WillReturnRowsAffected(1)

	dbB := tenants.ForTenantWithVendor("tenant-b", dbtypes.Oracle)
	dbB.ExpectTransaction().ExpectExec(`VALUES (:1, :2`).WillReturnRowsAffected(1)

	m := &Module{
		logger: logger.New("info", false),
		cfg:    config.OutboxConfig{Enabled: true, TableName: "gobricks_outbox", DefaultExchange: "ex"},
		getDB:  tenants.AsGetDBFunc(),
	}
	pub := &lazyPublisher{module: m}
	event := &app.OutboxEvent{EventType: "test.event", AggregateID: "agg-1", Payload: []byte(`{}`), Exchange: "ex"}

	ctxA := multitenant.SetTenant(context.Background(), "tenant-a")
	txA, err := dbA.Begin(ctxA)
	require.NoError(t, err)
	_, err = pub.Publish(ctxA, txA, event)
	require.NoError(t, err)

	ctxB := multitenant.SetTenant(context.Background(), "tenant-b")
	txB, err := dbB.Begin(ctxB)
	require.NoError(t, err)
	// Tenant B is Oracle (:N placeholders). Before the fix, lazyPublisher cached
	// the first Publish's store behind a sync.Once — this second call would still
	// emit tenant A's Postgres $N-placeholder SQL and fail txB's :N-only expectation.
	_, err = pub.Publish(ctxB, txB, event)
	require.NoError(t, err, "tenant B's publish must use tenant B's (Oracle) store, not a cached tenant-A store")
}
