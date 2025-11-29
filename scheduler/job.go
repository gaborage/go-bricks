package scheduler

import (
	"context"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// Job represents a unit of work to be executed on a schedule.
// Implementations must be thread-safe as different job instances may run concurrently
// (though the scheduler prevents overlapping executions of the SAME job per FR-026).
//
// Example:
//
//	type CleanupJob struct{}
//
//	func (j *CleanupJob) Execute(ctx JobContext) error {
//	    ctx.Logger().Info().Str("jobID", ctx.JobID()).Msg("Starting cleanup")
//	    rows, err := ctx.DB().Query(ctx, "DELETE FROM temp_data WHERE created_at < NOW() - INTERVAL '24 hours'")
//	    if err != nil {
//	        return fmt.Errorf("cleanup failed: %w", err)
//	    }
//	    defer rows.Close()
//	    return nil
//	}
type Job interface {
	// Execute performs the job's work using the provided context.
	// Return error to mark execution as failed (will be logged and recorded in metrics per FR-020).
	// Panic will be recovered, logged with stack trace, and marked as failed per FR-021.
	// Jobs SHOULD respect context cancellation (check ctx.Done()) for graceful shutdown per FR-024.
	Execute(ctx JobContext) error
}

// JobContext provides access to framework dependencies and execution metadata
// during job execution. Embeds context.Context for cancellation, deadlines, and trace context.
//
// JobContext mirrors the HTTP handler context pattern per Constitution VII (UX Consistency).
// OpenTelemetry trace context is automatically propagated to DB, Messaging calls per FR-019.
type JobContext interface {
	context.Context // Embed stdlib context for cancellation, deadlines, values, and trace context

	// JobID returns the unique identifier for this job (as registered via JobRegistrar)
	JobID() string

	// TriggerType returns how this job was triggered: "scheduled" or "manual"
	// "scheduled" = automatic execution based on schedule
	// "manual" = triggered via POST /_sys/job/:jobId
	TriggerType() string

	// Logger returns the framework logger with job-specific fields pre-populated
	// (jobID, trigger type). Use this for all job logging per FR-020.
	Logger() logger.Logger

	// DB returns the database interface (may be nil if not configured in ModuleDeps)
	DB() types.Interface

	// Messaging returns the messaging client (may be nil if not configured in ModuleDeps)
	Messaging() messaging.Client

	// Config returns the application configuration
	Config() *config.Config
}

// jobContextImpl is the internal implementation of JobContext
// (implementation details not exported per Constitution I: Explicit Over Implicit)
//
// Multi-tenancy support: DB and Messaging are resolved dynamically via functions
// to support tenant-specific resource resolution at execution time.
type jobContextImpl struct {
	context.Context //nolint:S8242 // NOSONAR: JobContext interface requires context embedding for OpenTelemetry propagation
	jobID           string
	triggerType     string
	logger          logger.Logger
	getDB           func() types.Interface  // Resolver for multi-tenant DB
	getMessaging    func() messaging.Client // Resolver for multi-tenant messaging
	config          *config.Config
}

// newJobContext creates a new JobContext with the provided dependencies.
// For multi-tenancy: Pass resolver functions that extract tenant-specific resources
// from the embedded context (e.g., using tenant ID from context values).
func newJobContext(
	ctx context.Context,
	jobID string,
	triggerType string,
	log logger.Logger,
	getDB func() types.Interface,
	getMessaging func() messaging.Client,
	cfg *config.Config,
) JobContext {
	return &jobContextImpl{
		Context:      ctx,
		jobID:        jobID,
		triggerType:  triggerType,
		logger:       log,
		getDB:        getDB,
		getMessaging: getMessaging,
		config:       cfg,
	}
}

// JobID implements JobContext
func (ctx *jobContextImpl) JobID() string {
	return ctx.jobID
}

// TriggerType implements JobContext
func (ctx *jobContextImpl) TriggerType() string {
	return ctx.triggerType
}

// Logger implements JobContext
func (ctx *jobContextImpl) Logger() logger.Logger {
	return ctx.logger
}

// DB implements JobContext
// Resolves tenant-specific database dynamically for multi-tenant support
func (ctx *jobContextImpl) DB() types.Interface {
	if ctx.getDB == nil {
		return nil
	}
	return ctx.getDB()
}

// Messaging implements JobContext
// Resolves tenant-specific messaging client dynamically for multi-tenant support
func (ctx *jobContextImpl) Messaging() messaging.Client {
	if ctx.getMessaging == nil {
		return nil
	}
	return ctx.getMessaging()
}

// Config implements JobContext
func (ctx *jobContextImpl) Config() *config.Config {
	return ctx.config
}
