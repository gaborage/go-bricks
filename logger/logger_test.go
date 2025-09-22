package logger

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	// Capture original stdout to restore later
	originalStdout := os.Stdout

	tests := []struct {
		name          string
		level         string
		pretty        bool
		expectedLevel zerolog.Level
	}{
		{
			name:          "info_level_pretty",
			level:         "info",
			pretty:        true,
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "debug_level_not_pretty",
			level:         "debug",
			pretty:        false,
			expectedLevel: zerolog.DebugLevel,
		},
		{
			name:          "error_level_pretty",
			level:         "error",
			pretty:        true,
			expectedLevel: zerolog.ErrorLevel,
		},
		{
			name:          "warn_level_not_pretty",
			level:         "warn",
			pretty:        false,
			expectedLevel: zerolog.WarnLevel,
		},
		{
			name:          "invalid_level_defaults_to_info",
			level:         "invalid_level",
			pretty:        false,
			expectedLevel: zerolog.InfoLevel, // This is what our code sets when there's an error
		},
		{
			name:          "empty_level_uses_zerolog_default",
			level:         "",
			pretty:        true,
			expectedLevel: zerolog.NoLevel, // Empty string parses to NoLevel without error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer to capture output
			var buf bytes.Buffer

			// Temporarily redirect stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			logger := New(tt.level, tt.pretty)

			// Restore stdout
			w.Close()
			os.Stdout = originalStdout

			// Read captured output
			io.Copy(&buf, r)

			// Verify logger is created
			require.NotNil(t, logger)
			require.NotNil(t, logger.zlog)
			require.NotNil(t, logger.filter)

			// Verify filter configuration
			assert.NotNil(t, logger.filter.config)
			assert.Equal(t, DefaultMaskValue, logger.filter.config.MaskValue)
			assert.Contains(t, logger.filter.config.SensitiveFields, "password")
			assert.Contains(t, logger.filter.config.SensitiveFields, "secret")

			// Verify level is set correctly
			assert.Equal(t, tt.expectedLevel, logger.zlog.GetLevel())

			// Test that the logger implements the interface
			var _ Logger = logger
		})
	}
}

func TestNewWithFilter(t *testing.T) {
	// Capture original stdout to restore later
	originalStdout := os.Stdout

	tests := []struct {
		name          string
		level         string
		pretty        bool
		filterConfig  *FilterConfig
		expectedLevel zerolog.Level
	}{
		{
			name:   "custom_filter_config",
			level:  "debug",
			pretty: false,
			filterConfig: &FilterConfig{
				SensitiveFields: []string{"custom_secret", "custom_key"},
				MaskValue:       "[HIDDEN]",
			},
			expectedLevel: zerolog.DebugLevel,
		},
		{
			name:          "nil_filter_config_uses_default",
			level:         "error",
			pretty:        true,
			filterConfig:  nil,
			expectedLevel: zerolog.ErrorLevel,
		},
		{
			name:   "empty_mask_value_gets_defaulted",
			level:  "warn",
			pretty: false,
			filterConfig: &FilterConfig{
				SensitiveFields: []string{"test_field"},
				MaskValue:       "", // This should be defaulted
			},
			expectedLevel: zerolog.WarnLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer to capture output
			var buf bytes.Buffer

			// Temporarily redirect stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Copy the filter config so assertions can inspect the original values
			var cfgToPass *FilterConfig
			var originalMask string
			if tt.filterConfig != nil {
				cfgCopy := *tt.filterConfig
				cfgToPass = &cfgCopy
				originalMask = tt.filterConfig.MaskValue
			}

			logger := NewWithFilter(tt.level, tt.pretty, cfgToPass)

			// Restore stdout
			w.Close()
			os.Stdout = originalStdout

			// Read captured output
			io.Copy(&buf, r)

			// Verify logger is created
			require.NotNil(t, logger)
			require.NotNil(t, logger.zlog)
			require.NotNil(t, logger.filter)

			// Verify level is set correctly
			assert.Equal(t, tt.expectedLevel, logger.zlog.GetLevel())

			// Verify filter configuration
			if tt.filterConfig == nil {
				// Should use default config
				assert.Equal(t, DefaultMaskValue, logger.filter.config.MaskValue)
				assert.Contains(t, logger.filter.config.SensitiveFields, "password")
			} else if originalMask == "" {
				// Should default empty mask value but keep custom fields
				assert.Equal(t, DefaultMaskValue, logger.filter.config.MaskValue)
				assert.Equal(t, DefaultMaskValue, cfgToPass.MaskValue)
				assert.Contains(t, logger.filter.config.SensitiveFields, "test_field")
			} else {
				// Should use custom config
				assert.Equal(t, tt.filterConfig.MaskValue, logger.filter.config.MaskValue)
				// Check for the actual fields from the test config
				for _, field := range tt.filterConfig.SensitiveFields {
					assert.Contains(t, logger.filter.config.SensitiveFields, field)
				}
			}

			// Test that the logger implements the interface
			var _ Logger = logger
		})
	}
}

func TestCallerMarshalFuncSetup(t *testing.T) {
	// Test that calling New multiple times doesn't reset the caller marshal function
	// This tests the sync.Once behavior

	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create multiple loggers
	logger1 := New("info", false)
	logger2 := New("debug", true)
	logger3 := NewWithFilter("error", false, nil)

	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)

	// All loggers should be created successfully
	assert.NotNil(t, logger1)
	assert.NotNil(t, logger2)
	assert.NotNil(t, logger3)

	// The caller marshal function should be set consistently
	// We can't directly test the sync.Once behavior, but we can ensure
	// all loggers work correctly
	assert.NotNil(t, logger1.zlog)
	assert.NotNil(t, logger2.zlog)
	assert.NotNil(t, logger3.zlog)
}

func TestLoggerWithContext(t *testing.T) {
	logger := New("info", false)

	tests := []struct {
		name     string
		ctx      any
		expected string // expected behavior description
	}{
		{
			name:     "valid_context_with_logger",
			ctx:      zerolog.New(os.Stdout).WithContext(context.Background()),
			expected: "should return logger with context",
		},
		{
			name:     "valid_context_without_logger",
			ctx:      context.Background(),
			expected: "should return original logger",
		},
		{
			name:     "context_with_disabled_logger",
			ctx:      zerolog.New(io.Discard).Level(zerolog.Disabled).WithContext(context.Background()),
			expected: "should return original logger",
		},
		{
			name:     "non_context_interface",
			ctx:      "not a context",
			expected: "should return original logger",
		},
		{
			name:     "nil_context",
			ctx:      nil,
			expected: "should return original logger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := logger.WithContext(tt.ctx)

			// Result should always be a valid logger
			assert.NotNil(t, result)
			assert.Implements(t, (*Logger)(nil), result)

			// Cast to check internal state
			resultLogger, ok := result.(*ZeroLogger)
			assert.True(t, ok)
			assert.NotNil(t, resultLogger.zlog)
			assert.NotNil(t, resultLogger.filter)

			// Filter should be preserved from original logger
			assert.Equal(t, logger.filter, resultLogger.filter)

			// Test actual context scenarios
			switch tt.name {
			case "valid_context_with_logger":
				if ctx, ok := tt.ctx.(context.Context); ok {
					contextLogger := zerolog.Ctx(ctx)
					if contextLogger != nil && contextLogger.GetLevel() != zerolog.Disabled {
						// Should return a different logger instance with context logger
						assert.NotEqual(t, logger.zlog, resultLogger.zlog)
					}
				}
			case "valid_context_without_logger", "context_with_disabled_logger",
				"non_context_interface", "nil_context":
				// Should return the same logger instance
				assert.Equal(t, logger, result)
			}
		})
	}
}

func TestLoggerWithFields(t *testing.T) {
	logger := New("info", false)

	tests := []struct {
		name   string
		fields map[string]any
	}{
		{
			name: "basic_fields",
			fields: map[string]any{
				"username": "john_doe",
				"action":   "login",
				"count":    42,
			},
		},
		{
			name: "sensitive_fields",
			fields: map[string]any{
				"username": "john_doe",
				"password": "secret123",
				"api_key":  "super_secret_key",
			},
		},
		{
			name:   "empty_fields",
			fields: map[string]any{},
		},
		{
			name: "mixed_types",
			fields: map[string]any{
				"string_field": "value",
				"int_field":    123,
				"float_field":  3.14,
				"bool_field":   true,
				"duration":     time.Second * 5,
			},
		},
		{
			name: "nested_map",
			fields: map[string]any{
				"user": map[string]any{
					"name":     "john",
					"password": "secret",
				},
				"public": "info",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := logger.WithFields(tt.fields)

			// Result should be a valid logger
			assert.NotNil(t, result)
			assert.Implements(t, (*Logger)(nil), result)

			// Should return a different logger instance (unless fields are empty)
			if len(tt.fields) > 0 {
				assert.NotEqual(t, logger, result)
			}

			// Cast to check internal state
			resultLogger, ok := result.(*ZeroLogger)
			assert.True(t, ok)
			assert.NotNil(t, resultLogger.zlog)
			assert.NotNil(t, resultLogger.filter)

			// Filter should be preserved from original logger
			assert.Equal(t, logger.filter, resultLogger.filter)

			// The returned logger should have a different underlying zerolog instance (unless fields are empty)
			if len(tt.fields) > 0 {
				assert.NotEqual(t, logger.zlog, resultLogger.zlog)
			}
		})
	}
}

func TestLoggerWithFieldsNilFilter(t *testing.T) {
	// Create a logger with nil filter to test the nil check
	zl := zerolog.New(os.Stdout).With().Logger()
	logger := &ZeroLogger{
		zlog:   &zl,
		filter: nil,
	}

	fields := map[string]any{
		"username": "john_doe",
		"password": "secret123",
	}

	result := logger.WithFields(fields)

	// Should work without panicking
	assert.NotNil(t, result)
	assert.Implements(t, (*Logger)(nil), result)
}

func TestLoggerIntegrationWithLoggingMethods(t *testing.T) {
	// Test that our logger constructor works with actual logging
	var buf bytes.Buffer

	// Create a logger that writes to our buffer instead of stdout
	zl := zerolog.New(&buf).With().Timestamp().Logger()
	logger := &ZeroLogger{
		zlog:   &zl,
		filter: NewSensitiveDataFilter(nil),
	}

	// Test info logging
	logger.Info().Str("message", "test").Msg("info test")

	// Test error logging
	logger.Error().Str("error", "test error").Msg("error test")

	// Test debug logging (might not appear due to level)
	logger.Debug().Str("debug", "test debug").Msg("debug test")

	output := buf.String()

	// Should contain log entries
	assert.Contains(t, output, "info test")
	assert.Contains(t, output, "error test")
	assert.Contains(t, output, "test")
}
