package database

import (
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// newTestLogger creates a logger for database package tests.
// Uses debug level with pretty printing for better test output readability.
// This eliminates duplication of logger.New("debug", true) across 39+ test locations.
func newTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDebug, true)
}

// newErrorTestLogger creates an error-level logger for manager tests.
// Used when testing error conditions where only error logs should appear.
// This eliminates duplication of logger.New("error", false) across 10+ test locations.
func newErrorTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelError, false)
}
