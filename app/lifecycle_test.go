package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
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
	err = app.prepareRuntime()
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
	testLogger := logger.New("info", false)
	deps := &ModuleDeps{Logger: testLogger, Config: cfg}
	return &App{
		cfg:      cfg,
		logger:   testLogger,
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

	err := app.prepareRuntime()
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

	require.NoError(t, app.prepareRuntime())
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

	require.NoError(t, app.prepareRuntime())
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

	require.NoError(t, app.prepareRuntime())
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

// TestPublisherCleanupIntervalTooLate guards the Fix-3 predicate: the previously
// implicit guarantee (a hardcoded 2m sweep always well below any configured TTL) no
// longer holds now that both messaging.publisher.cleanupinterval and
// messaging.publisher.idlettl are independently operator-configurable. The predicate
// must flag "sweep frequency >= TTL" (eviction merely lags, so this is advisory, not
// fatal — see startMaintenanceLoops) while treating idleTTL <= 0 as "nothing
// meaningful to compare". config.Validate applies the IdleTTL default unconditionally
// (see config/validation.go: validateMessaging), so that guard is purely defensive
// for callers that bypass Validate — as the zero/negative cases here exercise.
func TestPublisherCleanupIntervalTooLate(t *testing.T) {
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
			assert.Equal(t, tc.want, publisherCleanupIntervalTooLate(tc.cleanupInterval, tc.idleTTL))
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
