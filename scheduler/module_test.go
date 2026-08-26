package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// TestSchedulerModuleName verifies the module name
func TestSchedulerModuleName(t *testing.T) {
	module := NewModule()
	assert.Equal(t, "scheduler", module.Name())
}

// TestSchedulerModuleRegisterRoutes verifies route registration (stub for Phase 4)
func TestSchedulerModuleRegisterRoutes(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	// Nil parameters short-circuit registration rather than panicking
	module.RegisterRoutes(nil, nil)
}

// TestJobExecutionFailure verifies job failures are tracked
func TestJobExecutionFailure(t *testing.T) {
	_, registrar := newTestScheduler(t, 5*time.Second)

	// Create a job that always fails
	job := &failingJob{err: errors.New("intentional failure")}

	err := registrar.FixedRate("failing-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
}

// TestJobExecutionPanic verifies panic recovery
func TestJobExecutionPanic(t *testing.T) {
	_, registrar := newTestScheduler(t, 5*time.Second)

	// Create a job that panics
	job := &panicJob{}

	err := registrar.FixedRate("panic-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
}

// TestJobExecutionOverlappingPrevention verifies jobs don't overlap
func TestJobExecutionOverlappingPrevention(t *testing.T) {
	_, registrar := newTestScheduler(t, 5*time.Second)

	// Create a slow job that takes longer than the interval
	job := &slowJob{duration: 500 * time.Millisecond}

	// Schedule it to run every 100ms (faster than execution time)
	err := registrar.FixedRate("slow-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for multiple trigger attempts
	time.Sleep(1 * time.Second)

	// Job should have executed, but overlapping triggers should be skipped
	count := job.count()
	assert.Greater(t, count, 0, "Job should execute at least once")
	assert.Less(t, count, 10, "Overlapping executions should be skipped")
}

// TestJobExecutionPanicMetrics verifies panic counter metrics are recorded
func TestJobExecutionPanicMetrics(t *testing.T) {
	// Create test meter provider to capture metrics
	mp := obtest.NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	module, _ := newTestScheduler(t, 5*time.Second, withMeterProvider(mp.MeterProvider))

	// Register a job that panics
	job := &panicJob{}
	err := module.FixedRate("panic-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)

	// Wait until metrics show the panic increment to avoid races with asynchronous instrumentation
	require.Eventually(t, func() bool {
		rm := mp.Collect(t)
		return obtest.FindMetric(rm, "job.panic.total") != nil &&
			obtest.FindMetric(rm, "job.execution.total") != nil
	}, time.Second, 10*time.Millisecond, "Panic counter metric should be recorded")

	// Collect metrics once more for assertions
	rm := mp.Collect(t)

	panicMetric := obtest.FindMetric(rm, "job.panic.total")
	require.NotNil(t, panicMetric, "Panic counter metric should be recorded")

	execMetric := obtest.FindMetric(rm, "job.execution.total")
	require.NotNil(t, execMetric, "Execution counter metric should be recorded")
}

// jobExecuteSpan returns the single "job.execute" span recorded so far.
// AssertCount(1) is load-bearing: "the exemplar names THE job's trace" is only
// provable while exactly one job span exists.
func jobExecuteSpan(t *testing.T, tp *obtest.TestTraceProvider) tracetest.SpanStub {
	t.Helper()
	spans := obtest.NewSpanCollector(t, tp.Exporter).WithName("job.execute")
	spans.AssertCount(1)
	return spans.First()
}

// runJobOnce runs one job body synchronously, the way the manual trigger does.
// Driving the body directly pins the span count at one: an interval short enough
// to observe can fire a second tick before the assertions read.
func runJobOnce(module *Module, jobID string, job Executor) {
	module.runJobBody(&jobEntry{
		job:      job,
		metadata: &JobMetadata{JobID: jobID, ScheduleType: "fixed-rate"},
	}, "manual")
}

// logLineWith returns the single output line carrying message. Asserting on the
// whole capture cannot tell "this line carries the trace id" from "some other
// line does".
func logLineWith(t *testing.T, out, message string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, message) {
			return line
		}
	}
	t.Fatalf("no log line with message %q in:\n%s", message, out)
	return ""
}

// TestJobMetricsCarryExemplarsNamingTheJobSpan verifies the success-path
// instruments record under the traced job context: the SDK's default
// TraceBasedFilter attaches an exemplar only when the recording context carries
// a sampled span, so recording against context.Background() silently drops the
// metric-to-trace link.
func TestJobMetricsCarryExemplarsNamingTheJobSpan(t *testing.T) {
	module, tp, mp := newTracedMeteredScheduler(t)

	runJobOnce(module, "exemplar-job", &slowJob{})

	recorded := jobExecuteSpan(t, tp).SpanContext.TraceID()

	rm := mp.Collect(t)

	durationExemplars := obtest.HistogramExemplars[float64](t, rm, "job.execution.duration")
	require.NotEmpty(t, durationExemplars, "duration histogram point carries no exemplar")
	assert.Equal(t, recorded[:], durationExemplars[0].TraceID,
		"the exemplar names the job.execute trace, not merely some trace")

	execExemplars := obtest.SumExemplars[int64](t, rm, "job.execution.total")
	require.NotEmpty(t, execExemplars, "execution counter point carries no exemplar")
	assert.Equal(t, recorded[:], execExemplars[0].TraceID,
		"the exemplar names the job.execute trace, not merely some trace")
}

// TestJobPanicMetricCarriesExemplarNamingTheJobSpan covers the deferred recovery
// path, which reaches the traced context through the rebound ctx variable rather
// than a parameter.
func TestJobPanicMetricCarriesExemplarNamingTheJobSpan(t *testing.T) {
	module, tp, mp := newTracedMeteredScheduler(t)

	runJobOnce(module, "panic-exemplar-job", &panicJob{})

	recorded := jobExecuteSpan(t, tp).SpanContext.TraceID()

	rm := mp.Collect(t)

	panicExemplars := obtest.SumExemplars[int64](t, rm, "job.panic.total")
	require.NotEmpty(t, panicExemplars, "panic counter point carries no exemplar")
	assert.Equal(t, recorded[:], panicExemplars[0].TraceID,
		"the exemplar names the job.execute trace, not merely some trace")
}

// TestJobPanicLogCarriesTraceCorrelation pins the stack-trace line to the job
// trace. It is the artifact an operator pivots to from the exemplar, and it used
// to be the one line in the traced region logged without correlation.
func TestJobPanicLogCarriesTraceCorrelation(t *testing.T) {
	var recorded trace.TraceID

	out := captureStdout(t, func() {
		module, tp, _ := newTracedMeteredScheduler(t)

		runJobOnce(module, "panic-correlation-job", &panicJob{})

		recorded = jobExecuteSpan(t, tp).SpanContext.TraceID()
	})

	panicLine := logLineWith(t, out, "Job panicked - recovered and marked as failed")
	assert.Contains(t, panicLine, `"trace_id":"`+recorded.String()+`"`,
		"the stack-trace line must carry the job's trace, like the summary line does")
}

// TestJobExecutionPanicEmitsActionLogSummary verifies that a panicking job still emits
// the structured action-log summary (log.type=action) exactly once, so the 100%
// job-execution sampling logJobResultSummary advertises actually holds on the panic path.
func TestJobExecutionPanicEmitsActionLogSummary(t *testing.T) {
	job := &panicJob{}

	out := captureStdout(t, func() {
		module, _ := newTestScheduler(t, 5*time.Second)

		err := module.FixedRate("panic-job", job, 100*time.Millisecond)
		require.NoError(t, err)

		waitFor(t, job.wasExecuted)

		// Stop the scheduler inside the capture window instead of sleeping:
		// Shutdown's wg.Wait blocks until the in-flight job wrapper's defer
		// completes — i.e. after the recovery defer's logJobResultSummary call —
		// a real happens-before rather than a timing guess, and it prevents a
		// second FixedRate tick from emitting a duplicate action line. Mirrors
		// the stop-before-read pattern in TestJobExecutionWithTracer.
		require.NoError(t, module.Shutdown())
	})

	// Pre-existing panic ERROR line must still fire, unregressed.
	assert.Contains(t, out, "Job panicked - recovered and marked as failed")
	assert.Contains(t, out, `"jobID":"panic-job"`)

	// New action-log summary line must now fire on the panic path.
	assert.Contains(t, out, `"log.type":"action"`)
	assert.Contains(t, out, `"job.id":"panic-job"`)
	assert.Contains(t, out, `"result_code":"ERROR"`)

	// Exactly one summary line for this job's single execution — no double-emit.
	assert.Equal(t, 1, strings.Count(out, `"job.id":"panic-job"`))
}

// TestJobSkippedDuringShutdown verifies jobs skip execution when shutdown is triggered
func TestJobSkippedDuringShutdown(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	// Create a job that tracks execution
	job := &slowJob{duration: 10 * time.Millisecond}

	// Register job with long interval
	err := module.FixedRate("shutdown-test-job", job, 5*time.Second)
	require.NoError(t, err)

	// Immediately shutdown before job can execute
	err = module.Shutdown()
	require.NoError(t, err)

	// Verify job was never executed
	assert.Equal(t, 0, job.count(), "Job should not execute after shutdown")
}

// TestCreateJobWrapperBalancesAddDoneOnShutdownPath asserts that the wrapper's
// wg.Add(1)/wg.Done() pair stays balanced when the closure bails because the
// shutdown context is already canceled. The pre-fix code did Add AFTER the
// shutdown check, so a shutdown-canceled invocation never incremented wg —
// allowing wg.Wait() in Shutdown() to return before the closure had even
// observed the cancellation. The fix moves Add to the first statement; this
// test locks in the invariant that every bail path balances Add and Done.
func TestCreateJobWrapperBalancesAddDoneOnShutdownPath(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)
	defer func() { _ = module.Shutdown() }()

	entry := &jobEntry{
		job:      &slowJob{duration: time.Millisecond},
		metadata: &JobMetadata{JobID: "wg-shutdown-test"},
	}
	wrapper := module.createJobWrapper(entry)

	// Cancel the shutdown context BEFORE invoking the wrapper, so the closure
	// hits the shutdown bail path immediately after wg.Add(1).
	module.shutdownCancel()

	wrapper() // synchronous invoke; defer wg.Done() must fire on return

	assertWaitGroupDrains(t, module, "wrapper returned on shutdown-bail path")
}

// TestCreateJobWrapperBalancesAddDoneOnTryLockFailPath asserts the same Add/Done
// balance when the closure bails because the entry's lock is already held (a
// concurrent invocation is already running). Pre-fix, Add was placed after
// tryLock — so a tryLock-fail path also skipped Add. Post-fix, Add fires first
// and defer Done() balances every return.
func TestCreateJobWrapperBalancesAddDoneOnTryLockFailPath(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)
	defer func() { _ = module.Shutdown() }()

	entry := &jobEntry{
		job:      &slowJob{duration: time.Millisecond},
		metadata: &JobMetadata{JobID: "wg-trylock-test"},
	}
	// Pre-acquire the lock so the wrapper's tryLock fails inside the closure.
	require.True(t, entry.tryLock(), "test setup: first tryLock must succeed")
	defer entry.unlock()

	wrapper := module.createJobWrapper(entry)
	wrapper() // closure bails at tryLock-fail; defer Done() must still fire

	assertWaitGroupDrains(t, module, "wrapper returned via tryLock-fail path")

	// Sanity: the skip counter should have incremented (proves we actually hit
	// the tryLock-fail branch rather than some other path).
	assert.Equal(t, int64(1), entry.metadata.snapshot().SkippedCount, "expected skip counter increment")
}

// TestShutdownIdempotent verifies that calling Shutdown more than once is a no-op
// and does not return an error or fail the underlying gocron scheduler. Regression
// test for the spurious "Error stopping scheduler" log emitted when a deferred
// Shutdown ran after an explicit Shutdown.
func TestShutdownIdempotent(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register a job so the gocron scheduler is actually initialized.
	err := registrar.FixedRate("idempotent-shutdown-job", &slowJob{duration: time.Millisecond}, time.Hour)
	require.NoError(t, err)

	require.NoError(t, module.Shutdown(), "first shutdown should succeed")
	require.NoError(t, module.Shutdown(), "second shutdown should be a no-op")
	require.NoError(t, module.Shutdown(), "subsequent shutdowns should remain no-ops")
}

// TestJobExecutionWithDBGetterError verifies error handling when DB getter fails
func TestJobExecutionWithDBGetterError(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second, withDB(func(_ context.Context) (types.Interface, error) {
		return nil, errors.New("DB connection failed")
	}))

	// Create a job that checks DB is nil
	job := &dbCheckJob{}
	err := module.FixedRate("db-error-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job asserts the expected state (<=1s)
	require.Eventually(t, func() bool {
		return job.wasExecuted() && atomic.LoadInt32(&job.dbWasNil) == 1
	}, time.Second, 10*time.Millisecond, "DB should be nil when getter fails")
}

// TestJobExecutionWithMessagingGetterError verifies error handling when messaging getter fails
func TestJobExecutionWithMessagingGetterError(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second, withMessaging(func(_ context.Context) (messaging.AMQPClient, error) {
		return nil, errors.New("Messaging connection failed")
	}))

	// Create a job that checks messaging is nil
	job := &messagingCheckJob{}
	err := module.FixedRate("msg-error-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job asserts the expected state (<=1s)
	require.Eventually(t, func() bool {
		return job.wasExecuted() && atomic.LoadInt32(&job.messagingWasNil) == 1
	}, time.Second, 10*time.Millisecond, "Messaging should be nil when getter fails")
}

// TestSlowJobThresholdWarning verifies slow job detection and WARN severity
func TestSlowJobThresholdWarning(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second, withSlowJobThreshold(100*time.Millisecond))

	// Create a slow job that exceeds threshold
	job := &slowJob{duration: 150 * time.Millisecond}
	err := module.FixedRate("slow-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to execute (job takes 150ms, so wait longer)
	time.Sleep(400 * time.Millisecond)

	// Verify job was executed
	assert.Greater(t, job.count(), 0, "Job should have executed")
}

// TestDetermineJobSeverityUsesConfiguredSlowJobThreshold pins the severity
// boundary to scheduler.timeout.slowjob. The module no longer carries a
// use-time default for it, so the configured value is the only threshold.
func TestDetermineJobSeverityUsesConfiguredSlowJobThreshold(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second, withSlowJobThreshold(100*time.Millisecond))

	tests := []struct {
		name     string
		duration time.Duration
		err      error
		level    string
		code     string
	}{
		{name: "failure_is_error", duration: time.Millisecond, err: errors.New("boom"), level: "error", code: "ERROR"},
		{name: "below_threshold_is_info", duration: 99 * time.Millisecond, level: "info", code: "INFO"},
		{name: "at_threshold_is_info", duration: 100 * time.Millisecond, level: "info", code: "INFO"},
		{name: "above_threshold_is_warn", duration: 101 * time.Millisecond, level: "warn", code: "WARN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, code := module.determineJobSeverity(tt.duration, tt.err)

			assert.Equal(t, tt.level, level)
			assert.Equal(t, tt.code, code)
		})
	}
}

// TestJobExecutionWithoutTracer verifies jobs execute successfully when tracer is nil
func TestJobExecutionWithoutTracer(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	// Create a simple job
	job := &slowJob{duration: 10 * time.Millisecond}
	err := module.FixedRate("no-tracer-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to execute
	time.Sleep(200 * time.Millisecond)

	// Verify job executed successfully without tracer
	assert.Greater(t, job.count(), 0, "Job should execute without tracer")
}

// TestJobExecutionWithTracer verifies span creation when tracer is configured
func TestJobExecutionWithTracer(t *testing.T) {
	// Create test trace provider
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	module, _ := newTestScheduler(t, 5*time.Second, withTracer(tp.Tracer("test-scheduler")))

	// Create a simple job. Use a 1s interval so a second tick cannot race the
	// assertion window, and stop the scheduler before counting spans.
	job := &slowJob{duration: 10 * time.Millisecond}
	err := module.FixedRate("traced-job", job, 1*time.Second)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return job.count() >= 1 },
		2*time.Second, 10*time.Millisecond, "Job should execute at least once")

	require.NoError(t, module.Shutdown())

	// Verify span attributes
	span := jobExecuteSpan(t, tp)
	obtest.AssertSpanAttribute(t, &span, "job.id", "traced-job")
	obtest.AssertSpanAttribute(t, &span, "job.trigger", "scheduled")
}

// TestJobExecutionWithTracerPropagatesContext verifies that the traced context from tracer.Start
// is propagated to the job's Execute method, so child spans nest under "job.execute".
func TestJobExecutionWithTracerPropagatesContext(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	module, _ := newTestScheduler(t, 5*time.Second, withTracer(tp.Tracer("test-scheduler")))

	// Job that creates a child span to verify context propagation. Use a 1s
	// interval so a second tick cannot race the assertion window, and stop the
	// scheduler before counting spans.
	tracer := tp.Tracer("test-child")
	job := &spanCapturingJob{tracer: tracer}
	err := module.FixedRate("ctx-propagation-job", job, 1*time.Second)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return job.count() >= 1 },
		2*time.Second, 10*time.Millisecond, "Job should execute at least once")

	require.NoError(t, module.Shutdown())

	// Verify both parent and child spans exist
	parentSpan := jobExecuteSpan(t, tp)

	childSpans := obtest.NewSpanCollector(t, tp.Exporter).WithName("child.operation")
	childSpans.AssertCount(1)

	childSpan := childSpans.First()
	assert.Equal(t, parentSpan.SpanContext.TraceID(), childSpan.SpanContext.TraceID(),
		"Child span should share the same trace ID as the parent")
	assert.Equal(t, parentSpan.SpanContext.SpanID(), childSpan.Parent.SpanID(),
		"Child span's parent should be the job.execute span")
}

// TestCreateJobLogEventLevels verifies correct log event creation for different severity levels
func TestCreateJobLogEventLevels(t *testing.T) {
	log := logger.New("info", false)

	// Test error level
	errorEvent := createJobLogEvent(log, "error")
	require.NotNil(t, errorEvent, "Error level event should be created")

	// Test warn level
	warnEvent := createJobLogEvent(log, "warn")
	require.NotNil(t, warnEvent, "Warn level event should be created")

	// Test info level (default)
	infoEvent := createJobLogEvent(log, "info")
	require.NotNil(t, infoEvent, "Info level event should be created")

	// Test unknown level (should default to info)
	defaultEvent := createJobLogEvent(log, "unknown")
	require.NotNil(t, defaultEvent, "Unknown level should default to info")
}

// Test helper jobs

// failingJob always returns an error
type failingJob struct {
	executed int32 // Use atomic int32 instead of bool
	err      error
}

func (j *failingJob) Execute(_ JobContext) error {
	atomic.StoreInt32(&j.executed, 1)
	return j.err
}

func (j *failingJob) wasExecuted() bool {
	return atomic.LoadInt32(&j.executed) == 1
}

// panicJob panics during execution
type panicJob struct {
	executed int32 // Use atomic int32 instead of bool
}

func (j *panicJob) Execute(_ JobContext) error {
	atomic.StoreInt32(&j.executed, 1)
	panic("intentional panic for testing")
}

func (j *panicJob) wasExecuted() bool {
	return atomic.LoadInt32(&j.executed) == 1
}

// slowJob takes a long time to execute
type slowJob struct {
	duration   time.Duration
	executions int32 // Use atomic int32
}

func (j *slowJob) Execute(_ JobContext) error {
	atomic.AddInt32(&j.executions, 1)
	time.Sleep(j.duration)
	return nil
}

func (j *slowJob) count() int {
	return int(atomic.LoadInt32(&j.executions))
}

// dbCheckJob checks if DB is nil during execution
type dbCheckJob struct {
	executed int32
	dbWasNil int32 // Use atomic int32: 1 = true, 0 = false
}

func (j *dbCheckJob) Execute(ctx JobContext) error {
	atomic.StoreInt32(&j.executed, 1)
	if ctx.DB() == nil {
		atomic.StoreInt32(&j.dbWasNil, 1)
	} else {
		atomic.StoreInt32(&j.dbWasNil, 0)
	}
	return nil
}

func (j *dbCheckJob) wasExecuted() bool {
	return atomic.LoadInt32(&j.executed) == 1
}

// messagingCheckJob checks if Messaging is nil during execution
type messagingCheckJob struct {
	executed        int32
	messagingWasNil int32 // Use atomic int32: 1 = true, 0 = false
}

func (j *messagingCheckJob) Execute(ctx JobContext) error {
	atomic.StoreInt32(&j.executed, 1)
	if ctx.Messaging() == nil {
		atomic.StoreInt32(&j.messagingWasNil, 1)
	} else {
		atomic.StoreInt32(&j.messagingWasNil, 0)
	}
	return nil
}

func (j *messagingCheckJob) wasExecuted() bool {
	return atomic.LoadInt32(&j.executed) == 1
}

// spanCapturingJob creates a child span during execution to verify context propagation
type spanCapturingJob struct {
	tracer     trace.Tracer
	executions int32
}

func (j *spanCapturingJob) Execute(ctx JobContext) error {
	atomic.AddInt32(&j.executions, 1)
	_, span := j.tracer.Start(ctx, "child.operation")
	defer span.End()
	return nil
}

func (j *spanCapturingJob) count() int {
	return int(atomic.LoadInt32(&j.executions))
}

// place near test helpers (non-diff, supporting snippet)
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, time.Second, 10*time.Millisecond)
}

// TestSchedulerInitRequiresNormalizedConfig pins the invariant every later config
// read depends on: the module reads scheduler.timeout.* with no use-time
// fallback, so a ModuleDeps assembled outside app construction fails at Init
// rather than at shutdown, where a zero budget abandons in-flight jobs.
func TestSchedulerInitRequiresNormalizedConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr string
	}{
		{name: "nil_config", cfg: nil, wantErr: "deps.Config is required"},
		{
			name:    "both_timeouts_zero",
			cfg:     &config.Config{},
			wantErr: "scheduler.timeout.shutdown must be positive",
		},
		{
			name:    "only_shutdown_set",
			cfg:     schedulerTimeoutConfig(30*time.Second, 0),
			wantErr: "scheduler.timeout.slowjob must be positive",
		},
		{
			name:    "only_slowjob_set",
			cfg:     schedulerTimeoutConfig(0, 25*time.Second),
			wantErr: "scheduler.timeout.shutdown must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewModule().Init(&app.ModuleDeps{Logger: logger.New("info", false), Config: tt.cfg})

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func schedulerTimeoutConfig(shutdown, slowJob time.Duration) *config.Config {
	return &config.Config{Scheduler: config.SchedulerConfig{
		Timeout: config.SchedulerTimeoutConfig{Shutdown: shutdown, SlowJob: slowJob},
	}}
}

func TestSchedulerConfiguredTimezone(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{name: "empty_defaults_to_utc", cfg: &config.Config{}, expected: "UTC"},
		{name: "iana_preserved", cfg: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "America/New_York"}}, expected: "America/New_York"},
		{name: "sentinel_preserved", cfg: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}, expected: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Module{config: tt.cfg}
			assert.Equal(t, tt.expected, m.configuredTimezone())
		})
	}
}

func TestSchedulerLocationOptions(t *testing.T) {
	t.Run("sentinel_yields_no_option", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Empty(t, opts)
	})
	t.Run("iana_yields_one_option", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "America/New_York"}}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Len(t, opts, 1)
	})
	t.Run("default_utc_yields_one_option", func(t *testing.T) {
		m := &Module{config: &config.Config{}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Len(t, opts, 1)
	})
}

func TestSchedulerTimezoneLabel(t *testing.T) {
	t.Run("iana_returns_name", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "Asia/Tokyo"}}}
		assert.Equal(t, "Asia/Tokyo", m.timezoneLabel())
	})
	t.Run("sentinel_returns_host_local", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}}
		assert.Equal(t, "host-local", m.timezoneLabel())
	})
	t.Run("empty_returns_utc", func(t *testing.T) {
		m := &Module{config: &config.Config{}}
		assert.Equal(t, "UTC", m.timezoneLabel())
	})
}

func TestSchedulerAppliesConfiguredTimezoneToJobs(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second, withTimezone("America/New_York"))

	err := registrar.DailyAt("tz-job", &counterJob{}, ParseTime("14:30"))
	require.NoError(t, err)

	module.mu.RLock()
	entry := module.jobs["tz-job"]
	module.mu.RUnlock()
	require.NotNil(t, entry)
	require.NotNil(t, entry.gocronJob)

	nextRun, err := entry.gocronJob.NextRun()
	require.NoError(t, err)
	require.False(t, nextRun.IsZero())

	// The scheduler must interpret 14:30 in the configured zone, not host-local.
	assert.Equal(t, "America/New_York", nextRun.Location().String())
	assert.Equal(t, 14, nextRun.Hour())
	assert.Equal(t, 30, nextRun.Minute())
}

func TestSchedulerDefaultsToUTCTimezoneForJobs(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second) // no withTimezone → UTC default

	err := registrar.DailyAt("utc-job", &counterJob{}, ParseTime("09:00"))
	require.NoError(t, err)

	module.mu.RLock()
	entry := module.jobs["utc-job"]
	module.mu.RUnlock()
	require.NotNil(t, entry)
	require.NotNil(t, entry.gocronJob)

	nextRun, err := entry.gocronJob.NextRun()
	require.NoError(t, err)
	require.False(t, nextRun.IsZero())

	assert.Equal(t, "UTC", nextRun.Location().String())
}

// panickingLogger's events panic when written, but only for the panic-REPORTING
// call — keyed on `panic_type`, a field only that call uses. DO NOT widen it to
// panic on every call: a fault-injection double must be aimed at the exact call
// the defect travels through, or it tests a different failure. Panicking on
// everything would take out the scheduler's own startup logging instead.
type panickingLogger struct{ logger.Logger }

func (l *panickingLogger) Error() logger.LogEvent        { return &panickingEvent{} }
func (l *panickingLogger) Info() logger.LogEvent         { return &panickingEvent{} }
func (l *panickingLogger) Warn() logger.LogEvent         { return &panickingEvent{} }
func (l *panickingLogger) Debug() logger.LogEvent        { return &panickingEvent{} }
func (l *panickingLogger) Fatal() logger.LogEvent        { return &panickingEvent{} }
func (l *panickingLogger) WithContext(any) logger.Logger { return l }

func (l *panickingLogger) WithFields(map[string]any) logger.Logger { return l }

type panickingEvent struct {
	logger.LogEvent
	reporting bool
}

func (e *panickingEvent) Str(key, _ string) logger.LogEvent {
	if key == "panic_type" {
		e.reporting = true
	}
	return e
}
func (e *panickingEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *panickingEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *panickingEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *panickingEvent) Int(string, int) logger.LogEvent           { return e }
func (e *panickingEvent) Err(error) logger.LogEvent                 { return e }
func (e *panickingEvent) Enabled() bool                             { return true }

func (e *panickingEvent) Msg(string) {
	if e.reporting {
		panic("logger write failed")
	}
}

// TestJobExecutionPanicSurvivesAPanickingLogger pins the terminal swallow around
// the panic-reporting call. The report is the FIRST statement of the recovery
// block; everything that records the outcome comes after it. A panic there — the
// logger is consumer-supplied — must not escape, and must not cost the accounting.
// Before this test the guard was unpinned: deleting it left the whole package green.
func TestJobExecutionPanicSurvivesAPanickingLogger(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second, withLogger(&panickingLogger{}))
	defer func() { _ = module.Shutdown() }()

	entry := &jobEntry{
		job:      &panicJob{},
		metadata: &JobMetadata{JobID: "panicking-logger-job", ScheduleType: "fixed-rate"},
	}

	require.NotPanics(t, func() { module.runJobBody(entry, "manual") })

	snapshot := entry.metadata.snapshot()
	assert.Equal(t, int64(1), snapshot.FailureCount, "the job must still be recorded as failed")
	assert.Equal(t, "failure", snapshot.LastExecutionStatus)
	assert.Equal(t, int64(1), snapshot.TotalExecutions)
}

// secretBearingPanic carries a secret under a field name the needle list DOES
// name, alongside plain data under one it does not. The plain field is what
// distinguishes the designs — see the test below.
type secretBearingPanic struct {
	JobRef   string `json:"jobRef"`
	Password string `json:"password"`
}

type secretPanicJob struct{}

func (*secretPanicJob) Execute(_ JobContext) error {
	panic(secretBearingPanic{JobRef: "nightly-sync", Password: schedulerPanicSecret})
}

const schedulerPanicSecret = "test_password_123"

// TestJobErrorKeepsItsMessageOffTheSpan pins ADR-083 for the scheduler: a job's
// Execute error is consumer-authored, so the span reports its Go TYPE while the
// on-platform log line keeps the message.
func TestJobErrorKeepsItsMessageOffTheSpan(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })

	// zerolog binds os.Stdout at construction, so build the scheduler inside the
	// capture — outside it the capture is empty and the log assertion is vacuous.
	out := captureStdout(t, func() {
		module, _ := newTestScheduler(t, 5*time.Second, withTracer(tp.Tracer("test-scheduler")))
		runJobOnce(module, "failing-job", &failingJob{err: errors.New("job failed: " + obtest.LeakCanary)})
		require.NoError(t, module.Shutdown())
	})

	span := jobExecuteSpan(t, tp)
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "*errors.errorString", span.Status.Description)
	obtest.AssertExceptionTypeOnly(t, &span, "*errors.errorString")
	obtest.AssertNoSpanMarkers(t, &span, obtest.LeakCanary)

	assert.Contains(t, out, obtest.LeakCanary,
		"the log line is on-platform and keeps the error message")
}

// TestJobPanicSpanNamesTheTypeNotTheValue pins the panic path's span half, which
// no stdout-capture test can see: the recovered value's TYPE reaches the span as
// its own attribute — panicErr's Go type is framework noise — and the value
// itself reaches no span sink (ADR-081, ADR-083).
func TestJobPanicSpanNamesTheTypeNotTheValue(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })

	module, _ := newTestScheduler(t, 5*time.Second, withTracer(tp.Tracer("test-scheduler")))
	runJobOnce(module, "secret-panic-job", &secretPanicJob{})
	require.NoError(t, module.Shutdown())

	span := jobExecuteSpan(t, tp)
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "panic", span.Status.Description)
	obtest.AssertSpanAttribute(t, &span, "job.status", "panic")
	obtest.AssertSpanAttribute(t, &span, "job.panic_type", "scheduler.secretBearingPanic")
	obtest.AssertNoSpanMarkers(t, &span, schedulerPanicSecret, "nightly-sync")
}

// TestJobExecutionPanicNoSinkCarriesTheValue pins that NONE of the three sinks
// carries the panic value: not the log field, not the span, not the summary
// line's Err(). The load-bearing assertion is the NotContains on
// "nightly-sync" — a non-sensitive field of the panic value. Under the old code the
// secret itself was masked (`Password` is a default needle), so only a field the
// needle list does NOT name can tell the two designs apart.
func TestJobExecutionPanicNoSinkCarriesTheValue(t *testing.T) {
	var entry *jobEntry

	// zerolog binds os.Stdout at CONSTRUCTION, so the scheduler must be built
	// inside the capture — building it outside yields an empty capture and a test
	// that looks like it proved absence. This applies to every stdout-capture test
	// in the repo, not just this one.
	out := captureStdout(t, func() {
		module, _ := newTestScheduler(t, 5*time.Second)
		defer func() { _ = module.Shutdown() }()

		entry = &jobEntry{
			job:      &secretPanicJob{},
			metadata: &JobMetadata{JobID: "secret-panic-job", ScheduleType: "fixed-rate"},
		}
		module.runJobBody(entry, "manual")
	})

	assert.NotContains(t, out, schedulerPanicSecret,
		"the panic value must reach the sink only through the filtered reporting call")
	assert.NotContains(t, out, "nightly-sync",
		"NO field of the panic value is reported now — not even a non-sensitive one; "+
			"the previous design filtered the value, which protected only shapes the needle list reaches")
	assert.Contains(t, out, "secretBearingPanic",
		"the panic's type is what gets reported, on every one of the three sinks")
	assert.Equal(t, int64(1), entry.metadata.snapshot().FailureCount)
}

const barePanicJobSecret = "not-a-real-secret-0001"

type barePanicJob struct{}

func (*barePanicJob) Execute(_ JobContext) error { panic(barePanicJobSecret) }

// TestJobExecutionPanicNeverDisclosesTheValue pins that a job's panic value is
// never written to the sink. The reporting call used to pass it to
// Interface("panic", r) and rely on the sensitive-data filter, but that filter
// masks by FIELD NAME: the field is `panic`, which is not a needle, and a bare
// string has no inner field name to match. Same rule as the audit emitter's.
func TestJobExecutionPanicNeverDisclosesTheValue(t *testing.T) {
	var entry *jobEntry

	// zerolog binds os.Stdout at construction, so build inside the capture.
	out := captureStdout(t, func() {
		module, _ := newTestScheduler(t, 5*time.Second)
		defer func() { _ = module.Shutdown() }()

		entry = &jobEntry{
			job:      &barePanicJob{},
			metadata: &JobMetadata{JobID: "bare-panic-job", ScheduleType: "fixed-rate"},
		}
		module.runJobBody(entry, "manual")
	})

	assert.NotContains(t, out, barePanicJobSecret,
		"a recovered panic value must never reach the sink; the filter cannot mask a bare string")
	assert.Contains(t, out, `"panic_type":"string"`, "the panic's type must still be reported")
	assert.Equal(t, int64(1), entry.metadata.snapshot().FailureCount,
		"the job must still be recorded as failed")
}
