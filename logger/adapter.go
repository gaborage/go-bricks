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

// wrapEvent creates a new LogEventAdapter reusing the current filter/level/hook.
func (lea *LogEventAdapter) wrapEvent(e *zerolog.Event) *LogEventAdapter {
	return &LogEventAdapter{event: e, filter: lea.filter, level: lea.level, hook: lea.hook}
}

// maskIfSensitive returns a masked adapter when the key is sensitive, signalling via ok.
// Centralizes the typed-method mask check so adding a new typed slot (e.g. Float64) cannot
// silently bypass the privacy boundary by forgetting to wire the same conditional.
func (lea *LogEventAdapter) maskIfSensitive(key string) (LogEvent, bool) {
	if lea.filter != nil && lea.filter.isSensitiveField(key) {
		return lea.wrapEvent(lea.event.Interface(key, lea.filter.config.MaskValue)), true
	}
	return nil, false
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
	return lea.wrapEvent(lea.event.Err(err))
}

// Str adds a string field to the log event
func (lea *LogEventAdapter) Str(key, value string) LogEvent {
	if lea.filter != nil {
		value = lea.filter.FilterString(key, value)
	}
	return lea.wrapEvent(lea.event.Str(key, value))
}

// Int adds an integer field to the log event
func (lea *LogEventAdapter) Int(key string, value int) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	return lea.wrapEvent(lea.event.Int(key, value))
}

// Int64 adds an int64 field to the log event
func (lea *LogEventAdapter) Int64(key string, value int64) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	return lea.wrapEvent(lea.event.Int64(key, value))
}

// Uint64 adds a uint64 field to the log event
func (lea *LogEventAdapter) Uint64(key string, value uint64) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	return lea.wrapEvent(lea.event.Uint64(key, value))
}

// Dur adds a duration field to the log event
func (lea *LogEventAdapter) Dur(key string, d time.Duration) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	return lea.wrapEvent(lea.event.Dur(key, d))
}

// Interface adds an any field to the log event
func (lea *LogEventAdapter) Interface(key string, i any) LogEvent {
	if lea.filter != nil {
		i = lea.filter.FilterValue(key, i)
	}
	return lea.wrapEvent(lea.event.Interface(key, i))
}

// Bytes adds a byte slice field to the log event
func (lea *LogEventAdapter) Bytes(key string, val []byte) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	return lea.wrapEvent(lea.event.Bytes(key, val))
}

// Bool adds a boolean field to the log event
func (lea *LogEventAdapter) Bool(key string, value bool) LogEvent {
	if lea.filter != nil {
		filtered := lea.filter.FilterValue(key, value)
		if b, ok := filtered.(bool); ok {
			return lea.wrapEvent(lea.event.Bool(key, b))
		}
		// Sensitive field was masked to a string — fall back to Interface to preserve the mask
		return lea.wrapEvent(lea.event.Interface(key, filtered))
	}
	return lea.wrapEvent(lea.event.Bool(key, value))
}

// Enabled reports whether the underlying zerolog event will be emitted. It is
// nil-safe: zerolog returns a nil *Event for disabled levels, and *Event.Enabled
// returns false on a nil receiver.
func (lea *LogEventAdapter) Enabled() bool {
	return lea.event.Enabled()
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
