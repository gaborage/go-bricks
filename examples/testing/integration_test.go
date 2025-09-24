package testing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"github.com/gaborage/go-bricks/testing/fixtures"
	"github.com/gaborage/go-bricks/testing/mocks"
)

const (
	userEventsExchangeName  = "user.events"
	userNotificationsQueue  = "user.notifications"
	userCreated             = "user.created"
	userDeleted             = "user.deleted"
	userUpdated             = "user.updated"
	usersWildcardRoutingKey = "user.*"
)

// Example complete module for demonstration
type UserModule struct {
	db      types.Interface
	logger  logger.Logger
	client  messaging.Client
	service *UserService
}

func (m *UserModule) Name() string {
	return "user"
}

func (m *UserModule) Init(deps *app.ModuleDeps) error {
	m.db = deps.DB
	m.logger = deps.Logger
	m.client = deps.Messaging

	// Initialize service
	m.service = NewUserService(m.db)

	return nil
}

func (m *UserModule) RegisterRoutes(_ *server.HandlerRegistry, e *echo.Echo) {
	// Register HTTP routes
	e.GET("/users/:id", m.getUser)
	e.POST("/users", m.createUser)
}

func (m *UserModule) RegisterMessaging(registry messaging.RegistryInterface) {
	// Register messaging infrastructure
	registry.RegisterExchange(&messaging.ExchangeDeclaration{
		Name:    userEventsExchangeName,
		Type:    "topic",
		Durable: true,
	})

	registry.RegisterQueue(&messaging.QueueDeclaration{
		Name:    userNotificationsQueue,
		Durable: true,
	})

	registry.RegisterBinding(&messaging.BindingDeclaration{
		Queue:      userNotificationsQueue,
		Exchange:   userEventsExchangeName,
		RoutingKey: usersWildcardRoutingKey,
	})

	registry.RegisterPublisher(&messaging.PublisherDeclaration{
		Exchange:    userEventsExchangeName,
		RoutingKey:  userCreated,
		EventType:   userCreated,
		Description: "Published when a user is created",
	})
}

func (m *UserModule) Shutdown() error {
	// Cleanup resources
	return nil
}

// HTTP handlers
func (m *UserModule) getUser(c echo.Context) error {
	// Implementation would go here
	return c.JSON(http.StatusOK, map[string]string{"message": "User found"})
}

func (m *UserModule) createUser(c echo.Context) error {
	// Implementation would go here
	return c.JSON(http.StatusCreated, map[string]string{"message": "User created"})
}

// Mock logger for testing
type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) noop() logger.LogEvent {
	args := m.Called()
	return args.Get(0).(logger.LogEvent)
}

func (m *MockLogger) Info() logger.LogEvent {
	return m.noop()
}

func (m *MockLogger) Error() logger.LogEvent {
	return m.noop()
}

func (m *MockLogger) Debug() logger.LogEvent {
	return m.noop()
}

func (m *MockLogger) Warn() logger.LogEvent {
	return m.noop()
}

func (m *MockLogger) Fatal() logger.LogEvent {
	return m.noop()
}

func (m *MockLogger) WithContext(ctx any) logger.Logger {
	args := m.Called(ctx)
	return args.Get(0).(logger.Logger)
}

func (m *MockLogger) WithFields(fields map[string]any) logger.Logger {
	args := m.Called(fields)
	return args.Get(0).(logger.Logger)
}

// Mock LogEvent for testing
type MockLogEvent struct {
	mock.Mock
}

func (m *MockLogEvent) Str(key, val string) logger.LogEvent {
	args := m.Called(key, val)
	return args.Get(0).(logger.LogEvent)
}

func (m *MockLogEvent) Int(key string, val int) logger.LogEvent {
	args := m.Called(key, val)
	return args.Get(0).(logger.LogEvent)
}

func (m *MockLogEvent) Msg(msg string) {
	m.Called(msg)
}

// Integration Test Examples

// TestUserModule_CompleteIntegration demonstrates full module testing with all dependencies
func TestUserModuleCompleteIntegration(t *testing.T) {
	// Set up all mocks
	mockDB := fixtures.NewHealthyDatabase()
	mockMessaging := fixtures.NewWorkingMessagingClient()
	mockRegistry := fixtures.NewWorkingRegistry()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure logger mock (simplified for this example)
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	// Create configuration
	cfg := &config.Config{}

	// Create module dependencies
	deps := &app.ModuleDeps{
		DB:        mockDB,
		Messaging: mockMessaging,
		Logger:    mockLogger,
		Config:    cfg,
	}

	// Initialize module
	module := &UserModule{}
	err := module.Init(deps)
	assert.NoError(t, err)

	// Test route registration
	e := echo.New()
	hr := server.NewHandlerRegistry(&config.Config{})
	module.RegisterRoutes(hr, e)

	// Verify routes are registered (basic check)
	routes := e.Routes()
	assert.NotEmpty(t, routes)

	// Test messaging registration (just verify it doesn't panic)
	module.RegisterMessaging(mockRegistry)

	// Test module shutdown
	err = module.Shutdown()
	assert.NoError(t, err)
}

// TestUserModule_HTTPHandlers demonstrates HTTP handler testing
func TestUserModuleHTTPHandlers(t *testing.T) {
	// Set up minimal dependencies for HTTP testing
	mockDB := fixtures.NewHealthyDatabase()
	mockMessaging := fixtures.NewWorkingMessagingClient()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure basic logger expectations
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		DB:        mockDB,
		Messaging: mockMessaging,
		Logger:    mockLogger,
		Config:    &config.Config{},
	}

	// Initialize module
	module := &UserModule{}
	err := module.Init(deps)
	assert.NoError(t, err)

	// Set up Echo instance
	e := echo.New()
	hr := server.NewHandlerRegistry(&config.Config{})
	module.RegisterRoutes(hr, e)

	t.Run("get_user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/users/1", http.NoBody)
		rec := httptest.NewRecorder()

		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("1")

		err := module.getUser(c)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "User found")
	})

	t.Run("create_user", func(t *testing.T) {
		userJSON := `{"name": "John Doe", "email": "john@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(userJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		c := e.NewContext(req, rec)

		err := module.createUser(c)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Contains(t, rec.Body.String(), "User created")
	})
}

// TestUserModule_MessagingIntegration demonstrates messaging integration testing
func TestUserModuleMessagingIntegration(t *testing.T) {
	mockDB := fixtures.NewHealthyDatabase()
	mockMessaging := fixtures.NewWorkingMessagingClient()
	mockRegistry := mocks.NewMockRegistry()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Set up specific messaging expectations
	mockRegistry.On("RegisterExchange", mock.MatchedBy(func(decl *messaging.ExchangeDeclaration) bool {
		return decl.Name == userEventsExchangeName && decl.Type == "topic"
	})).Return()

	mockRegistry.On("RegisterQueue", mock.MatchedBy(func(decl *messaging.QueueDeclaration) bool {
		return decl.Name == userNotificationsQueue
	})).Return()

	mockRegistry.On("RegisterBinding", mock.MatchedBy(func(decl *messaging.BindingDeclaration) bool {
		return decl.Queue == userNotificationsQueue && decl.Exchange == userEventsExchangeName
	})).Return()

	mockRegistry.On("RegisterPublisher", mock.MatchedBy(func(decl *messaging.PublisherDeclaration) bool {
		return decl.EventType == userCreated
	})).Return()

	// Configure logger
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		DB:        mockDB,
		Messaging: mockMessaging,
		Logger:    mockLogger,
		Config:    &config.Config{},
	}

	// Initialize and test module
	module := &UserModule{}
	err := module.Init(deps)
	assert.NoError(t, err)

	// Test messaging registration
	module.RegisterMessaging(mockRegistry)

	// Verify all messaging registrations occurred
	mockRegistry.AssertExpectations(t)
}

// TestUserModule_ErrorScenarios demonstrates error scenario testing
func TestUserModuleErrorScenarios(t *testing.T) {
	t.Run("database_failure", func(t *testing.T) {
		mockDB := fixtures.NewFailingDatabase(assert.AnError)
		mockMessaging := fixtures.NewWorkingMessagingClient()
		mockLogger := &MockLogger{}
		mockLogEvent := &MockLogEvent{}

		// Configure logger for potential error logging
		mockLogger.On("Error").Return(mockLogEvent).Maybe()
		mockLogger.On("Info").Return(mockLogEvent).Maybe()
		mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
		mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

		deps := &app.ModuleDeps{
			DB:        mockDB,
			Messaging: mockMessaging,
			Logger:    mockLogger,
			Config:    &config.Config{},
		}

		module := &UserModule{}
		err := module.Init(deps)

		// Module init should succeed even if DB is unhealthy (depends on implementation)
		assert.NoError(t, err)

		// But service operations should fail
		user, err := module.service.GetUser(context.Background(), 1)
		assert.Error(t, err)
		assert.Nil(t, user)
	})

	t.Run("messaging_failure", func(t *testing.T) {
		mockDB := fixtures.NewHealthyDatabase()
		mockMessaging := fixtures.NewFailingMessagingClient(0) // Fail immediately
		mockLogger := &MockLogger{}
		mockLogEvent := &MockLogEvent{}

		mockLogger.On("Error").Return(mockLogEvent).Maybe()
		mockLogger.On("Info").Return(mockLogEvent).Maybe()
		mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
		mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

		deps := &app.ModuleDeps{
			DB:        mockDB,
			Messaging: mockMessaging,
			Logger:    mockLogger,
			Config:    &config.Config{},
		}

		module := &UserModule{}
		err := module.Init(deps)
		assert.NoError(t, err)

		// Messaging should not be ready
		assert.False(t, mockMessaging.IsReady())
	})
}

// TestUserModule_ConfigurationInjection demonstrates configuration testing
func TestUserModuleConfigurationInjection(t *testing.T) {
	// This test would demonstrate how to test configuration injection
	// For now, it's a placeholder since the example module doesn't use config injection

	cfg := &config.Config{}
	// Set some configuration values
	// cfg.Set("user.timeout", "30s")

	deps := &app.ModuleDeps{
		DB:        fixtures.NewHealthyDatabase(),
		Messaging: fixtures.NewWorkingMessagingClient(),
		Logger:    &MockLogger{},
		Config:    cfg,
	}

	module := &UserModule{}
	err := module.Init(deps)
	assert.NoError(t, err)

	// Test that configuration was properly injected
	// This would depend on the actual module implementation
	// assert.Equal(t, 30*time.Second, module.someService.timeout)
}

// TestUserModule_LifecycleManagement demonstrates module lifecycle testing
func TestUserModuleLifecycleManagement(t *testing.T) {
	mockDB := fixtures.NewHealthyDatabase()
	mockMessaging := fixtures.NewWorkingMessagingClient()
	mockRegistry := fixtures.NewWorkingRegistry()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure logger
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		DB:        mockDB,
		Messaging: mockMessaging,
		Logger:    mockLogger,
		Config:    &config.Config{},
	}

	module := &UserModule{}

	// Test initialization
	err := module.Init(deps)
	assert.NoError(t, err)
	assert.NotNil(t, module.service)

	// Test route and messaging registration
	e := echo.New()
	hr := server.NewHandlerRegistry(&config.Config{})
	module.RegisterRoutes(hr, e)
	module.RegisterMessaging(mockRegistry)

	// Test shutdown
	err = module.Shutdown()
	assert.NoError(t, err)

	// Verify all mocks were used as expected
	mockRegistry.AssertExpectations(t)
}
