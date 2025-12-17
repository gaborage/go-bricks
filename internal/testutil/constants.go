// Package testutil provides shared constants and utilities for testing across go-bricks.
// These constants eliminate repeated string literals in test files and ensure consistency.
package testutil

// Test Error Messages
//
// These constants define common error messages used in test assertions.
// Using constants prevents typos and enables IDE-assisted refactoring.

const (
	// TestError is a generic error message for test error scenarios.
	// Used across multiple test files (10+ occurrences).
	TestError = "test error"

	// TestConnectionRefused is the common network error message for connection failures.
	// Used in error handling tests across config, cache, database, and httpclient packages.
	TestConnectionRefused = "connection refused"
)

// Test Host Configuration
//
// These constants define common host values used in test configurations.

const (
	// TestHost is the standard localhost hostname for test environments.
	// Used extensively across configuration, database, and integration tests (130+ occurrences).
	TestHost = "localhost"

	// TestHostWithPort is localhost with a common test port.
	TestHostWithPort = "localhost:8080"
)

// Test Tenant Configuration
//
// These constants define common tenant identifiers for multi-tenant testing.

const (
	// TestTenantID is a generic tenant identifier for single-tenant tests.
	TestTenantID = "test-tenant"

	// TestTenantAcme is a fictional company tenant for multi-tenant tests.
	TestTenantAcme = "acme"

	// TestTenantGlobex is another fictional company tenant for multi-tenant tests.
	TestTenantGlobex = "globex"
)

// Test Configuration Keys
//
// These constants define common configuration key patterns used in tests.

const (
	// TestConfigKey is a generic configuration key for testing.
	TestConfigKey = "test.key"

	// TestConfigPort is a common port configuration key.
	TestConfigPort = "8080"
)
