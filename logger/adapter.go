// Package logger provides logging functionality with zerolog adapter
package logger

import (
	"time"

	"github.com/rs/zerolog"
)

// LogEventAdapter adapts zerolog events to our logger interface
type LogEventAdapter struct {
	event  *zerolog.Event
	filter *SensitiveDataFilter
}

// Msg logs the message
func (lea *LogEventAdapter) Msg(msg string) {
	lea.event.Msg(msg)
}

// Msgf logs a formatted message
func (lea *LogEventAdapter) Msgf(format string, args ...interface{}) {
	lea.event.Msgf(format, args...)
}

// Err adds an error to the log event
func (lea *LogEventAdapter) Err(err error) LogEvent {
	return &LogEventAdapter{event: lea.event.Err(err), filter: lea.filter}
}

// Str adds a string field to the log event
func (lea *LogEventAdapter) Str(key, value string) LogEvent {
	if lea.filter != nil {
		value = lea.filter.FilterString(key, value)
	}
	return &LogEventAdapter{event: lea.event.Str(key, value), filter: lea.filter}
}

// Int adds an integer field to the log event
func (lea *LogEventAdapter) Int(key string, value int) LogEvent {
	return &LogEventAdapter{event: lea.event.Int(key, value), filter: lea.filter}
}

// Int64 adds an int64 field to the log event
func (lea *LogEventAdapter) Int64(key string, value int64) LogEvent {
	return &LogEventAdapter{event: lea.event.Int64(key, value), filter: lea.filter}
}

// Uint64 adds a uint64 field to the log event
func (lea *LogEventAdapter) Uint64(key string, value uint64) LogEvent {
	return &LogEventAdapter{event: lea.event.Uint64(key, value), filter: lea.filter}
}

// Dur adds a duration field to the log event
func (lea *LogEventAdapter) Dur(key string, d time.Duration) LogEvent {
	return &LogEventAdapter{event: lea.event.Dur(key, d), filter: lea.filter}
}

// Interface adds an interface{} field to the log event
func (lea *LogEventAdapter) Interface(key string, i interface{}) LogEvent {
	if lea.filter != nil {
		i = lea.filter.FilterValue(key, i)
	}
	return &LogEventAdapter{event: lea.event.Interface(key, i), filter: lea.filter}
}

// Bytes adds a byte slice field to the log event
func (lea *LogEventAdapter) Bytes(key string, val []byte) LogEvent {
	return &LogEventAdapter{event: lea.event.Bytes(key, val), filter: lea.filter}
}

// Info creates an info-level log event
func (l *ZeroLogger) Info() LogEvent {
	return &LogEventAdapter{event: l.zlog.Info(), filter: l.filter}
}

func (l *ZeroLogger) Error() LogEvent {
	return &LogEventAdapter{event: l.zlog.Error(), filter: l.filter}
}

// Debug creates a debug-level log event
func (l *ZeroLogger) Debug() LogEvent {
	return &LogEventAdapter{event: l.zlog.Debug(), filter: l.filter}
}

// Warn creates a warning-level log event
func (l *ZeroLogger) Warn() LogEvent {
	return &LogEventAdapter{event: l.zlog.Warn(), filter: l.filter}
}

// Fatal creates a fatal-level log event
func (l *ZeroLogger) Fatal() LogEvent {
	return &LogEventAdapter{event: l.zlog.Fatal(), filter: l.filter}
}
