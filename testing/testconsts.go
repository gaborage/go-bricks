package testing

// Logger Constants
// These constants define common logger configurations used across test files
// to avoid duplicating logger initialization strings.
const (
	// TestLoggerLevelDebug is the debug log level used in most tests
	TestLoggerLevelDebug = "debug"
	// TestLoggerLevelError is the error log level for tests requiring minimal output
	TestLoggerLevelError = "error"
	// TestLoggerLevelDisabled completely disables logging in tests
	TestLoggerLevelDisabled = "disabled"
)
