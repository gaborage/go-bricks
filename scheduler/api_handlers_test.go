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
	defer module.Shutdown()

	// Register a test job
	job := &counterJob{}
	err := registrar.FixedRate(testJobID, job, 10*time.Second)
	require.NoError(t, err)

	// Call the handler
	req := EmptyRequest{}
	ctx := server.HandlerContext{
		Echo:   nil, // Not needed for this test
		Config: &config.Config{},
	}

	result, apiErr := module.listJobsHandler(req, ctx)

	// Verify success
	assert.Nil(t, apiErr)
	assert.Equal(t, 200, result.Status)
	assert.Len(t, result.Data.Jobs, 1)

	// Verify metadata is complete
	metadata := result.Data.Jobs[0]
	assert.NotNil(t, metadata)
	assert.Equal(t, testJobID, metadata.JobID)
	assert.Equal(t, "fixed-rate", metadata.ScheduleType)
	assert.Equal(t, "@every 10s", metadata.CronExpression)
	assert.Equal(t, "Every 10 seconds", metadata.HumanReadable)
}

// TestListJobsHandlerEmptyScheduler verifies empty job list when no jobs registered
func TestListJobsHandlerEmptyScheduler(t *testing.T) {
	module := NewSchedulerModule()

	req := EmptyRequest{}
	ctx := server.HandlerContext{
		Echo:   nil,
		Config: &config.Config{},
	}

	result, apiErr := module.listJobsHandler(req, ctx)

	assert.Nil(t, apiErr)
	assert.Equal(t, 200, result.Status)
	assert.Len(t, result.Data.Jobs, 0)
}

// TestTriggerJobHandler verifies POST /_sys/job/:jobId triggers job
func TestTriggerJobHandler(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second)
	defer module.Shutdown()

	// Register a test job
	job := &counterJob{}
	err := registrar.FixedRate(testJobID, job, 10*time.Second)
	require.NoError(t, err)

	// Call the handler
	req := JobIDParam{JobID: testJobID}
	ctx := server.HandlerContext{
		Echo:   nil,
		Config: &config.Config{},
	}

	result, apiErr := module.triggerJobHandler(req, ctx)

	// Verify success - handler returns 202 Accepted for async job triggering
	assert.Nil(t, apiErr)
	assert.Equal(t, 202, result.Status) // HTTP 202 Accepted for async operations
	assert.Equal(t, testJobID, result.Data.JobID)
	assert.Equal(t, "manual", result.Data.Trigger)

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
	module := NewSchedulerModule()

	req := JobIDParam{JobID: "non-existent-job"}
	ctx := server.HandlerContext{
		Echo:   nil,
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
		Echo:   nil,
		Config: &config.Config{},
	}

	_, apiErr := module.triggerJobHandler(req, ctx)

	// Verify service unavailable error
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.HTTPStatus())
}
