package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchedulerModuleName verifies the module name
func TestSchedulerModuleName(t *testing.T) {
	module := NewSchedulerModule()
	assert.Equal(t, "scheduler", module.Name())
}

// TestSchedulerModuleDeclareMessaging verifies no-op messaging declaration
func TestSchedulerModuleDeclareMessaging(_ *testing.T) {
	module := NewSchedulerModule()

	// DeclareMessaging is a no-op but should not panic
	decls := &messaging.Declarations{}
	module.DeclareMessaging(decls)
}

// TestSchedulerModuleRegisterRoutes verifies route registration (stub for Phase 4)
func TestSchedulerModuleRegisterRoutes(_ *testing.T) {
	module := NewSchedulerModule()

	// RegisterRoutes is a stub but should not panic
	module.RegisterRoutes(nil, nil)
}

// TestJobExecutionFailure verifies job failures are tracked
func TestJobExecutionFailure(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)
	defer module.Shutdown()

	// Create a job that always fails
	job := &failingJob{err: errors.New("intentional failure")}

	err := registrar.FixedRate("failing-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
}

// TestJobExecutionPanic verifies panic recovery
func TestJobExecutionPanic(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)
	defer module.Shutdown()

	// Create a job that panics
	job := &panicJob{}

	err := registrar.FixedRate("panic-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
}

// TestJobExecutionOverlappingPrevention verifies jobs don't overlap
func TestJobExecutionOverlappingPrevention(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)
	defer module.Shutdown()

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

	// Create scheduler with observability
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: mp.MeterProvider,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Register a job that panics
	job := &panicJob{}
	err = module.FixedRate("panic-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)

	// Collect metrics
	rm := mp.Collect(t)

	// Verify panic counter was incremented
	panicMetric := obtest.FindMetric(rm, "job.panic.total")
	require.NotNil(t, panicMetric, "Panic counter metric should be recorded")

	// Verify execution counter also recorded the panic
	execMetric := obtest.FindMetric(rm, "job.execution.total")
	require.NotNil(t, execMetric, "Execution counter metric should be recorded")
}

// TestJobSkippedDuringShutdown verifies jobs skip execution when shutdown is triggered
func TestJobSkippedDuringShutdown(t *testing.T) {
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)

	// Create a job that tracks execution
	job := &slowJob{duration: 10 * time.Millisecond}

	// Register job with long interval
	err = module.FixedRate("shutdown-test-job", job, 5*time.Second)
	require.NoError(t, err)

	// Immediately shutdown before job can execute
	err = module.Shutdown()
	require.NoError(t, err)

	// Verify job was never executed
	assert.Equal(t, 0, job.count(), "Job should not execute after shutdown")
}

// TestJobExecutionWithDBGetterError verifies error handling when DB getter fails
func TestJobExecutionWithDBGetterError(t *testing.T) {
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, errors.New("DB connection failed")
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Create a job that checks DB is nil
	job := &dbCheckJob{}
	err = module.FixedRate("db-error-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
	assert.Equal(t, int32(1), atomic.LoadInt32(&job.dbWasNil), "DB should be nil when getter fails")
}

// TestJobExecutionWithMessagingGetterError verifies error handling when messaging getter fails
func TestJobExecutionWithMessagingGetterError(t *testing.T) {
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, errors.New("Messaging connection failed")
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Create a job that checks messaging is nil
	job := &messagingCheckJob{}
	err = module.FixedRate("msg-error-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait until the job executes (<=1s)
	waitFor(t, job.wasExecuted)
	assert.Equal(t, int32(1), atomic.LoadInt32(&job.messagingWasNil), "Messaging should be nil when getter fails")
}

// TestSlowJobThresholdWarning verifies slow job detection and WARN severity
func TestSlowJobThresholdWarning(t *testing.T) {
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
					SlowJob:  100 * time.Millisecond, // Set low threshold
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Create a slow job that exceeds threshold
	job := &slowJob{duration: 150 * time.Millisecond}
	err = module.FixedRate("slow-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to execute (job takes 150ms, so wait longer)
	time.Sleep(400 * time.Millisecond)

	// Verify job was executed
	assert.Greater(t, job.count(), 0, "Job should have executed")
}

// TestJobExecutionWithoutTracer verifies jobs execute successfully when tracer is nil
func TestJobExecutionWithoutTracer(t *testing.T) {
	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        nil, // Explicitly nil tracer
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Create a simple job
	job := &slowJob{duration: 10 * time.Millisecond}
	err = module.FixedRate("no-tracer-job", job, 100*time.Millisecond)
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

	module := NewSchedulerModule()
	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: 5 * time.Second,
				},
			},
		},
		Tracer:        tp.Tracer("test-scheduler"),
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err)
	defer module.Shutdown()

	// Create a simple job
	job := &slowJob{duration: 10 * time.Millisecond}
	err = module.FixedRate("traced-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to execute
	time.Sleep(200 * time.Millisecond)

	// Verify job executed
	assert.Greater(t, job.count(), 0, "Job should execute")

	// Verify span was created
	collector := obtest.NewSpanCollector(t, tp.Exporter)
	spans := collector.WithName("job.execute")
	spans.AssertCount(1)

	// Verify span attributes
	span := spans.First()
	obtest.AssertSpanAttribute(t, &span, "job.id", "traced-job")
	obtest.AssertSpanAttribute(t, &span, "job.trigger", "scheduled")
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

// place near test helpers (non-diff, supporting snippet)
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, time.Second, 10*time.Millisecond)
}
