package migration

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// secretBearingPanic carries a secret under a field name the needle list DOES
// name. That is the point: relying on the filter would have looked safe for this
// shape and failed on the ones it cannot reach, so the report must withhold the
// value regardless of shape.
type secretBearingPanic struct {
	Detail   string
	Password string
}

// TestEmitterSinkPanicReportsTypeNotValue pins the stronger half of the rule: the
// report names the panic's TYPE even when the value is one the filter WOULD have
// masked. secretBearingPanic carries `Password`, which the needle list does name —
// so relying on the filter would have looked safe here and failed on the shapes it
// cannot reach (a bare string, or a key the list does not name). The report does
// not depend on the value's shape at all.
func TestEmitterSinkPanicReportsTypeNotValue(t *testing.T) {
	setupTestTracer(t)
	setupTestMeter(t)
	sink := &secretPanicSink{recordingSink: recordingSink{hits: make(chan struct{}, 256)}}

	output := captureMigrationStdout(t, func() {
		emitter := newAuditEmitter(logger.New("error", false), sink)
		emitter.Emit(context.Background(), baseEvent())
		emitter.Emit(context.Background(), baseEvent())
		sink.waitForFirst(t, time.Second)
		_ = emitter.Close(context.Background())
	})

	assert.NotContains(t, output, sinkPanicSecret, "the panic value must never reach the sink")
	assert.Contains(t, output, "secretBearingPanic", "the panic's type must be reported")
	assert.Contains(t, output, testTarget, "the drop must stay attributable")
}

const sinkPanicSecret = "test_password_123"

type secretPanicSink struct {
	recordingSink
	panicked bool
}

func (s *secretPanicSink) Record(ctx context.Context, event *AuditEvent) error {
	if !s.panicked {
		s.panicked = true
		panic(secretBearingPanic{Detail: "boom", Password: sinkPanicSecret})
	}
	return s.recordingSink.Record(ctx, event)
}

// captureMigrationStdout redirects os.Stdout for the duration of fn. The framework
// logger writes there directly.
func captureMigrationStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	// Drain CONCURRENTLY. A panic report carries debug.Stack(), which can exceed
	// the pipe buffer; reading only after fn returns would block the writer
	// forever and hang the test instead of failing it.
	var buf bytes.Buffer
	copied := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		copied <- copyErr
	}()

	fn()

	require.NoError(t, w.Close())
	require.NoError(t, <-copied)
	return buf.String()
}

// panickingLogger is a logger.Logger whose events panic when written. It stands
// for a consumer-supplied logger or writer that fails during output — the one
// thing the report cannot assume works, since the report IS a log call and it
// runs after the guard has already spent its recover.
type panickingLogger struct{ logger.Logger }

func (l *panickingLogger) Error() logger.LogEvent { return &panickingEvent{} }
func (l *panickingLogger) Info() logger.LogEvent  { return &panickingEvent{} }
func (l *panickingLogger) Warn() logger.LogEvent  { return &panickingEvent{} }
func (l *panickingLogger) Debug() logger.LogEvent { return &panickingEvent{} }
func (l *panickingLogger) Fatal() logger.LogEvent { return &panickingEvent{} }
func (l *panickingLogger) WithContext(_ any) logger.Logger {
	return l
}

func (l *panickingLogger) WithFields(_ map[string]any) logger.Logger { return l }

type panickingEvent struct{ reporting bool }

// Msg panics only for the panic-REPORTING call, identified by the `panic_type`
// key that only it uses. DO NOT "simplify" this to panic on every call: a fault-injection double
// has to be aimed at the exact call the defect travels through, or it tests a
// different failure entirely. Panicking on everything takes out Emit's own
// structured log on the CALLER's goroutine — a surface deliverToSink's escape
// route cannot reach — so the test would pass a fix that does nothing here and
// fail one that works. The tell is a failure surfacing somewhere the defect
// could not have reached.
func (e *panickingEvent) Msg(string) {
	if e.reporting {
		panic("logger write failed")
	}
}

func (e *panickingEvent) Msgf(string, ...any)       { e.Msg("") }
func (e *panickingEvent) Err(error) logger.LogEvent { return e }
func (e *panickingEvent) Str(key, _ string) logger.LogEvent {
	if key == "panic_type" { // the panic report
		e.reporting = true
	}
	return e
}
func (e *panickingEvent) Int(string, int) logger.LogEvent           { return e }
func (e *panickingEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *panickingEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *panickingEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *panickingEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *panickingEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *panickingEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *panickingEvent) Enabled() bool                             { return true }

// TestEmitterSinkPanicSurvivesAPanickingLogger pins the terminal swallow. The
// report is a log call on a consumer-supplied logger, and the defer around it has
// already spent its recover() — so a panic there escapes into consumeSink's bare
// goroutine unless the swallow catches it. The consumer goroutine must survive AND
// the accounting that ran BEFORE the report must stand, so the drop is still counted.
func TestEmitterSinkPanicSurvivesAPanickingLogger(t *testing.T) {
	setupTestTracer(t)
	mp := setupTestMeter(t) // BEFORE newAuditEmitter: counters bind at construction
	sink := &barePanicSink{recordingSink: recordingSink{hits: make(chan struct{}, 256)}}
	emitter := newAuditEmitter(&panickingLogger{}, sink)
	t.Cleanup(func() { _ = emitter.Close(context.Background()) })

	require.NotPanics(t, func() {
		emitter.Emit(context.Background(), baseEvent())
		emitter.Emit(context.Background(), baseEvent())
		sink.waitForFirst(t, time.Second)
	})

	// The consumer goroutine survived both failed reports.
	require.Len(t, sink.snapshot(), 1, "second event should be delivered after the panic")
	// And the accounting that precedes the reporting still ran.
	rm := mp.Collect(t)
	obtest.AssertMetricValue(t, rm, "migration.audit.sink_failures", int64(1))
}

const barePanicSecret = "not-a-real-secret-0000"

// barePanicSink panics with a bare string. Name-matching cannot help here: the
// log field is `panic`, which is not a needle, and a string value has no inner
// field name to match — so nothing about the sensitive-data filter protects it.
type barePanicSink struct {
	recordingSink
	panicked bool
}

func (s *barePanicSink) Record(ctx context.Context, event *AuditEvent) error {
	if !s.panicked {
		s.panicked = true
		panic(barePanicSecret)
	}
	return s.recordingSink.Record(ctx, event)
}

// TestEmitterSinkPanicNeverDisclosesTheValue pins that a recovered panic value is
// never written to the sink, on EITHER reporting path. The primary path used to
// log it via Interface("panic", r) and rely on the filter, but the filter masks by
// FIELD NAME — it cannot reach a bare string, and it misses any map key the needle
// list does not name. Only the type is safe to report.
func TestEmitterSinkPanicNeverDisclosesTheValue(t *testing.T) {
	setupTestTracer(t)
	setupTestMeter(t)
	sink := &barePanicSink{recordingSink: recordingSink{hits: make(chan struct{}, 256)}}

	output := captureMigrationStdout(t, func() {
		emitter := newAuditEmitter(logger.New("error", false), sink)
		emitter.Emit(context.Background(), baseEvent())
		emitter.Emit(context.Background(), baseEvent())
		sink.waitForFirst(t, time.Second)
		_ = emitter.Close(context.Background())
	})

	assert.NotContains(t, output, barePanicSecret,
		"a recovered panic value must never reach the sink; the filter cannot mask a bare string")
	assert.Contains(t, output, "audit sink panicked",
		"the drop must still be reported")
	assert.Contains(t, output, `"panic_type":"string"`,
		"the panic's type must be reported instead — parity with the scheduler pin")
	assert.Contains(t, output, testTarget,
		"the report must stay attributable")
}
