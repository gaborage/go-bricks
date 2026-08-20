package database

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// newTestLogger creates a logger for database package tests.
// Uses debug level with pretty printing for better test output readability.
// This eliminates duplication of logger.New("debug", true) across 39+ test locations.
func newTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDebug, true)
}

// newErrorTestLogger creates an error-level logger for manager tests.
// Used when testing error conditions where only error logs should appear.
// This eliminates duplication of logger.New("error", false) across 10+ test locations.
func newErrorTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelError, false)
}

// warnRecorder is a logger.Logger double that records the message of every Warn() event.
// Other levels are discarded. NewDbManager logs nothing but this WARN at construction.
type warnRecorder struct{ warns []string }

func (r *warnRecorder) Info() logger.LogEvent                   { return &recordedEvent{} }
func (r *warnRecorder) Error() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Debug() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Fatal() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Warn() logger.LogEvent                   { return &recordedEvent{sink: r} }
func (r *warnRecorder) WithContext(any) logger.Logger           { return r }
func (r *warnRecorder) WithFields(map[string]any) logger.Logger { return r }

// recordedEvent appends to its sink on Msg; a nil sink discards, which is how the non-Warn
// levels are served.
type recordedEvent struct{ sink *warnRecorder }

func (e *recordedEvent) Msg(msg string) {
	if e.sink != nil {
		e.sink.warns = append(e.sink.warns, msg)
	}
}

func (e *recordedEvent) Msgf(format string, args ...any)           { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordedEvent) Err(error) logger.LogEvent                 { return e }
func (e *recordedEvent) Str(_, _ string) logger.LogEvent           { return e }
func (e *recordedEvent) Int(string, int) logger.LogEvent           { return e }
func (e *recordedEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *recordedEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *recordedEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *recordedEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *recordedEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *recordedEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *recordedEvent) Enabled() bool                             { return true }
