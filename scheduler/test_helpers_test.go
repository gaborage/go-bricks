package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// assertWaitGroupDrains fails if module m's in-flight WaitGroup does not reach
// zero within 100ms after the code path under test returned. A hang means a
// wg.Add(1) was not matched by a deferred wg.Done() on that path — surfaced as a
// failure, not a hung test.
func assertWaitGroupDrains(t *testing.T, m *Module, afterContext string) {
	t.Helper()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("wg.Wait() blocked after %s — Add(1) was not balanced by Done()", afterContext)
	}
}

type testSchedulerOption func(*app.ModuleDeps)

func withTracer(tr trace.Tracer) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Tracer = tr }
}

func withMeterProvider(mp metric.MeterProvider) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.MeterProvider = mp }
}

func withDB(fn func(context.Context) (types.Interface, error)) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.DB = fn }
}

func withMessaging(fn func(context.Context) (messaging.AMQPClient, error)) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Messaging = fn }
}

func withLogger(l logger.Logger) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Logger = l }
}

func withSlowJobThreshold(threshold time.Duration) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Timeout.SlowJob = threshold }
}

func withTimezone(tz string) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Timezone = tz }
}

func withSummaryWindow(window time.Duration) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Summary.Window = window }
}

// fakeClock is the test clock behind Module.now.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newWindowedTestScheduler builds a scheduler whose clock and summary window the
// test owns. Tests drive runs through runJobBody, so register windowed jobs with an
// interval no real tick reaches during the test.
func newWindowedTestScheduler(t *testing.T, window time.Duration, clock *fakeClock, opts ...testSchedulerOption) *Module {
	t.Helper()
	module, _ := newTestScheduler(t, 5*time.Second, append([]testSchedulerOption{withSummaryWindow(window)}, opts...)...)
	module.now = clock.Now
	return module
}

func registeredEntry(t *testing.T, m *Module, jobID string) *jobEntry {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.jobs[jobID]
	require.True(t, ok, "job %q is not registered", jobID)
	return entry
}

// recordedLine is one line a recordingLogger wrote.
type recordedLine struct {
	level      string
	msg        string
	fields     map[string]any
	viaContext bool
}

// recordingLogger keeps every line it writes, so a test reads fields by name. It
// replaces captureStdout where live goroutines would race the os.Stdout swap.
type recordingLogger struct {
	sink       *lineSink
	viaContext bool
}

type lineSink struct {
	mu    sync.Mutex
	lines []recordedLine
}

func newRecordingLogger() *recordingLogger { return &recordingLogger{sink: &lineSink{}} }

func (l *recordingLogger) event(level string) logger.LogEvent {
	return &recordingEvent{sink: l.sink, line: recordedLine{level: level, fields: map[string]any{}, viaContext: l.viaContext}}
}

func (l *recordingLogger) Info() logger.LogEvent  { return l.event("info") }
func (l *recordingLogger) Error() logger.LogEvent { return l.event("error") }
func (l *recordingLogger) Debug() logger.LogEvent { return l.event("debug") }
func (l *recordingLogger) Warn() logger.LogEvent  { return l.event("warn") }
func (l *recordingLogger) Fatal() logger.LogEvent { return l.event("fatal") }

func (l *recordingLogger) WithContext(any) logger.Logger {
	return &recordingLogger{sink: l.sink, viaContext: true}
}

func (l *recordingLogger) WithFields(map[string]any) logger.Logger { return l }

func (l *recordingLogger) lines() []recordedLine {
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	return append([]recordedLine(nil), l.sink.lines...)
}

func (l *recordingLogger) linesWith(match func(recordedLine) bool) []recordedLine {
	var out []recordedLine
	for _, line := range l.lines() {
		if match(line) {
			out = append(out, line)
		}
	}
	return out
}

func (l *recordingLogger) actionLines() []recordedLine {
	return l.linesWith(func(line recordedLine) bool { return line.fields["log.type"] == "action" })
}

// windowLines returns the window summary lines: the only lines carrying window.start.
func (l *recordingLogger) windowLines() []recordedLine {
	return l.linesWith(func(line recordedLine) bool { _, ok := line.fields["window.start"]; return ok })
}

func (l *recordingLogger) messageLines(msg string) []recordedLine {
	return l.linesWith(func(line recordedLine) bool { return line.msg == msg })
}

type recordingEvent struct {
	sink *lineSink
	line recordedLine
}

func (e *recordingEvent) set(key string, value any) logger.LogEvent {
	e.line.fields[key] = value
	return e
}

func (e *recordingEvent) Str(key, value string) logger.LogEvent         { return e.set(key, value) }
func (e *recordingEvent) Int(key string, value int) logger.LogEvent     { return e.set(key, value) }
func (e *recordingEvent) Int64(key string, value int64) logger.LogEvent { return e.set(key, value) }
func (e *recordingEvent) Uint64(key string, value uint64) logger.LogEvent {
	return e.set(key, value)
}
func (e *recordingEvent) Dur(key string, d time.Duration) logger.LogEvent { return e.set(key, d) }
func (e *recordingEvent) Interface(key string, i any) logger.LogEvent     { return e.set(key, i) }
func (e *recordingEvent) Bytes(key string, val []byte) logger.LogEvent    { return e.set(key, val) }
func (e *recordingEvent) Bool(key string, value bool) logger.LogEvent     { return e.set(key, value) }
func (e *recordingEvent) Enabled() bool                                   { return true }

// Err mirrors zerolog: a nil error adds no field.
func (e *recordingEvent) Err(err error) logger.LogEvent {
	if err == nil {
		return e
	}
	return e.set("error", err.Error())
}

func (e *recordingEvent) Msg(msg string) {
	e.line.msg = msg
	e.sink.mu.Lock()
	defer e.sink.mu.Unlock()
	e.sink.lines = append(e.sink.lines, e.line)
}

func (e *recordingEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }

// newTracedMeteredScheduler wires a scheduler with both providers, so a recorded
// metric point can be matched against the span it was recorded under.
func newTracedMeteredScheduler(t *testing.T) (*Module, *obtest.TestTraceProvider, *obtest.TestMeterProvider) {
	t.Helper()

	tp := obtest.NewTestTraceProvider()
	mp := obtest.NewTestMeterProvider()
	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	module, _ := newTestScheduler(t, 5*time.Second,
		withTracer(tp.Tracer("test-scheduler")),
		withMeterProvider(mp.MeterProvider))

	return module, tp, mp
}

// newTestScheduler creates and initializes a scheduler module for testing.
func newTestScheduler(t *testing.T, shutdownTimeout time.Duration, opts ...testSchedulerOption) (*Module, app.JobRegistrar) {
	module := NewModule()

	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timezone: config.DefaultTimezone,
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: shutdownTimeout,
					// Positive threshold, as config normalization guarantees, so
					// short test jobs are not all WARN-flagged as slow.
					SlowJob: 25 * time.Second,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		DB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	for _, o := range opts {
		o(appDeps)
	}

	require.NoError(t, module.Init(appDeps), "Module initialization should succeed")
	t.Cleanup(func() {
		if err := module.Shutdown(); err != nil {
			t.Errorf("module shutdown failed during cleanup: %v", err)
		}
	})

	return module, module
}
