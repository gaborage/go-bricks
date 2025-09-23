# Go-Bricks Testing Examples

This directory contains comprehensive examples demonstrating how to test applications built with the go-bricks framework using the built-in testing utilities.

## Overview

The examples in this directory show real-world testing patterns for:

- **Database operations** - Mocking database interfaces, transactions, and error scenarios
- **Messaging systems** - Testing publishers, consumers, and message handlers
- **Module integration** - Complete module lifecycle testing with all dependencies
- **HTTP handlers** - Testing REST API endpoints and request/response handling
- **Configuration injection** - Testing service configuration and dependency management

## Files

### `database_test.go`
Demonstrates database testing patterns including:
- Basic mock setup and expectations
- Transaction testing (success and failure scenarios)
- Health check testing
- Database-specific behavior testing (PostgreSQL, Oracle, MongoDB)
- Error scenario testing
- Using fixtures for common scenarios

**Key Patterns:**
```go
// Using fixtures for healthy database
mockDB := fixtures.NewHealthyDatabase()

// Testing transactions
mockTx := fixtures.NewSuccessfulTransaction()
mockDB.ExpectTransaction(mockTx, nil)

// Testing failures
mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)
```

### `messaging_test.go`
Demonstrates messaging testing patterns including:
- Publishing and consuming messages
- Message simulation and real-time testing
- AMQP infrastructure testing
- Registry pattern testing
- Error handling and connection failures

**Key Patterns:**
```go
// Basic message publishing test
mockClient := &mocks.MockMessagingClient{}
mockClient.ExpectPublish("user.events", expectedData, nil)

// Message simulation
mockClient := fixtures.NewMessageSimulator(messages...)

// AMQP infrastructure testing
mockAMQP := fixtures.NewWorkingAMQPClient()
```

### `integration_test.go`
Demonstrates complete module integration testing including:
- Full module lifecycle testing
- HTTP handler testing with Echo
- Messaging infrastructure registration
- Configuration injection testing
- Error scenario testing across all components

**Key Patterns:**
```go
// Complete module setup
deps := &app.ModuleDeps{
    DB:        fixtures.NewHealthyDatabase(),
    Messaging: fixtures.NewWorkingMessagingClient(),
    Logger:    mockLogger,
    Config:    cfg,
}

// Module lifecycle testing
module := &UserModule{}
err := module.Init(deps)
module.RegisterRoutes(hr, echo)
module.RegisterMessaging(mockRegistry)
```

## Running the Examples

To run these test examples:

```bash
# Run all tests in the testing examples
go test ./examples/testing/...

# Run specific test files
go test ./examples/testing/database_test.go
go test ./examples/testing/messaging_test.go
go test ./examples/testing/integration_test.go

# Run with verbose output to see test details
go test -v ./examples/testing/...

# Run with coverage
go test -cover ./examples/testing/...
```

## Key Testing Concepts

### 1. Mock vs Fixture Usage

**Mocks** - Use when you need precise control over expectations:
```go
mockDB := &mocks.MockDatabase{}
mockDB.On("Query", mock.Anything, "SELECT * FROM users", mock.Anything).Return(rows, nil)
mockDB.AssertExpectations(t) // Important!
```

**Fixtures** - Use for common scenarios and faster setup:
```go
mockDB := fixtures.NewHealthyDatabase()
// Pre-configured with common healthy responses
```

### 2. Message Testing Patterns

**Static Message Testing:**
```go
messages := [][]byte{
    []byte(`{"event": "user.created"}`),
    []byte(`{"event": "user.updated"}`),
}
mockClient := fixtures.NewMessageSimulator(messages...)
```

**Dynamic Message Testing:**
```go
mockClient := fixtures.NewWorkingMessagingClient()
// Later in test...
mockClient.SimulateMessage("queue", []byte(`{"event": "dynamic"}`))
```

### 3. Error Scenario Testing

Always test both success and failure paths:
```go
t.Run("success", func(t *testing.T) {
    mockDB := fixtures.NewHealthyDatabase()
    // Test happy path
})

t.Run("database_failure", func(t *testing.T) {
    mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)
    // Test error handling
})
```

### 4. Integration Testing Structure

For complex integration tests, use a consistent structure:
```go
func TestModule_Integration(t *testing.T) {
    // 1. Set up all mocks and dependencies
    // 2. Initialize module
    // 3. Test each integration point
    // 4. Verify expectations
    // 5. Clean up
}
```

## Common Patterns

### Database Transaction Testing
```go
mockDB := &mocks.MockDatabase{}
mockTx := fixtures.NewSuccessfulTransaction()
mockDB.ExpectTransaction(mockTx, nil)

// Test operations within transaction
result := fixtures.NewMockResult(1, 1)
mockTx.ExpectExec("INSERT INTO...", result, nil)
```

### Message Handler Testing
```go
handler := NewMessageHandler()
delivery := fixtures.NewJSONDelivery([]byte(`{"event": "test"}`))
err := handler.Handle(context.Background(), &delivery)
assert.NoError(t, err)
```

### HTTP Handler Testing
```go
req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
rec := httptest.NewRecorder()
c := echo.NewContext(req, rec)

err := handler.GetUser(c)
assert.NoError(t, err)
assert.Equal(t, http.StatusOK, rec.Code)
```

## Best Practices Demonstrated

1. **Always assert mock expectations** - Call `AssertExpectations(t)` on mocks
2. **Use fixtures for common scenarios** - Reduces boilerplate and improves consistency
3. **Test error paths** - Don't just test the happy path
4. **Use table-driven tests** - For testing multiple scenarios
5. **Context handling** - Always use context in tests
6. **Resource cleanup** - Use defer or teardown functions
7. **Descriptive test names** - Make test failures easy to understand

## Additional Resources

- **Framework Documentation**: See `TESTING.md` in the repository root
- **Mock Implementations**: `github.com/gaborage/go-bricks/testing/mocks`
- **Test Fixtures**: `github.com/gaborage/go-bricks/testing/fixtures`
- **Real Framework Tests**: Check `*_test.go` files throughout the go-bricks codebase

## Contributing

When adding new test examples:

1. Follow the existing patterns and structure
2. Include both success and failure scenarios
3. Add comprehensive comments explaining the testing approach
4. Update this README with any new patterns or concepts
5. Ensure examples compile and run successfully