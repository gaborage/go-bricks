package scheduler

import (
	"net/http"

	"github.com/gaborage/go-bricks/server"
)

// JobListResponse represents the response for GET /_sys/job using standard GoBricks envelope
type JobListResponse struct {
	Data []*JobMetadata         `json:"data"`
	Meta map[string]interface{} `json:"meta"`
}

// JobTriggerResponse represents the response for POST /_sys/job/:jobId using standard GoBricks envelope
type JobTriggerResponse struct {
	Data JobTriggerData         `json:"data"`
	Meta map[string]interface{} `json:"meta"`
}

// JobTriggerData contains the trigger response data
type JobTriggerData struct {
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
func (m *Module) listJobsHandler(_ EmptyRequest, _ server.HandlerContext) (server.Result[JobListResponse], server.IAPIError) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect metadata from all jobs
	jobs := make([]*JobMetadata, 0, len(m.jobs))
	for _, entry := range m.jobs {
		snapshot := entry.metadata.snapshot()

		// Populate NextExecutionTime from gocron job
		if entry.gocronJob != nil {
			nextRun, err := entry.gocronJob.NextRun()
			if err == nil && !nextRun.IsZero() {
				snapshot.NextExecutionTime = &nextRun
			}
		}

		jobs = append(jobs, snapshot)
	}

	// Return with standard GoBricks envelope
	response := JobListResponse{
		Data: jobs,
		Meta: map[string]interface{}{
			"total":    len(jobs),
			"timezone": m.timezoneLabel(),
		},
	}

	return server.NewResult(http.StatusOK, response), nil
}

// triggerJobHandler manually triggers a job execution
// POST /_sys/job/:jobId
func (m *Module) triggerJobHandler(req JobIDParam, _ server.HandlerContext) (server.Result[JobTriggerResponse], server.IAPIError) {
	jobID := req.JobID

	m.mu.RLock()
	entry, exists := m.jobs[jobID]
	m.mu.RUnlock()

	if !exists {
		return server.Result[JobTriggerResponse]{}, server.NewNotFoundError("job").WithDetails("job_id", jobID)
	}

	// Atomically check shutdown + register the in-flight slot under m.mu (Shutdown
	// cancels under the same lock), so wg.Add happens-before Shutdown's wg.Wait and
	// can never land after Wait returned. Nothing may return/panic between here and
	// the spawn, or the Add leaks and hangs Shutdown.
	if !m.registerManualTrigger() {
		return server.Result[JobTriggerResponse]{}, server.NewServiceUnavailableError("scheduler is shutting down")
	}
	go m.executeManualJob(entry)

	// Return with standard GoBricks envelope
	response := JobTriggerResponse{
		Data: JobTriggerData{
			JobID:   jobID,
			Trigger: "manual",
			Message: "Request accepted: job will run unless an instance is already running",
		},
		Meta: map[string]interface{}{},
	}

	return server.NewResult(http.StatusAccepted, response), nil
}

// executeManualJob runs a manually-triggered job in its own goroutine. Its caller
// (triggerJobHandler) MUST have registered the in-flight slot via
// registerManualTrigger (which did the wg.Add); this method owns the matching
// wg.Done. Shares the execution body with the scheduled path via runJobBody.
func (m *Module) executeManualJob(entry *jobEntry) {
	defer m.wg.Done()
	m.runJobBody(entry, "manual")
}
