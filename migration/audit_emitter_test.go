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

// unloggableValue renders nothing: encoding it panics. It stands for any value a
// consumer's sink can panic with that the panic-reporting log call itself cannot
// render — the class deliverToSink's recovery handler has to survive, because by
// the time it logs it has already spent its recover().
type unloggableValue string

func (unloggableValue) MarshalJSON() ([]byte, error) {
	panic("encoding the recovered value panicked")
}

// unloggablePanicSink panics once with a value that cannot be logged, then
// records normally, so a test can tell whether the consumer goroutine survived.
type unloggablePanicSink struct {
	recordingSink
	panicked bool // consumer goroutine calls Record sequentially; no lock needed
}

func newUnloggablePanicSink() *unloggablePanicSink {
	return &unloggablePanicSink{recordingSink: recordingSink{hits: make(chan struct{}, 256)}}
}

func (s *unloggablePanicSink) Record(ctx context.Context, event *AuditEvent) error {
	if !s.panicked {
		s.panicked = true
		panic(unloggableValue("sink exploded"))
	}
	return s.recordingSink.Record(ctx, event)
}

// TestEmitterSinkPanicWithUnloggableValueDoesNotEscape restores the guarantee
// #686 shipped: "a faulty AuditRecorder cannot crash a migration mid-run".
// deliverToSink's recovery handler reports the recovered value with a log call;
// a panic in THAT call is inside a defer that has already recovered, so it
// propagates — out of consumeSink, which runs as a bare goroutine with no guard,
// and takes the process with it.
func TestEmitterSinkPanicWithUnloggableValueDoesNotEscape(t *testing.T) {
	setupTestTracer(t)
	mp := setupTestMeter(t) // BEFORE newAuditEmitter: counters bind at construction
	sink := newUnloggablePanicSink()
	// An ENABLED logger: the reporting call has to actually render the value.
	emitter := newAuditEmitter(logger.New("error", false), sink)
	t.Cleanup(func() { _ = emitter.Close(context.Background()) })

	emitter.Emit(context.Background(), baseEvent()) // sink panics unloggably
	emitter.Emit(context.Background(), baseEvent()) // consumer must survive to deliver this

	sink.waitForFirst(t, time.Second)
	require.Len(t, sink.snapshot(), 1, "second event should be delivered after the panic")

	rm := mp.Collect(t)
	obtest.AssertMetricValue(t, rm, "migration.audit.sink_failures", int64(1))
}

// TestEmitterSinkPanicWithUnloggableValueStillReports pins that the guard does not
// buy survival with silence. Nothing downstream of deliverToSink's defer logs, so
// swallowing the reporting call outright would leave a counter tick as the only
// trace of a dropped audit event — unattributable to a tenant or an event type.
func TestEmitterSinkPanicWithUnloggableValueStillReports(t *testing.T) {
	setupTestTracer(t)
	setupTestMeter(t)
	sink := newUnloggablePanicSink()

	output := captureMigrationStdout(t, func() {
		emitter := newAuditEmitter(logger.New("error", false), sink)
		emitter.Emit(context.Background(), baseEvent())
		emitter.Emit(context.Background(), baseEvent())
		sink.waitForFirst(t, time.Second)
		_ = emitter.Close(context.Background())
	})

	assert.Contains(t, output, "audit sink panicked", "the drop must still be reported")
	assert.Contains(t, output, testTarget, "the report must name the target it dropped")
}

// secretBearingPanic carries a secret alongside a field that cannot be rendered.
// The unrenderable field forces the fallback path; the secret is what must not
// reach the sink through it.
type secretBearingPanic struct {
	Detail   unloggableValue
	Password string
}

// TestEmitterSinkPanicFallbackReportsTypeNotValue pins that the fallback reports
// the panic's TYPE and never its value. The primary reporting call renders the
// value through the sensitive-data filter, which masks by field NAME; the
// fallback can only use Str, which masks on the key — so rendering the value
// there would emit a secret the primary path would have masked, in a place no
// needle can reach.
func TestEmitterSinkPanicFallbackReportsTypeNotValue(t *testing.T) {
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

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}
