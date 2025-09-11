// Package logger defines the logging interface used throughout the application.
// It provides a contract for structured logging implementations.
package logger

import "time"

// Logger defines the contract for structured logging throughout the application.
// It provides methods for creating log events at different severity levels and for contextual logging.
type Logger interface {
	Info() LogEvent
	Error() LogEvent
	Debug() LogEvent
	Warn() LogEvent
	Fatal() LogEvent
	WithContext(ctx interface{}) Logger
	WithFields(fields map[string]interface{}) Logger
}

// LogEvent represents a structured log event that can be built with fields and sent.
// It provides methods for adding various field types and sending the final log message.
type LogEvent interface {
	Msg(msg string)
	Msgf(format string, args ...interface{})
	Err(err error) LogEvent
	Str(key, value string) LogEvent
	Int(key string, value int) LogEvent
	Int64(key string, value int64) LogEvent
	Uint64(key string, value uint64) LogEvent
	Dur(key string, d time.Duration) LogEvent
	Interface(key string, i interface{}) LogEvent
	Bytes(key string, val []byte) LogEvent
}
