package scheduler

import (
	"sync"
	"time"
)

// JobMetadata contains information about a registered job for system API responses.
// Thread-safe access is managed by jobEntry mutex.
//
// Exposed via GET /_sys/job endpoint per FR-009, FR-010.
type JobMetadata struct {
	// JobID is the unique identifier provided during registration
	JobID string `json:"jobId"`

	// ScheduleType identifies the scheduling pattern (fixed-rate, daily, weekly, hourly, monthly)
	ScheduleType string `json:"scheduleType"`

	// CronExpression is a cron-style representation of the schedule (e.g., "0 3 * * *" for daily at 3 AM)
	// Generated from ScheduleConfiguration per data-model.md
	CronExpression string `json:"cronExpression"`

	// HumanReadable is a user-friendly description (e.g., "Every Monday at 2:00 AM", "Every 30 minutes")
	HumanReadable string `json:"humanReadable"`

	// NextExecutionTime is when the job will execute next (nil if not yet scheduled)
	NextExecutionTime *time.Time `json:"nextExecutionTime,omitempty"`

	// LastExecutionTime is when the job last executed (nil if never run)
	LastExecutionTime *time.Time `json:"lastExecutionTime,omitempty"`

	// LastExecutionStatus is the status of the last execution: "success", "failure", "skipped" (nil if never run)
	LastExecutionStatus string `json:"lastExecutionStatus,omitempty"`

	// TotalExecutions is the total number of times the job has been triggered (scheduled + manual)
	TotalExecutions int64 `json:"totalExecutions"`

	// SuccessCount is the number of executions that completed without error
	SuccessCount int64 `json:"successCount"`

	// FailureCount is the number of executions that returned error or panicked
	FailureCount int64 `json:"failureCount"`

	// SkippedCount is the number of triggers skipped due to already-running instance (overlapping prevention per FR-026)
	SkippedCount int64 `json:"skippedCount"`

	// mu protects all fields above for thread-safe updates
	// Not exported - internal to scheduler package
	mu sync.Mutex `json:"-"`
}

// incrementSuccess updates metadata after successful job execution
func (m *JobMetadata) incrementSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SuccessCount++
	m.TotalExecutions++
	now := time.Now()
	m.LastExecutionTime = &now
	m.LastExecutionStatus = "success"
}

// incrementFailed updates metadata after failed job execution (error returned or panic recovered)
func (m *JobMetadata) incrementFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailureCount++
	m.TotalExecutions++
	now := time.Now()
	m.LastExecutionTime = &now
	m.LastExecutionStatus = "failure"
}

// incrementSkipped updates metadata when job trigger is skipped (overlapping prevention)
func (m *JobMetadata) incrementSkipped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SkippedCount++
	// Note: skipped executions don't update LastExecutionTime or LastExecutionStatus
	// per data-model.md line 375
}

// snapshot returns a thread-safe copy of the metadata for API responses.
// The returned JobMetadata contains a zero-value mutex (not copied from source).
func (m *JobMetadata) snapshot() *JobMetadata {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := &JobMetadata{
		JobID:               m.JobID,
		ScheduleType:        m.ScheduleType,
		CronExpression:      m.CronExpression,
		HumanReadable:       m.HumanReadable,
		LastExecutionStatus: m.LastExecutionStatus,
		TotalExecutions:     m.TotalExecutions,
		SuccessCount:        m.SuccessCount,
		FailureCount:        m.FailureCount,
		SkippedCount:        m.SkippedCount,
		mu:                  sync.Mutex{}, // Explicit zero value - not a copy
	}

	// Copy time pointers if they exist
	if m.NextExecutionTime != nil {
		t := *m.NextExecutionTime
		snapshot.NextExecutionTime = &t
	}
	if m.LastExecutionTime != nil {
		t := *m.LastExecutionTime
		snapshot.LastExecutionTime = &t
	}

	return snapshot
}
