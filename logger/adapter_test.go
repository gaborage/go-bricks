package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestLogger creates a logger that outputs to a buffer for testing
func createTestLogger() (*ZeroLogger, *bytes.Buffer) {
	var buf bytes.Buffer
	zl := zerolog.New(&buf)
	return &ZeroLogger{zlog: &zl}, &buf
}

func TestLogEventAdapter_Msg(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event and send a message
	logger.Info().Msg("test message")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the message
	assert.Equal(t, "test message", logEntry["message"])
	assert.Equal(t, "info", logEntry["level"])
}

func TestLogEventAdapter_Msgf(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event and send a formatted message
	logger.Info().Msgf("test %s with %d", "message", 42)

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the formatted message
	assert.Equal(t, "test message with 42", logEntry["message"])
	assert.Equal(t, "info", logEntry["level"])
}

func TestLogEventAdapter_Err(t *testing.T) {
	logger, buf := createTestLogger()

	testErr := errors.New("test error")

	// Create a log event with an error
	logger.Error().Err(testErr).Msg("error occurred")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the error and message
	assert.Equal(t, "test error", logEntry["error"])
	assert.Equal(t, "error occurred", logEntry["message"])
	assert.Equal(t, "error", logEntry["level"])
}

func TestLogEventAdapter_Str(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with a string field
	logger.Info().Str("username", "john_doe").Msg("user action")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the string field
	assert.Equal(t, "john_doe", logEntry["username"])
	assert.Equal(t, "user action", logEntry["message"])
	assert.Equal(t, "info", logEntry["level"])
}

func TestLogEventAdapter_Int(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with an integer field
	logger.Info().Int("count", 42).Msg("processing items")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the integer field (JSON unmarshals numbers as float64)
	assert.Equal(t, float64(42), logEntry["count"])
	assert.Equal(t, "processing items", logEntry["message"])
}

func TestLogEventAdapter_Int64(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with an int64 field
	logger.Info().Int64("timestamp", 1640995200).Msg("event occurred")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the int64 field
	assert.Equal(t, float64(1640995200), logEntry["timestamp"])
	assert.Equal(t, "event occurred", logEntry["message"])
}

func TestLogEventAdapter_Uint64(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with a uint64 field
	logger.Info().Uint64("size", 1024).Msg("file processed")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the uint64 field
	assert.Equal(t, float64(1024), logEntry["size"])
	assert.Equal(t, "file processed", logEntry["message"])
}

func TestLogEventAdapter_Dur(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with a duration field
	duration := 150 * time.Millisecond
	logger.Info().Dur("processing_time", duration).Msg("request completed")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the duration field (zerolog stores duration in milliseconds)
	assert.Equal(t, float64(150), logEntry["processing_time"])
	assert.Equal(t, "request completed", logEntry["message"])
}

func TestLogEventAdapter_Interface(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with an interface{} field
	data := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}
	logger.Info().Interface("data", data).Msg("structured data")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the interface field
	dataField, ok := logEntry["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "value1", dataField["key1"])
	assert.Equal(t, "value2", dataField["key2"])
	assert.Equal(t, "structured data", logEntry["message"])
}

func TestLogEventAdapter_Bytes(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with a bytes field
	data := []byte("binary data")
	logger.Info().Bytes("payload", data).Msg("binary payload")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify the bytes field (zerolog may store as string, not base64)
	// The actual encoding depends on zerolog's internal implementation
	assert.NotEmpty(t, logEntry["payload"])
	assert.Equal(t, "binary payload", logEntry["message"])
}

func TestLogEventAdapter_ChainedFields(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a log event with chained fields
	testErr := errors.New("chained error")
	logger.Error().
		Str("user", "alice").
		Int("attempt", 3).
		Dur("duration", 250*time.Millisecond).
		Err(testErr).
		Msg("failed operation")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify all chained fields
	assert.Equal(t, "alice", logEntry["user"])
	assert.Equal(t, float64(3), logEntry["attempt"])
	assert.Equal(t, float64(250), logEntry["duration"])
	assert.Equal(t, "chained error", logEntry["error"])
	assert.Equal(t, "failed operation", logEntry["message"])
	assert.Equal(t, "error", logEntry["level"])
}

func TestZeroLogger_Info(t *testing.T) {
	logger, buf := createTestLogger()

	// Create an info-level log event
	event := logger.Info()
	require.NotNil(t, event)

	// Verify it's a LogEventAdapter
	adapter, ok := event.(*LogEventAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter.event)

	// Send a message and verify level
	event.Msg("info message")

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "info", logEntry["level"])
}

func TestZeroLogger_Error(t *testing.T) {
	logger, buf := createTestLogger()

	// Create an error-level log event
	event := logger.Error()
	require.NotNil(t, event)

	// Verify it's a LogEventAdapter
	adapter, ok := event.(*LogEventAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter.event)

	// Send a message and verify level
	event.Msg("error message")

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "error", logEntry["level"])
}

func TestZeroLogger_Debug(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a debug-level log event
	event := logger.Debug()
	require.NotNil(t, event)

	// Verify it's a LogEventAdapter
	adapter, ok := event.(*LogEventAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter.event)

	// Send a message and verify level
	event.Msg("debug message")

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "debug", logEntry["level"])
}

func TestZeroLogger_Warn(t *testing.T) {
	logger, buf := createTestLogger()

	// Create a warning-level log event
	event := logger.Warn()
	require.NotNil(t, event)

	// Verify it's a LogEventAdapter
	adapter, ok := event.(*LogEventAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter.event)

	// Send a message and verify level
	event.Msg("warning message")

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "warn", logEntry["level"])
}

func TestZeroLogger_Fatal(t *testing.T) {
	// Note: Fatal logs and then calls os.Exit, so we need to be careful
	logger, buf := createTestLogger()

	// Create a fatal-level log event
	event := logger.Fatal()
	require.NotNil(t, event)

	// Verify it's a LogEventAdapter
	adapter, ok := event.(*LogEventAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter.event)

	// We can't actually call Msg() here as it would exit the test
	// Instead, we'll verify the event creation worked
	assert.NotNil(t, adapter.event)

	// Verify the buffer is still empty (since we didn't send a message)
	assert.Empty(t, buf.String())
}

func TestLogEventAdapter_InterfaceCompliance(t *testing.T) {
	// Verify that LogEventAdapter implements the LogEvent interface
	logger, _ := createTestLogger()

	event := logger.Info()
	require.NotNil(t, event)

	// Test that all methods are available and return LogEvent interface
	event = event.Str("key", "value")
	event = event.Int("count", 1)
	event = event.Int64("timestamp", 123456789)
	event = event.Uint64("size", 1024)
	event = event.Dur("duration", time.Second)
	event = event.Interface("data", map[string]string{"test": "value"})
	event = event.Bytes("bytes", []byte("test"))
	event = event.Err(errors.New("test error"))

	// This shouldn't panic
	event.Msg("test complete")
}

func TestLogEventAdapter_EdgeCases(t *testing.T) {
	logger, buf := createTestLogger()

	tests := []struct {
		name string
		fn   func() LogEvent
	}{
		{
			name: "empty_string_value",
			fn:   func() LogEvent { return logger.Info().Str("empty", "") },
		},
		{
			name: "zero_values",
			fn: func() LogEvent {
				return logger.Info().
					Int("zero_int", 0).
					Int64("zero_int64", 0).
					Uint64("zero_uint64", 0).
					Dur("zero_duration", 0)
			},
		},
		{
			name: "nil_error",
			fn:   func() LogEvent { return logger.Info().Err(nil) },
		},
		{
			name: "nil_interface",
			fn:   func() LogEvent { return logger.Info().Interface("nil_data", nil) },
		},
		{
			name: "empty_bytes",
			fn:   func() LogEvent { return logger.Info().Bytes("empty_bytes", []byte{}) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()

			// This shouldn't panic
			event := tt.fn()
			event.Msg("test message")

			// Verify we got some output
			assert.NotEmpty(t, buf.String())

			// Parse and verify basic structure
			var logEntry map[string]interface{}
			err := json.Unmarshal(buf.Bytes(), &logEntry)
			require.NoError(t, err)
			assert.Equal(t, "test message", logEntry["message"])
		})
	}
}

func TestLogEventAdapter_LargeValues(t *testing.T) {
	logger, buf := createTestLogger()

	// Test with large values
	largeString := strings.Repeat("a", 10000)
	largeBytes := bytes.Repeat([]byte("b"), 5000)

	logger.Info().
		Str("large_string", largeString).
		Bytes("large_bytes", largeBytes).
		Int64("max_int64", 9223372036854775807).
		Uint64("max_uint64", 18446744073709551615).
		Msg("large values test")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify large values are handled correctly
	assert.Equal(t, largeString, logEntry["large_string"])
	assert.NotEmpty(t, logEntry["large_bytes"]) // base64 encoded bytes
	assert.Equal(t, float64(9223372036854775807), logEntry["max_int64"])
	assert.Equal(t, float64(18446744073709551615), logEntry["max_uint64"])
}

func TestLogEventAdapter_SpecialCharacters(t *testing.T) {
	logger, buf := createTestLogger()

	// Test with special characters and unicode
	specialString := "Special chars: \n\t\r\"'\\/ 🚀 中文"

	logger.Info().
		Str("special", specialString).
		Msg("special characters test")

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Verify special characters are properly escaped/handled
	assert.Equal(t, specialString, logEntry["special"])
	assert.Equal(t, "special characters test", logEntry["message"])
}

func TestLogEventAdapter_ReturnedTypes(t *testing.T) {
	logger, _ := createTestLogger()

	// Test that all field methods return LogEvent interface
	event := logger.Info()

	// Each of these should return LogEvent, allowing method chaining
	event = event.Str("test", "value")
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Int("count", 1)
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Int64("timestamp", 123)
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Uint64("size", 456)
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Dur("duration", time.Microsecond)
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Interface("data", "test")
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Bytes("bytes", []byte("test"))
	assert.Implements(t, (*LogEvent)(nil), event)

	event = event.Err(errors.New("test"))
	assert.Implements(t, (*LogEvent)(nil), event)
}

// TestLogEventAdapter_FilterCoverage tests the filter paths in Str and Interface methods
func TestLogEventAdapter_FilterCoverage(t *testing.T) {
	// Create logger with custom filter to ensure filter path is taken
	var buf bytes.Buffer
	zl := zerolog.New(&buf)

	filterConfig := &FilterConfig{
		SensitiveFields: []string{"password", "secret", "api_key"},
		MaskValue:       "[FILTERED]",
	}

	logger := &ZeroLogger{
		zlog:   &zl,
		filter: NewSensitiveDataFilter(filterConfig),
	}

	t.Run("Str_with_filter_applied", func(t *testing.T) {
		buf.Reset()

		// Test Str method with sensitive field - should trigger filter path
		logger.Info().
			Str("username", "john_doe").  // Not filtered
			Str("password", "secret123"). // Should be filtered
			Msg("login attempt")

		output := buf.String()

		// Parse JSON to verify filtering
		var logEntry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &logEntry)
		require.NoError(t, err)

		// Verify filtering worked
		assert.Equal(t, "john_doe", logEntry["username"])
		assert.Equal(t, "[FILTERED]", logEntry["password"])
		assert.NotContains(t, output, "secret123")
	})

	t.Run("Interface_with_filter_applied", func(t *testing.T) {
		buf.Reset()

		// Test Interface method with sensitive data - should trigger filter path
		sensitiveData := map[string]interface{}{
			"username": "john_doe",
			"api_key":  "super_secret_key",
			"config": map[string]interface{}{
				"theme":  "dark",
				"secret": "nested_secret",
			},
		}

		logger.Info().
			Interface("user_data", sensitiveData).
			Msg("user data logged")

		output := buf.String()

		// Parse JSON to verify filtering
		var logEntry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &logEntry)
		require.NoError(t, err)

		// Verify filtering worked on nested data
		userData, ok := logEntry["user_data"].(map[string]interface{})
		require.True(t, ok)

		assert.Equal(t, "john_doe", userData["username"])
		assert.Equal(t, "[FILTERED]", userData["api_key"])

		// Check nested map filtering
		config, ok := userData["config"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "dark", config["theme"])
		assert.Equal(t, "[FILTERED]", config["secret"])

		// Ensure sensitive data not in raw output
		assert.NotContains(t, output, "super_secret_key")
		assert.NotContains(t, output, "nested_secret")
	})

	t.Run("Str_and_Interface_with_nil_filter", func(t *testing.T) {
		// Test the nil filter path to ensure 100% coverage
		buf.Reset()

		loggerNoFilter := &ZeroLogger{
			zlog:   &zl,
			filter: nil, // No filter
		}

		// Both Str and Interface should pass through without filtering
		loggerNoFilter.Info().
			Str("password", "visible_password").
			Interface("data", map[string]interface{}{
				"secret": "visible_secret",
			}).
			Msg("no filtering")

		output := buf.String()

		// Parse JSON
		var logEntry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &logEntry)
		require.NoError(t, err)

		// Without filter, sensitive data should be visible
		assert.Equal(t, "visible_password", logEntry["password"])

		data, ok := logEntry["data"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "visible_secret", data["secret"])

		// Raw output should contain sensitive data
		assert.Contains(t, output, "visible_password")
		assert.Contains(t, output, "visible_secret")
	})
}
