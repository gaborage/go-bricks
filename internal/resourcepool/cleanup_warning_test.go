package resourcepool

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// warnRecorder is a logger.Logger double that records the message and fields of every Warn()
// event. Non-Warn levels are discarded (nil sink).
type warnRecorder struct {
	msgs   []string
	fields []map[string]any
}

func (r *warnRecorder) Info() logger.LogEvent                   { return &warnEvent{} }
func (r *warnRecorder) Error() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Debug() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Fatal() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Warn() logger.LogEvent                   { return &warnEvent{sink: r, fields: map[string]any{}} }
func (r *warnRecorder) WithContext(any) logger.Logger           { return r }
func (r *warnRecorder) WithFields(map[string]any) logger.Logger { return r }

type warnEvent struct {
	sink   *warnRecorder
	fields map[string]any
}

func (e *warnEvent) Msg(msg string) {
	if e.sink == nil {
		return
	}
	e.sink.msgs = append(e.sink.msgs, msg)
	e.sink.fields = append(e.sink.fields, e.fields)
}
func (e *warnEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *warnEvent) Err(error) logger.LogEvent       { return e }
func (e *warnEvent) Str(k, v string) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Int(k string, v int) logger.LogEvent {
	return e.set(k, v)
}
func (e *warnEvent) Int64(k string, v int64) logger.LogEvent   { return e.set(k, v) }
func (e *warnEvent) Uint64(k string, v uint64) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Dur(k string, v time.Duration) logger.LogEvent {
	return e.set(k, v)
}
func (e *warnEvent) Interface(k string, v any) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Bytes(k string, v []byte) logger.LogEvent  { return e.set(k, v) }
func (e *warnEvent) Bool(k string, v bool) logger.LogEvent     { return e.set(k, v) }
func (e *warnEvent) Enabled() bool                             { return true }

func (e *warnEvent) set(k string, v any) logger.LogEvent {
	if e.fields != nil {
		e.fields[k] = v
	}
	return e
}

func TestWarnIfCleanupIntervalTooLate(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "cleanup_greater_than_idle_warns", cleanupInterval: 15 * time.Minute, idleTTL: 10 * time.Minute, wantWarn: true},
		{name: "cleanup_equals_idle_warns", cleanupInterval: 10 * time.Minute, idleTTL: 10 * time.Minute, wantWarn: true},
		{name: "cleanup_below_idle_ok", cleanupInterval: 2 * time.Minute, idleTTL: 1 * time.Hour, wantWarn: false},
		{name: "zero_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: 0, wantWarn: false},
		{name: "zero_cleanup_below_positive_idle_ok", cleanupInterval: 0, idleTTL: 1 * time.Hour, wantWarn: false},
		{name: "negative_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: -1 * time.Second, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			WarnIfCleanupIntervalTooLate(rec, "database.manager", tc.cleanupInterval, tc.idleTTL)

			if !tc.wantWarn {
				assert.Empty(t, rec.msgs, "an interval that sweeps more often than the TTL must stay silent")
				return
			}
			require.Len(t, rec.msgs, 1, "a late cleanup interval must WARN exactly once")
			assert.Equal(t,
				"database.manager.cleanupinterval is >= database.manager.idlettl; "+
					"idle handle eviction will lag by up to one extra cleanup cycle "+
					"(lower database.manager.cleanupinterval or raise database.manager.idlettl)",
				rec.msgs[0])
			assert.Equal(t, "database.manager", rec.fields[0]["resource"])
			assert.Equal(t, tc.cleanupInterval, rec.fields[0]["cleanupinterval"])
			assert.Equal(t, tc.idleTTL, rec.fields[0]["idlettl"])
		})
	}
}

// TestWarnIfCleanupIntervalTooLateUsesTheCallersKeyPrefix pins that the message and the
// "resource" field are built from the caller's prefix, so the messaging manager's WARN names
// messaging.publisher rather than the database's keys.
func TestWarnIfCleanupIntervalTooLateUsesTheCallersKeyPrefix(t *testing.T) {
	rec := &warnRecorder{}
	WarnIfCleanupIntervalTooLate(rec, "messaging.publisher", time.Minute, time.Minute)

	require.Len(t, rec.msgs, 1)
	assert.Contains(t, rec.msgs[0], "messaging.publisher.cleanupinterval is >= messaging.publisher.idlettl")
	assert.Equal(t, "messaging.publisher", rec.fields[0]["resource"])
}
