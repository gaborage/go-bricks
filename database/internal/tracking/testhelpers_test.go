package tracking

import (
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// newDisabledTestLogger creates a disabled logger for tracking package tests.
// Used in metrics and observability tests where logging output is not needed.
func newDisabledTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDisabled, false)
}
