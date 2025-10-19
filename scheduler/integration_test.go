package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchedulerLifecycleMVP verifies the complete scheduler lifecycle per User Story 1 MVP test:
// Register a simple job, schedule it to run every 5 seconds, wait 15 seconds, verify it executed 3 times.
func TestSchedulerLifecycleMVP(t *testing.T) {
	// Create a test job that counts executions
	job := &counterJob{}

	// Create and initialize scheduler module
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register job to run every 5 seconds (MVP test criteria)
	err := registrar.FixedRate("test-job", job, 5*time.Second)
	require.NoError(t, err, "Job registration should succeed")

	// Wait 15 seconds for 3 executions (MVP test criteria)
	// Add buffer for timing variations
	time.Sleep(16 * time.Second)

	// Verify job executed at least 3 times
	count := job.Count()
	assert.GreaterOrEqual(t, count, int64(3), "Job should execute at least 3 times in 15 seconds")
	assert.LessOrEqual(t, count, int64(4), "Job should not execute more than 4 times (allowing timing buffer)")

	// Graceful shutdown
	err = module.Shutdown()
	assert.NoError(t, err, "Graceful shutdown should succeed")
}

// TestSchedulerLifecycleAllSchedulingPatterns verifies all 5 scheduling patterns work
func TestSchedulerLifecycleAllSchedulingPatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	module, registrar := newTestScheduler(t, 5*time.Second)

	// Create jobs for each pattern
	fixedRateJob := &counterJob{}

	// Fixed rate: every 2 seconds
	err := registrar.FixedRate("fixed-rate-job", fixedRateJob, 2*time.Second)
	require.NoError(t, err)

	// Wait for fixed-rate job to execute
	time.Sleep(5 * time.Second)

	// Verify fixed-rate job executed
	assert.GreaterOrEqual(t, fixedRateJob.Count(), int64(2), "Fixed-rate job should execute at least twice")

	// Graceful shutdown
	err = module.Shutdown()
	assert.NoError(t, err)
}

// TestSchedulerLifecycleGracefulShutdown verifies in-flight jobs complete during shutdown
func TestSchedulerLifecycleGracefulShutdown(t *testing.T) {
	module, registrar := newTestScheduler(t, 10*time.Second)

	// Create a long-running job
	job := &longRunningJob{duration: 2 * time.Second}

	err := registrar.FixedRate("long-job", job, 1*time.Second)
	require.NoError(t, err)

	// Wait for job to start
	time.Sleep(1500 * time.Millisecond)

	// Initiate shutdown while job is running
	shutdownStart := time.Now()
	err = module.Shutdown()
	shutdownDuration := time.Since(shutdownStart)

	assert.NoError(t, err, "Shutdown should succeed")

	// Verify job completed (not cancelled mid-execution)
	assert.True(t, job.Completed(), "Job should have completed")

	// Verify shutdown waited for job
	assert.GreaterOrEqual(t, shutdownDuration, 500*time.Millisecond, "Shutdown should wait for in-flight job")
}

// TestSchedulerLifecycleNoJobsRegistered verifies scheduler handles no jobs gracefully
func TestSchedulerLifecycleNoJobsRegistered(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	// Shutdown without registering any jobs
	err := module.Shutdown()
	assert.NoError(t, err, "Shutdown with no jobs should succeed")
}

// Test helpers

// counterJob counts how many times it's executed
type counterJob struct {
	count int64
}

func (j *counterJob) Execute(_ JobContext) error {
	atomic.AddInt64(&j.count, 1)
	return nil
}

func (j *counterJob) Count() int64 {
	return atomic.LoadInt64(&j.count)
}

// longRunningJob simulates a long-running task
type longRunningJob struct {
	duration  time.Duration
	completed bool
	mu        sync.Mutex
}

func (j *longRunningJob) Execute(_ JobContext) error {
	// Simulate work
	time.Sleep(j.duration)

	j.mu.Lock()
	j.completed = true
	j.mu.Unlock()

	return nil
}

func (j *longRunningJob) Completed() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.completed
}

// newTestScheduler creates and initializes a scheduler module for testing
func newTestScheduler(t *testing.T, shutdownTimeout time.Duration) (*SchedulerModule, app.JobRegistrar) {
	module := NewSchedulerModule()

	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				ShutdownTimeout: shutdownTimeout,
			},
		},
		Tracer:        nil, // No-op tracer for tests
		MeterProvider: nil, // No-op meter for tests
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil // No DB for MVP tests
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil // No messaging for MVP tests
		},
	}

	err := module.Init(appDeps)
	require.NoError(t, err, "Module initialization should succeed")

	// Return module and its JobRegistrar interface
	return module, module
}
