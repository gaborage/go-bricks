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

// maskIfSensitive masks the key in place when it is sensitive, signaling via ok.
// Centralizes the typed-method mask check so adding a new typed slot (e.g. Float64) cannot
// silently bypass the privacy boundary by forgetting to wire the same conditional.
// zerolog's *Event.Interface mutates the event in place and returns the same pointer, so
// reusing the receiver here avoids allocating a wrapper per masked field.
func (lea *LogEventAdapter) maskIfSensitive(key string) (LogEvent, bool) {
	if lea.filter != nil && lea.filter.isSensitiveField(key) {
		lea.event = lea.event.Interface(key, lea.filter.config.MaskValue)
		return lea, true
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
	// A nil err emits nothing (zerolog's own behavior) and never reaches the
	// redactor. Neither does a disabled level: zerolog hands back a nil *Event
	// there and drops the field, but a redactor is unbounded consumer code, so
	// the framework checks before paying for it rather than after — unlike the
	// bounded needle lookups the other typed methods run. Redaction writes the
	// returned string under the same field name zerolog's Err would have used,
	// bypassing FilterString: the value is already the consumer's redacted
	// rendering, not raw field content.
	if err != nil && lea.event != nil {
		if redacted, ok := lea.filter.redactError(err); ok {
			// Masking runs on the redactor's OUTPUT, not instead of it: both
			// configured is the stricter-of-the-two case, and the redacted string
			// is still a value under a field name the operator called sensitive.
			if masked, sensitive := lea.filter.maskField(zerolog.ErrorFieldName, redacted); sensitive {
				redacted = masked
			}
			lea.event = lea.event.Str(zerolog.ErrorFieldName, redacted)
			return lea
		}
		// Field-name masking applies here too, so this door agrees with Str about a
		// field the operator marked sensitive. Without it, moving a call site from
		// Str("error", …) to Err would silently drop that masking and ship the
		// message in clear (CWE-532, found reviewing #1182). The check is a needle
		// lookup, and only a HIT rewrites anything — a miss falls through to
		// zerolog's own Err below, so the default configuration, where "error" is
		// not a needle, keeps its byte-identical rendering and its ErrorMarshalFunc.
		if masked, sensitive := lea.filter.maskField(zerolog.ErrorFieldName, err.Error()); sensitive {
			lea.event = lea.event.Str(zerolog.ErrorFieldName, masked)
			return lea
		}
	}
	lea.event = lea.event.Err(err)
	return lea
}

// Str adds a string field to the log event
func (lea *LogEventAdapter) Str(key, value string) LogEvent {
	if lea.filter != nil {
		value = lea.filter.FilterString(key, value)
	}
	lea.event = lea.event.Str(key, value)
	return lea
}

// Int adds an integer field to the log event
func (lea *LogEventAdapter) Int(key string, value int) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	lea.event = lea.event.Int(key, value)
	return lea
}

// Int64 adds an int64 field to the log event
func (lea *LogEventAdapter) Int64(key string, value int64) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	lea.event = lea.event.Int64(key, value)
	return lea
}

// Uint64 adds a uint64 field to the log event
func (lea *LogEventAdapter) Uint64(key string, value uint64) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	lea.event = lea.event.Uint64(key, value)
	return lea
}

// Dur adds a duration field to the log event
func (lea *LogEventAdapter) Dur(key string, d time.Duration) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	lea.event = lea.event.Dur(key, d)
	return lea
}

// Interface adds an any field to the log event
func (lea *LogEventAdapter) Interface(key string, i any) LogEvent {
	if lea.filter != nil {
		i = lea.filter.FilterValue(key, i)
	}
	lea.event = lea.event.Interface(key, i)
	return lea
}

// Bytes adds a byte slice field to the log event
func (lea *LogEventAdapter) Bytes(key string, val []byte) LogEvent {
	if masked, ok := lea.maskIfSensitive(key); ok {
		return masked
	}
	lea.event = lea.event.Bytes(key, val)
	return lea
}

// Bool adds a boolean field to the log event
func (lea *LogEventAdapter) Bool(key string, value bool) LogEvent {
	if lea.filter != nil {
		filtered := lea.filter.FilterValue(key, value)
		if b, ok := filtered.(bool); ok {
			lea.event = lea.event.Bool(key, b)
			return lea
		}
		// Sensitive field was masked to a string — fall back to Interface to preserve the mask
		lea.event = lea.event.Interface(key, filtered)
		return lea
	}
	lea.event = lea.event.Bool(key, value)
	return lea
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
