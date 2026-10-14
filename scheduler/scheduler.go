package scheduler

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/go-co-op/gocron/v2"
)

// jobEntry represents a registered job with metadata and execution control.
// Each jobEntry holds:
// - The user-provided Executor implementation
// - Schedule configuration (how/when to run)
// - Metadata for system APIs (execution statistics, next run time, etc.)
// - Mutex for overlapping execution prevention per FR-026
// - gocron job handle for lifecycle management
type jobEntry struct {
	// job is the user-provided Executor implementation
	job Executor

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

	// window is a Windowed job's summary window; nil for a plain job.
	window *windowState
}

// windowFor returns the summary window a run of triggerType reports to: none for a
// plain job or a manual trigger.
func (e *jobEntry) windowFor(triggerType string) *windowState {
	if e.window == nil || triggerType != triggerScheduled {
		return nil
	}
	return e.window
}

// newRecorder returns the Recorder a Windowed job's run reports through; nil for a
// plain job. runWindow is the window the run reports to, nil for a manual run; ctx
// is the run's traced context, which its metric points record under.
func (e *jobEntry) newRecorder(ctx context.Context, runWindow *windowState) *Recorder {
	if e.window == nil {
		return nil
	}
	var late *atomic.Int64
	if runWindow != nil {
		late = &runWindow.late
	}
	return newRecorder(ctx, e.window.spec, late)
}

// execute runs the job: a plain one through Execute, a Windowed one through its
// Recorder, which is sealed however run ends.
func (e *jobEntry) execute(ctx JobContext, rec *Recorder) error {
	if rec == nil {
		return e.job.Execute(ctx)
	}
	defer rec.seal()
	return e.window.spec.run(ctx, rec)
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
