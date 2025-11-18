package testing

import "time"

// Logger Constants
// These constants define common logger configurations used across test files
// to eliminate duplication of logger initialization strings (96+ occurrences).
const (
	// TestLoggerLevelDebug is the debug log level used in most tests
	TestLoggerLevelDebug = "debug"
	// TestLoggerLevelError is the error log level for tests requiring minimal output
	TestLoggerLevelError = "error"
	// TestLoggerLevelDisabled completely disables logging in tests
	TestLoggerLevelDisabled = "disabled"
)

// Service and Component Names
// Common service/component names used in test configurations.
const (
	TestServiceName  = "test-service"
	TestCacheName    = "test-cache"
	TestKeyName      = "test-key"
	TestValueDefault = "test-value"
)

// Messaging Constants
// Common queue, exchange, and routing key names for messaging tests.
const (
	TestQueueName    = "test-queue"
	TestExchangeName = "test-exchange"
	TestRoutingKey   = "test-routing-key"
	TestConsumerTag  = "test-consumer"
)

// Database Constants
// Common database-related test strings (table names, queries, connection strings).
const (
	TestTableUsers      = "users"
	TestTableProducts   = "products"
	TestTableOrders     = "orders"
	TestUsername        = "testuser"
	TestDatabaseName    = "testdb"
	TestHostLocalhost   = "localhost"
	TestPasswordDefault = "testpass"
	TestStatusActive    = "active"
	TestStatusInactive  = "inactive"
	TestSelectQuery     = "SELECT"
	TestInsertQuery     = "INSERT"
	TestUpdateQuery     = "UPDATE"
	TestDeleteQuery     = "DELETE"
)

// Test User Data
// Common test user data used across multiple test suites.
const (
	TestEmailAlice = "alice@example.com"
	TestEmailBob   = "bob@example.com"
	TestNameAlice  = "Alice"
	TestNameBob    = "Bob"
)

// Multi-Tenant Constants
// Common tenant identifiers for multi-tenant testing.
const (
	TestTenantOne    = "tenant-1"
	TestTenantTwo    = "tenant-2"
	TestTenantThree  = "tenant-3"
	TestTenantAcme   = "acme"
	TestTenantGlobex = "globex"
)

// OpenTelemetry Constants
// Common names for tracing, metrics, and observability tests.
const (
	TestTracerName = "test"
	TestMeterName  = "test-meter"
	TestSpanName   = "test-span"
)

// Time Duration Constants
// Common time durations used in test synchronization and timeouts.
const (
	// TestShortDelay is a short delay for goroutine synchronization (100ms)
	TestShortDelay = 100 * time.Millisecond
	// TestMediumDelay is a medium delay for async operations (300ms)
	TestMediumDelay = 300 * time.Millisecond
	// TestLongDelay is a longer delay for slow operations (1 second)
	TestLongDelay = 1 * time.Second
	// TestCleanupWaitPeriod is the wait time for cleanup operations (300ms)
	TestCleanupWaitPeriod = 300 * time.Millisecond
	// TestEventuallyTimeout is the timeout for require.Eventually assertions (500ms)
	TestEventuallyTimeout = 500 * time.Millisecond
	// TestEventuallyTick is the polling interval for require.Eventually (50ms)
	TestEventuallyTick = 50 * time.Millisecond
)

// Port Numbers
// Common port numbers for test services.
const (
	TestPortRedis      = 6379
	TestPortHTTP       = 8080
	TestPortPostgreSQL = 5432
	TestPortMongoDB    = 27017
)

// Size Constants
// Common size values for buffers, payloads, and varchar lengths.
const (
	TestVarcharSize           = 100
	TestBytesPerKB            = 1024
	TestBenchmarkLargePayload = 100 * 1024 // 100KB
	TestBenchmarkSmallPayload = 256        // 256 bytes
)
