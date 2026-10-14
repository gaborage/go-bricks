package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gaborage/go-bricks/logger"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// windowTestStart sits one second into an hour, so a 1h window opens at the top of it.
var windowTestStart = time.Date(2026, 10, 14, 3, 0, 1, 0, time.UTC)

const (
	windowTestWidth    = time.Hour
	windowTestInterval = 10 * time.Minute
)

func TestWindowedJobSummarizesAThousandFailingRunsInOneWarnLine(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log))

	job := Windowed(func(_ JobContext, rec *Recorder) error {
		rec.Fail("published", 1)
		return nil
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	for range 1000 {
		module.runJobBody(entry, triggerScheduled)
	}
	clock.Advance(windowTestWidth)
	module.runJobBody(entry, triggerScheduled)

	edges := log.edgeLines()
	require.Len(t, edges, 1, "the first failing run is the job's one clean-to-failing edge")
	assert.Equal(t, "new", edges[0].fields["edge.reason"])
	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Len(t, log.actionLines(), 2, "the window and edge lines are the only action lines: no per-run action line")
	line := windows[0]
	assert.False(t, line.viaContext, "the window line goes through the module logger, never a run's WithContext")
	assert.Equal(t, "warn", line.level)
	assert.Equal(t, "2026-10-14T03:00:00Z", line.fields["window.start"])
	assert.Equal(t, int64(1000), line.fields["runs"])
	assert.Equal(t, int64(1000), line.fields["runs.failing"])
}

// gatedJob is a Windowed job whose run blocks until release closes, then ends with
// finish; started receives once per run.
func gatedJob(started chan<- struct{}, release <-chan struct{}, finish func(*Recorder) error) Executor {
	return Windowed(func(_ JobContext, rec *Recorder) error {
		started <- struct{}{}
		<-release
		return finish(rec)
	}, Counter{Name: "published"})
}

func finishClean(*Recorder) error { return nil }

// startRun drives one scheduled trigger in the background; the channel closes when it returns.
func startRun(module *Module, entry *jobEntry) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		module.runJobBody(entry, triggerScheduled)
	}()
	return done
}

func newGatedRelay(t *testing.T, log *recordingLogger, clock *fakeClock) (module *Module, entry *jobEntry, started, release chan struct{}) {
	t.Helper()
	module = newWindowedTestScheduler(t, clock, withLogger(log))
	started, release = make(chan struct{}, 1), make(chan struct{})
	require.NoError(t, module.FixedRate("relay", gatedJob(started, release, finishClean), windowTestInterval))
	return module, registeredEntry(t, module, "relay"), started, release
}

func TestWindowedRunCountsInTheWindowItCompletesIn(t *testing.T) {
	tests := []struct {
		name           string
		closeByTrigger bool
	}{
		{name: "a_trigger_closes_the_window", closeByTrigger: true},
		{name: "the_completion_closes_the_window"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			clock := newFakeClock(windowTestStart)
			module, entry, started, release := newGatedRelay(t, log, clock)

			done := startRun(module, entry)
			<-started
			clock.Advance(windowTestWidth)
			if tt.closeByTrigger {
				module.runJobBody(entry, triggerScheduled)
				require.Len(t, log.windowLines(), 1, "the trigger past the boundary closes window k")
			} else {
				require.Empty(t, log.windowLines())
			}
			close(release)
			<-done

			windows := log.windowLines()
			require.Len(t, windows, 1)
			assert.Equal(t, "info", windows[0].level, "a window holding only an in-flight run is not a warning")
			assert.Equal(t, "2026-10-14T03:00:00Z", windows[0].fields["window.start"])
			assert.Equal(t, "2026-10-14T04:00:00Z", windows[0].fields["window.end"])
			assert.Equal(t, int64(0), windows[0].fields["runs"])
			assert.Equal(t, int64(1), windows[0].fields["runs.inflight"])

			clock.Advance(windowTestWidth)
			module.runJobBody(entry, triggerScheduled)
			<-started

			windows = log.windowLines()
			require.Len(t, windows, 2)
			assert.Equal(t, "2026-10-14T04:00:00Z", windows[1].fields["window.start"])
			assert.Equal(t, int64(1), windows[1].fields["runs"], "the run counts in the window it completed in")
			assert.Equal(t, int64(0), windows[1].fields["runs.inflight"])
		})
	}
}

func TestHungWindowedRunWritesOneWarnLinePerWindow(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module, entry, started, release := newGatedRelay(t, log, clock)

	done := startRun(module, entry)
	<-started
	defer func() { close(release); <-done }()

	const triggersPerWindow = 4
	for range 3 {
		for range triggersPerWindow {
			module.runJobBody(entry, triggerScheduled)
			clock.Advance(windowTestInterval)
		}
		clock.Advance(windowTestWidth - triggersPerWindow*windowTestInterval)
	}

	for _, skip := range log.messageLines("Job trigger skipped - job is already running") {
		assert.Equal(t, "debug", skip.level, "a Windowed job's skips are counted, not warned per trigger")
	}
	assert.Equal(t, int64(3*triggersPerWindow), entry.metadata.snapshot().SkippedCount, "/_sys/job still counts every skip")
	windows := log.windowLines()
	require.Len(t, windows, 2, "each window the hung run spans closes on the next window's first trigger")
	var previous string
	for _, line := range windows {
		assert.Equal(t, "warn", line.level)
		assert.Equal(t, int64(0), line.fields["runs"])
		assert.Equal(t, int64(triggersPerWindow), line.fields["runs.skipped"])
		assert.Equal(t, int64(1), line.fields["runs.inflight"])
		traceparent, ok := line.fields["traceparent"].(string)
		require.True(t, ok, "the traceparent key stays on the line")
		assert.Empty(t, traceparent, "no run completed, so there is no span to point at")
		id, _ := line.fields["correlation_id"].(string)
		_, err := uuid.Parse(id)
		require.NoError(t, err, "a line with no completed run mints its own correlation_id")
		assert.NotEqual(t, previous, id)
		previous = id
	}
}

// stopTimedOutScheduler stands in for gocron's shutdown returning ErrStopJobsTimedOut.
type stopTimedOutScheduler struct{ gocron.Scheduler }

func (s stopTimedOutScheduler) Shutdown() error {
	_ = s.Scheduler.Shutdown()
	return gocron.ErrStopJobsTimedOut
}

func TestShutdownFlushesThePartialWindowOnEveryExit(t *testing.T) {
	t.Run("gocron_stop_timeout", func(t *testing.T) {
		log := newRecordingLogger()
		module, entry := newFailingRelay(t, log, newFakeClock(windowTestStart))
		module.runJobBody(entry, triggerScheduled)
		module.mu.Lock()
		module.scheduler = stopTimedOutScheduler{module.scheduler}
		module.mu.Unlock()

		require.ErrorIs(t, module.Shutdown(), gocron.ErrStopJobsTimedOut)

		requirePartialWindow(t, log, 1, 0)
	})

	t.Run("jobs_done", func(t *testing.T) {
		log := newRecordingLogger()
		module, entry := newFailingRelay(t, log, newFakeClock(windowTestStart))
		module.runJobBody(entry, triggerScheduled)

		require.NoError(t, module.Shutdown())

		requirePartialWindow(t, log, 1, 0)
	})

	lateCompletions := []struct {
		name      string
		finish    func(*Recorder) error
		wantLevel string
	}{
		{name: "timeout_then_a_clean_late_completion", finish: finishClean, wantLevel: "info"},
		{name: "timeout_then_an_erroring_late_completion", finish: func(*Recorder) error { return errors.New("relay failed") }, wantLevel: "error"},
		{name: "timeout_then_a_failing_late_completion", finish: func(rec *Recorder) error {
			rec.Fail("published", 1)
			return nil
		}, wantLevel: "warn"},
	}
	for _, tt := range lateCompletions {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			clock := newFakeClock(windowTestStart)
			module := newWindowedTestScheduler(t, clock, withLogger(log), withShutdownTimeout(20*time.Millisecond))
			started, release := make(chan struct{}, 1), make(chan struct{})
			require.NoError(t, module.FixedRate("relay", gatedJob(started, release, tt.finish), windowTestInterval))
			wrapper := module.createJobWrapper(registeredEntry(t, module, "relay"))
			done := make(chan struct{})
			go func() { defer close(done); wrapper() }()
			<-started

			require.ErrorContains(t, module.Shutdown(), "shutdown timeout")
			requirePartialWindow(t, log, 0, 1)

			close(release)
			<-done
			late := log.linesWith(func(line recordedLine) bool {
				return line.fields["log.type"] == "action" && line.fields["window.start"] == nil
			})
			require.Len(t, late, 1, "a completion after the seal writes one per-run action line")
			assert.Equal(t, tt.wantLevel, late[0].level)
			assert.Len(t, log.windowLines(), 1, "the sealed window takes no late completion")
		})
	}
}

func TestShutdownWritesAnElapsedWindowWhole(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log))
	require.NoError(t, module.FixedRate("relay", Windowed(noopRun), windowTestInterval))
	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	clock.Advance(windowTestWidth)

	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1, "the empty window shutdown falls in writes nothing")
	partial, _ := windows[0].fields["window.partial"].(bool)
	assert.False(t, partial, "the window ended before shutdown, so it is whole")
	assert.Equal(t, int64(1), windows[0].fields["runs"])
}

func TestPanickingCompletionClosesTheWindow(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log))
	started, release := make(chan struct{}, 1), make(chan struct{})
	require.NoError(t, module.FixedRate("relay", gatedJob(started, release, func(*Recorder) error { panic("boom") }), windowTestInterval))

	done := startRun(module, registeredEntry(t, module, "relay"))
	<-started
	clock.Advance(windowTestWidth)
	close(release)
	<-done

	windows := log.windowLines()
	require.Len(t, windows, 1, "the panicking completion closes window k")
	assert.Equal(t, int64(1), windows[0].fields["runs.inflight"])
}

// linePanickingLogger panics the first time it writes a line carrying one of its
// armed keys; later lines, and every other line, write normally. Only Str keeps
// the wrapper, so arm a key the line sets before any other method.
type linePanickingLogger struct {
	*recordingLogger
	armed map[string]*atomic.Bool
}

func newLinePanickingLogger(keys ...string) linePanickingLogger {
	armed := make(map[string]*atomic.Bool, len(keys))
	for _, key := range keys {
		armed[key] = &atomic.Bool{}
	}
	return linePanickingLogger{newRecordingLogger(), armed}
}

func (l linePanickingLogger) wrap(event logger.LogEvent) logger.LogEvent {
	return linePanickingEvent{event, l.armed}
}

func (l linePanickingLogger) Info() logger.LogEvent  { return l.wrap(l.recordingLogger.Info()) }
func (l linePanickingLogger) Warn() logger.LogEvent  { return l.wrap(l.recordingLogger.Warn()) }
func (l linePanickingLogger) Error() logger.LogEvent { return l.wrap(l.recordingLogger.Error()) }
func (l linePanickingLogger) Debug() logger.LogEvent { return l.wrap(l.recordingLogger.Debug()) }

func (l linePanickingLogger) WithContext(any) logger.Logger { return l }

type linePanickingEvent struct {
	logger.LogEvent
	armed map[string]*atomic.Bool
}

func (e linePanickingEvent) Str(key, value string) logger.LogEvent {
	if fired, ok := e.armed[key]; ok && fired.CompareAndSwap(false, true) {
		panic("logger write failed")
	}
	return linePanickingEvent{e.LogEvent.Str(key, value), e.armed}
}

func TestPanicAfterTheSettleCountsTheRunOnce(t *testing.T) {
	// job.schedule_type is on the run's own summary line, written after its settle.
	log := newLinePanickingLogger("job.schedule_type")
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
	require.NoError(t, module.FixedRate("relay", Windowed(noopRun), windowTestInterval))

	require.NotPanics(t, func() { module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled) })
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, int64(1), windows[0].fields["runs"], "the recovery defer must not settle a settled run again")
	assert.Empty(t, windows[0].fields["failing.keys"])
}

func TestPanicPathSurvivesPanickingSummaryLines(t *testing.T) {
	clock := newFakeClock(windowTestStart)
	log := newLinePanickingLogger("window.start", "edge.reason")
	module := newWindowedTestScheduler(t, clock, withLogger(log))
	started, release := make(chan struct{}, 1), make(chan struct{})
	require.NoError(t, module.FixedRate("relay", gatedJob(started, release, func(*Recorder) error { panic("boom") }), windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	done := make(chan any)
	go func() {
		defer func() { done <- recover() }()
		module.runJobBody(entry, triggerScheduled)
	}()
	<-started
	clock.Advance(windowTestWidth)
	close(release)

	require.Nil(t, <-done, "a summary line's panic must not escape the recovery defer")
	for key, fired := range log.armed {
		assert.True(t, fired.Load(), "the %s line was written and panicked", key)
	}
	assert.Equal(t, int64(1), entry.metadata.snapshot().FailureCount, "the run is still recorded as failed")
}

func TestSealedWindowIgnoresALateTrigger(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module, entry := newFailingRelay(t, log, clock)
	module.runJobBody(entry, triggerScheduled)
	require.NoError(t, module.Shutdown())
	clock.Advance(windowTestWidth)

	// A trigger that passed the shutdown check just before the seal.
	acquired, closed := entry.window.trigger(clock.Now(), entry.tryLock)
	entry.unlock()

	assert.True(t, acquired)
	assert.Nil(t, closed, "the sealed window is never written twice")
	assert.Len(t, log.windowLines(), 1)
}

func TestScheduledSkipsBehindAManualRunWarnWithNothingInFlight(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module, entry, started, release := newGatedRelay(t, log, clock)
	manual := make(chan struct{})
	go func() {
		defer close(manual)
		module.runJobBody(entry, "manual")
	}()
	<-started
	defer func() { close(release); <-manual }()

	for range 3 {
		module.runJobBody(entry, triggerScheduled)
	}
	clock.Advance(windowTestWidth)
	module.runJobBody(entry, triggerScheduled)

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, "warn", windows[0].level)
	assert.Equal(t, int64(0), windows[0].fields["runs"])
	assert.Equal(t, int64(3), windows[0].fields["runs.skipped"])
	assert.Equal(t, int64(0), windows[0].fields["runs.inflight"], "a manual run is never in a window")
}

func newFailingRelay(t *testing.T, log *recordingLogger, clock *fakeClock) (*Module, *jobEntry) {
	t.Helper()
	module := newWindowedTestScheduler(t, clock, withLogger(log))
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		rec.Fail("published", 1)
		return nil
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	return module, registeredEntry(t, module, "relay")
}

func requirePartialWindow(t *testing.T, log *recordingLogger, runs, inflight int64) {
	t.Helper()
	windows := log.windowLines()
	require.Len(t, windows, 1)
	partial, _ := windows[0].fields["window.partial"].(bool)
	assert.True(t, partial)
	assert.Equal(t, runs, windows[0].fields["runs"])
	assert.Equal(t, inflight, windows[0].fields["runs.inflight"])
}

// zerologOwnFields are the fields every real line carries besides the event's own:
// level, time, caller and message.
const zerologOwnFields = 4

func TestWindowLineKeysStayFixedWhateverTheValues(t *testing.T) {
	values := []string{"val_a", "val_b", "val_c"}
	counters := make([]Counter, maxWindowedCounters)
	for i := range counters {
		counters[i] = Counter{Name: fmt.Sprintf("c%02d", i), Attrs: []AttrSet{{Key: "dim_a", Values: values}, {Key: "dim_b", Values: values}}}
	}
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		for _, c := range counters {
			recordEveryCombination(rec, c.Name, values)
		}
		return errors.New("relay failed")
	}, counters...)

	tp := obtest.NewTestTraceProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
	log := newRecordingLogger()
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart),
		withLogger(log), withTracer(tp.Tracer("test-scheduler")))
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	runCtx := gobrickstrace.WithTraceID(context.Background(), "run-correlation-id")
	module.executeJob(entry, newJobContext(runCtx, "relay", triggerScheduled, module.logger, nil, nil, module.config))
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	line := windows[0]
	assert.Len(t, line.fields, 82, "the brief's budget: 17 fixed keys, log.type and two per Counter")
	assert.LessOrEqual(t, len(line.fields)+zerologOwnFields, 100)
	for key := range line.fields {
		for _, token := range append([]string{"dim_a", "dim_b"}, values...) {
			assert.NotContains(t, key, token, "no declared value or attribute key may become a field name")
		}
	}
	assert.Equal(t, int64(18), line.fields["c31.ok"])
	assert.Equal(t, int64(9), line.fields["c31.failing"])
	listed, _ := line.fields["failing.keys"].([]string)
	assert.Equal(t, []string{
		keyRunError,
		"c00{dim_a=val_a,dim_b=val_a}", "c00{dim_a=val_a,dim_b=val_b}", "c00{dim_a=val_a,dim_b=val_c}",
		"c00{dim_a=val_b,dim_b=val_a}", "c00{dim_a=val_b,dim_b=val_b}", "c00{dim_a=val_b,dim_b=val_c}",
		"c00{dim_a=val_c,dim_b=val_a}", "c00{dim_a=val_c,dim_b=val_b}", "c00{dim_a=val_c,dim_b=val_c}",
	}, listed)
	assert.Equal(t, maxWindowedCounters*9+1-maxFailingKeysListed, line.fields["failing.keys.more"])

	span := jobExecuteSpan(t, tp)
	assert.Equal(t, "00-"+span.SpanContext.TraceID().String()+"-"+span.SpanContext.SpanID().String()+"-01", line.fields["traceparent"])
	assert.Equal(t, "run-correlation-id", line.fields["correlation_id"])
}

func recordEveryCombination(rec *Recorder, name string, values []string) {
	for _, a := range values {
		for _, b := range values {
			attrs := []attribute.KeyValue{attribute.String("dim_b", b), attribute.String("dim_a", a)}
			rec.Count(name, 2, attrs...)
			rec.Fail(name, 1, attrs...)
		}
	}
}

func TestWindowedPanicKeepsEarlierCountsAndNeverTheValue(t *testing.T) {
	log := newRecordingLogger()
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		rec.Count("published", 3)
		panic("secret-xyz")
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	require.NotPanics(t, func() { module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled) })
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, int64(3), windows[0].fields["published.ok"], "counts recorded before the panic survive")
	assert.Equal(t, []string{keyRunPanic}, windows[0].fields["failing.keys"])
	assert.Equal(t, "panic (type: string)", windows[0].fields["error"])
	for _, line := range log.lines() {
		assert.NotContains(t, fmt.Sprint(line.msg, line.fields), "secret-xyz")
	}
}

func TestSlowWindowedRunFailsUnderRunSlow(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log), withSlowJobThreshold(time.Second))
	job := Windowed(func(_ JobContext, _ *Recorder) error {
		clock.Advance(time.Second + time.Millisecond)
		return nil
	})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, "warn", windows[0].level)
	assert.Equal(t, int64(1), windows[0].fields["runs.slow"])
	assert.Equal(t, int64(1), windows[0].fields["runs.failing"])
	assert.Equal(t, []string{keyRunSlow}, windows[0].fields["failing.keys"])
}

func TestWindowedRunWritesItsOwnLineAtDebugWithoutLogType(t *testing.T) {
	log := newRecordingLogger()
	module, entry := newFailingRelay(t, log, newFakeClock(windowTestStart))

	module.runJobBody(entry, triggerScheduled)

	runs := log.messageLines("Job 'relay' completed successfully in 0s")
	require.Len(t, runs, 1)
	assert.Equal(t, "debug", runs[0].level)
	assert.NotContains(t, runs[0].fields, "log.type")
}

func TestManualTriggerOfAWindowedJobKeepsTheActionLineAndLeavesTheWindow(t *testing.T) {
	log := newRecordingLogger()
	module, entry := newFailingRelay(t, log, newFakeClock(windowTestStart))

	module.runJobBody(entry, "manual")
	require.NoError(t, module.Shutdown())

	action := log.actionLines()
	require.Len(t, action, 1, "no window line: a manual run never touches the window")
	assert.Equal(t, "manual", action[0].fields["job.trigger"])
	assert.Equal(t, "info", action[0].level)
}

func TestReservedFailingKeysListFirst(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log), withSlowJobThreshold(time.Second))
	counters := countersN(11)
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		for _, c := range counters {
			rec.Fail(c.Name, 1)
		}
		clock.Advance(2 * time.Second)
		return errors.New("relay failed")
	}, counters...)
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, []string{keyRunError, keyRunSlow, "c00", "c01", "c02", "c03", "c04", "c05", "c06", "c07"},
		windows[0].fields["failing.keys"])
	assert.Equal(t, 3, windows[0].fields["failing.keys.more"])
}

func TestFailingManualRunWritesNoEdgeAndLeavesTheWindow(t *testing.T) {
	log := newRecordingLogger()
	mp := obtest.NewTestMeterProvider()
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log), withMeterProvider(mp.MeterProvider))
	failNext := false
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		if failNext {
			rec.Fail("published", 4)
			return errors.New("relay failed")
		}
		rec.Count("published", 1)
		return nil
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	module.runJobBody(entry, triggerScheduled)
	failNext = true
	module.runJobBody(entry, "manual")
	require.NoError(t, module.Shutdown())

	assert.Empty(t, log.edgeLines(), "a manual run never emits an edge")
	manual := log.linesWith(func(line recordedLine) bool { return line.fields["job.trigger"] == "manual" })
	require.Len(t, manual, 1)
	assert.Equal(t, "error", manual[0].level)
	assert.Equal(t, "action", manual[0].fields["log.type"])
	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, int64(1), windows[0].fields["runs"])
	assert.Equal(t, int64(0), windows[0].fields["published.failing"])
	assert.Equal(t, "clean", windows[0].fields["job.state"])
	assert.Equal(t, map[string]int64{"job.id=relay": 5}, counterPoints(t, mp, "published"),
		"the manual run's Fail still reaches the metric")
}

func TestRecorderCallAfterTheRunIsCountedLate(t *testing.T) {
	log := newRecordingLogger()
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
	var kept *Recorder
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		kept = rec
		return nil
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	kept.Count("published", 5)
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	assert.Equal(t, int64(1), windows[0].fields["recorder.late"])
	assert.Equal(t, int64(0), windows[0].fields["published.ok"], "a late call is dropped")
}

// scriptedJob fails one scripted key set per run; an empty set is a clean run.
func scriptedJob(script [][]string, counters ...Counter) (Executor, *atomic.Int64) {
	var run atomic.Int64
	return Windowed(func(_ JobContext, rec *Recorder) error {
		for _, name := range script[run.Add(1)-1] {
			rec.Fail(name, 1)
		}
		return nil
	}, counters...), &run
}

func runWithID(module *Module, entry *jobEntry, id string) {
	ctx := gobrickstrace.WithTraceID(context.Background(), id)
	module.executeJob(entry, newJobContext(ctx, entry.metadata.JobID, triggerScheduled, module.logger, nil, nil, module.config))
}

func TestEdgesFollowTheFailingSetAcrossWindows(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, clock, withLogger(log), withTracer(tp.Tracer("test-scheduler")))
	job, _ := scriptedJob([][]string{{"a"}, {"b"}, {"c"}, {}, {}, {"a"}},
		Counter{Name: "a"}, Counter{Name: "b"}, Counter{Name: "c"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	for _, id := range []string{"run-1", "run-2", "run-3"} {
		runWithID(module, entry, id)
	}
	for _, id := range []string{"run-4", "run-5", "run-6"} {
		clock.Advance(windowTestWidth)
		runWithID(module, entry, id)
	}
	require.NoError(t, module.Shutdown())

	spans := obtest.NewSpanCollector(t, tp.Exporter).WithName("job.execute").AssertCount(6)
	edges := log.edgeLines()
	require.Len(t, edges, 3)
	for i, want := range []struct {
		reason, correlationID string
		span                  int
	}{{"new", "run-1", 0}, {"grew", "run-2", 1}, {"new", "run-6", 5}} {
		assert.Equal(t, want.reason, edges[i].fields["edge.reason"])
		assert.Equal(t, "warn", edges[i].level, "a failing run with no error or panic edges at WARN")
		assert.Equal(t, want.correlationID, edges[i].fields["correlation_id"])
		sc := spans.Get(want.span).SpanContext
		assert.Equal(t, "00-"+sc.TraceID().String()+"-"+sc.SpanID().String()+"-01", edges[i].fields["traceparent"])
	}

	windows := log.windowLines()
	require.Len(t, windows, 4)
	states := make([]any, 0, len(windows))
	for _, line := range windows {
		states = append(states, line.fields["job.state"])
	}
	assert.Equal(t, []any{"failing", "recovered", "clean", "failing"}, states)
	assert.Equal(t, int64(1), windows[0].fields["edges.suppressed"], "a third edge in one window is only counted")
	assert.Equal(t, int64(0), windows[3].fields["edges.suppressed"])
}

func TestEdgeLevelFollowsTheRunKeys(t *testing.T) {
	tests := []struct {
		name      string
		run       func(JobContext, *Recorder) error
		wantLevel string
	}{
		{name: "fail_only_warns", run: func(_ JobContext, rec *Recorder) error {
			rec.Fail("published", 1)
			return nil
		}, wantLevel: "warn"},
		{name: "error_is_error", run: func(JobContext, *Recorder) error { return errors.New("relay failed") }, wantLevel: "error"},
		{name: "panic_is_error", run: func(JobContext, *Recorder) error { panic("boom") }, wantLevel: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
			require.NoError(t, module.FixedRate("relay", Windowed(tt.run, Counter{Name: "published"}), windowTestInterval))

			module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)

			edges := log.edgeLines()
			require.Len(t, edges, 1)
			assert.Equal(t, tt.wantLevel, edges[0].level)
			assert.False(t, edges[0].viaContext, "the edge line goes through the module logger")
		})
	}
}

const panicStackMessage = "Job panicked - recovered and marked as failed"

func TestPanicStackLineIsErrorOnlyOnAnEdge(t *testing.T) {
	panicking := func(JobContext, *Recorder) error { panic("boom") }

	t.Run("second_panic_in_the_window_drops_to_debug", func(t *testing.T) {
		log := newRecordingLogger()
		module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
		require.NoError(t, module.FixedRate("relay", Windowed(panicking), windowTestInterval))
		entry := registeredEntry(t, module, "relay")

		module.runJobBody(entry, triggerScheduled)
		module.runJobBody(entry, triggerScheduled)

		edges := log.edgeLines()
		require.Len(t, edges, 1)
		assert.Equal(t, "new", edges[0].fields["edge.reason"])
		stacks := log.messageLines(panicStackMessage)
		require.Len(t, stacks, 2)
		assert.Equal(t, "error", stacks[0].level, "the run that wrote the edge keeps its stack at ERROR")
		assert.Equal(t, "debug", stacks[1].level)
	})

	t.Run("manual_panic_drops_to_debug_and_keeps_its_action_line", func(t *testing.T) {
		log := newRecordingLogger()
		module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
		require.NoError(t, module.FixedRate("relay", Windowed(panicking), windowTestInterval))

		module.runJobBody(registeredEntry(t, module, "relay"), "manual")

		requireStackAt(t, log, "debug")
		requireRunActionLineNamingThePanic(t, log)
	})

	t.Run("post_seal_panic_drops_to_debug_and_keeps_its_action_line", func(t *testing.T) {
		log := newRecordingLogger()
		module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
		require.NoError(t, module.FixedRate("relay", Windowed(panicking), windowTestInterval))
		entry := registeredEntry(t, module, "relay")
		module.runJobBody(entry, triggerScheduled)
		require.NoError(t, module.Shutdown())
		log.reset()

		module.executeJob(entry, newJobContext(context.Background(), "relay", triggerScheduled, module.logger, nil, nil, module.config))

		requireStackAt(t, log, "debug")
		requireRunActionLineNamingThePanic(t, log)
	})

	t.Run("plain_job_panic_stays_error", func(t *testing.T) {
		log := newRecordingLogger()
		module, _ := newTestScheduler(t, 5*time.Second, withLogger(log))

		runJobOnce(module, "plain-job", &panicJob{})

		requireStackAt(t, log, "error")
	})
}

func requireStackAt(t *testing.T, log *recordingLogger, level string) {
	t.Helper()
	stacks := log.messageLines(panicStackMessage)
	require.Len(t, stacks, 1)
	assert.Equal(t, level, stacks[0].level)
}

func requireRunActionLineNamingThePanic(t *testing.T, log *recordingLogger) {
	t.Helper()
	runs := log.linesWith(func(line recordedLine) bool {
		return line.fields["log.type"] == "action" && line.fields["job.status"] != nil
	})
	require.Len(t, runs, 1)
	assert.Equal(t, "error", runs[0].level)
	assert.Equal(t, "panic (type: string)", runs[0].fields["error"])
	assert.Empty(t, log.edgeLines())
}
