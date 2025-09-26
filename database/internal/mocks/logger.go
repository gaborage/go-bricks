// Package mocks provides shared mock implementations for testing database components.
// This package centralizes common mocks used across all database package tests.
package mocks

import (
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// Logger implements logger.Logger interface for testing.
// It provides a no-op implementation that satisfies the interface
// without producing any actual logging output.
type Logger struct{}

// Compile-time checks to ensure mocks satisfy interfaces
var _ logger.Logger = (*Logger)(nil)
var _ logger.LogEvent = (*LogEvent)(nil)

// Info returns a no-op LogEvent for info-level logging
func (l *Logger) Info() logger.LogEvent { return &LogEvent{} }

// Error returns a no-op LogEvent for error-level logging
func (l *Logger) Error() logger.LogEvent { return &LogEvent{} }

// Debug returns a no-op LogEvent for debug-level logging
func (l *Logger) Debug() logger.LogEvent { return &LogEvent{} }

// Warn returns a no-op LogEvent for warning-level logging
func (l *Logger) Warn() logger.LogEvent { return &LogEvent{} }

// Fatal returns a no-op LogEvent for fatal-level logging
func (l *Logger) Fatal() logger.LogEvent { return &LogEvent{} }

// WithContext returns the same logger instance (no-op for testing)
func (l *Logger) WithContext(_ any) logger.Logger { return l }

// WithFields returns the same logger instance (no-op for testing)
func (l *Logger) WithFields(_ map[string]any) logger.Logger { return l }

// LogEvent implements logger.LogEvent interface for testing.
// All methods are no-op implementations that satisfy the interface
// without producing any output.
type LogEvent struct{}

// Str adds a string field to the log event (no-op for testing)
func (e *LogEvent) Str(_, _ string) logger.LogEvent { return e }

// Int adds an int field to the log event (no-op for testing)
func (e *LogEvent) Int(_ string, _ int) logger.LogEvent { return e }

// Int64 adds an int64 field to the log event (no-op for testing)
func (e *LogEvent) Int64(_ string, _ int64) logger.LogEvent { return e }

// Uint64 adds a uint64 field to the log event (no-op for testing)
func (e *LogEvent) Uint64(_ string, _ uint64) logger.LogEvent { return e }

// Dur adds a duration field to the log event (no-op for testing)
func (e *LogEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }

// Interface adds an interface{} field to the log event (no-op for testing)
func (e *LogEvent) Interface(_ string, _ any) logger.LogEvent { return e }

// Bytes adds a byte slice field to the log event (no-op for testing)
func (e *LogEvent) Bytes(_ string, _ []byte) logger.LogEvent { return e }

// Err adds an error field to the log event (no-op for testing)
func (e *LogEvent) Err(_ error) logger.LogEvent { return e }

// Msg logs the message (no-op for testing)
func (e *LogEvent) Msg(_ string) {
	// No-op
}

// Msgf logs a formatted message (no-op for testing)
func (e *LogEvent) Msgf(_ string, _ ...any) {
	// No-op
}
