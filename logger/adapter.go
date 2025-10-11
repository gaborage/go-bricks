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
	level  zerolog.Level
	hook   func(zerolog.Level)
}

// Msg logs the message
func (lea *LogEventAdapter) Msg(msg string) {
	lea.trackSeverity()
	lea.event.Msg(msg)
}

// Msgf logs a formatted message
func (lea *LogEventAdapter) Msgf(format string, args ...any) {
	lea.trackSeverity()
	lea.event.Msgf(format, args...)
}

// Err adds an error to the log event
func (lea *LogEventAdapter) Err(err error) LogEvent {
	return &LogEventAdapter{event: lea.event.Err(err), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Str adds a string field to the log event
func (lea *LogEventAdapter) Str(key, value string) LogEvent {
	if lea.filter != nil {
		value = lea.filter.FilterString(key, value)
	}
	return &LogEventAdapter{event: lea.event.Str(key, value), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Int adds an integer field to the log event
func (lea *LogEventAdapter) Int(key string, value int) LogEvent {
	return &LogEventAdapter{event: lea.event.Int(key, value), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Int64 adds an int64 field to the log event
func (lea *LogEventAdapter) Int64(key string, value int64) LogEvent {
	return &LogEventAdapter{event: lea.event.Int64(key, value), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Uint64 adds a uint64 field to the log event
func (lea *LogEventAdapter) Uint64(key string, value uint64) LogEvent {
	return &LogEventAdapter{event: lea.event.Uint64(key, value), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Dur adds a duration field to the log event
func (lea *LogEventAdapter) Dur(key string, d time.Duration) LogEvent {
	return &LogEventAdapter{event: lea.event.Dur(key, d), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Interface adds an any field to the log event
func (lea *LogEventAdapter) Interface(key string, i any) LogEvent {
	if lea.filter != nil {
		i = lea.filter.FilterValue(key, i)
	}
	return &LogEventAdapter{event: lea.event.Interface(key, i), filter: lea.filter, level: lea.level, hook: lea.hook}
}

// Bytes adds a byte slice field to the log event
func (lea *LogEventAdapter) Bytes(key string, val []byte) LogEvent {
	return &LogEventAdapter{event: lea.event.Bytes(key, val), filter: lea.filter, level: lea.level, hook: lea.hook}
}

func (lea *LogEventAdapter) trackSeverity() {
	if lea.hook != nil && lea.level >= zerolog.WarnLevel {
		lea.hook(lea.level)
	}
}

// Info creates an info-level log event
func (l *ZeroLogger) Info() LogEvent {
	return &LogEventAdapter{event: l.zlog.Info(), filter: l.filter, level: zerolog.InfoLevel, hook: l.severityHook}
}

func (l *ZeroLogger) Error() LogEvent {
	return &LogEventAdapter{event: l.zlog.Error(), filter: l.filter, level: zerolog.ErrorLevel, hook: l.severityHook}
}

// Debug creates a debug-level log event
func (l *ZeroLogger) Debug() LogEvent {
	return &LogEventAdapter{event: l.zlog.Debug(), filter: l.filter, level: zerolog.DebugLevel, hook: l.severityHook}
}

// Warn creates a warning-level log event
func (l *ZeroLogger) Warn() LogEvent {
	return &LogEventAdapter{event: l.zlog.Warn(), filter: l.filter, level: zerolog.WarnLevel, hook: l.severityHook}
}

// Fatal creates a fatal-level log event
func (l *ZeroLogger) Fatal() LogEvent {
	return &LogEventAdapter{event: l.zlog.Fatal(), filter: l.filter, level: zerolog.FatalLevel, hook: l.severityHook}
}
