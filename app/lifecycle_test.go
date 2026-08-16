package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	testApp = "test-app"
)

func TestShutdownTimeouts(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wantInner time.Duration
		wantOuter time.Duration
	}{
		{
			name:      "nil_cfg_uses_documented_defaults",
			cfg:       nil,
			wantInner: 10 * time.Second,
			wantOuter: 15 * time.Second,
		},
		{
			name:      "positive_config_overrides_default",
			cfg:       &config.Config{Server: config.ServerConfig{Timeout: config.TimeoutConfig{Shutdown: 30 * time.Second}}},
			wantInner: 30 * time.Second,
			wantOuter: 35 * time.Second,
		},
		{
			name:      "small_positive_config_keeps_5s_headroom",
			cfg:       &config.Config{Server: config.ServerConfig{Timeout: config.TimeoutConfig{Shutdown: 1 * time.Second}}},
			wantInner: 1 * time.Second,
			wantOuter: 6 * time.Second,
		},
		{
			name:      "zero_config_falls_back_to_default",
			cfg:       &config.Config{Server: config.ServerConfig{Timeout: config.TimeoutConfig{Shutdown: 0}}},
			wantInner: 10 * time.Second,
			wantOuter: 15 * time.Second,
		},
		{
			name:      "negative_config_falls_back_to_default",
			cfg:       &config.Config{Server: config.ServerConfig{Timeout: config.TimeoutConfig{Shutdown: -5 * time.Second}}},
			wantInner: 10 * time.Second,
			wantOuter: 15 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: tc.cfg}
			gotInner, gotOuter := app.shutdownTimeouts()
			assert.Equal(t, tc.wantInner, gotInner, "inner timeout")
			assert.Equal(t, tc.wantOuter, gotOuter, "outer timeout")
			assert.Equal(t, gotInner+5*time.Second, gotOuter, "outer must equal inner + 5s headroom")
		})
	}
}

// recordingModule runs a hook when its Shutdown is invoked, letting tests observe what
// other shutdown phases have already run at module-teardown time.
type recordingModule struct {
	onShutdown func()
}

func (recordingModule) Name() string           { return "recording" }
func (recordingModule) Init(*ModuleDeps) error { return nil }
func (m recordingModule) Shutdown() error {
	if m.onShutdown != nil {
		m.onShutdown()
	}
	return nil
}

func TestShutdownStopsServerBeforeModules(t *testing.T) {
	fixture := newTestAppFixture(t)
	fixture.messaging.ExpectClose(nil)
	fixture.db.On(methodClose).Return(nil)

	serverShutdownsAtModuleTeardown := -1
	require.NoError(t, fixture.app.registry.Register(&recordingModule{onShutdown: func() {
		serverShutdownsAtModuleTeardown = fixture.server.shutdownCount()
	}}))

	require.NoError(t, fixture.app.Shutdown(context.Background()))

	// The HTTP server must be shut down before modules are torn down: http.Server.Shutdown
	// synchronously drains in-flight HTTP handlers, so they finish against live modules
	// rather than racing module teardown.
	assert.Equal(t, 1, serverShutdownsAtModuleTeardown,
		"server must shut down before modules")
}

func TestShutdownTiming(t *testing.T) {
	// Test that the shutdown process completes within reasonable time
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    testApp,
			Env:     "test",
			Version: "1.0.0",
		},
	}

	testLogger := logger.New("info", false)

	deps := &ModuleDeps{
		Logger: testLogger,
		Config: cfg,
	}

	app := &App{
		cfg:          cfg,
		logger:       testLogger,
		registry:     NewModuleRegistry(deps),
		closers:      []namedCloser{},
		healthProbes: []Prober{},
	}

	// Test that shutdown completes in reasonable time
	start := time.Now()
	err := app.Shutdown(context.TODO())
	duration := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, duration, 1*time.Second, "Shutdown should complete quickly with no components")

	t.Logf("Shutdown completed in %v", duration)
}

// TestPrepareRuntimeWithScheduler verifies that RegisterJobs is called during prepareRuntime
func TestPrepareRuntimeWithScheduler(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    testApp,
			Env:     "test",
			Version: "1.0.0",
		},
		Server: config.ServerConfig{
			Port: 8080,
		},
		Debug: config.DebugConfig{
			Enabled: false,
		},
		Multitenant: config.MultitenantConfig{
			Enabled: false,
		},
	}

	testLogger := logger.New("info", false)

	deps := &ModuleDeps{
		Logger: testLogger,
		Config: cfg,
	}

	registry := NewModuleRegistry(deps)

	// Register scheduler module
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)
	err := registry.Register(scheduler)
	assert.NoError(t, err)

	// Register JobProvider module
	jobProvider := &MockJobProviderModule{name: "job-provider"}
	jobProvider.On("Init", deps).Return(nil)
	jobProvider.On("RegisterJobs", scheduler).Return(nil)
	err = registry.Register(jobProvider)
	assert.NoError(t, err)

	// Create minimal app with mocked server
	mockSrv := newMockServer()

	app := &App{
		cfg:      cfg,
		logger:   testLogger,
		registry: registry,
		server:   mockSrv,
		closers:  []namedCloser{},
	}

	// Call prepareRuntime
	err = app.prepareRuntime(context.Background())
	assert.NoError(t, err)

	// Verify RegisterJobs was called on the provider
	jobProvider.AssertExpectations(t)
	scheduler.AssertExpectations(t)
}

// publisherDeclaringModule registers a single publisher during DeclareMessaging.
// Used to drive the structural fail-fast check in prepareRuntime — see issue #366.
type publisherDeclaringModule struct{}

func (publisherDeclaringModule) Name() string             { return "publisher-declaring-module" }
func (publisherDeclaringModule) Init(_ *ModuleDeps) error { return nil }
func (publisherDeclaringModule) Shutdown() error          { return nil }
func (publisherDeclaringModule) DeclareMessaging(decls *messaging.Declarations) {
	decls.RegisterExchange(&messaging.ExchangeDeclaration{
		Name:    "test.exchange",
		Type:    "topic",
		Durable: true,
	})
	decls.RegisterPublisher(&messaging.PublisherDeclaration{
		Exchange:   "test.exchange",
		RoutingKey: "test.routing",
		EventType:  "test.event",
	})
}

// newLifecycleCheckApp builds a minimal App suitable for exercising prepareRuntime's
// fail-fast check without standing up the full bootstrap pipeline.
func newLifecycleCheckApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	return newLifecycleCheckAppWithLogger(t, cfg, logger.New("info", false))
}

// newLifecycleCheckAppWithLogger is newLifecycleCheckApp with the logger supplied,
// so tests can assert on what prepareRuntime itself emits.
func newLifecycleCheckAppWithLogger(t *testing.T, cfg *config.Config, log logger.Logger) *App {
	t.Helper()
	deps := &ModuleDeps{Logger: log, Config: cfg}
	return &App{
		cfg:      cfg,
		logger:   log,
		registry: NewModuleRegistry(deps),
		server:   newMockServer(),
		closers:  []namedCloser{},
	}
}

// TestPrepareRuntimeFailsWhenDeclarationsExistAndMessagingUnconfigured guards
// the structural half of issue #366: declarations from MessagingDeclarer modules
// must not be silently dropped when messaging is unset.
func TestPrepareRuntimeFailsWhenDeclarationsExistAndMessagingUnconfigured(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		// Messaging.Broker.URL intentionally empty.
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	err := app.prepareRuntime(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging is not configured")
	assert.Contains(t, err.Error(), "publishers=1")
}

// TestPrepareRuntimeAllowsEmptyDeclarationsWithMessagingUnconfigured verifies
// that the check does not fire when no module declared messaging — startup
// without messaging configured remains valid for apps that don't use AMQP.
func TestPrepareRuntimeAllowsEmptyDeclarationsWithMessagingUnconfigured(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		// Messaging.Broker.URL intentionally empty; no MessagingDeclarer module registered.
	}
	app := newLifecycleCheckApp(t, cfg)

	require.NoError(t, app.prepareRuntime(context.Background()))
}

type preWarmCtxSentinelKey struct{}

// ctxRecordingDBConfigProvider captures the sentinel carried by the context that
// reaches DBConfig — the first ctx-aware seam below PreWarmSingleTenant.
type ctxRecordingDBConfigProvider struct {
	mu   sync.Mutex
	seen any
}

func (p *ctxRecordingDBConfigProvider) DBConfig(ctx context.Context, _ string) (*config.DatabaseConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = ctx.Value(preWarmCtxSentinelKey{})
	return &config.DatabaseConfig{Type: dbTypePostgres}, nil
}

// TestPrepareRuntimePropagatesContextToPreWarm pins that prepareRuntime hands its
// own ctx to PreWarmSingleTenant instead of a fresh context.Background(): a
// sentinel value on the caller's context must survive down to the pre-warm
// database seam. The value is the right observable because resourcepool detaches
// cancellation with context.WithoutCancel, which preserves values — so a deadline
// or cancellation would not survive that hop, but a value does.
func TestPrepareRuntimePropagatesContextToPreWarm(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
	}
	a := newLifecycleCheckApp(t, cfg)

	provider := &ctxRecordingDBConfigProvider{}
	connector := func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
		return dbtesting.NewTestDB(dbTypePostgres), nil
	}
	dbManager := database.NewDbManager(provider, a.logger,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, connector)
	defer func() { _ = dbManager.Close() }()

	a.dbManager = dbManager
	a.connectionPreWarmer = NewConnectionPreWarmer(a.logger, dbManager, nil)

	ctx := context.WithValue(context.Background(), preWarmCtxSentinelKey{}, "from-prepare-runtime")
	require.NoError(t, a.prepareRuntime(ctx))

	provider.mu.Lock()
	defer provider.mu.Unlock()
	assert.Equal(t, "from-prepare-runtime", provider.seen,
		"prepareRuntime must pass its own context to PreWarmSingleTenant; context.Background() drops the sentinel")
}

const (
	preWarmWarnMsg = "Pre-warming completed with warnings"
	// preWarmFailureMarker travels from the failing config resolution into the WARN's
	// attached error, so the emission is attributable to pre-warming and nothing else.
	preWarmFailureMarker = "prewarm-config-resolution-refused"
)

// staticDBConfigProvider resolves a usable single-tenant config, or fails with err
// when set — the two outcomes that make PreWarmSingleTenant succeed or return an error.
type staticDBConfigProvider struct{ err error }

func (p staticDBConfigProvider) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &config.DatabaseConfig{Type: dbTypePostgres}, nil
}

// TestPrepareRuntimeWarnsOnlyWhenPreWarmFails pins both directions of prepareRuntime's
// pre-warm error check: a failed pre-warm must surface as a WARN carrying the cause,
// and a clean one must stay silent. Startup succeeds either way — pre-warming is
// advisory, never fatal.
func TestPrepareRuntimeWarnsOnlyWhenPreWarmFails(t *testing.T) {
	tests := []struct {
		configErr error
		name      string
		wantWarn  bool
	}{
		{name: "failed_prewarm_warns", configErr: errors.New(preWarmFailureMarker), wantWarn: true},
		{name: "successful_prewarm_stays_silent", configErr: nil, wantWarn: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
				Multitenant: config.MultitenantConfig{Enabled: false},
			}
			rec := &recLogger{}
			a := newLifecycleCheckAppWithLogger(t, cfg, rec)

			connector := func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
				return dbtesting.NewTestDB(dbTypePostgres), nil
			}
			dbManager := database.NewDbManager(staticDBConfigProvider{err: tt.configErr}, rec,
				database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, connector)
			t.Cleanup(func() { _ = dbManager.Close() })

			a.dbManager = dbManager
			a.connectionPreWarmer = NewConnectionPreWarmer(rec, dbManager, nil)

			require.NoError(t, a.prepareRuntime(context.Background()),
				"pre-warming trouble is advisory: startup completes either way")

			event, emitted := loggedEvent(rec, preWarmWarnMsg)
			require.Equal(t, tt.wantWarn, emitted,
				"the pre-warm WARN must track whether pre-warming actually failed")
			if tt.wantWarn {
				assert.Equal(t, "warn", event.level)
				assert.Contains(t, event.err, preWarmFailureMarker,
					"the WARN must carry the pre-warm error, not just announce one")
			}
		})
	}
}

// TestPrepareRuntimeSkipsCheckInMultiTenantMode verifies that the static check
// is skipped when multitenant.enabled=true — each tenant supplies its own
// broker URL via the resource source, so a global check would yield false
// positives.
func TestPrepareRuntimeSkipsCheckInMultiTenantMode(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: true},
		// Messaging.Broker.URL intentionally empty.
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	require.NoError(t, app.prepareRuntime(context.Background()))
}

// TestPrepareRuntimeSucceedsWithDeclarationsAndConfiguredMessaging is the
// regression guard for the happy path: messaging configured, declarations
// present, prepareRuntime proceeds.
func TestPrepareRuntimeSucceedsWithDeclarationsAndConfiguredMessaging(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://localhost"},
		},
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	require.NoError(t, app.prepareRuntime(context.Background()))
}

// debugCheckConfig builds a config whose Debug block enables one endpoint, leaving the
// access-control keys to the caller. Assembled in Go, so it never receives the koanf
// loopback default for allowedips — the state ADR-049 refuses is reachable here by omission.
func debugCheckConfig(allowedIPs []string, bearerToken string) *config.Config {
	return &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		Debug: config.DebugConfig{
			Enabled:     true,
			PathPrefix:  "/_sys",
			AllowedIPs:  allowedIPs,
			BearerToken: bearerToken,
			Endpoints:   config.DebugEndpointsConfig{Health: true},
		},
	}
}

// TestPrepareRuntimeFailsWhenDebugEndpointsHaveNoAccessControl is the guard for ADR-049's
// central claim — that the refusal is *fatal at startup*, not merely an error returned from
// RegisterDebugEndpoints. Every other test calls that method directly; this one drives the
// path the framework actually takes, so a future refactor that drops the error on the floor
// in registerDebugHandlers or prepareRuntime fails here.
func TestPrepareRuntimeFailsWhenDebugEndpointsHaveNoAccessControl(t *testing.T) {
	tests := []struct {
		name        string
		bearerToken string
	}{
		{name: "no_token", bearerToken: ""},
		// A whitespace-only token is not a credential (`Bearer  ` matches it), so it must
		// reach the same abort rather than registering the group behind a guessable one.
		{name: "whitespace_only_token", bearerToken: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newLifecycleCheckApp(t, debugCheckConfig(nil, tt.bearerToken))

			err := app.prepareRuntime(context.Background())

			require.Error(t, err)
			// prepareRuntime passes its callees' errors through untouched, so the error
			// reaching startup must be self-describing on its own: what would have been
			// exposed, at which prefix, and both keys that resolve it.
			assert.Contains(t, err.Error(), "would expose enhanced health at /_sys")
			assert.Contains(t, err.Error(), "debug.allowedips")
			assert.Contains(t, err.Error(), "debug.bearertoken")
		})
	}
}

// TestPrepareRuntimeAllowsDebugEndpointsWithAccessControl is the negative half: startup must
// still proceed once either access-control key is set, so the abort cannot be over-eager.
func TestPrepareRuntimeAllowsDebugEndpointsWithAccessControl(t *testing.T) {
	tests := []struct {
		name        string
		allowedIPs  []string
		bearerToken string
	}{
		{name: "allowlist_set", allowedIPs: []string{localhostIPV4}},
		{name: "token_set", bearerToken: "s3cret-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newLifecycleCheckApp(t, debugCheckConfig(tt.allowedIPs, tt.bearerToken))

			require.NoError(t, app.prepareRuntime(context.Background()))
		})
	}
}

// TestPrepareRuntimeSucceedsWithNoMessagingConfigured is the regression guard for
// the population that has nothing to do with messaging at all. Consumer bootstrap
// is attempted unconditionally — the messaging manager is built whether or not a
// broker is configured, and the declaration set is non-nil even when empty — so
// `EnsureConsumers` fails here with a not-configured error on every messaging-free
// service. Grading that failure as fatal would stop all of them from booting.
// Built through the public constructor because the failure only appears once the
// real manager and declaration wiring are in place.
func TestPrepareRuntimeSucceedsWithNoMessagingConfigured(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Server: config.ServerConfig{
			Port: 8080,
			// The validated timeout floor lives in one fixture; reuse it.
			Timeout: defaultTestConfig().Server.Timeout,
		},
		Multitenant: config.MultitenantConfig{Enabled: false},
		Log:         config.LogConfig{Level: "info"},
		// No Messaging and no Database block at all.
	}

	a, _, err := NewWithConfig(cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if a.messagingManager != nil {
			a.messagingManager.StopCleanup()
		}
		if a.dbManager != nil {
			a.dbManager.StopCleanup()
		}
	})

	require.True(t, a.messagingInitializer.IsAvailable(),
		"a messaging manager is built even with no broker configured — the premise of this guard")

	require.NoError(t, a.prepareRuntime(context.Background()))
}

// globalMWCapturingServer implements ServerRunner (via embedded mockServer) plus the
// optional RegisterGlobalMiddleware capability, capturing what it receives.
type globalMWCapturingServer struct {
	*mockServer
	received []server.MiddlewareFunc
}

func (s *globalMWCapturingServer) RegisterGlobalMiddleware(mw ...server.MiddlewareFunc) {
	s.received = append(s.received, mw...)
}

// TestApplyGlobalMiddlewareRegistersOnSupportingServer verifies collected middleware is
// handed to a server that supports registration.
func TestApplyGlobalMiddlewareRegistersOnSupportingServer(t *testing.T) {
	log := logger.New("debug", true)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	require.NoError(t, registry.Register(&globalMWModule{name: "auth", mws: []server.MiddlewareFunc{
		func(_ server.HandlerContext, next func() error) error { return next() },
	}}))

	srv := &globalMWCapturingServer{mockServer: &mockServer{}}
	app := &App{server: srv, registry: registry, logger: log}

	require.NoError(t, app.applyGlobalMiddleware())
	assert.Len(t, srv.received, 1, "supporting server must receive the collected middleware")
}

// TestApplyGlobalMiddlewareFailsClosedOnUnsupportingServer verifies startup aborts (rather
// than silently dropping a security gate) when the server cannot install global middleware.
func TestApplyGlobalMiddlewareFailsClosedOnUnsupportingServer(t *testing.T) {
	log := logger.New("debug", true)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	require.NoError(t, registry.Register(&globalMWModule{name: "auth", mws: []server.MiddlewareFunc{
		func(_ server.HandlerContext, next func() error) error { return next() },
	}}))

	app := &App{server: &mockServer{}, registry: registry, logger: log}

	err := app.applyGlobalMiddleware()
	require.Error(t, err, "startup must fail closed when the server cannot install global middleware")
	assert.Contains(t, err.Error(), "global middleware")
}

// TestApplyGlobalMiddlewareNoopWhenNoModulesContribute verifies an unsupporting server is
// fine as long as no module actually contributes middleware.
func TestApplyGlobalMiddlewareNoopWhenNoModulesContribute(t *testing.T) {
	log := logger.New("debug", true)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	require.NoError(t, registry.Register(&minimalModule{name: "minimal"}))

	app := &App{server: &mockServer{}, registry: registry, logger: log}
	require.NoError(t, app.applyGlobalMiddleware(), "no contributing modules → no error even on an unsupporting server")
}

// TestStartMaintenanceLoopsUsesConfiguredPublisherCleanupInterval proves
// startMaintenanceLoops passes the operator-configured
// messaging.publisher.cleanupinterval through to Manager.StartCleanup instead
// of the old hardcoded 2m literal. A 2m default interval would never fire
// within this test's window; observing the idle publisher actually get swept
// proves the short configured interval was used.
func TestStartMaintenanceLoopsUsesConfiguredPublisherCleanupInterval(t *testing.T) {
	log := logger.New("error", false)

	client := testmocks.NewMockAMQPClient()
	client.ExpectClose(nil)
	factory := func(string, logger.Logger) messaging.AMQPClient { return client }
	manager := messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond}, factory)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(context.Background(), testKey)
	require.NoError(t, err)
	rel()

	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Publisher: config.PublisherPoolConfig{CleanupInterval: 20 * time.Millisecond},
		},
	}
	a := &App{cfg: cfg, logger: log, messagingManager: manager}
	a.startMaintenanceLoops()
	defer a.messagingManager.StopCleanup()

	assert.Eventually(t, func() bool {
		return manager.Stats()["active_publishers"] == 0
	}, time.Second, 10*time.Millisecond)
}

// TestStartMaintenanceLoopsUsesConfiguredDatabaseCleanupInterval proves the
// configured 20ms interval reached DbManager.StartCleanup: the 5m hardcoded
// default would never fire in this test's 1s window, so a swept idle connection
// is the proof.
func TestStartMaintenanceLoopsUsesConfiguredDatabaseCleanupInterval(t *testing.T) {
	log := logger.New("error", false)

	connector := func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
		return dbtesting.NewTestDB("postgresql"), nil
	}
	dbManager := database.NewDbManager(&stubTenantResource{}, log, database.DbManagerOptions{MaxSize: 5, IdleTTL: 10 * time.Millisecond}, connector)
	defer func() { _ = dbManager.Close() }()

	_, rel, err := dbManager.Get(context.Background(), testKey)
	require.NoError(t, err)
	rel()

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Manager: config.DatabaseManagerConfig{CleanupInterval: 20 * time.Millisecond},
		},
	}
	a := &App{cfg: cfg, logger: log, dbManager: dbManager}
	a.startMaintenanceLoops()
	defer a.dbManager.StopCleanup()

	assert.Eventually(t, func() bool {
		return dbManager.Stats()["active_connections"] == 0
	}, time.Second, 10*time.Millisecond)
}

func TestCleanupIntervalTooLate(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		want            bool
	}{
		{name: "cleanup_greater_than_idle_warns", cleanupInterval: 15 * time.Minute, idleTTL: 10 * time.Minute, want: true},
		{name: "cleanup_equals_idle_warns", cleanupInterval: 10 * time.Minute, idleTTL: 10 * time.Minute, want: true},
		{name: "cleanup_below_idle_ok", cleanupInterval: 2 * time.Minute, idleTTL: 1 * time.Hour, want: false},
		{name: "zero_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: 0, want: false},
		{name: "zero_cleanup_below_positive_idle_ok", cleanupInterval: 0, idleTTL: 1 * time.Hour, want: false},
		{name: "negative_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: -1 * time.Second, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cleanupIntervalTooLate(tc.cleanupInterval, tc.idleTTL))
		})
	}
}

// TestStartMaintenanceLoopsWarnsOnLateCleanupInterval exercises the WARN path
// end-to-end through startMaintenanceLoops: a misconfigured cleanupinterval >=
// idlettl is advisory only, so the cleanup loop must still start and function
// normally (an idle publisher is still eventually swept) without panicking.
func TestStartMaintenanceLoopsWarnsOnLateCleanupInterval(t *testing.T) {
	log := logger.New("error", false)

	client := testmocks.NewMockAMQPClient()
	client.ExpectClose(nil)
	factory := func(string, logger.Logger) messaging.AMQPClient { return client }
	manager := messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond}, factory)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(context.Background(), testKey)
	require.NoError(t, err)
	rel()

	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Publisher: config.PublisherPoolConfig{
				// Misconfigured on purpose: cleanupinterval >= idlettl should WARN, not fail.
				CleanupInterval: 20 * time.Millisecond,
				IdleTTL:         10 * time.Millisecond,
			},
		},
	}
	a := &App{cfg: cfg, logger: log, messagingManager: manager}

	require.NotPanics(t, func() { a.startMaintenanceLoops() })
	defer a.messagingManager.StopCleanup()

	assert.Eventually(t, func() bool {
		return manager.Stats()["active_publishers"] == 0
	}, time.Second, 10*time.Millisecond, "cleanup loop must still run despite the late-cleanupinterval WARN")
}

// TestCheckRouteConflictsAggregatesAndSkips drives checkRouteConflicts directly against a
// real server.Server (white-box field access — the mock server used elsewhere in this file
// has no-op registrars and never implements RouteConflicts(), so positive cases can only be
// exercised against a real server), covering report, aggregate, and skip-path behaviors.
func TestCheckRouteConflictsAggregatesAndSkips(t *testing.T) {
	log := logger.New("error", false)
	noop := func(c server.HandlerContext) error { return c.String(http.StatusOK, "") }

	tests := []struct {
		name         string
		buildApp     func() *App
		wantErr      bool
		wantContains []string
	}{
		{
			name: "one_conflict",
			buildApp: func() *App {
				srv := server.New(&config.Config{}, log)
				mg := srv.ModuleGroup()
				mg.Add(http.MethodGet, "/dup", noop)
				mg.Add(http.MethodGet, "/dup", noop)
				return &App{server: srv}
			},
			wantErr:      true,
			wantContains: []string{"duplicate route registration (1 conflict(s))", "GET /dup"},
		},
		{
			name: "two_conflicts",
			buildApp: func() *App {
				srv := server.New(&config.Config{}, log)
				mg := srv.ModuleGroup()
				mg.Add(http.MethodGet, "/one", noop)
				mg.Add(http.MethodGet, "/one", noop)
				mg.Add(http.MethodPost, "/two", noop)
				mg.Add(http.MethodPost, "/two", noop)
				return &App{server: srv}
			},
			wantErr:      true,
			wantContains: []string{"(2 conflict(s))", "GET /one", "POST /two"},
		},
		{
			name: "no_conflicts",
			buildApp: func() *App {
				srv := server.New(&config.Config{}, log)
				mg := srv.ModuleGroup()
				mg.Add(http.MethodGet, "/one", noop)
				mg.Add(http.MethodPost, "/two", noop)
				return &App{server: srv}
			},
			wantErr: false,
		},
		{
			name: "fake_server_skipped",
			buildApp: func() *App {
				return &App{server: newMockServer()}
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.buildApp()
			err := a.checkRouteConflicts()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantContains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestReadyCheckDowngradesCallerCancellationLog pins that a probe failure caused by the
// caller abandoning its own request cannot mint ERROR lines: /ready is unauthenticated and
// exempt from rate limiting, and wiki/cache.md tells operators to alert on that line. The
// discriminator is the caller's own request context, not the probe error: a context.Canceled
// raised while the caller is still waiting is an internal fault and must stay ERROR.
func TestReadyCheckDowngradesCallerCancellationLog(t *testing.T) {
	tests := []struct {
		name         string
		probeErr     error
		cancelCaller bool
		wantLevel    string
	}{
		// The caller aborted its own GET, so its context is done before the probe reports.
		{name: "caller_cancellation_is_warn", probeErr: context.Canceled, cancelCaller: true, wantLevel: "warn"},
		// Same wrapped error, but the caller is still waiting: nobody walked away, so the
		// cancellation came from inside the service and is a real incident.
		{name: "internal_cancellation_stays_error", probeErr: fmt.Errorf("dial: %w", context.Canceled), wantLevel: "error"},
		{name: "real_outage_stays_error", probeErr: errors.New(errorRedisDown), wantLevel: "error"},
		// A probe timeout is the service's own budget expiring, not the caller walking away.
		{name: "probe_timeout_stays_error", probeErr: fmt.Errorf("ping: %w", context.DeadlineExceeded), wantLevel: "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{App: config.AppConfig{Name: testApp}}
			rec := &recLogger{}
			app := &App{cfg: cfg, logger: rec, cacheManager: createTestCacheManagerWithGetError(t, tc.probeErr)}
			app.healthProbes = app.createHealthProbes()
			require.Len(t, app.healthProbes, 1)

			reqCtx := context.Background()
			if tc.cancelCaller {
				canceled, cancel := context.WithCancel(reqCtx)
				cancel()
				reqCtx = canceled
			}
			req := httptest.NewRequestWithContext(reqCtx, http.MethodGet, readyEndpoint, http.NoBody)
			w := httptest.NewRecorder()
			require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(w, req, cfg)))

			assert.Equal(t, http.StatusServiceUnavailable, w.Code, "the log level must not change the response")
			event, ok := loggedEvent(rec, "Readiness check failed")
			require.True(t, ok)
			assert.Equal(t, tc.wantLevel, event.level)
			assert.Contains(t, event.err, tc.probeErr.Error(), "the full error must reach the log at either level")
		})
	}
}

// TestPublicProbeError pins both branches of the /ready sanitization switch. A negated
// condition here would either leak every probe's raw error or force the default onto a
// probe that declared its own wording, and neither shows up as a compile or type failure.
func TestPublicProbeError(t *testing.T) {
	t.Run("public_error_set_overrides_the_default", func(t *testing.T) {
		result := HealthStatus{
			Name:      componentDatabase,
			PublicErr: "database temporarily unavailable",
			Err:       errors.New(pgconnIdentityError),
		}

		assert.Equal(t, "database temporarily unavailable", publicProbeError(&result))
	})

	t.Run("public_error_empty_synthesizes_a_safe_default", func(t *testing.T) {
		result := HealthStatus{Name: componentDatabase, Err: errors.New(pgconnIdentityError)}

		assert.Equal(t, databaseUnavailableBody, publicProbeError(&result))
	})

	t.Run("nil_error_renders_without_panicking", func(t *testing.T) {
		result := HealthStatus{Name: componentCache}

		assert.Equal(t, cacheUnavailableBody, publicProbeError(&result))
	})
}

// TestReadyCheckWithholdsDatabaseIdentityFromBody is the end-to-end assertion for #879:
// /ready is unauthenticated and carries no CIDR gate, so a database outage must not turn
// its 503 body into a connection-identity oracle. The detail keeps living in the app log
// and, through HealthStatus.Err, in the IP-allowlisted /_sys/health-debug.
func TestReadyCheckWithholdsDatabaseIdentityFromBody(t *testing.T) {
	t.Run("driver_identity_never_reaches_the_body", func(t *testing.T) {
		cfg := &config.Config{App: config.AppConfig{Name: testApp}}
		rec := &recLogger{}
		app := &App{cfg: cfg, logger: rec}
		app.healthProbes = []Prober{healthProbeFunc{
			name:     componentDatabase,
			critical: true,
			fn: func(context.Context) (string, map[string]any, error) {
				return unhealthyStatus, nil, errors.New(pgconnIdentityError)
			},
		}}

		body, code := runReadyCheck(t, app, cfg)

		assert.Equal(t, http.StatusServiceUnavailable, code)
		assert.Equal(t, databaseUnavailableBody, body[errorKey])

		// The premise this test rests on: the raw error really does carry the identity,
		// so the assertion above is withholding something rather than passing vacuously.
		event, ok := loggedEvent(rec, "Readiness check failed")
		require.True(t, ok)
		assert.Contains(t, event.err, "user=app database=payments")
		assert.Contains(t, event.err, "10.0.0.5:5432")
	})

	t.Run("probe_built_by_the_real_constructor_is_sanitized", func(t *testing.T) {
		// Same path, but the probe comes from databaseManagerHealthProbe rather than a
		// hand-built one, so it covers the constructor's own wiring reaching readyCheck.
		cfg := &config.Config{App: config.AppConfig{Name: testApp}}
		cfg.Database.Type = "mysql" // resolves as intended, then fails to connect
		cfg.Database.Host = "control-plane.internal"
		rec := &recLogger{}
		app := &App{cfg: cfg, logger: rec, dbManager: newRealConnectorDBManager(cfg)}
		app.healthProbes = app.createHealthProbes()
		require.Len(t, app.healthProbes, 1)

		body, code := runReadyCheck(t, app, cfg)

		assert.Equal(t, http.StatusServiceUnavailable, code)
		assert.Equal(t, databaseUnavailableBody, body[errorKey])
		event, ok := loggedEvent(rec, "Readiness check failed")
		require.True(t, ok)
		assert.NotEmpty(t, event.err)
		assert.NotEqual(t, event.err, body[errorKey], "the body must not simply echo the logged driver error")
	})
}

// TestReadyCheckSanitizesCriticalProbeWithoutPublicError is the assertion the inverted
// default exists for. SECURITY: a critical probe that never declares a public string —
// the omission the opt-in shape invited, and one no compiler catches — still cannot render
// its raw error into the unauthenticated 503 body. The probe is deliberately not one of
// the framework's own: it stands in for the probe someone adds next.
func TestReadyCheckSanitizesCriticalProbeWithoutPublicError(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp}}
	rec := &recLogger{}
	app := &App{cfg: cfg, logger: rec}
	app.healthProbes = []Prober{healthProbeFunc{
		name:     "vault",
		critical: true,
		fn: func(context.Context) (string, map[string]any, error) {
			return unhealthyStatus, nil, errors.New(pgconnIdentityError)
		},
	}}

	body, code := runReadyCheck(t, app, cfg)

	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "vault unavailable", body[errorKey])

	// The premise the assertion above rests on: the raw error really does carry the
	// identity, so it is withholding something rather than passing vacuously.
	event, ok := loggedEvent(rec, "Readiness check failed")
	require.True(t, ok)
	assert.Contains(t, event.err, "user=app database=payments")
	assert.Contains(t, event.err, "10.0.0.5:5432")
}

// TestPublicDBStatsDropsConnectionsOnACopy pins both halves of the render-site redaction:
// the /ready view loses the per-connection array, and the map it was built from keeps it.
// The copy is what keeps the redaction local to the unauthenticated body — the same map is
// what /_sys/health-debug renders as the database component's details.
func TestPublicDBStatsDropsConnectionsOnACopy(t *testing.T) {
	details := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		statusKey:            healthyStatus,
		dbConnectionsKey: []map[string]any{
			{"key": "tenant-alpha", "last_used": "2026-08-05T10:00:00Z", "idle_duration": 4},
		},
	}

	public := publicDBStats(details)

	assert.Equal(t, map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		statusKey:            healthyStatus,
	}, public, "every scalar counter must survive; only the keyed array is withheld")

	assert.Contains(t, details, dbConnectionsKey,
		"redacting in place would strip the array from the debug endpoint's view too")
}

// TestPublicDBStatsPreservesAbsentDatabaseShape guards the published body for a deployment
// with no database: db_stats {"status":"disabled"} rather than null or a missing key.
func TestPublicDBStatsPreservesAbsentDatabaseShape(t *testing.T) {
	assert.Equal(t, map[string]any{statusKey: disabledStatus},
		publicDBStats(map[string]any{statusKey: disabledStatus}))

	assert.Equal(t, map[string]any{}, publicDBStats(nil),
		"a nil details map must still render {} — never JSON null")
}

// runReadyCheck drives readyCheck over httptest and decodes the JSON body.
func runReadyCheck(t *testing.T, app *App, cfg *config.Config) (body map[string]any, code int) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyEndpoint, http.NoBody)
	w := httptest.NewRecorder()
	require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(w, req, cfg)))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body, w.Code
}

// TestReadyCheckOmitsStreamsWhenNoneDeclared pins that services without streams keep
// the /ready body they had: the component is reported only where a probe exists.
func TestReadyCheckOmitsStreamsWhenNoneDeclared(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	app := &App{cfg: cfg, logger: logger.New("error", false), healthProbes: []Prober{}}

	body, code := runReadyCheck(t, app, cfg)

	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, componentStreams)
	assert.NotContains(t, body, "streams_stats")
}

// TestReadyCheckReportsStreamsWhenProbed is the other half: once the runtime probe is
// registered the component and its stats reach the body.
func TestReadyCheckReportsStreamsWhenProbed(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	app := &App{cfg: cfg, logger: logger.New("error", false), healthProbes: []Prober{
		healthProbeFunc{
			name: componentStreams,
			fn: func(context.Context) (string, map[string]any, error) {
				return healthyStatus, map[string]any{statusKey: healthyStatus, "consumers": 2}, nil
			},
		},
	}}

	body, code := runReadyCheck(t, app, cfg)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, healthyStatus, body[componentStreams])
	assert.Equal(t, map[string]any{statusKey: healthyStatus, "consumers": float64(2)}, body["streams_stats"])
}

// streamsProbeWithOffsets returns a probe whose details carry the identifier-bearing
// stored_offsets map a real streams.Manager reports.
func streamsProbeWithOffsets(streamName, consumerName string) Prober {
	return healthProbeFunc{
		name: componentStreams,
		fn: func(context.Context) (string, map[string]any, error) {
			return healthyStatus, map[string]any{
				statusKey:            healthyStatus,
				"started":            true,
				"consumers":          1,
				streamsOffsetsKey:    map[string]int64{streamName + "/" + consumerName: 4242},
				"offset_store_count": 500,
			}, nil
		},
	}
}

// TestReadyCheckWithholdsStreamIdentifiers is the /ready half of the disclosure
// guard: the unauthenticated body must carry the counters but never the stream or
// consumer-group names, and never the live offsets that differencing two polls
// would turn into a per-stream message rate.
func TestReadyCheckWithholdsStreamIdentifiers(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	app := &App{cfg: cfg, logger: logger.New("error", false), healthProbes: []Prober{
		streamsProbeWithOffsets("payments-ledger", "fraud-scoring"),
	}}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyEndpoint, http.NoBody)
	w := httptest.NewRecorder()
	require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(w, req, cfg)))

	body := w.Body.String()
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, body, "payments-ledger", "the declared stream name must not reach an unauthenticated body")
	assert.NotContains(t, body, "fraud-scoring", "the consumer-group name must not reach an unauthenticated body")
	assert.NotContains(t, body, streamsOffsetsKey)
	assert.NotContains(t, body, "4242", "live offsets must not reach an unauthenticated body")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded))
	stats, ok := decoded["streams_stats"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), stats["consumers"], "the counters survive the filter")
	assert.Equal(t, true, stats["started"])
}

// TestHealthDebugRetainsStreamIdentifiers is the other half: the access-controlled
// debug endpoint still renders the offsets operators need.
func TestHealthDebugRetainsStreamIdentifiers(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	app := &App{cfg: cfg, logger: logger.New("error", false), healthProbes: []Prober{
		streamsProbeWithOffsets("payments-ledger", "fraud-scoring"),
	}}

	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	w := httptest.NewRecorder()
	require.NoError(t, handlers.handleHealthDebug(server.NewHandlerContextForTest(w, req, nil)))

	body := w.Body.String()
	assert.Contains(t, body, streamsOffsetsKey)
	assert.Contains(t, body, "payments-ledger/fraud-scoring")
	assert.Contains(t, body, "4242")
}

func TestPublicStreamsStatsCopiesRatherThanMutates(t *testing.T) {
	details := map[string]any{
		statusKey:         healthyStatus,
		streamsOffsetsKey: map[string]int64{"orders/projector": 7},
	}

	public := publicStreamsStats(details)

	assert.NotContains(t, public, streamsOffsetsKey)
	assert.Equal(t, healthyStatus, public[statusKey])
	assert.Contains(t, details, streamsOffsetsKey, "the caller's map must not be mutated")
}
