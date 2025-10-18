package scheduler

import (
	"context"
	"net/http"

	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// JobListResponse represents the response for GET /_sys/job
type JobListResponse struct {
	Jobs []*JobMetadata `json:"jobs"`
}

// JobTriggerResponse represents the response for POST /_sys/job/:jobId
type JobTriggerResponse struct {
	JobID   string `json:"jobId"`
	Trigger string `json:"trigger"`
	Message string `json:"message"`
}

// Empty request type for handlers with no request body
type EmptyRequest struct{}

// JobIDParam captures the jobId path parameter
type JobIDParam struct {
	JobID string `param:"jobId" validate:"required"`
}

// listJobsHandler returns all registered jobs with their metadata
// GET /_sys/job
func (m *SchedulerModule) listJobsHandler(_ EmptyRequest, _ server.HandlerContext) (server.Result[JobListResponse], server.IAPIError) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect metadata from all jobs
	jobs := make([]*JobMetadata, 0, len(m.jobs))
	for _, entry := range m.jobs {
		snapshot := entry.metadata.snapshot()
		jobs = append(jobs, snapshot)
	}

	return server.NewResult(http.StatusOK, JobListResponse{Jobs: jobs}), nil
}

// triggerJobHandler manually triggers a job execution
// POST /_sys/job/:jobId
func (m *SchedulerModule) triggerJobHandler(req JobIDParam, _ server.HandlerContext) (server.Result[JobTriggerResponse], server.IAPIError) {
	jobID := req.JobID

	m.mu.RLock()
	entry, exists := m.jobs[jobID]
	m.mu.RUnlock()

	if !exists {
		return server.Result[JobTriggerResponse]{}, server.NewNotFoundError("job").WithDetails("job_id", jobID)
	}

	// Check for shutdown
	select {
	case <-m.shutdownCtx.Done():
		return server.Result[JobTriggerResponse]{}, server.NewServiceUnavailableError("scheduler is shutting down")
	default:
	}

	// Trigger job execution asynchronously
	go m.executeManualJob(entry)

	response := JobTriggerResponse{
		JobID:   jobID,
		Trigger: "manual",
		Message: "Request accepted: job will run unless an instance is already running",
	}

	return server.NewResult(http.StatusAccepted, response), nil
}

// executeManualJob executes a job triggered manually (not by scheduler)
func (m *SchedulerModule) executeManualJob(entry *jobEntry) {
	// Overlapping prevention (same as scheduled execution)
	if !entry.tryLock() {
		m.logger.Warn().
			Str("jobID", entry.metadata.JobID).
			Str("triggerType", "manual").
			Msg("Job trigger skipped - job is already running")
		entry.metadata.incrementSkipped()
		return
	}

	// Track in-flight execution
	m.wg.Add(1)

	defer func() {
		entry.unlock()
		m.wg.Done()
	}()

	// Create execution context
	ctx, cancel := context.WithCancel(m.shutdownCtx)
	defer cancel()

	// Create JobContext with manual trigger type
	jobCtx := newJobContext(
		ctx,
		entry.metadata.JobID,
		"manual", // Trigger type is manual, not scheduled
		m.logger,
		func() types.Interface {
			db, err := m.getDB(ctx)
			if err != nil {
				m.logger.Error().Err(err).Msg("Failed to get database connection")
				return nil
			}
			return db
		},
		func() messaging.Client {
			msg, err := m.getMessaging(ctx)
			if err != nil {
				m.logger.Error().Err(err).Msg("Failed to get messaging client")
				return nil
			}
			return msg
		},
		m.config,
	)

	// Execute with same panic recovery as scheduled jobs
	m.executeJob(entry, jobCtx)
}
