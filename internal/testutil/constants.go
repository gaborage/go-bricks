// Package testutil provides shared constants for testing across go-bricks.
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
