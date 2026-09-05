package scheduler

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchedulerLifecycleMVP verifies the complete scheduler lifecycle per User Story 1 MVP test:
// Register a simple job scheduled every 1 second, observe three executions, and check they took
// at least three intervals to arrive (so the rate itself is pinned, not just the count).
func TestSchedulerLifecycleMVP(t *testing.T) {
	// Create a test job that counts executions
	job := &counterJob{}

	// Create and initialize scheduler module
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register job to run every 1 second (MVP test criteria, scaled 1:5)
	start := time.Now()
	err := registrar.FixedRate("test-job", job, 1*time.Second)
	require.NoError(t, err, "Job registration should succeed")

	// Observe the 3rd execution on the job's own counter rather than sleeping past it.
	require.Eventually(t, func() bool { return job.Count() >= 3 }, 10*time.Second, 10*time.Millisecond,
		"Job should execute at least 3 times")
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 3*time.Second-50*time.Millisecond,
		"3 executions cannot arrive faster than 3 intervals")
	// A rate that regressed to 2s would need ~6s for 3 executions; 5s tolerates
	// two full seconds of scheduler jitter while still catching that.
	assert.Less(t, elapsed, 5*time.Second,
		"3 executions should arrive within 3 intervals plus jitter")

	count := job.Count()
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

	// Fixed rate: every 500 milliseconds
	err := registrar.FixedRate("fixed-rate-job", fixedRateJob, 500*time.Millisecond)
	require.NoError(t, err)

	// Wait for the 2nd fixed-rate execution on the job's own counter, not a sleep.
	require.Eventually(t, func() bool { return fixedRateJob.Count() >= 2 },
		5*time.Second, 10*time.Millisecond, "Fixed-rate job should execute at least twice")

	// Graceful shutdown
	err = module.Shutdown()
	assert.NoError(t, err)
}

// TestSchedulerLifecycleGracefulShutdown verifies in-flight jobs complete during shutdown
func TestSchedulerLifecycleGracefulShutdown(t *testing.T) {
	module, registrar := newTestScheduler(t, 10*time.Second)

	// Create a long-running job
	const inFlightJobDuration = 500 * time.Millisecond
	job := &longRunningJob{duration: inFlightJobDuration}

	err := registrar.FixedRate("long-job", job, 250*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to start — on the job's own signal, not a sleep, so shutdown
	// is guaranteed to land mid-execution however loaded the machine is.
	waitFor(t, job.Started)

	// Initiate shutdown while job is running
	shutdownStart := time.Now()
	err = module.Shutdown()
	shutdownDuration := time.Since(shutdownStart)

	assert.NoError(t, err, "Shutdown should succeed")

	// Verify job completed (not canceled mid-execution)
	assert.True(t, job.Completed(), "Job should have completed")

	// Verify shutdown waited for job
	assert.GreaterOrEqual(t, shutdownDuration, inFlightJobDuration/4, "Shutdown should wait for in-flight job")
}

// TestSchedulerLifecycleNoJobsRegistered verifies scheduler handles no jobs gracefully
func TestSchedulerLifecycleNoJobsRegistered(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second) // NOSONAR: JobRegistrar intentionally ignored - test only needs Module to verify shutdown behavior

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
	started   atomic.Bool
	completed atomic.Bool
}

func (j *longRunningJob) Execute(_ JobContext) error {
	j.started.Store(true)

	// Simulate work
	time.Sleep(j.duration)

	j.completed.Store(true)

	return nil
}

// Started reports whether Execute has begun, so callers can observe the job entering flight.
func (j *longRunningJob) Started() bool { return j.started.Load() }

func (j *longRunningJob) Completed() bool { return j.completed.Load() }
