package oracle

import (
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// newTestLogger creates a logger for Oracle package tests.
// Uses debug level with pretty printing for better test output readability.
func newTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDebug, true)
}

// newDisabledTestLogger creates a disabled logger for integration tests.
// Used to reduce noise in tests that verify functionality without needing logs.
func newDisabledTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDisabled, true)
}
