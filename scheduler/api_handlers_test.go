package scheduler

import (
	"net/http"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testJobID = "test-job-1"
)

// TestListJobsHandler verifies GET /_sys/job returns job metadata
func TestListJobsHandler(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register a test job
	job := &counterJob{}
	err := registrar.FixedRate(testJobID, job, 10*time.Second)
	require.NoError(t, err)

	// Call the handler
	req := EmptyRequest{}
	ctx := server.HandlerContext{
		Config: &config.Config{},
	}

	result, apiErr := module.listJobsHandler(req, ctx)

	// Verify success
	assert.Nil(t, apiErr)
	assert.Equal(t, 200, result.Status)
	assert.Len(t, result.Data.Data, 1)
	assert.Equal(t, 1, result.Data.Meta["total"])

	// Verify metadata is complete
	metadata := result.Data.Data[0]
	assert.NotNil(t, metadata)
	assert.Equal(t, testJobID, metadata.JobID)
	assert.Equal(t, "fixed-rate", metadata.ScheduleType)
	assert.Equal(t, "@every 10s", metadata.CronExpression)
	assert.Equal(t, "Every 10 seconds", metadata.HumanReadable)
}

// TestListJobsHandlerEmptyScheduler verifies empty job list when no jobs registered
func TestListJobsHandlerEmptyScheduler(t *testing.T) {
	module := NewModule()

	req := EmptyRequest{}
	ctx := server.HandlerContext{
		Config: &config.Config{},
	}

	result, apiErr := module.listJobsHandler(req, ctx)

	assert.Nil(t, apiErr)
	assert.Equal(t, 200, result.Status)
	assert.Len(t, result.Data.Data, 0)
	assert.Equal(t, 0, result.Data.Meta["total"])
}

// TestTriggerJobHandler verifies POST /_sys/job/:jobId triggers job
func TestTriggerJobHandler(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register a test job
	job := &counterJob{}
	err := registrar.FixedRate(testJobID, job, 10*time.Second)
	require.NoError(t, err)

	// Call the handler
	req := JobIDParam{JobID: testJobID}
	ctx := server.HandlerContext{
		Config: &config.Config{},
	}

	result, apiErr := module.triggerJobHandler(req, ctx)

	// Verify success - handler returns 202 Accepted for async job triggering
	assert.Nil(t, apiErr)
	assert.Equal(t, 202, result.Status) // HTTP 202 Accepted for async operations
	assert.Equal(t, testJobID, result.Data.Data.JobID)
	assert.Equal(t, "manual", result.Data.Data.Trigger)

	// Wait for async execution with polling
	timeout := time.After(1 * time.Second)
	tick := time.Tick(10 * time.Millisecond)

	executed := false
	for !executed {
		select {
		case <-timeout:
			t.Fatal("Timed out waiting for job execution")
		case <-tick:
			if job.Count() >= 1 {
				executed = true
			}
		}
	}

	assert.Greater(t, job.Count(), int64(0), "Job should have executed at least once")
}

// TestTriggerJobHandlerNotFound verifies 404 for unknown job
func TestTriggerJobHandlerNotFound(t *testing.T) {
	module := NewModule()

	req := JobIDParam{JobID: "non-existent-job"}
	ctx := server.HandlerContext{
		Config: &config.Config{},
	}

	_, apiErr := module.triggerJobHandler(req, ctx)

	// Verify not found error
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.HTTPStatus())
	assert.Contains(t, apiErr.ErrorCode(), "NOT_FOUND")
}

// TestTriggerJobHandlerShuttingDown verifies 503 when scheduler is shutting down
func TestTriggerJobHandlerShuttingDown(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)

	// Register a job
	job := &counterJob{}
	err := registrar.FixedRate(testJobID, job, 10*time.Second)
	require.NoError(t, err)

	// Initiate shutdown
	module.shutdownCancel()

	// Try to trigger job
	req := JobIDParam{JobID: testJobID}
	ctx := server.HandlerContext{
		Config: &config.Config{},
	}

	_, apiErr := module.triggerJobHandler(req, ctx)

	// Verify service unavailable error
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.HTTPStatus())
}

// TestListJobsHandlerExposesTimezone verifies GET /_sys/job reports the active zone
func TestListJobsHandlerExposesTimezone(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second, withTimezone("America/New_York"))

	err := registrar.FixedRate(testJobID, &counterJob{}, 10*time.Second)
	require.NoError(t, err)

	result, apiErr := module.listJobsHandler(EmptyRequest{}, server.HandlerContext{Config: &config.Config{}})
	require.Nil(t, apiErr)
	assert.Equal(t, "America/New_York", result.Data.Meta["timezone"])
}

// TestExecuteManualJobSkippedAfterShutdown is the distinguishing regression test
// for the Add-after-Wait race: the pre-fix executeManualJob had no shutdown
// re-check at all (tryLock ran first, wg.Add ran after), so a manual trigger
// racing Shutdown would run Execute even after wg.Wait() had already returned.
// Post-fix, wg.Add(1) runs first and the shutdown re-check bails before
// tryLock/Execute, so the job never runs.
func TestExecuteManualJobSkippedAfterShutdown(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	job := &counterJob{}
	entry := &jobEntry{job: job, metadata: &JobMetadata{JobID: "manual-shutdown-test"}}

	// Initiate shutdown BEFORE invoking the manual path.
	module.shutdownCancel()

	module.executeManualJob(entry)

	// Execute must never have run.
	assert.Equal(t, int64(0), job.Count(), "job Execute should not run after shutdown")

	// Add/Done must balance — a hang here means wg.Add(1) was not matched by a
	// deferred wg.Done() on the shutdown-bail path.
	done := make(chan struct{})
	go func() { module.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("wg.Wait() blocked after executeManualJob returned via shutdown-bail path")
	}
}

// TestExecuteManualJobBalancesAddDoneOnTryLockFailPath mirrors
// TestCreateJobWrapperBalancesAddDoneOnTryLockFailPath (module_test.go) for the
// manual-trigger path: pre-acquire the entry lock so executeManualJob's tryLock
// fails, and verify the skip is counted (this skip path DOES increment
// SkippedCount, unlike the shutdown-bail path) and Add/Done stays balanced.
func TestExecuteManualJobBalancesAddDoneOnTryLockFailPath(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	job := &counterJob{}
	entry := &jobEntry{job: job, metadata: &JobMetadata{JobID: "manual-trylock-test"}}

	// Pre-acquire the lock so executeManualJob's tryLock fails.
	require.True(t, entry.tryLock(), "test setup: first tryLock must succeed")
	defer entry.unlock()

	module.executeManualJob(entry)

	assert.Equal(t, int64(0), job.Count(), "job Execute should not run when tryLock fails")
	assert.Equal(t, int64(1), entry.metadata.snapshot().SkippedCount, "expected skip counter increment")

	done := make(chan struct{})
	go func() { module.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("wg.Wait() blocked after executeManualJob returned via tryLock-fail path")
	}
}
