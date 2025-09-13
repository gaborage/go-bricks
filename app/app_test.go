package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// MockDatabase implements the database.Interface for testing
type MockDatabase struct {
	mock.Mock
}

func (m *MockDatabase) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	argsList := m.Called(ctx, query, args)
	return argsList.Get(0).(*sql.Rows), argsList.Error(1)
}

func (m *MockDatabase) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	argsList := m.Called(ctx, query, args)
	return argsList.Get(0).(*sql.Row)
}

func (m *MockDatabase) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
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

func (m *MockDatabase) Stats() (map[string]interface{}, error) {
	argsList := m.Called()
	return argsList.Get(0).(map[string]interface{}), argsList.Error(1)
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

	registry := &ModuleRegistry{
		modules: make([]Module, 0),
		deps: &ModuleDeps{
			DB:        mockDB,
			Logger:    log,
			Messaging: mockMessaging,
			Config:    &config.Config{},
		},
		logger:            log,
		messagingRegistry: nil,
	}

	// Create test server
	srv := server.New(cfg, log)

	app := &App{
		cfg:       cfg,
		server:    srv,
		db:        mockDB,
		logger:    log,
		messaging: mockMessaging,
		registry:  registry,
	}

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
	mockDB.On("Stats").Return(map[string]interface{}{
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
	var response map[string]interface{}
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])
	assert.Equal(t, "healthy", response["messaging"])
	assert.NotNil(t, response["time"])
	assert.NotNil(t, response["db_stats"])
	assert.NotNil(t, response["app"])

	// Verify app details
	appDetails := response["app"].(map[string]interface{})
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
	var response map[string]interface{}
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
	mockDB.On("Stats").Return(map[string]interface{}{
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
	var response map[string]interface{}
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
	mockDB.On("Stats").Return(map[string]interface{}{}, errors.New("stats unavailable"))
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
	var response map[string]interface{}
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "healthy", response["database"])

	// Check that db_stats contains error
	dbStats := response["db_stats"].(map[string]interface{})
	assert.Equal(t, "stats unavailable", dbStats["error"])

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
}

func TestApp_Shutdown_Success(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

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

	mockDB.AssertExpectations(t)
	mockMessaging.AssertExpectations(t)
	module.AssertExpectations(t)
}

func TestApp_Shutdown_WithErrors(t *testing.T) {
	app, mockDB, mockMessaging := createTestApp(t)

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
