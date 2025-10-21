package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	appName       = "test-app"
	moduleName    = "test-module"
	appVersion    = "v1.0.0"
	readyEndpoint = "/ready"
)

type MockSignalHandler struct {
	mock.Mock
	shouldExit chan bool
	waiting    atomic.Bool
	triggered  atomic.Bool
	mu         sync.Mutex
	signalChan chan<- os.Signal
}

func NewMockSignalHandler() *MockSignalHandler {
	return &MockSignalHandler{
		shouldExit: make(chan bool, 1),
	}
}

func (m *MockSignalHandler) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.waiting.Store(false)
	m.triggered.Store(false)
	m.signalChan = nil

	// Drain the channel if there's a value
	select {
	case <-m.shouldExit:
	default:
	}
}

func (m *MockSignalHandler) Notify(c chan<- os.Signal, sig ...os.Signal) {
	// If no mock expectations are set, just return (no-op)
	if len(m.ExpectedCalls) == 0 {
		// Store the channel for TriggerShutdown to use
		m.mu.Lock()
		m.signalChan = c
		m.mu.Unlock()
		return
	}
	args := m.Called(c, sig)
	_ = args
}

func (m *MockSignalHandler) WaitForSignal(c <-chan os.Signal) {
	// If no mock expectations are set, just return (no-op for tests)
	if len(m.ExpectedCalls) == 0 {
		return
	}
	m.Called(c)

	// Mark that we're waiting and check if already triggered
	m.waiting.Store(true)
	if m.triggered.Load() {
		// Already triggered, return immediately
		return
	}

	// Wait for shutdown signal with timeout protection
	select {
	case <-m.shouldExit:
		// Signal received
	case <-time.After(5 * time.Second):
		// Timeout protection - should not happen in well-behaved tests
	}
}

func (m *MockSignalHandler) TriggerShutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.triggered.Store(true)

	// Send signal to the actual signal channel if available
	if m.signalChan != nil {
		select {
		case m.signalChan <- os.Interrupt:
		default:
		}
	}

	// Also send to shouldExit for WaitForSignal compatibility
	if m.waiting.Load() {
		select {
		case m.shouldExit <- true:
		default:
		}
	}
}

type MockTimeoutProvider struct {
	mock.Mock
}

func (m *MockTimeoutProvider) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// If no mock expectations are set, use default behavior
	if len(m.ExpectedCalls) == 0 {
		return context.WithTimeout(parent, timeout)
	}
	args := m.Called(parent, timeout)
	return args.Get(0).(context.Context), args.Get(1).(context.CancelFunc)
}

type mockServer struct {
	startErr      error
	shutdownErr   error
	startCalls    int32
	shutdownCalls int32
	e             *echo.Echo
	readyHandler  echo.HandlerFunc

	gate     chan struct{}
	gateOnce sync.Once
}

func newMockServer() *mockServer {
	return &mockServer{
		startErr: http.ErrServerClosed,
		e:        echo.New(),
		gate:     make(chan struct{}),
	}
}

func (m *mockServer) releaseStart() {
	m.gateOnce.Do(func() {
		close(m.gate)
	})
}

func (m *mockServer) Start() error {
	atomic.AddInt32(&m.startCalls, 1)
	<-m.gate
	return m.startErr
}

func (m *mockServer) Shutdown(ctx context.Context) error {
	_ = ctx
	atomic.AddInt32(&m.shutdownCalls, 1)
	m.releaseStart()
	return m.shutdownErr
}

func (m *mockServer) Echo() *echo.Echo {
	return m.e
}

func (m *mockServer) ModuleGroup() server.RouteRegistrar {
	return &noopRouteRegistrar{}
}

func (m *mockServer) RegisterReadyHandler(handler echo.HandlerFunc) {
	m.readyHandler = handler
}

func (m *mockServer) startCount() int {
	return int(atomic.LoadInt32(&m.startCalls))
}

func (m *mockServer) shutdownCount() int {
	return int(atomic.LoadInt32(&m.shutdownCalls))
}

type noopRouteRegistrar struct{}

func (n *noopRouteRegistrar) Add(_, _ string, _ echo.HandlerFunc, _ ...echo.MiddlewareFunc) *echo.Route {
	return nil
}

func (n *noopRouteRegistrar) Group(_ string, _ ...echo.MiddlewareFunc) server.RouteRegistrar {
	return &noopRouteRegistrar{}
}

func (n *noopRouteRegistrar) Use(_ ...echo.MiddlewareFunc) {
	// No-op
}

func (n *noopRouteRegistrar) FullPath(path string) string {
	if path == "" {
		return "/"
	}
	if path[0] == '/' {
		return path
	}
	return "/" + path
}

type MockModule struct {
	mock.Mock
	name string
}

func (m *MockModule) Name() string {
	if m.name != "" {
		return m.name
	}
	return m.Called().String(0)
}

func (m *MockModule) Init(deps *ModuleDeps) error {
	return m.Called(deps).Error(0)
}

func (m *MockModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	m.Called(hr, r)
}

func (m *MockModule) DeclareMessaging(decls *messaging.Declarations) {
	m.Called(decls)
}

func (m *MockModule) Shutdown() error {
	return m.Called().Error(0)
}

// MockSchedulerModule implements Module + JobRegistrar for testing scheduler wiring
type MockSchedulerModule struct {
	mock.Mock
	name string
}

func (m *MockSchedulerModule) Name() string {
	if m.name != "" {
		return m.name
	}
	return m.Called().String(0)
}

func (m *MockSchedulerModule) Init(deps *ModuleDeps) error {
	return m.Called(deps).Error(0)
}

func (m *MockSchedulerModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	m.Called(hr, r)
}

func (m *MockSchedulerModule) DeclareMessaging(decls *messaging.Declarations) {
	m.Called(decls)
}

func (m *MockSchedulerModule) Shutdown() error {
	return m.Called().Error(0)
}

// JobRegistrar interface implementation
func (m *MockSchedulerModule) FixedRate(jobID string, job any, interval time.Duration) error {
	return m.Called(jobID, job, interval).Error(0)
}

func (m *MockSchedulerModule) DailyAt(jobID string, job any, localTime time.Time) error {
	return m.Called(jobID, job, localTime).Error(0)
}

func (m *MockSchedulerModule) WeeklyAt(jobID string, job any, dayOfWeek time.Weekday, localTime time.Time) error {
	return m.Called(jobID, job, dayOfWeek, localTime).Error(0)
}

func (m *MockSchedulerModule) HourlyAt(jobID string, job any, minute int) error {
	return m.Called(jobID, job, minute).Error(0)
}

func (m *MockSchedulerModule) MonthlyAt(jobID string, job any, dayOfMonth int, localTime time.Time) error {
	return m.Called(jobID, job, dayOfMonth, localTime).Error(0)
}

// MockJobProviderModule implements Module + JobProvider for testing job registration
type MockJobProviderModule struct {
	mock.Mock
	name string
}

func (m *MockJobProviderModule) Name() string {
	if m.name != "" {
		return m.name
	}
	return m.Called().String(0)
}

func (m *MockJobProviderModule) Init(deps *ModuleDeps) error {
	return m.Called(deps).Error(0)
}

func (m *MockJobProviderModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	m.Called(hr, r)
}

func (m *MockJobProviderModule) DeclareMessaging(decls *messaging.Declarations) {
	m.Called(decls)
}

func (m *MockJobProviderModule) Shutdown() error {
	return m.Called().Error(0)
}

// JobProvider interface implementation
func (m *MockJobProviderModule) RegisterJobs(registrar JobRegistrar) error {
	return m.Called(registrar).Error(0)
}

type testAppFixture struct {
	t         *testing.T
	app       *App
	db        *testmocks.MockDatabase
	messaging *testmocks.MockAMQPClient
	server    *mockServer
}

type fixtureOption func(*testAppFixture)

func newTestAppFixture(t *testing.T, opts ...fixtureOption) *testAppFixture {
	t.Helper()

	cfg := defaultTestConfig()
	log := logger.New("debug", true)

	db := &testmocks.MockDatabase{}
	msg := testmocks.NewMockAMQPClient()

	// Create resource source for test
	resourceSource := config.NewTenantStore(cfg)

	// Create real managers with mock factories
	dbManager := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return db, nil
		},
	)

	messagingManager := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return msg
		},
	)

	resourceProvider := NewSingleTenantResourceProvider(dbManager, messagingManager, nil)

	deps := &ModuleDeps{
		Logger: log,
		Config: cfg,
		GetDB: func(ctx context.Context) (database.Interface, error) {
			return dbManager.Get(ctx, "")
		},
		GetMessaging: func(ctx context.Context) (messaging.AMQPClient, error) {
			return messagingManager.GetPublisher(ctx, "")
		},
	}

	srv := newMockServer()

	app := &App{
		cfg:              cfg,
		server:           srv,
		logger:           log,
		registry:         NewModuleRegistry(deps),
		signalHandler:    &OSSignalHandler{},
		timeoutProvider:  &StandardTimeoutProvider{},
		dbManager:        dbManager,
		messagingManager: messagingManager,
		resourceProvider: resourceProvider,
		messagingInitializer: NewMessagingInitializer(
			log,
			messagingManager,
			cfg.Multitenant.Enabled,
		),
		connectionPreWarmer: NewConnectionPreWarmer(log, dbManager, messagingManager),
	}

	app.messagingDeclarations = nil

	fixture := &testAppFixture{
		t:         t,
		app:       app,
		db:        db,
		messaging: msg,
		server:    srv,
	}

	for _, opt := range opts {
		opt(fixture)
	}

	fixture.rebuildClosersAndHealth()
	fixture.server.Echo().GET(readyEndpoint, fixture.app.readyCheck)

	return fixture
}

func (f *testAppFixture) rebuildClosersAndHealth() {
	f.app.healthProbes = createHealthProbesForManagers(f.app.dbManager, f.app.messagingManager, f.app.logger)
	f.app.closers = nil
	f.app.registerCloser("database manager", f.app.dbManager)
	f.app.registerCloser("messaging manager", f.app.messagingManager)
}

func withSignalHandler(handler SignalHandler) fixtureOption {
	return func(f *testAppFixture) {
		f.app.signalHandler = handler
	}
}

func withTimeoutProvider(provider TimeoutProvider) fixtureOption {
	return func(f *testAppFixture) {
		f.app.timeoutProvider = provider
	}
}

func defaultTestConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Name:    appName,
			Env:     "test",
			Version: appVersion,
		},
		Server: config.ServerConfig{Host: "localhost", Port: 8080},
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
		Log: config.LogConfig{Level: "debug"},
	}
}

func (f *testAppFixture) newReadyContext() (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, readyEndpoint, http.NoBody)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

type closerMock struct {
	mock.Mock
}

func (c *closerMock) Close() error {
	return c.Called().Error(0)
}

func TestRegisterModuleSuccess(t *testing.T) {
	fixture := newTestAppFixture(t)
	module := &MockModule{name: moduleName}
	module.On("Init", mock.Anything).Return(nil)

	err := fixture.app.RegisterModule(module)
	require.NoError(t, err)
	assert.Len(t, fixture.app.registry.modules, 1)
	module.AssertExpectations(t)
}

func TestRegisterModuleInitError(t *testing.T) {
	fixture := newTestAppFixture(t)
	module := &MockModule{name: moduleName}
	expectedErr := errors.New("init failed")
	module.On("Init", mock.Anything).Return(expectedErr)

	err := fixture.app.RegisterModule(module)
	require.ErrorIs(t, err, expectedErr)
	assert.Empty(t, fixture.app.registry.modules)
	module.AssertExpectations(t)
}

type stubTenantResource struct {
	dbCalls  int
	msgCalls int
}

func (s *stubTenantResource) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	s.dbCalls++
	return &config.DatabaseConfig{Type: "postgresql"}, nil
}

func (s *stubTenantResource) BrokerURL(context.Context, string) (string, error) {
	s.msgCalls++
	return "amqp://stub/", nil
}

func (s *stubTenantResource) IsDynamic() bool {
	return false // Test stub uses static behavior
}

func TestAppUsesProvidedResourceSource(t *testing.T) {
	cfg := defaultTestConfig()
	resource := &stubTenantResource{}

	opts := &Options{
		ResourceSource: resource,
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	}

	app, _, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)

	_, err = app.dbManager.Get(context.Background(), "")
	require.NoError(t, err)
	_, err = app.messagingManager.GetPublisher(context.Background(), "")
	require.NoError(t, err)

	assert.Greater(t, resource.dbCalls, 0)
	assert.Greater(t, resource.msgCalls, 0)
}

func TestReadyCheckScenarios(t *testing.T) {
	cases := []struct {
		name           string
		prepare        func(f *testAppFixture)
		expectedStatus int
		assertBody     func(t *testing.T, body map[string]any)
	}{
		{
			name: "healthy",
			prepare: func(f *testAppFixture) {
				f.db.On("Health", mock.Anything).Return(nil)
				f.messaging.SetReady(true)
			},
			expectedStatus: http.StatusOK,
			assertBody: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "ready", body["status"])
				assert.Equal(t, "healthy", body["database"])
				stats, ok := body["db_stats"].(map[string]any)
				assert.True(t, ok)
				assert.Contains(t, stats, "active_connections")
				assert.Equal(t, "healthy", body["messaging"])
				msgStats, ok := body["messaging_stats"].(map[string]any)
				assert.True(t, ok)
				assert.Contains(t, msgStats, "active_publishers")
			},
		},
		{
			name: "database unhealthy",
			prepare: func(f *testAppFixture) {
				f.db.On("Health", mock.Anything).Return(errors.New("db down"))
			},
			expectedStatus: http.StatusServiceUnavailable,
			assertBody: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "not ready", body["status"])
				assert.Equal(t, "unhealthy", body["database"])
				assert.Equal(t, "db down", body["error"])
			},
		},
		{
			name: "messaging disabled",
			prepare: func(f *testAppFixture) {
				f.db.On("Health", mock.Anything).Return(nil)
				// Set messaging manager to nil to simulate disabled messaging
				f.app.messagingManager = nil
				f.rebuildClosersAndHealth()
			},
			expectedStatus: http.StatusOK,
			assertBody: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "ready", body["status"])
				assert.Equal(t, "healthy", body["database"])
				assert.Equal(t, "disabled", body["messaging"])
				msgStats, ok := body["messaging_stats"].(map[string]any)
				assert.True(t, ok)
				assert.Len(t, msgStats, 0)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestAppFixture(t)
			tc.prepare(fixture)

			ctx, rec := fixture.newReadyContext()
			err := fixture.app.readyCheck(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedStatus, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			tc.assertBody(t, body)

			fixture.db.AssertExpectations(t)
		})
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	signalHandler := NewMockSignalHandler()
	defer signalHandler.Reset() // Cleanup after test
	timeoutProvider := &MockTimeoutProvider{}

	fixture := newTestAppFixture(t, withSignalHandler(signalHandler), withTimeoutProvider(timeoutProvider))
	fixture.messaging.ExpectClose(nil)
	fixture.db.On("Close").Return(nil)

	// Don't set up mock expectations - use the no-expectation path that stores signalChan
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timeoutProvider.On("WithTimeout", mock.Anything, 10*time.Second).Return(shutdownCtx, cancel)

	done := make(chan error, 1)
	go func() {
		done <- fixture.app.Run()
	}()

	// Wait for server to start before triggering shutdown
	assert.Eventually(t, func() bool { return fixture.server.startCount() == 1 }, time.Second, 10*time.Millisecond)

	// Give a small delay to ensure Run() has reached the signal waiting phase
	time.Sleep(100 * time.Millisecond)

	// Now trigger shutdown
	t.Log("Triggering shutdown...")
	signalHandler.TriggerShutdown()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete in time")
	}

	assert.Equal(t, 1, fixture.server.shutdownCount())
	timeoutProvider.AssertExpectations(t)
	fixture.messaging.AssertExpectations(t)
	fixture.db.AssertExpectations(t)
}

func TestRunPropagatesServerError(t *testing.T) {
	signalHandler := NewMockSignalHandler()
	defer signalHandler.Reset() // Cleanup after test
	timeoutProvider := &MockTimeoutProvider{}
	fixture := newTestAppFixture(t, withSignalHandler(signalHandler), withTimeoutProvider(timeoutProvider))

	fixture.messaging.ExpectClose(nil)
	fixture.db.On("Close").Return(nil)

	// Only expect Notify to be called - WaitForSignal won't be called if server fails to start
	signalHandler.On("Notify", mock.Anything, []os.Signal{os.Interrupt, syscall.SIGTERM}).Return()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timeoutProvider.On("WithTimeout", mock.Anything, 10*time.Second).Return(shutdownCtx, cancel)

	startErr := errors.New("start failed")
	fixture.server.startErr = startErr
	fixture.server.releaseStart()

	err := fixture.app.Run()

	require.Error(t, err)
	assert.ErrorIs(t, err, startErr)

	signalHandler.AssertExpectations(t)
	timeoutProvider.AssertExpectations(t)
	fixture.messaging.AssertExpectations(t)
	fixture.db.AssertExpectations(t)
}

func TestShutdownAggregatesErrors(t *testing.T) {
	fixture := newTestAppFixture(t)
	fixture.app.closers = nil

	serverErr := errors.New("server fail")
	fixture.server.shutdownErr = serverErr

	resourceErr := errors.New("resource fail")
	closer := &closerMock{}
	closer.On("Close").Return(resourceErr)
	fixture.app.registerCloser("resource", closer)

	err := fixture.app.Shutdown(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, serverErr)
	assert.ErrorIs(t, err, resourceErr)
	closer.AssertExpectations(t)
}

func TestNewWithConfigUsesConnectors(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Database.Host = "db-host"
	cfg.Messaging.Broker.URL = "amqp://broker"

	dbMock := &testmocks.MockDatabase{}
	msgMock := testmocks.NewMockAMQPClient()

	var dbCalled, messagingCalled bool

	opts := &Options{
		DatabaseConnector: func(dbCfg *config.DatabaseConfig, _ logger.Logger) (database.Interface, error) {
			dbCalled = true
			assert.Equal(t, "db-host", dbCfg.Host)
			return dbMock, nil
		},
		MessagingClientFactory: func(url string, _ logger.Logger) messaging.AMQPClient {
			messagingCalled = true
			assert.Equal(t, "amqp://broker", url)
			return msgMock
		},
	}

	app, _, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.True(t, dbCalled)
	assert.True(t, messagingCalled)
	// Verify the injected factories are used by testing manager behavior
	ctx := context.Background()
	dbConn, err := app.dbManager.Get(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, dbMock, dbConn)

	msgClient, err := app.messagingManager.GetPublisher(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, msgMock, msgClient)
}

func TestNewWithOptionsLoadError(t *testing.T) {
	opts := &Options{
		ConfigLoader: func() (*config.Config, error) {
			return nil, errors.New("load failed")
		},
	}

	app, log, err := NewWithOptions(opts)
	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log) // Logger should always be available
}

func TestStandardTimeoutProviderWithTimeout(t *testing.T) {
	provider := &StandardTimeoutProvider{}
	ctx, cancel := provider.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(10*time.Millisecond), deadline, 20*time.Millisecond)
}

func TestOSSignalHandlerWaitForSignal(t *testing.T) {
	handler := &OSSignalHandler{}
	signals := make(chan os.Signal, 1)
	handler.Notify(signals, os.Interrupt)

	done := make(chan struct{})
	go func() {
		handler.WaitForSignal(signals)
		close(done)
	}()

	signals <- os.Interrupt

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait for signal timed out")
	}
}

// Test module that counts how many times DeclareMessaging is called
type declarationCounterModule struct {
	callCount int
}

func (m *declarationCounterModule) Name() string             { return "counter-module" }
func (m *declarationCounterModule) Init(_ *ModuleDeps) error { return nil }
func (m *declarationCounterModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {
	// no-op
}
func (m *declarationCounterModule) DeclareMessaging(_ *messaging.Declarations) {
	m.callCount++
}
func (m *declarationCounterModule) Shutdown() error { return nil }

func TestMessagingDeclarationsBuiltOnceAndReused(t *testing.T) {
	counterModule := &declarationCounterModule{}

	// Create app without building it
	fixture := newTestAppFixture(t)

	// Register the module - this should trigger DeclareMessaging during app build
	err := fixture.app.RegisterModule(counterModule)
	require.NoError(t, err)

	// Declarations are built lazily during runtime preparation
	require.NoError(t, fixture.app.prepareRuntime())
	assert.Equal(t, 1, counterModule.callCount, "DeclareMessaging should be called during runtime preparation")
	assert.NotNil(t, fixture.app.messagingDeclarations, "messagingDeclarations should be built during runtime preparation")
}

func TestNew(t *testing.T) {
	t.Run("error handling returns logger even on failure", func(t *testing.T) {
		// This test verifies that even when New() fails, it returns a logger
		// We trigger a failure by providing an invalid config loader
		opts := &Options{
			ConfigLoader: func() (*config.Config, error) {
				return nil, fmt.Errorf("simulated config error")
			},
		}

		app, log, err := NewWithOptions(opts)

		assert.Error(t, err)
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available even on failure

		// Verify logger is functional even when app creation fails
		log.Info().Msg("Test log from New() failure scenario")
	})
}

func TestGetMessagingDeclarations(t *testing.T) {
	t.Run("returns nil when no declarations built", func(t *testing.T) {
		app := &App{}

		result := app.GetMessagingDeclarations()
		assert.Nil(t, result)
	})

	t.Run("returns declarations when available", func(t *testing.T) {
		decls := messaging.NewDeclarations()
		app := &App{
			messagingDeclarations: decls,
		}

		result := app.GetMessagingDeclarations()
		assert.Equal(t, decls, result)
	})
}

func TestCreateBootstrapLogger(t *testing.T) {
	// Store original env vars
	originalEnv := os.Getenv("APP_ENV")
	originalLogLevel := os.Getenv("LOG_LEVEL")

	defer func() {
		// Restore original env vars
		if originalEnv != "" {
			os.Setenv("APP_ENV", originalEnv)
		} else {
			os.Unsetenv("APP_ENV")
		}
		if originalLogLevel != "" {
			os.Setenv("LOG_LEVEL", originalLogLevel)
		} else {
			os.Unsetenv("LOG_LEVEL")
		}
	}()

	t.Run("development environment settings", func(t *testing.T) {
		os.Setenv("APP_ENV", "development")
		os.Unsetenv("LOG_LEVEL")

		log := createBootstrapLogger()
		assert.NotNil(t, log)

		// Verify logger works
		log.Debug().Msg("Test debug message")
	})

	t.Run("production environment settings", func(t *testing.T) {
		os.Setenv("APP_ENV", "production")
		os.Unsetenv("LOG_LEVEL")

		log := createBootstrapLogger()
		assert.NotNil(t, log)

		// Verify logger works
		log.Info().Msg("Test info message")
	})

	t.Run("custom log level override", func(t *testing.T) {
		os.Setenv("APP_ENV", "production")
		os.Setenv("LOG_LEVEL", "debug")

		log := createBootstrapLogger()
		assert.NotNil(t, log)

		// Verify logger works
		log.Debug().Msg("Test debug message with override")
	})

	t.Run("empty environment defaults", func(t *testing.T) {
		os.Unsetenv("APP_ENV")
		os.Unsetenv("LOG_LEVEL")

		log := createBootstrapLogger()
		assert.NotNil(t, log)

		// Verify logger works
		log.Debug().Msg("Test with default settings")
	})
}

func TestResolveServer(t *testing.T) {
	cfg := defaultTestConfig()
	log := logger.New("debug", true)

	t.Run("uses provided server from options", func(t *testing.T) {
		mockServer := &stubServerRunner{}
		opts := &Options{
			Server: mockServer,
		}

		result := resolveServer(cfg, log, opts)
		assert.Equal(t, mockServer, result)
	})

	t.Run("creates new server when none provided", func(t *testing.T) {
		result := resolveServer(cfg, log, nil)
		assert.NotNil(t, result)
	})

	t.Run("creates new server with empty options", func(t *testing.T) {
		opts := &Options{}

		result := resolveServer(cfg, log, opts)
		assert.NotNil(t, result)
	})
}

func TestRegisterCloser(t *testing.T) {
	app := &App{}

	t.Run("registers valid closer", func(t *testing.T) {
		closer := &stubCloser{}
		app.registerCloser("test-closer", closer)

		assert.Len(t, app.closers, 1)
		assert.Equal(t, "test-closer", app.closers[0].name)
		assert.Equal(t, closer, app.closers[0].closer)
	})

	t.Run("ignores nil closer", func(t *testing.T) {
		initialCount := len(app.closers)
		app.registerCloser("nil-closer", nil)

		assert.Len(t, app.closers, initialCount)
	})
}

func TestNewWithConfigErrors(t *testing.T) {
	t.Run("nil config causes error", func(t *testing.T) {
		app, log, err := NewWithConfig(nil, &Options{})

		assert.Error(t, err)
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})

	t.Run("nil options with empty config succeeds (messaging/database now optional)", func(t *testing.T) {
		cfg := &config.Config{} // Empty config - database and messaging not configured
		app, log, err := NewWithConfig(cfg, nil)

		// After the fix: messaging and database are optional, so app creation succeeds
		// Config validation is NOT performed by NewWithConfig (user's responsibility)
		assert.NoError(t, err)
		assert.NotNil(t, app)
		assert.NotNil(t, log)
	})

	t.Run("invalid database config causes dependency resolution error", func(t *testing.T) {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:    "test-app",
				Env:     "test",
				Version: "1.0.0",
			},
			Log: config.LogConfig{
				Level:  "info",
				Pretty: false,
			},
			Database: config.DatabaseConfig{
				Type:     "postgresql",
				Host:     "", // Invalid empty host
				Port:     0,  // Invalid port
				Database: "",
			},
		}

		app, log, err := NewWithConfig(cfg, &Options{})

		assert.Error(t, err)
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})
}

func TestOSSignalHandler(t *testing.T) {
	t.Run("Notify and WaitForSignal methods", func(t *testing.T) {
		handler := &OSSignalHandler{}

		// Test Notify - should not panic
		c := make(chan os.Signal, 1)
		handler.Notify(c, os.Interrupt)

		// The actual signal handling is OS-dependent, so we just test that the methods exist
		assert.NotNil(t, handler)
	})
}

func TestStandardTimeoutProvider(t *testing.T) {
	t.Run("WithTimeout creates context with timeout", func(t *testing.T) {
		provider := &StandardTimeoutProvider{}
		ctx := context.Background()

		childCtx, cancel := provider.WithTimeout(ctx, time.Millisecond*100)
		defer cancel()

		assert.NotNil(t, childCtx)
		deadline, ok := childCtx.Deadline()
		assert.True(t, ok)
		assert.True(t, deadline.After(time.Now()))
	})
}

func TestBuildMessagingDeclarations(t *testing.T) {
	t.Run("nil registry error", func(t *testing.T) {
		app := &App{}

		err := app.buildMessagingDeclarations()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "module registry not initialized")
	})

	t.Run("already built returns nil", func(t *testing.T) {
		app := &App{
			messagingDeclarations: messaging.NewDeclarations(),
		}

		err := app.buildMessagingDeclarations()
		assert.NoError(t, err)
	})
}

func TestShutdownResource(t *testing.T) {
	t.Run("successful closure with name", func(t *testing.T) {
		log := logger.New("debug", true)
		app := &App{logger: log}

		// Create a mock closer that succeeds
		mockCloser := &stubCloser{}
		closer := namedCloser{
			name:   "test-resource",
			closer: mockCloser,
		}

		var errs []error
		app.shutdownResource(closer, &errs)

		assert.Empty(t, errs)
	})

	t.Run("successful closure with empty name", func(t *testing.T) {
		log := logger.New("debug", true)
		app := &App{logger: log}

		mockCloser := &stubCloser{}
		closer := namedCloser{
			name:   "",
			closer: mockCloser,
		}

		var errs []error
		app.shutdownResource(closer, &errs)

		assert.Empty(t, errs)
	})

	t.Run("failed closure appends error", func(t *testing.T) {
		log := logger.New("debug", true)
		app := &App{logger: log}

		// Create a mock closer that fails
		mockCloser := &failingCloser{}
		closer := namedCloser{
			name:   "failing-resource",
			closer: mockCloser,
		}

		var errs []error
		app.shutdownResource(closer, &errs)

		assert.Len(t, errs, 1)
		assert.Contains(t, errs[0].Error(), "failing-resource")
		assert.Contains(t, errs[0].Error(), "close failed")
	})
}

func TestResolveSignalAndTimeout(t *testing.T) {
	t.Run("uses custom handlers from options", func(t *testing.T) {
		mockSignal := NewMockSignalHandler()
		mockTimeout := &MockTimeoutProvider{}

		opts := &Options{
			SignalHandler:   mockSignal,
			TimeoutProvider: mockTimeout,
		}

		signal, timeout := resolveSignalAndTimeout(opts)

		assert.Equal(t, mockSignal, signal)
		assert.Equal(t, mockTimeout, timeout)
	})

	t.Run("uses defaults when options is nil", func(t *testing.T) {
		signal, timeout := resolveSignalAndTimeout(nil)

		assert.IsType(t, &OSSignalHandler{}, signal)
		assert.IsType(t, &StandardTimeoutProvider{}, timeout)
	})

	t.Run("uses defaults when handlers not provided", func(t *testing.T) {
		opts := &Options{}

		signal, timeout := resolveSignalAndTimeout(opts)

		assert.IsType(t, &OSSignalHandler{}, signal)
		assert.IsType(t, &StandardTimeoutProvider{}, timeout)
	})
}

func TestIsDescriber(t *testing.T) {
	t.Run("module implements Describer interface", func(t *testing.T) {
		module := &describerModule{}
		describer, ok := IsDescriber(module)

		assert.True(t, ok)
		assert.NotNil(t, describer)
		assert.Equal(t, module, describer)
	})

	t.Run("module does not implement Describer interface", func(t *testing.T) {
		module := &simpleTestModule{}
		describer, ok := IsDescriber(module)

		assert.False(t, ok)
		assert.Nil(t, describer)
	})
}

func TestDrainServerError(t *testing.T) {
	t.Run("nil channel returns nil", func(t *testing.T) {
		app := &App{}
		err := app.drainServerError(nil)
		assert.NoError(t, err)
	})

	t.Run("closed channel returns nil", func(t *testing.T) {
		app := &App{}
		ch := make(chan error)
		close(ch)

		err := app.drainServerError(ch)
		assert.NoError(t, err)
	})

	t.Run("channel with error returns error", func(t *testing.T) {
		app := &App{}
		ch := make(chan error, 1)
		expectedErr := fmt.Errorf("server error")
		ch <- expectedErr

		err := app.drainServerError(ch)
		assert.Equal(t, expectedErr, err)
	})
}

// Test helpers
type stubServerRunner struct{}

func (s *stubServerRunner) Start() error                       { return nil }
func (s *stubServerRunner) Shutdown(_ context.Context) error   { return nil }
func (s *stubServerRunner) Echo() *echo.Echo                   { return echo.New() }
func (s *stubServerRunner) ModuleGroup() server.RouteRegistrar { return nil }
func (s *stubServerRunner) RegisterReadyHandler(_ echo.HandlerFunc) {
	// no-op
}

type stubCloser struct{}

func (c *stubCloser) Close() error { return nil }

type failingCloser struct{}

func (c *failingCloser) Close() error { return fmt.Errorf("close failed") }

type describerModule struct{}

func (m *describerModule) Name() string             { return "describer-module" }
func (m *describerModule) Init(_ *ModuleDeps) error { return nil }
func (m *describerModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {
	// no-op
}
func (m *describerModule) DeclareMessaging(_ *messaging.Declarations) {
	// no-op
}
func (m *describerModule) Shutdown() error { return nil }
func (m *describerModule) DescribeModule() ModuleDescriptor {
	return ModuleDescriptor{
		Description: "A test module that implements Describer",
		Version:     "1.0.0",
	}
}
func (m *describerModule) DescribeRoutes() []server.RouteDescriptor {
	return []server.RouteDescriptor{}
}
