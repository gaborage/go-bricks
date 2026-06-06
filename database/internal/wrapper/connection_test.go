package wrapper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// recordingLogEvent is a logger.LogEvent test double that records field values
// instead of emitting them, so tests can assert on what AppendPoolFields adds.
type recordingLogEvent struct {
	fields map[string]any
}

func newRecordingLogEvent() *recordingLogEvent {
	return &recordingLogEvent{fields: map[string]any{}}
}

func (e *recordingLogEvent) Msg(string)                                    {}
func (e *recordingLogEvent) Msgf(string, ...any)                           {}
func (e *recordingLogEvent) Err(error) logger.LogEvent                     { return e }
func (e *recordingLogEvent) Str(k, v string) logger.LogEvent               { e.fields[k] = v; return e }
func (e *recordingLogEvent) Int(k string, v int) logger.LogEvent           { e.fields[k] = v; return e }
func (e *recordingLogEvent) Int64(k string, v int64) logger.LogEvent       { e.fields[k] = v; return e }
func (e *recordingLogEvent) Uint64(k string, v uint64) logger.LogEvent     { e.fields[k] = v; return e }
func (e *recordingLogEvent) Dur(k string, d time.Duration) logger.LogEvent { e.fields[k] = d; return e }
func (e *recordingLogEvent) Interface(k string, i any) logger.LogEvent     { e.fields[k] = i; return e }
func (e *recordingLogEvent) Bytes(k string, v []byte) logger.LogEvent      { e.fields[k] = v; return e }
func (e *recordingLogEvent) Bool(k string, v bool) logger.LogEvent         { e.fields[k] = v; return e }
func (e *recordingLogEvent) Enabled() bool                                 { return true }

func TestAppendPoolFields(t *testing.T) {
	cfg := &config.DatabaseConfig{}
	cfg.Pool.Max.Connections = 25
	cfg.Pool.Idle.Connections = 25
	cfg.Pool.Lifetime.Max = 30 * time.Minute
	cfg.Pool.Idle.Time = 5 * time.Minute

	ev := newRecordingLogEvent()
	AppendPoolFields(ev, cfg).Msg("connected")

	assert.Equal(t, 25, ev.fields["pool_max_connections"])
	assert.Equal(t, 25, ev.fields["pool_idle_connections"])
	assert.Equal(t, 30*time.Minute, ev.fields["pool_max_lifetime"])
	assert.Equal(t, 5*time.Minute, ev.fields["pool_idle_time"])
}
