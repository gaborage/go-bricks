package app

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/gaborage/go-bricks/multitenant"
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
}

func NewMockSignalHandler() *MockSignalHandler {
	return &MockSignalHandler{shouldExit: make(chan bool, 1)}
}

func (m *MockSignalHandler) Notify(c chan<- os.Signal, sig ...os.Signal) {
	m.Called(c, sig)
}

func (m *MockSignalHandler) WaitForSignal(c <-chan os.Signal) {
	m.Called(c)
	<-m.shouldExit
}

func (m *MockSignalHandler) TriggerShutdown() {
	select {
	case m.shouldExit <- true:
	default:
	}
}

type MockTimeoutProvider struct {
	mock.Mock
}

func (m *MockTimeoutProvider) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
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

func (m *MockModule) RegisterMessaging(registry *messaging.Registry) {
	m.Called(registry)
}

func (m *MockModule) Shutdown() error {
	return m.Called().Error(0)
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

	deps := &ModuleDeps{
		Logger: log,
		Config: cfg,
		GetDB: func(context.Context) (database.Interface, error) {
			return db, nil
		},
		GetMessaging: func(context.Context) (messaging.AMQPClient, error) {
			return msg, nil
		},
	}

	srv := newMockServer()

	app := &App{
		cfg:             cfg,
		server:          srv,
		db:              db,
		logger:          log,
		messaging:       msg,
		registry:        NewModuleRegistry(deps),
		signalHandler:   &OSSignalHandler{},
		timeoutProvider: &StandardTimeoutProvider{},
	}

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
	f.app.healthProbes = createHealthProbes(f.app.db, f.app.messaging, f.app.logger)
	f.app.closers = nil
	f.app.registerCloser("messaging client", f.app.messaging)
	f.app.registerCloser("database connection", f.app.db)
	if f.app.tenantConnManager != nil {
		f.app.registerCloser("tenant connection manager", f.app.tenantConnManager)
	}
	if f.app.messagingManager != nil {
		f.app.registerCloser("messaging manager", f.app.messagingManager)
	}
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

func withTenantMessaging(orchestrator TenantMessagingOrchestrator) fixtureOption {
	return func(f *testAppFixture) {
		f.app.cfg.Multitenant.Enabled = true
		f.app.tenantMessaging = orchestrator
		f.rebuildClosersAndHealth()
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

type stubTenantMessaging struct {
	captureCalled    bool
	captureErr       error
	publisher        messaging.AMQPClient
	publishErr       error
	lastTenant       string
	lastDeclarations *multitenant.MessagingDeclarations
}

func (s *stubTenantMessaging) CaptureDeclarations() (*multitenant.MessagingDeclarations, error) {
	s.captureCalled = true
	if s.captureErr != nil {
		return nil, s.captureErr
	}
	return multitenant.NewMessagingDeclarations(), nil
}

func (s *stubTenantMessaging) PublisherForTenant(_ context.Context, tenantID string, declarations *multitenant.MessagingDeclarations) (messaging.AMQPClient, error) {
	s.lastTenant = tenantID
	s.lastDeclarations = declarations
	if s.publishErr != nil {
		return nil, s.publishErr
	}
	return s.publisher, nil
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
				f.db.On("Stats").Return(map[string]any{"open_connections": 5}, nil)
				f.messaging.SetReady(true)
			},
			expectedStatus: http.StatusOK,
			assertBody: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "ready", body["status"])
				assert.Equal(t, "healthy", body["database"])
				assert.Equal(t, float64(5), body["db_stats"].(map[string]any)["open_connections"])
				assert.Equal(t, "healthy", body["messaging"])
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
				f.db.On("Stats").Return(map[string]any{"status": "disabled"}, nil)
				f.app.messaging = nil
				f.rebuildClosersAndHealth()
			},
			expectedStatus: http.StatusOK,
			assertBody: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "disabled", body["messaging"])
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
	timeoutProvider := &MockTimeoutProvider{}

	fixture := newTestAppFixture(t, withSignalHandler(signalHandler), withTimeoutProvider(timeoutProvider))
	fixture.messaging.ExpectClose(nil)
	fixture.db.On("Close").Return(nil)

	signalHandler.On("Notify", mock.Anything, []os.Signal{os.Interrupt, syscall.SIGTERM}).Return()
	signalHandler.On("WaitForSignal", mock.Anything).Return()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timeoutProvider.On("WithTimeout", mock.Anything, 10*time.Second).Return(shutdownCtx, cancel)

	done := make(chan error, 1)
	go func() {
		done <- fixture.app.Run()
	}()

	assert.Eventually(t, func() bool { return fixture.server.startCount() == 1 }, time.Second, 10*time.Millisecond)

	signalHandler.TriggerShutdown()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete in time")
	}

	assert.Equal(t, 1, fixture.server.shutdownCount())
	signalHandler.AssertExpectations(t)
	timeoutProvider.AssertExpectations(t)
	fixture.messaging.AssertExpectations(t)
	fixture.db.AssertExpectations(t)
}

func TestRunPropagatesServerError(t *testing.T) {
	signalHandler := NewMockSignalHandler()
	timeoutProvider := &MockTimeoutProvider{}
	fixture := newTestAppFixture(t, withSignalHandler(signalHandler), withTimeoutProvider(timeoutProvider))

	fixture.messaging.ExpectClose(nil)
	fixture.db.On("Close").Return(nil)

	signalHandler.On("Notify", mock.Anything, []os.Signal{os.Interrupt, syscall.SIGTERM}).Return()
	signalHandler.On("WaitForSignal", mock.Anything).Return()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timeoutProvider.On("WithTimeout", mock.Anything, 10*time.Second).Return(shutdownCtx, cancel)

	startErr := errors.New("start failed")
	fixture.server.startErr = startErr
	fixture.server.releaseStart()

	err := fixture.app.Run()
	signalHandler.TriggerShutdown()

	require.Error(t, err)
	assert.ErrorIs(t, err, startErr)

	signalHandler.AssertExpectations(t)
	timeoutProvider.AssertExpectations(t)
	fixture.messaging.AssertExpectations(t)
	fixture.db.AssertExpectations(t)
}

func TestCaptureMessagingDeclarationsUsesOrchestrator(t *testing.T) {
	stub := &stubTenantMessaging{}
	fixture := newTestAppFixture(t, withTenantMessaging(stub))

	err := fixture.app.captureMessagingDeclarations()
	require.NoError(t, err)
	assert.True(t, stub.captureCalled)
	assert.NotNil(t, fixture.app.messagingDeclarations)
}

func TestSetupMultitenantGetMessagingUsesOrchestrator(t *testing.T) {
	stub := &stubTenantMessaging{publisher: testmocks.NewMockAMQPClient()}
	fixture := newTestAppFixture(t, withTenantMessaging(stub))
	fixture.app.setupMultitenantGetMessaging(fixture.app.registry.deps)

	ctx := multitenant.SetTenant(context.Background(), "tenant-1")

	_, err := fixture.app.registry.deps.GetMessaging(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging declarations not yet captured")

	fixture.app.messagingDeclarations = multitenant.NewMessagingDeclarations()

	client, err := fixture.app.registry.deps.GetMessaging(ctx)
	require.NoError(t, err)
	assert.Equal(t, stub.publisher, client)
	assert.Equal(t, "tenant-1", stub.lastTenant)
	assert.Equal(t, fixture.app.messagingDeclarations, stub.lastDeclarations)
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
		MessagingClientFactory: func(url string, _ logger.Logger) messaging.Client {
			messagingCalled = true
			assert.Equal(t, "amqp://broker", url)
			return msgMock
		},
	}

	app, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.True(t, dbCalled)
	assert.True(t, messagingCalled)
	assert.Equal(t, dbMock, app.db)
	assert.Equal(t, msgMock, app.messaging)
}

func TestNewWithOptionsLoadError(t *testing.T) {
	opts := &Options{
		ConfigLoader: func() (*config.Config, error) {
			return nil, errors.New("load failed")
		},
	}

	app, err := NewWithOptions(opts)
	require.Error(t, err)
	assert.Nil(t, app)
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
