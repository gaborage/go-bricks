package mongodb

import (
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// newTestLogger creates a logger for MongoDB package tests.
// Uses debug level with pretty printing for better test output readability.
func newTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDebug, true)
}
