package scheduler

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/messaging"
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

	// Wait for job to execute and fail
	time.Sleep(200 * time.Millisecond)

	// Verify job was called
	assert.True(t, job.wasExecuted(), "Job should have been executed")
}

// TestJobExecutionPanic verifies panic recovery
func TestJobExecutionPanic(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)
	defer module.Shutdown()

	// Create a job that panics
	job := &panicJob{}

	err := registrar.FixedRate("panic-job", job, 100*time.Millisecond)
	require.NoError(t, err)

	// Wait for job to execute and panic
	time.Sleep(200 * time.Millisecond)

	// Verify job was called (and panic was recovered)
	assert.True(t, job.wasExecuted(), "Job should have been executed before panic")
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
