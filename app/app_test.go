package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbpkg "github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// MockDatabase implements the database.Interface for testing
type MockDatabase struct {
	mock.Mock
}

func (m *MockDatabase) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	argsList := m.Called(ctx, query, args)
	return argsList.Get(0).(*sql.Rows), argsList.Error(1)
}

func (m *MockDatabase) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	argsList := m.Called(ctx, query, args)
	return argsList.Get(0).(*sql.Row)
}

func (m *MockDatabase) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	argsList := m.Called(ctx, query, args)
	return argsList.Get(0).(sql.Result), argsList.Error(1)
}

func (m *MockDatabase) Prepare(ctx context.Context, query string) (database.Statement, error) {
	argsList := m.Called(ctx, query)
	if argsList.Get(0) == nil {
		return nil, argsList.Error(1)
	}
	return argsList.Get(0).(database.Statement), argsList.Error(1)
}

func (m *MockDatabase) Begin(ctx context.Context) (database.Tx, error) {
	argsList := m.Called(ctx)
	if argsList.Get(0) == nil {
		return nil, argsList.Error(1)
	}
	return argsList.Get(0).(database.Tx), argsList.Error(1)
}

func (m *MockDatabase) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	argsList := m.Called(ctx, opts)
	if argsList.Get(0) == nil {
		return nil, argsList.Error(1)
	}
	return argsList.Get(0).(database.Tx), argsList.Error(1)
}

func (m *MockDatabase) Health(ctx context.Context) error {
	argsList := m.Called(ctx)
	return argsList.Error(0)
}

func (m *MockDatabase) Stats() (map[string]any, error) {
	argsList := m.Called()
	return argsList.Get(0).(map[string]any), argsList.Error(1)
}

func (m *MockDatabase) Close() error {
	argsList := m.Called()
	return argsList.Error(0)
}

func (m *MockDatabase) DatabaseType() string {
	argsList := m.Called()
	return argsList.String(0)
}

func (m *MockDatabase) GetMigrationTable() string {
	argsList := m.Called()
	return argsList.String(0)
}

func (m *MockDatabase) CreateMigrationTable(ctx context.Context) error {
	argsList := m.Called(ctx)
	return argsList.Error(0)
}

// MockSignalHandler implements SignalHandler for testing
type MockSignalHandler struct {
	mock.Mock
	shouldExit chan bool
}

func NewMockSignalHandler() *MockSignalHandler {
	return &MockSignalHandler{
		shouldExit: make(chan bool, 1),
	}
}

func (m *MockSignalHandler) Notify(c chan<- os.Signal, sig ...os.Signal) {
	m.Called(c, sig)
	// In tests, we don't use the real signal mechanism
}

func (m *MockSignalHandler) WaitForSignal(c <-chan os.Signal) {
	m.Called(c)
	// Wait for test to signal us to exit
	<-m.shouldExit
}

func (m *MockSignalHandler) TriggerShutdown() {
	m.shouldExit <- true
}

// MockTimeoutProvider implements TimeoutProvider for testing
type MockTimeoutProvider struct {
	mock.Mock
}

func (m *MockTimeoutProvider) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	args := m.Called(parent, timeout)
	ctx := args.Get(0).(context.Context)
	cancel := args.Get(1).(context.CancelFunc)
	return ctx, cancel
}

// MockMessagingClient implements the messaging.Client for testing
type MockMessagingClient struct {
	mock.Mock
}

func (m *MockMessagingClient) Publish(ctx context.Context, destination string, data []byte) error {
	argsList := m.Called(ctx, destination, data)
	return argsList.Error(0)
}

func (m *MockMessagingClient) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	argsList := m.Called(ctx, destination)
	return argsList.Get(0).(<-chan amqp.Delivery), argsList.Error(1)
}

func (m *MockMessagingClient) Close() error {
	argsList := m.Called()
	return argsList.Error(0)
}

func (m *MockMessagingClient) IsReady() bool {
	argsList := m.Called()
	return argsList.Bool(0)
}

func (m *MockMessagingClient) PublishToExchange(ctx context.Context, options messaging.PublishOptions, data []byte) error {
	argsList := m.Called(ctx, options, data)
	return argsList.Error(0)
}

func (m *MockMessagingClient) ConsumeFromQueue(ctx context.Context, options messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	argsList := m.Called(ctx, options)
	return argsList.Get(0).(<-chan amqp.Delivery), argsList.Error(1)
}

func (m *MockMessagingClient) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	argsList := m.Called(name, durable, autoDelete, exclusive, noWait)
	return argsList.Error(0)
}

func (m *MockMessagingClient) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	argsList := m.Called(name, kind, durable, autoDelete, internal, noWait)
	return argsList.Error(0)
}

func (m *MockMessagingClient) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	argsList := m.Called(queue, exchange, routingKey, noWait)
	return argsList.Error(0)
}

type mockServer struct {
	startErr      error
	shutdownErr   error
	startCalls    int32
	shutdownCalls int32
	e             *echo.Echo
}

func newMockServer() *mockServer {
	return &mockServer{e: echo.New()}
}

func (m *mockServer) Start() error {
	atomic.AddInt32(&m.startCalls, 1)
	return m.startErr
}

func (m *mockServer) Shutdown(ctx context.Context) error {
	atomic.AddInt32(&m.shutdownCalls, 1)
	_ = ctx
	return m.shutdownErr
}

func (m *mockServer) Echo() *echo.Echo {
	return m.e
}

func (m *mockServer) startCount() int {
	return int(atomic.LoadInt32(&m.startCalls))
}

func (m *mockServer) shutdownCount() int {
	return int(atomic.LoadInt32(&m.shutdownCalls))
}

// MockModule implements the Module interface for testing
type MockModule struct {
	mock.Mock
	name string
}

func (m *MockModule) Name() string {
	if m.name != "" {
		return m.name
	}
	argsList := m.Called()
	return argsList.String(0)
}

func (m *MockModule) Init(deps *ModuleDeps) error {
	argsList := m.Called(deps)
	return argsList.Error(0)
}

func (m *MockModule) RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo) {
	m.Called(hr, e)
}

func (m *MockModule) RegisterMessaging(registry *messaging.Registry) {
	m.Called(registry)
}

func (m *MockModule) Shutdown() error {
	argsList := m.Called()
	return argsList.Error(0)
}

// Helper to create a test app with mocked dependencies
func createTestApp(_ *testing.T) (*App, *MockDatabase, *MockMessagingClient) {
	return createTestAppWithMocks(nil, nil)
}

// Helper to create a test app with injectable mocked dependencies
func createTestAppWithMocks(mockSignalHandler *MockSignalHandler, mockTimeoutProvider *MockTimeoutProvider) (*App, *MockDatabase, *MockMessagingClient) {
	// Create test config
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0-test",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "amqp://localhost:5672",
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	log := logger.New("debug", true)

	mockDB := &MockDatabase{}
	mockMessaging := &MockMessagingClient{}

	deps := &ModuleDeps{
		DB:        mockDB,
		Logger:    log,
		Messaging: mockMessaging,
		Config:    cfg,
	}
	registry := NewModuleRegistry(deps)

	// Create test server
	srv := newMockServer()

	// Use default implementations if not provided
	var signalHandler SignalHandler = &OSSignalHandler{}
	var timeoutProvider TimeoutProvider = &StandardTimeoutProvider{}

	if mockSignalHandler != nil {
		signalHandler = mockSignalHandler
	}
	if mockTimeoutProvider != nil {
		timeoutProvider = mockTimeoutProvider
	}

	app := &App{
		cfg:             cfg,
		server:          srv,
		db:              mockDB,
		logger:          log,
		messaging:       mockMessaging,
		registry:        registry,
		signalHandler:   signalHandler,
		timeoutProvider: timeoutProvider,
	}

	srv.Echo().GET("/ready", app.readyCheck)

	return app, mockDB, mockMessaging
}

func TestApp_RegisterModule_Success(t *testing.T) {
	app, _, _ := createTestApp(t)

	module := &MockModule{name: "test-module"}
	module.On("Init", mock.Anything).Return(nil)

	err := app.RegisterModule(module)
	assert.NoError(t, err)

	// Verify module was added to registry
	assert.Len(t, app.registry.modules, 1)
	assert.Equal(t, "test-module", app.registry.modules[0].Name())

	module.AssertExpectations(t)
}

func TestApp_RegisterModule_InitError(t *testing.T) {
	app, _, _ := createTestApp(t)

	module := &MockModule{name: "failing-module"}
	expectedErr := errors.New("module init failed")
	module.On("Init", mock.Anything).Return(expectedErr)

	err := app.RegisterModule(module)
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)

	// Verify module was not added to registry
	assert.Len(t, app.registry.modules, 0)

	module.AssertExpectations(t)
}

func TestApp_ReadyCheck_Healthy(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

	// Setup mocks for healthy state
	mockDB.On("Health", mock.Anything).Return(nil)
	mockDB.On("Stats").Return(map[string]any{
		"open_connections": 5,
		"max_connections":  25,
	}, nil)
	mockMessaging.On("IsReady").Return(true)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])
	assert.Equal(t, "healthy", response["messaging"])
	assert.NotNil(t, response["time"])
	assert.NotNil(t, response["db_stats"])
	assert.NotNil(t, response["app"])

	// Verify app details
	appDetails := response["app"].(map[string]any)
	assert.Equal(t, "test-app", appDetails["name"])
	assert.Equal(t, "test", appDetails["environment"])
	assert.Equal(t, "v1.0.0-test", appDetails["version"])

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
}

func TestApp_ReadyCheck_UnhealthyDatabase(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

	// Setup mocks for unhealthy database
	expectedErr := errors.New("database connection failed")
	mockDB.On("Health", mock.Anything).Return(expectedErr)
	mockMessaging.On("IsReady").Return(true)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err) // Handler should not return error, but HTTP status should be 503

	// Verify response
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "not ready", response["status"])
	assert.Equal(t, "unhealthy", response["database"])
	assert.Equal(t, "database connection failed", response["error"])

	mockDB.AssertExpectations(t)
}

func TestApp_ReadyCheck_NoMessaging(t *testing.T) {
	app, mockDB, _ := createTestApp(t)

	// Disable messaging
	app.messaging = nil

	// Setup mocks for healthy database
	mockDB.On("Health", mock.Anything).Return(nil)
	mockDB.On("Stats").Return(map[string]any{
		"open_connections": 5,
	}, nil)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])
	assert.Equal(t, "disabled", response["messaging"])

	mockDB.AssertExpectations(t)
}

func TestApp_ReadyCheck_DatabaseStatsError(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

	// Setup mocks - healthy database but stats error
	mockDB.On("Health", mock.Anything).Return(nil)
	mockDB.On("Stats").Return(map[string]any{}, errors.New("stats unavailable"))
	mockMessaging.On("IsReady").Return(true)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])

	// Check that db_stats contains error
	dbStats := response["db_stats"].(map[string]any)
	assert.Equal(t, "stats unavailable", dbStats["error"])

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
}

func TestApp_Shutdown_Success(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)
	mockSrv := app.server.(*mockServer)

	// Add a test module
	module := &MockModule{name: "test-module"}
	module.On("Shutdown").Return(nil)
	app.registry.modules = append(app.registry.modules, module)

	// Setup mocks
	mockDB.On("Close").Return(nil)
	mockMessaging.On("Close").Return(nil)

	// Execute shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := app.Shutdown(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, mockSrv.shutdownCount())

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
	module.AssertExpectations(t)
}

func TestApp_Shutdown_WithErrors(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)
	mockSrv := app.server.(*mockServer)

	// Add a test module that fails shutdown
	module := &MockModule{name: "failing-module"}
	module.On("Shutdown").Return(errors.New("module shutdown failed"))
	app.registry.modules = append(app.registry.modules, module)

	// Setup mocks with errors
	mockDB.On("Close").Return(errors.New("database close failed"))
	mockMessaging.On("Close").Return(errors.New("messaging close failed"))

	// Execute shutdown - should complete even with errors
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := app.Shutdown(ctx)
	// Shutdown should not return error, but log the errors internally
	assert.NoError(t, err)
	assert.Equal(t, 1, mockSrv.shutdownCount())

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
	module.AssertExpectations(t)
}

func TestApp_Shutdown_NoMessaging(t *testing.T) {
	app, mockDB, _ := createTestApp(t)
	app.messaging = nil

	// Setup mocks
	mockDB.On("Close").Return(nil)

	// Execute shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := app.Shutdown(ctx)
	assert.NoError(t, err)

	mockDB.AssertExpectations(t)
}

// =============================================================================
// Application Lifecycle Tests
// =============================================================================

func TestApp_ReadyCheck_UnhealthyMessaging(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

	// Setup mocks for unhealthy messaging
	mockDB.On("Health", mock.Anything).Return(nil)
	mockDB.On("Stats").Return(map[string]any{
		"open_connections": 5,
	}, nil)
	mockMessaging.On("IsReady").Return(false) // Messaging not ready

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])
	assert.Equal(t, "unhealthy", response["messaging"])

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
}

func TestApp_RegisterMessaging_Success(t *testing.T) {
	app, _, mockMessaging := createTestApp(t)

	// Add a test module with messaging registration
	module := &MockModule{name: "messaging-module"}
	module.On("RegisterMessaging", mock.Anything).Return()
	app.registry.modules = append(app.registry.modules, module)

	// Mock the messaging operations that will be called during RegisterMessaging
	mockMessaging.On("IsReady").Return(true)

	err := app.registry.RegisterMessaging()
	assert.NoError(t, err)

	module.AssertExpectations(t)
}

func TestApp_RegisterMessaging_NoMessagingClient(t *testing.T) {
	app, _, _ := createTestApp(t)

	// Set messaging to nil to simulate no messaging client
	app.registry.deps.Messaging = nil
	// Recreate registry without messaging
	app.registry = NewModuleRegistry(app.registry.deps)

	err := app.registry.RegisterMessaging()
	// Should not return error when no messaging client is available
	assert.NoError(t, err)
}

func TestApp_Shutdown_ServerShutdownError(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)
	mockSrv := app.server.(*mockServer)
	mockSrv.shutdownErr = errors.New("server shutdown failed")

	// Mock server that fails to shutdown gracefully
	// Since we can't easily mock the echo server, we'll focus on the shutdown logic
	mockDB.On("Close").Return(nil)
	mockMessaging.On("Close").Return(nil)

	// Execute shutdown with very short timeout to test timeout scenario
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	err := app.Shutdown(ctx)
	// Shutdown should not return error even if server shutdown fails
	assert.NoError(t, err)
	assert.Equal(t, 1, mockSrv.shutdownCount())

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
}

// =============================================================================
// Run() Method Tests - Application Lifecycle
// =============================================================================

func TestApp_Run_Success(t *testing.T) {
	mockSignalHandler := NewMockSignalHandler()
	mockTimeoutProvider := &MockTimeoutProvider{}

	app, mockDB, mockMessaging := createTestAppWithMocks(mockSignalHandler, mockTimeoutProvider)
	mockSrv := app.server.(*mockServer)

	// Setup mocks for successful run
	mockSignalHandler.On("Notify", mock.Anything, []os.Signal{os.Interrupt, syscall.SIGTERM}).Return()
	mockSignalHandler.On("WaitForSignal", mock.Anything).Return()

	// Mock timeout creation for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mockTimeoutProvider.On("WithTimeout", context.Background(), 10*time.Second).Return(ctx, cancel)

	// Setup mocks for shutdown sequence
	mockDB.On("Close").Return(nil)
	mockMessaging.On("Close").Return(nil)

	// Mock messaging client health check during infrastructure declaration
	mockMessaging.On("IsReady").Return(true)

	// Test module for registry
	module := &MockModule{name: "test-module"}
	module.On("RegisterRoutes", mock.Anything, mock.Anything).Return()
	module.On("RegisterMessaging", mock.Anything).Return()
	module.On("Shutdown").Return(nil)
	app.registry.modules = append(app.registry.modules, module)

	// Run the app in a goroutine since it blocks
	var runErr error
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		runErr = app.Run()
	}()

	// Give the app time to start
	time.Sleep(50 * time.Millisecond)

	// Send shutdown signal
	mockSignalHandler.TriggerShutdown()

	// Wait for shutdown to complete
	select {
	case <-done:
		// Expected completion
	case <-time.After(2 * time.Second):
		t.Fatal("App.Run() did not complete in expected time")
	}

	assert.NoError(t, runErr)
	assert.Equal(t, 1, mockSrv.startCount())
	assert.Equal(t, 1, mockSrv.shutdownCount())
	mockSignalHandler.AssertExpectations(t)
	mockTimeoutProvider.AssertExpectations(t)
	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
	module.AssertExpectations(t)
}

func TestApp_Run_IgnoresServerClosedError(t *testing.T) {
	mockSignalHandler := NewMockSignalHandler()
	mockTimeoutProvider := &MockTimeoutProvider{}

	app, mockDB, mockMessaging := createTestAppWithMocks(mockSignalHandler, mockTimeoutProvider)
	mockSrv := app.server.(*mockServer)
	mockSrv.startErr = http.ErrServerClosed

	mockSignalHandler.On("Notify", mock.Anything, []os.Signal{os.Interrupt, syscall.SIGTERM}).Return()
	mockSignalHandler.On("WaitForSignal", mock.Anything).Return()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mockTimeoutProvider.On("WithTimeout", context.Background(), 10*time.Second).Return(ctx, cancel)

	mockDB.On("Close").Return(nil)
	mockMessaging.On("Close").Return(nil)
	mockMessaging.On("IsReady").Return(true)

	module := &MockModule{name: "test-module"}
	module.On("RegisterRoutes", mock.Anything, mock.Anything).Return()
	module.On("RegisterMessaging", mock.Anything).Return()
	module.On("Shutdown").Return(nil)
	app.registry.modules = append(app.registry.modules, module)

	var runErr error
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		runErr = app.Run()
	}()

	time.Sleep(50 * time.Millisecond)
	mockSignalHandler.TriggerShutdown()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("App.Run() did not complete in expected time")
	}

	assert.NoError(t, runErr)
	assert.Equal(t, 1, mockSrv.startCount())
	assert.Equal(t, 1, mockSrv.shutdownCount())
	mockSignalHandler.AssertExpectations(t)
	mockTimeoutProvider.AssertExpectations(t)
	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
	module.AssertExpectations(t)
}

// =============================================================================
// New() Constructor Tests - Factory Method Testing
// =============================================================================

func TestApp_New_DefaultConfig(t *testing.T) {
	app, err := New()
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.NotNil(t, app.server)
	assert.Nil(t, app.db)
	assert.Nil(t, app.messaging)
}

func TestApp_New_ConfigLoadError(t *testing.T) {
	originalLoader := configLoader
	configLoader = func() (*config.Config, error) {
		return nil, errors.New("load failed")
	}
	t.Cleanup(func() { configLoader = originalLoader })

	app, err := New()
	assert.Error(t, err)
	assert.Nil(t, app)
}

func TestApp_NewWithConfig_UsesConnectors(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Type:     config.PostgreSQL,
			Host:     "db-host",
			Port:     5432,
			Database: "testdb",
			Username: "user",
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "amqp://broker",
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	dbMock := &MockDatabase{}
	msgMock := &MockMessagingClient{}
	var dbCalled, messagingCalled bool

	originalDB := databaseConnector
	databaseConnector = func(dbCfg *config.DatabaseConfig, _ logger.Logger) (dbpkg.Interface, error) {
		dbCalled = true
		assert.Equal(t, cfg.Database.Host, dbCfg.Host)
		return dbMock, nil
	}
	originalMessaging := messagingClientFactory
	messagingClientFactory = func(url string, _ logger.Logger) messaging.Client {
		messagingCalled = true
		assert.Equal(t, cfg.Messaging.BrokerURL, url)
		return msgMock
	}
	t.Cleanup(func() {
		databaseConnector = originalDB
		messagingClientFactory = originalMessaging
	})

	app, err := NewWithConfig(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.True(t, dbCalled)
	assert.True(t, messagingCalled)
	assert.Equal(t, dbMock, app.db)
	assert.Equal(t, msgMock, app.messaging)
}

func TestApp_NewWithConfig_DatabaseConnectorError(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Type:     config.PostgreSQL,
			Host:     "db-host",
			Port:     5432,
			Database: "testdb",
			Username: "user",
		},
		Messaging: config.MessagingConfig{},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	originalDB := databaseConnector
	databaseConnector = func(_ *config.DatabaseConfig, _ logger.Logger) (dbpkg.Interface, error) {
		return nil, errors.New("connection failed")
	}
	originalMessaging := messagingClientFactory
	messagingClientFactory = func(_ string, _ logger.Logger) messaging.Client {
		t.Fatalf("messaging factory should not be called on db failure")
		return nil
	}
	t.Cleanup(func() {
		databaseConnector = originalDB
		messagingClientFactory = originalMessaging
	})

	app, err := NewWithConfig(cfg, nil)
	assert.Error(t, err)
	assert.Nil(t, app)
}

func TestApp_NewWithConfig_UsesProvidedServer(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	providedServer := newMockServer()
	opts := &Options{Server: providedServer}

	app, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, providedServer, app.server)
}

func TestApp_NewWithConfig_DatabaseAndMessagingEnabled(t *testing.T) {
	// Create test config with both database and messaging enabled
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "test-host",
			Port: 5432,
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "amqp://test-broker:5672",
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	// Use mocks to avoid real connections
	mockDB := &MockDatabase{}
	mockMessaging := &MockMessagingClient{}
	mockSignalHandler := NewMockSignalHandler()
	mockTimeoutProvider := &MockTimeoutProvider{}

	opts := &Options{
		Database:        mockDB,
		MessagingClient: mockMessaging,
		SignalHandler:   mockSignalHandler,
		TimeoutProvider: mockTimeoutProvider,
	}

	app, err := NewWithConfig(cfg, opts)

	assert.NoError(t, err)
	assert.NotNil(t, app)
	assert.Equal(t, mockDB, app.db)
	assert.Equal(t, mockMessaging, app.messaging)
	assert.Equal(t, mockSignalHandler, app.signalHandler)
	assert.Equal(t, mockTimeoutProvider, app.timeoutProvider)
	assert.Equal(t, "test-app", app.cfg.App.Name)
}

func TestApp_NewWithConfig_DatabaseOnlyEnabled(t *testing.T) {
	// Create test config with only database enabled
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "test-host",
			Port: 5432,
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "", // Empty means disabled
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	mockDB := &MockDatabase{}

	opts := &Options{
		Database: mockDB,
	}

	app, err := NewWithConfig(cfg, opts)

	assert.NoError(t, err)
	assert.NotNil(t, app)
	assert.Equal(t, mockDB, app.db)
	assert.Nil(t, app.messaging) // Should be nil when not configured
}

func TestApp_NewWithConfig_MessagingOnlyEnabled(t *testing.T) {
	// Create test config with only messaging enabled
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			// Empty database config means disabled
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "amqp://test-broker:5672",
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	mockMessaging := &MockMessagingClient{}

	opts := &Options{
		MessagingClient: mockMessaging,
	}

	app, err := NewWithConfig(cfg, opts)

	assert.NoError(t, err)
	assert.NotNil(t, app)
	assert.Nil(t, app.db) // Should be nil when not configured
	assert.Equal(t, mockMessaging, app.messaging)
}

func TestStandardTimeoutProvider_WithTimeout(t *testing.T) {
	provider := &StandardTimeoutProvider{}

	start := time.Now()
	ctx, cancel := provider.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.WithinDuration(t, start.Add(5*time.Millisecond), deadline, 20*time.Millisecond)

	cancel()
	assert.Eventually(t, func() bool {
		return ctx.Err() == context.Canceled
	}, 10*time.Millisecond, time.Millisecond)
}

func TestOSSignalHandler_WaitForSignal(t *testing.T) {
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
		// expected
	case <-time.After(time.Second):
		t.Fatal("wait for signal timed out")
	}

	signal.Stop(signals)
}

func TestApp_NewWithConfig_NeitherEnabled(t *testing.T) {
	// Create test config with neither database nor messaging enabled
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Version: "v1.0.0",
			Env:     "test",
		},
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			// Empty database config means disabled
		},
		Messaging: config.MessagingConfig{
			BrokerURL: "", // Empty means disabled
		},
		Log: config.LogConfig{
			Level:  "info",
			Pretty: false,
		},
	}

	app, err := NewWithConfig(cfg, nil)

	assert.NoError(t, err)
	assert.NotNil(t, app)
	assert.Nil(t, app.db)        // Should be nil when not configured
	assert.Nil(t, app.messaging) // Should be nil when not configured
	assert.NotNil(t, app.server)
	assert.NotNil(t, app.logger)
	assert.NotNil(t, app.registry)
}

func TestApp_isDatabaseEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		expected bool
	}{
		{
			name: "enabled with host",
			config: &config.Config{
				Database: config.DatabaseConfig{
					Host: "localhost",
				},
			},
			expected: true,
		},
		{
			name: "enabled with type",
			config: &config.Config{
				Database: config.DatabaseConfig{
					Type: "postgresql",
				},
			},
			expected: true,
		},
		{
			name: "enabled with both",
			config: &config.Config{
				Database: config.DatabaseConfig{
					Host: "localhost",
					Type: "postgresql",
				},
			},
			expected: true,
		},
		{
			name: "disabled when empty",
			config: &config.Config{
				Database: config.DatabaseConfig{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDatabaseEnabled(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestApp_isMessagingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		expected bool
	}{
		{
			name: "enabled with broker URL",
			config: &config.Config{
				Messaging: config.MessagingConfig{
					BrokerURL: "amqp://localhost:5672",
				},
			},
			expected: true,
		},
		{
			name: "disabled when empty",
			config: &config.Config{
				Messaging: config.MessagingConfig{
					BrokerURL: "",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isMessagingEnabled(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// readyCheck Tests with Optional Dependencies
// =============================================================================

func TestApp_ReadyCheck_DatabaseDisabled(t *testing.T) {
	// Create app without database
	cfg := &config.Config{
		App:       config.AppConfig{Name: "test-app", Version: "v1.0.0", Env: "test"},
		Server:    config.ServerConfig{Host: "localhost", Port: 8080},
		Database:  config.DatabaseConfig{}, // Empty means disabled
		Messaging: config.MessagingConfig{BrokerURL: ""},
		Log:       config.LogConfig{Level: "info", Pretty: false},
	}

	app, err := NewWithConfig(cfg, nil)
	require.NoError(t, err)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err = app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse JSON response
	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "disabled", response["database"])
	assert.Equal(t, "disabled", response["messaging"])

	// Check db_stats shows disabled status
	dbStats := response["db_stats"].(map[string]any)
	assert.Equal(t, "disabled", dbStats["status"])
}

func TestApp_ReadyCheck_MessagingDisabled(t *testing.T) {
	app, mockDB, _ := createTestApp(t)

	// Disable messaging
	app.messaging = nil

	// Setup healthy database
	mockDB.On("Health", mock.Anything).Return(nil)
	mockDB.On("Stats").Return(map[string]any{"open_connections": 5}, nil)

	// Create test request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute ready check
	err := app.readyCheck(c)
	require.NoError(t, err)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])
	assert.Equal(t, "disabled", response["messaging"])

	mockDB.AssertExpectations(t)
}
