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

// TestRegisterManualTrigger is the deterministic invariant that closes the
// Add-after-Wait race: registration is refused (no wg.Add) once shutdown is
// signaled, and accepted (one wg.Add) while live.
func TestRegisterManualTrigger(t *testing.T) {
	t.Run("rejects_after_shutdown", func(t *testing.T) {
		module, _ := newTestScheduler(t, 5*time.Second)
		module.shutdownCancel()
		assert.False(t, module.registerManualTrigger(), "must reject after shutdown")
		assertWaitGroupDrains(t, module, "registerManualTrigger rejected after shutdown")
	})
	t.Run("accepts_when_live", func(t *testing.T) {
		module, _ := newTestScheduler(t, 5*time.Second)
		require.True(t, module.registerManualTrigger(), "must accept when live")
		module.executeManualJob(&jobEntry{job: &counterJob{}, metadata: &JobMetadata{JobID: "live"}})
		assertWaitGroupDrains(t, module, "executeManualJob balanced a live registration")
	})
}

// TestExecuteManualJobBailsWhenShutdownAfterRegister covers the case the internal
// re-check guards: registration succeeds, THEN shutdown fires, so runJobBody bails
// before running the job — and the Add/Done still balances.
func TestExecuteManualJobBailsWhenShutdownAfterRegister(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)
	job := &counterJob{}
	entry := &jobEntry{job: job, metadata: &JobMetadata{JobID: "manual-shutdown-test"}}

	require.True(t, module.registerManualTrigger()) // Add(1)
	module.shutdownCancel()                         // shutdown after registering
	module.executeManualJob(entry)                  // defers Done; runJobBody bails at re-check

	assert.Equal(t, int64(0), job.Count(), "job must not run when shutdown fires after registration")
	assertWaitGroupDrains(t, module, "executeManualJob bailed via shutdown re-check")
}

// TestExecuteManualJobBalancesAddDoneOnTryLockFailPath: with the slot registered,
// a tryLock failure still balances and counts the skip.
func TestExecuteManualJobBalancesAddDoneOnTryLockFailPath(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)
	job := &counterJob{}
	entry := &jobEntry{job: job, metadata: &JobMetadata{JobID: "manual-trylock-test"}}

	require.True(t, module.registerManualTrigger()) // Add(1) — pairs with executeManualJob's Done
	require.True(t, entry.tryLock(), "test setup: first tryLock must succeed")
	defer entry.unlock()

	module.executeManualJob(entry)

	assert.Equal(t, int64(0), job.Count(), "job Execute should not run when tryLock fails")
	assert.Equal(t, int64(1), entry.metadata.snapshot().SkippedCount, "expected skip counter increment")
	assertWaitGroupDrains(t, module, "executeManualJob returned via tryLock-fail path")
}
