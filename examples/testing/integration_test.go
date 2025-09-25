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
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"github.com/gaborage/go-bricks/testing/fixtures"
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
	deps    *app.ModuleDeps
	logger  logger.Logger
	service *UserService
}

func (m *UserModule) Name() string {
	return "user"
}

func (m *UserModule) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	m.logger = deps.Logger

	// Initialize service with dynamic database access
	m.service = NewUserService(nil) // Updated to not require DB in constructor

	return nil
}

func (m *UserModule) RegisterRoutes(_ *server.HandlerRegistry, e *echo.Echo) {
	// Register HTTP routes
	e.GET("/users/:id", m.getUser)
	e.POST("/users", m.createUser)
}

func (m *UserModule) DeclareMessaging(decls *messaging.Declarations) {
	// Declare messaging infrastructure
	decls.RegisterExchange(&messaging.ExchangeDeclaration{
		Name:    userEventsExchangeName,
		Type:    "topic",
		Durable: true,
	})

	decls.RegisterQueue(&messaging.QueueDeclaration{
		Name:    userNotificationsQueue,
		Durable: true,
	})

	decls.RegisterBinding(&messaging.BindingDeclaration{
		Queue:      userNotificationsQueue,
		Exchange:   userEventsExchangeName,
		RoutingKey: usersWildcardRoutingKey,
	})

	decls.RegisterPublisher(&messaging.PublisherDeclaration{
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
	mockMessaging := fixtures.NewWorkingAMQPClient()
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
		Logger: mockLogger,
		Config: cfg,
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
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

	// Test messaging declaration (just verify it doesn't panic)
	decls := messaging.NewDeclarations()
	module.DeclareMessaging(decls)

	// Test module shutdown
	err = module.Shutdown()
	assert.NoError(t, err)
}

// TestUserModule_HTTPHandlers demonstrates HTTP handler testing
func TestUserModuleHTTPHandlers(t *testing.T) {
	// Set up minimal dependencies for HTTP testing
	mockDB := fixtures.NewHealthyDatabase()
	mockMessaging := fixtures.NewWorkingAMQPClient()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure basic logger expectations
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		Logger: mockLogger,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
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
	mockMessaging := fixtures.NewWorkingAMQPClient()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure logger
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		Logger: mockLogger,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
	}

	// Initialize and test module
	module := &UserModule{}
	err := module.Init(deps)
	assert.NoError(t, err)

	// Test messaging declarations
	decls := messaging.NewDeclarations()
	module.DeclareMessaging(decls)

	// Verify all messaging declarations are present
	stats := decls.Stats()
	assert.Equal(t, 1, stats.Exchanges)
	assert.Equal(t, 1, stats.Queues)
	assert.Equal(t, 1, stats.Bindings)
	assert.Equal(t, 1, stats.Publishers)
	assert.Equal(t, 0, stats.Consumers)

	// Verify specific declarations
	exchanges := decls.Exchanges
	assert.Contains(t, exchanges, userEventsExchangeName)
	assert.Equal(t, "topic", exchanges[userEventsExchangeName].Type)

	queues := decls.Queues
	assert.Contains(t, queues, userNotificationsQueue)
	assert.True(t, queues[userNotificationsQueue].Durable)

	// Verify validation passes
	err = decls.Validate()
	assert.NoError(t, err)
}

// TestUserModule_ErrorScenarios demonstrates error scenario testing
func TestUserModuleErrorScenarios(t *testing.T) {
	t.Run("database_failure", func(t *testing.T) {
		mockDB := fixtures.NewFailingDatabase(assert.AnError)
		mockMessaging := fixtures.NewWorkingAMQPClient()
		mockLogger := &MockLogger{}
		mockLogEvent := &MockLogEvent{}

		// Configure logger for potential error logging
		mockLogger.On("Error").Return(mockLogEvent).Maybe()
		mockLogger.On("Info").Return(mockLogEvent).Maybe()
		mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
		mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

		deps := &app.ModuleDeps{
			Logger: mockLogger,
			Config: &config.Config{},
			GetDB: func(_ context.Context) (database.Interface, error) {
				return mockDB, nil
			},
			GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
				return mockMessaging, nil
			},
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
		mockMessaging := fixtures.NewFailingAMQPClient() // Fail immediately
		mockLogger := &MockLogger{}
		mockLogEvent := &MockLogEvent{}

		mockLogger.On("Error").Return(mockLogEvent).Maybe()
		mockLogger.On("Info").Return(mockLogEvent).Maybe()
		mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
		mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

		deps := &app.ModuleDeps{
			Logger: mockLogger,
			Config: &config.Config{},
			GetDB: func(_ context.Context) (database.Interface, error) {
				return mockDB, nil
			},
			GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
				return mockMessaging, nil
			},
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
		Logger: &MockLogger{},
		Config: cfg,
		GetDB: func(_ context.Context) (database.Interface, error) {
			return fixtures.NewHealthyDatabase(), nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return fixtures.NewWorkingAMQPClient(), nil
		},
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
	mockMessaging := fixtures.NewWorkingAMQPClient()
	mockLogger := &MockLogger{}
	mockLogEvent := &MockLogEvent{}

	// Configure logger
	mockLogger.On("Info").Return(mockLogEvent).Maybe()
	mockLogger.On("Debug").Return(mockLogEvent).Maybe()
	mockLogEvent.On("Str", mock.Anything, mock.Anything).Return(mockLogEvent).Maybe()
	mockLogEvent.On("Msg", mock.Anything).Return().Maybe()

	deps := &app.ModuleDeps{
		Logger: mockLogger,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
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

	decls := messaging.NewDeclarations()
	module.DeclareMessaging(decls)

	// Test shutdown
	err = module.Shutdown()
	assert.NoError(t, err)
}
