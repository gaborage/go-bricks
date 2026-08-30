package lanecontract

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// SetupTelemetry installs a test tracer and meter provider, both restored on
// cleanup. The tracking meter and the pipeline's tracer are package singletons,
// so both resets bracket the test on both sides: without the reset a provider
// swapped in here is never observed, since the singleton still holds the
// previous one.
//
// Call it before constructing anything that caches a tracer. The streams lane
// resolves its tracer at manager construction, not per delivery, so a lane built
// first pins the previous provider and reports zero spans — a false failure that
// looks like a broken assertion.
func SetupTelemetry(t *testing.T) (*tracetest.InMemoryExporter, *obtest.TestMeterProvider) {
	t.Helper()

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	prevMP := otel.GetMeterProvider()

	ttp := obtest.NewTestTraceProvider()
	otel.SetTracerProvider(ttp)
	// Defensive only: nothing in the delivery path reads the global propagator —
	// trace.ExtractFromHeaders parses the carrier into context values by hand, so
	// the consume span is a root on both lanes and no family can assert otherwise.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()
	delivery.ResetTracerForTesting()

	t.Cleanup(func() {
		// Restore and reset BEFORE asserting: require.NoError runs Goexit on
		// failure, which would skip everything after it and leave a shut-down
		// provider installed process-wide, so every later test in the binary
		// would silently record nothing.
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		otel.SetMeterProvider(prevMP)
		tracking.ResetMeterForTesting()
		delivery.ResetTracerForTesting()
		require.NoError(t, ttp.Shutdown(context.Background()))
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	return ttp.Exporter, mp
}

// Outcomes records every Result a lane's LogOutcome was handed. The mutex is
// load-bearing, not defensive: the streams lane invokes handlers from one
// goroutine per partition, so the unsynchronized recorder this was lifted from
// would race there.
type Outcomes struct {
	mu   sync.Mutex
	seen []*delivery.Result
}

// Log is the LogOutcome hook: hand it to delivery.Request.LogOutcome directly.
func (o *Outcomes) Log(res *delivery.Result) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, res)
}

// Seen copies the recorded results, so reading them cannot race a delivery still
// in flight on another partition.
func (o *Outcomes) Seen() []*delivery.Result {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*delivery.Result, len(o.seen))
	copy(out, o.seen)
	return out
}

// RecordingLogger captures every emitted line with its fields in order.
// Derived loggers share the parent's buffer and mutex, so a lane's per-message
// logger records into the same place.
type RecordingLogger struct {
	mu     *sync.Mutex
	lines  *[]LogLine
	fields [][2]string // context-level fields prepended to every event
}

// NewRecordingLogger returns a logger recording into a fresh buffer.
func NewRecordingLogger() *RecordingLogger {
	return &RecordingLogger{mu: &sync.Mutex{}, lines: &[]LogLine{}}
}

// Lines copies every line recorded so far, including through derived loggers.
func (l *RecordingLogger) Lines() []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LogLine, len(*l.lines))
	copy(out, *l.lines)
	return out
}

// WithContext returns a logger sharing this one's buffer.
func (l *RecordingLogger) WithContext(_ any) logger.Logger {
	return &RecordingLogger{mu: l.mu, lines: l.lines, fields: l.fields}
}

// WithFields returns a logger carrying f on every later event.
func (l *RecordingLogger) WithFields(f map[string]any) logger.Logger {
	merged := make([][2]string, 0, len(l.fields)+len(f))
	merged = append(merged, l.fields...)
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys) // map order is random; sort so the recorded shape is stable
	for _, k := range keys {
		merged = append(merged, [2]string{k, fmt.Sprint(f[k])})
	}
	return &RecordingLogger{mu: l.mu, lines: l.lines, fields: merged}
}

func (l *RecordingLogger) Info() logger.LogEvent  { return l.event(LevelInfo) }
func (l *RecordingLogger) Error() logger.LogEvent { return l.event(LevelError) }
func (l *RecordingLogger) Debug() logger.LogEvent { return l.event(LevelDebug) }
func (l *RecordingLogger) Warn() logger.LogEvent  { return l.event(LevelWarn) }
func (l *RecordingLogger) Fatal() logger.LogEvent { return l.event(LevelFatal) }

func (l *RecordingLogger) event(level string) *recordingEvent {
	fields := make([][2]string, len(l.fields), len(l.fields)+8)
	copy(fields, l.fields)
	return &recordingEvent{log: l, level: level, fields: fields}
}

// recordingEvent records every field write in order. Every setter stringifies,
// so a family can read an Int64 offset or a Uint64 delivery tag as readily as a
// Str — unlike the streams lane's own recorder, which keeps only Str.
type recordingEvent struct {
	log    *RecordingLogger
	level  string
	fields [][2]string
}

func (e *recordingEvent) add(key string, value any) logger.LogEvent {
	e.fields = append(e.fields, [2]string{key, fmt.Sprint(value)})
	return e
}

func (e *recordingEvent) Str(k, v string) logger.LogEvent               { return e.add(k, v) }
func (e *recordingEvent) Int(k string, v int) logger.LogEvent           { return e.add(k, v) }
func (e *recordingEvent) Int64(k string, v int64) logger.LogEvent       { return e.add(k, v) }
func (e *recordingEvent) Uint64(k string, v uint64) logger.LogEvent     { return e.add(k, v) }
func (e *recordingEvent) Dur(k string, v time.Duration) logger.LogEvent { return e.add(k, v) }
func (e *recordingEvent) Interface(k string, v any) logger.LogEvent     { return e.add(k, v) }
func (e *recordingEvent) Bytes(k string, v []byte) logger.LogEvent      { return e.add(k, string(v)) }
func (e *recordingEvent) Bool(k string, v bool) logger.LogEvent         { return e.add(k, v) }
func (e *recordingEvent) Enabled() bool                                 { return true }

func (e *recordingEvent) Err(err error) logger.LogEvent {
	if err == nil {
		return e
	}
	return e.add("error", err.Error())
}

func (e *recordingEvent) Msg(msg string) {
	e.log.mu.Lock()
	defer e.log.mu.Unlock()
	*e.log.lines = append(*e.log.lines, LogLine{Level: e.level, Msg: msg, Fields: e.fields})
}

func (e *recordingEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }

var (
	_ logger.Logger   = (*RecordingLogger)(nil)
	_ logger.LogEvent = (*recordingEvent)(nil)
)
