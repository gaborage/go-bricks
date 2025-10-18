package scheduler

import (
	"sync"

	"github.com/go-co-op/gocron/v2"
)

// jobEntry represents a registered job with metadata and execution control.
// Each jobEntry holds:
// - The user-provided Job implementation
// - Schedule configuration (how/when to run)
// - Metadata for system APIs (execution statistics, next run time, etc.)
// - Mutex for overlapping execution prevention per FR-026
// - gocron job handle for lifecycle management
type jobEntry struct {
	// job is the user-provided Job implementation
	job Job

	// schedule describes when/how the job should execute
	schedule ScheduleConfiguration

	// metadata tracks execution statistics and is exposed via system APIs
	metadata *JobMetadata

	// mu prevents overlapping execution of the same job per FR-026
	// Lock is acquired before job execution, released after completion
	mu sync.Mutex

	// gocronJob is the handle from gocron/v2 scheduler
	// Used for cancellation, extracting next execution time, etc.
	gocronJob gocron.Job

	// running tracks if this job is currently executing
	// Checked under mutex lock to enforce overlapping prevention
	running bool
}

// tryLock attempts to acquire the execution lock for this job.
// Returns true if lock acquired (job can execute), false if job is already running.
// Per FR-026, FR-027: Skip triggers when job is already running, log warning.
func (e *jobEntry) tryLock() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		return false // Job already running
	}

	e.running = true
	return true
}

// unlock releases the execution lock after job completes.
func (e *jobEntry) unlock() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = false
}
