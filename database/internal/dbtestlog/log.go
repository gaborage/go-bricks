// Package dbtestlog provides shared test-logger helpers for the database
// vendor packages. Extracted from byte-identical newTestLogger /
// newDisabledTestLogger pairs that previously lived in
// database/postgresql/testhelpers.go and database/oracle/testhelpers.go.
//
// Visible only within the database/ subtree (internal/ boundary).
package dbtestlog

import (
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// NewTestLogger creates a logger for database-vendor package tests.
// Uses debug level with pretty printing for better test output readability.
func NewTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDebug, true)
}

// NewDisabledTestLogger creates a disabled logger for integration tests.
// Used to reduce noise in tests that verify functionality without needing logs.
func NewDisabledTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDisabled, true)
}
