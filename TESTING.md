# Testing Guide for Go-Bricks

This guide provides comprehensive information on testing applications built with the go-bricks framework, including mock usage, testing patterns, and best practices.

## Table of Contents

1. [Overview](#overview)
2. [Testing Package Structure](#testing-package-structure)
3. [Database Testing](#database-testing)
4. [Messaging Testing](#messaging-testing)
5. [Module Testing](#module-testing)
6. [Best Practices](#best-practices)
7. [Migration Guide](#migration-guide)

## Overview

Go-bricks provides a comprehensive testing framework that includes:

- **Mocks**: Testify-based mock implementations for all major interfaces
- **Fixtures**: Pre-configured mocks and helpers for common scenarios
- **Examples**: Real-world testing patterns and integration examples

The testing utilities are designed to make testing go-bricks applications straightforward and consistent.

## Testing Package Structure

```
github.com/gaborage/go-bricks/testing/
├── mocks/           # Mock implementations
│   ├── database.go      # Database interface mocks
│   ├── statement.go     # Statement interface mocks
│   ├── transaction.go   # Transaction interface mocks
│   ├── messaging.go     # Messaging client mocks
│   ├── amqp.go         # AMQP client mocks
│   └── registry.go     # Registry interface mocks
└── fixtures/        # Helper functions and pre-configured mocks
    ├── database.go      # Database fixtures and builders
    └── messaging.go     # Messaging fixtures and builders
```

## Database Testing

### Basic Mock Usage

```go
import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"

    "github.com/gaborage/go-bricks/testing/mocks"
    "github.com/gaborage/go-bricks/testing/fixtures"
)

func TestUserService_GetUser(t *testing.T) {
    // Create a mock database
    mockDB := &mocks.MockDatabase{}

    // Set up expectations
    expectedRows := fixtures.NewMockRows(
        []string{"id", "name", "email"},
        [][]interface{}{
            {1, "John Doe", "john@example.com"},
        },
    )
    mockDB.ExpectQuery("SELECT * FROM users WHERE id = ?", expectedRows, nil)

    // Test your service
    service := NewUserService(mockDB)
    user, err := service.GetUser(context.Background(), 1)

    // Assertions
    assert.NoError(t, err)
    assert.Equal(t, "John Doe", user.Name)
    mockDB.AssertExpectations(t)
}
```

### Using Database Fixtures

```go
func TestUserService_HealthyDatabase(t *testing.T) {
    // Use a pre-configured healthy database
    mockDB := fixtures.NewHealthyDatabase()

    service := NewUserService(mockDB)
    err := service.CheckHealth(context.Background())

    assert.NoError(t, err)
}

func TestUserService_FailingDatabase(t *testing.T) {
    // Use a pre-configured failing database
    mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)

    service := NewUserService(mockDB)
    err := service.CheckHealth(context.Background())

    assert.Error(t, err)
}
```

### Transaction Testing

```go
func TestUserService_CreateUser(t *testing.T) {
    mockDB := &mocks.MockDatabase{}
    mockTx := fixtures.NewSuccessfulTransaction()

    // Expect transaction to be started
    mockDB.ExpectTransaction(mockTx, nil)

    // Expect operations within transaction
    result := fixtures.NewMockResult(1, 1) // lastInsertId=1, rowsAffected=1
    mockTx.ExpectExec("INSERT INTO users", result, nil)

    service := NewUserService(mockDB)
    user, err := service.CreateUser(context.Background(), "John", "john@example.com")

    assert.NoError(t, err)
    assert.Equal(t, int64(1), user.ID)
    mockDB.AssertExpectations(t)
    mockTx.AssertExpectations(t)
}
```

### Database-Specific Testing

```go
func TestUserService_PostgreSQL(t *testing.T) {
    mockDB := fixtures.NewPostgreSQLDatabase()

    // PostgreSQL-specific behavior testing
    service := NewUserService(mockDB)
    assert.Equal(t, "postgres", service.GetDatabaseType())
}

func TestUserService_Oracle(t *testing.T) {
    mockDB := fixtures.NewOracleDatabase()

    // Oracle-specific behavior testing
    service := NewUserService(mockDB)
    assert.Equal(t, "oracle", service.GetDatabaseType())
}
```

## Messaging Testing

### Basic Messaging Mock Usage

```go
func TestEventService_PublishEvent(t *testing.T) {
    mockClient := &mocks.MockMessagingClient{}

    // Set up publish expectation
    expectedData := []byte(`{"event": "user.created", "user_id": 1}`)
    mockClient.ExpectPublish("user.events", expectedData, nil)
    mockClient.ExpectIsReady(true)

    service := NewEventService(mockClient)
    err := service.PublishUserCreated(context.Background(), 1)

    assert.NoError(t, err)
    mockClient.AssertExpectations(t)
}
```

### Message Simulation

```go
func TestEventService_ConsumeEvents(t *testing.T) {
    mockClient := fixtures.NewMessageSimulator(
        []byte(`{"event": "user.created", "user_id": 1}`),
        []byte(`{"event": "user.updated", "user_id": 1}`),
    )

    service := NewEventService(mockClient)

    // Start consuming (this would typically run in a goroutine)
    messages, err := service.StartConsuming(context.Background(), "user.events")
    assert.NoError(t, err)

    // Simulate additional messages
    mockClient.SimulateMessage("user.events", []byte(`{"event": "user.deleted", "user_id": 1}`))

    // Test message processing
    // ... your test logic here
}
```

### AMQP Infrastructure Testing

```go
func TestModule_RegisterInfrastructure(t *testing.T) {
    mockRegistry := fixtures.NewWorkingRegistry()

    module := &UserModule{}
    module.RegisterMessaging(mockRegistry)

    // Verify infrastructure was registered
    exchanges := mockRegistry.GetExchanges()
    assert.Contains(t, exchanges, "user.events")

    queues := mockRegistry.GetQueues()
    assert.Contains(t, queues, "user.notifications")

    mockRegistry.AssertExpectations(t)
}
```

### Testing Message Handlers

```go
func TestUserMessageHandler_HandleUserCreated(t *testing.T) {
    handler := &UserMessageHandler{}

    // Create a mock delivery
    delivery := fixtures.NewJSONDelivery([]byte(`{
        "event": "user.created",
        "user_id": 1,
        "user_name": "John Doe"
    }`))

    err := handler.Handle(context.Background(), &delivery)

    assert.NoError(t, err)
    // Assert side effects of message processing
}
```

### Failure Scenarios

```go
func TestEventService_PublishFailure(t *testing.T) {
    mockClient := fixtures.NewFailingMessagingClient(0) // Fail immediately

    service := NewEventService(mockClient)
    err := service.PublishUserCreated(context.Background(), 1)

    assert.Error(t, err)
    assert.Contains(t, err.Error(), "connection closed")
}

func TestEventService_RetryLogic(t *testing.T) {
    mockClient := fixtures.NewFailingMessagingClient(2) // Succeed twice, then fail

    service := NewEventService(mockClient)

    // First two calls should succeed
    assert.NoError(t, service.PublishUserCreated(context.Background(), 1))
    assert.NoError(t, service.PublishUserCreated(context.Background(), 2))

    // Third call should fail
    assert.Error(t, service.PublishUserCreated(context.Background(), 3))
}
```

## Module Testing

### Complete Module Testing

```go
func TestUserModule_Integration(t *testing.T) {
    // Set up mocks
    mockDB := fixtures.NewHealthyDatabase()
    mockMessaging := fixtures.NewWorkingMessagingClient()
    mockRegistry := fixtures.NewWorkingRegistry()
    mockLogger := &mocks.MockLogger{} // Assuming you have a logger mock
    mockConfig := &config.Config{}   // Use real config or mock

    // Create module dependencies
    deps := &app.ModuleDeps{
        DB:        mockDB,
        Messaging: mockMessaging,
        Logger:    mockLogger,
        Config:    mockConfig,
    }

    // Initialize module
    module := &UserModule{}
    err := module.Init(deps)
    assert.NoError(t, err)

    // Test route registration
    echo := echo.New()
    hr := server.NewHandlerRegistry()
    module.RegisterRoutes(hr, echo)

    // Test messaging registration
    module.RegisterMessaging(mockRegistry)

    // Verify registrations
    exchanges := mockRegistry.GetExchanges()
    assert.NotEmpty(t, exchanges)

    // Test cleanup
    err = module.Shutdown()
    assert.NoError(t, err)

    // Assert all expectations
    mockDB.AssertExpectations(t)
    mockMessaging.AssertExpectations(t)
    mockRegistry.AssertExpectations(t)
}
```

### HTTP Handler Testing

```go
func TestUserHandler_GetUser(t *testing.T) {
    mockDB := fixtures.NewDatabaseWithData(map[string][]interface{}{
        "SELECT * FROM users WHERE id = ?": {
            []interface{}{1, "John Doe", "john@example.com"},
        },
    })

    handler := &UserHandler{db: mockDB}

    // Create test request
    req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
    rec := httptest.NewRecorder()

    e := echo.New()
    c := e.NewContext(req, rec)
    c.SetParamNames("id")
    c.SetParamValues("1")

    // Execute handler
    err := handler.GetUser(c)
    assert.NoError(t, err)

    // Verify response
    assert.Equal(t, http.StatusOK, rec.Code)
    assert.Contains(t, rec.Body.String(), "John Doe")
}
```

## Best Practices

### 1. Use Fixtures for Common Scenarios

```go
// Good: Use fixtures for common scenarios
mockDB := fixtures.NewHealthyDatabase()

// Less ideal: Set up every expectation manually
mockDB := &mocks.MockDatabase{}
mockDB.On("Health", mock.Anything).Return(nil)
mockDB.On("DatabaseType").Return("mock")
// ... many more setup calls
```

### 2. Test Both Success and Failure Paths

```go
func TestUserService_GetUser(t *testing.T) {
    t.Run("success", func(t *testing.T) {
        mockDB := fixtures.NewHealthyDatabase()
        // ... test success case
    })

    t.Run("database_failure", func(t *testing.T) {
        mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)
        // ... test failure case
    })

    t.Run("user_not_found", func(t *testing.T) {
        mockDB := fixtures.NewHealthyDatabase()
        mockDB.ExpectQuery("SELECT * FROM users", nil, sql.ErrNoRows)
        // ... test not found case
    })
}
```

### 3. Assert Mock Expectations

Always call `AssertExpectations(t)` to verify that all expected calls were made:

```go
func TestExample(t *testing.T) {
    mockDB := &mocks.MockDatabase{}
    // ... set up expectations and test

    mockDB.AssertExpectations(t) // This is important!
}
```

### 4. Use Context in Tests

Always use context in your tests, even if it's just `context.Background()`:

```go
func TestExample(t *testing.T) {
    ctx := context.Background()
    result, err := service.DoSomething(ctx)
    // ...
}
```

### 5. Test Configuration Injection

```go
func TestService_ConfigInjection(t *testing.T) {
    cfg := &config.Config{}
    cfg.Set("custom.service.timeout", "30s")

    deps := &app.ModuleDeps{Config: cfg}

    service := &MyService{}
    err := service.Init(deps) // This should inject config
    assert.NoError(t, err)

    assert.Equal(t, 30*time.Second, service.config.Timeout)
}
```

## Migration Guide

### From Internal Mocks to Framework Mocks

If you're currently using internal mock implementations, here's how to migrate:

**Before (internal mocks):**
```go
type MockDB struct {
    // custom implementation
}

func TestExample(t *testing.T) {
    mockDB := &MockDB{}
    // custom setup
}
```

**After (framework mocks):**
```go
import "github.com/gaborage/go-bricks/testing/mocks"

func TestExample(t *testing.T) {
    mockDB := &mocks.MockDatabase{}
    mockDB.ExpectHealthCheck(true)
}
```

### From sqlmock to Framework Database Mocks

**Before (sqlmock):**
```go
db, mock, err := sqlmock.New()
require.NoError(t, err)

rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
mock.ExpectQuery("SELECT").WillReturnRows(rows)
```

**After (framework mocks):**
```go
mockDB := &mocks.MockDatabase{}
rows := fixtures.NewMockRows([]string{"id", "name"}, [][]interface{}{{1, "John"}})
mockDB.ExpectQuery("SELECT", rows, nil)
```

### Updating Test Dependencies

Update your `go.mod` to include the testing package:

```go
// go.mod
require (
    github.com/gaborage/go-bricks v1.x.x
    github.com/stretchr/testify v1.x.x
)
```

Update your imports:

```go
import (
    "github.com/gaborage/go-bricks/testing/mocks"
    "github.com/gaborage/go-bricks/testing/fixtures"
)
```

## Examples

For more comprehensive examples, see the `examples/testing` directory which contains:

- Complete module testing examples
- Integration test patterns
- HTTP handler testing
- Message handler testing
- Configuration testing
- Error scenario testing

## Troubleshooting

### Common Issues

1. **Mock expectations not met**: Always call `AssertExpectations(t)` and check that your test actually calls the expected methods.

2. **Interface compatibility**: Ensure your mocks implement the correct interfaces by checking compilation errors.

3. **Context handling**: Make sure you're passing context consistently in both your code and tests.

4. **Resource cleanup**: Use `defer` statements or test teardown functions to clean up resources and close mocks properly.

### Getting Help

- Check the examples in `examples/testing`
- Review this documentation
- Look at existing tests in the framework for patterns
- Open an issue in the go-bricks repository for framework-specific questions