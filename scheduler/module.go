package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"github.com/go-co-op/gocron/v2"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// SchedulerModule implements the GoBricks Module interface for job scheduling.
// It provides lazy initialization per FR-016: scheduler created only when first job is registered.
//
// Example usage:
//
//	func (m *MyModule) Init(deps *app.ModuleDeps) error {
//	    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
//	}
//
//nolint:revive // Intentional stutter for clarity - follows GoBricks naming pattern (e.g., server.Server)
type SchedulerModule struct {
	// GoBricks dependencies
	logger        logger.Logger
	config        *config.Config
	tracer        trace.Tracer
	meterProvider metric.MeterProvider
	getDB         func(context.Context) (types.Interface, error)
	getMessaging  func(context.Context) (messaging.Client, error)

	// Scheduler state
	scheduler gocron.Scheduler // Lazy-initialized on first job registration
	jobs      map[string]*jobEntry
	mu        sync.RWMutex // Protects scheduler and jobs map

	// Shutdown coordination
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	wg             sync.WaitGroup // Tracks in-flight job executions
}

// NewSchedulerModule creates a new SchedulerModule instance.
// Per FR-016: The scheduler itself is lazy-initialized on first job registration.
func NewSchedulerModule() *SchedulerModule {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	return &SchedulerModule{
		jobs:           make(map[string]*jobEntry),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}
}

// Name implements app.Module
func (m *SchedulerModule) Name() string {
	return "scheduler"
}

// Init implements app.Module
// Stores dependencies and makes the module available as a JobRegistrar via deps.
func (m *SchedulerModule) Init(deps *app.ModuleDeps) error {
	m.logger = deps.Logger
	m.config = deps.Config
	m.tracer = deps.Tracer
	m.meterProvider = deps.MeterProvider

	// Store multi-tenant resource resolvers
	m.getDB = deps.GetDB
	m.getMessaging = func(ctx context.Context) (messaging.Client, error) {
		amqpClient, err := deps.GetMessaging(ctx)
		if err != nil {
			return nil, err
		}
		// Convert AMQPClient to generic Client interface
		return amqpClient, nil
	}

	// Note: Scheduler itself is lazy-initialized in ensureSchedulerInitialized()
	// per FR-016 (optional jobs, zero overhead)

	m.logger.Info().Msg("Scheduler module initialized (scheduler will start on first job registration)")

	return nil
}

// RegisterRoutes implements app.Module
// Registers system API routes for job listing and manual triggering
func (m *SchedulerModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Skip route registration if parameters are nil (e.g., in tests)
	if hr == nil || r == nil {
		return
	}

	// Create CIDR middleware for /_sys/job endpoints
	var allowlist []string
	if m.config != nil {
		allowlist = m.config.Scheduler.CIDRAllowlist
	}
	cidrMiddleware := CIDRMiddleware(allowlist)

	// Create a group for system endpoints with CIDR protection
	sysGroup := r.Group("/_sys")
	sysGroup.Use(cidrMiddleware)

	// Register routes
	server.GET(hr, sysGroup, "/job", m.listJobsHandler)
	server.POST(hr, sysGroup, "/job/:jobId", m.triggerJobHandler)
}

// DeclareMessaging implements app.Module
// Scheduler does not declare any messaging exchanges/queues
func (m *SchedulerModule) DeclareMessaging(_ *messaging.Declarations) {
	// No-op: scheduler doesn't use messaging for infrastructure
}

// Shutdown implements app.Module
// Gracefully shuts down the scheduler per FR-013, FR-014, FR-015.
func (m *SchedulerModule) Shutdown() error {
	m.mu.Lock()
	if m.scheduler == nil {
		m.mu.Unlock()
		m.logger.Info().Msg("Scheduler not initialized, nothing to shut down")
		return nil
	}

	scheduler := m.scheduler
	m.mu.Unlock()

	m.logger.Info().Msg("Initiating graceful scheduler shutdown")

	// Signal shutdown to all job wrappers
	m.shutdownCancel()

	// Get shutdown timeout from config (default 30s per ASSUME-010)
	timeout := 30 * time.Second
	if m.config != nil && m.config.Scheduler.ShutdownTimeout > 0 {
		timeout = m.config.Scheduler.ShutdownTimeout
	}

	// Stop scheduler (prevents new job triggers)
	if err := scheduler.Shutdown(); err != nil {
		m.logger.Error().Err(err).Msg("Error stopping scheduler")
		return fmt.Errorf("scheduler: shutdown failed: %w", err)
	}

	// Wait for in-flight jobs to complete with timeout
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info().Msg("All in-flight jobs completed successfully")
		return nil
	case <-time.After(timeout):
		m.logger.Warn().
			Dur("timeout", timeout).
			Msg("Shutdown timeout reached, some jobs may not have completed")
		return fmt.Errorf("scheduler: shutdown timeout after %v", timeout)
	}
}

// ensureSchedulerInitialized creates the gocron scheduler on first job registration.
// Per FR-016: Lazy initialization for zero overhead when no jobs are registered.
// Must be called with m.mu write lock held.
func (m *SchedulerModule) ensureSchedulerInitialized() error {
	if m.scheduler != nil {
		return nil // Already initialized
	}

	// Create gocron scheduler
	s, err := gocron.NewScheduler()
	if err != nil {
		return fmt.Errorf("scheduler: failed to create gocron scheduler: %w", err)
	}

	m.scheduler = s

	// Start the scheduler
	m.scheduler.Start()

	m.logger.Info().Msg("Scheduler initialized and started")

	return nil
}

// JobRegistrar interface implementation

// FixedRate implements JobRegistrar per FR-003
func (m *SchedulerModule) FixedRate(jobID string, job Job, interval time.Duration) error {
	// Validate parameters per FR-023
	if interval <= 0 {
		return &ValidationError{
			Field:   "interval",
			Message: "must be positive. Choose a duration greater than 0.",
		}
	}

	return m.registerJob(jobID, job, ScheduleConfiguration{
		Type:     ScheduleTypeFixedRate,
		Interval: interval,
	})
}

// DailyAt implements JobRegistrar per FR-004
func (m *SchedulerModule) DailyAt(jobID string, job Job, localTime time.Time) error {
	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, job, ScheduleConfiguration{
		Type:   ScheduleTypeDaily,
		Hour:   hour,
		Minute: minute,
	})
}

// WeeklyAt implements JobRegistrar per FR-005
func (m *SchedulerModule) WeeklyAt(jobID string, job Job, dayOfWeek time.Weekday, localTime time.Time) error {
	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, job, ScheduleConfiguration{
		Type:      ScheduleTypeWeekly,
		Hour:      hour,
		Minute:    minute,
		DayOfWeek: dayOfWeek,
	})
}

// HourlyAt implements JobRegistrar per FR-006
func (m *SchedulerModule) HourlyAt(jobID string, job Job, minute int) error {
	// Validate parameters per FR-023
	if minute < 0 || minute > 59 {
		return &ValidationError{
			Field:   "minute",
			Message: "must be 0-59. Choose a valid minute value.",
		}
	}

	return m.registerJob(jobID, job, ScheduleConfiguration{
		Type:   ScheduleTypeHourly,
		Minute: minute,
	})
}

// MonthlyAt implements JobRegistrar per FR-007
func (m *SchedulerModule) MonthlyAt(jobID string, job Job, dayOfMonth int, localTime time.Time) error {
	// Validate parameters per FR-023
	if dayOfMonth < 1 || dayOfMonth > 31 {
		return &ValidationError{
			Field:   "day",
			Message: "must be 1-31. Choose a valid day of the month.",
		}
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, job, ScheduleConfiguration{
		Type:       ScheduleTypeMonthly,
		Hour:       hour,
		Minute:     minute,
		DayOfMonth: dayOfMonth,
	})
}

// registerJob is the internal method that handles job registration and scheduler setup.
// Per FR-022: Validates unique job IDs.
// Per FR-016: Lazy-initializes scheduler on first job registration.
func (m *SchedulerModule) registerJob(jobID string, job Job, schedule ScheduleConfiguration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate unique job ID per FR-022
	if _, exists := m.jobs[jobID]; exists {
		return &ValidationError{
			Field:   "jobID",
			Message: fmt.Sprintf("'%s' already registered. Choose a unique identifier.", jobID),
		}
	}

	// Lazy-initialize scheduler per FR-016
	if err := m.ensureSchedulerInitialized(); err != nil {
		return err
	}

	// Create job entry
	entry := &jobEntry{
		job:      job,
		schedule: schedule,
		metadata: &JobMetadata{
			JobID:        jobID,
			ScheduleType: string(schedule.Type),
			// CronExpression and HumanReadable will be populated after scheduling
		},
	}

	// Schedule the job with gocron
	gocronJob, err := m.scheduleWithGocron(entry)
	if err != nil {
		return fmt.Errorf("scheduler: failed to schedule job '%s': %w", jobID, err)
	}

	entry.gocronJob = gocronJob

	// Store job entry
	m.jobs[jobID] = entry

	m.logger.Info().
		Str("jobID", jobID).
		Str("scheduleType", string(schedule.Type)).
		Msg("Job registered successfully")

	return nil
}

// scheduleWithGocron creates a gocron job based on the schedule configuration.
// Must be called with m.mu lock held.
func (m *SchedulerModule) scheduleWithGocron(entry *jobEntry) (gocron.Job, error) {
	// Create job wrapper that will be executed by gocron
	jobFunc := m.createJobWrapper(entry)

	var gocronJob gocron.Job
	var err error

	switch entry.schedule.Type {
	case ScheduleTypeFixedRate:
		gocronJob, err = m.scheduler.NewJob(
			gocron.DurationJob(entry.schedule.Interval),
			gocron.NewTask(jobFunc),
		)

	case ScheduleTypeDaily:
		gocronJob, err = m.scheduler.NewJob(
			gocron.DailyJob(1, gocron.NewAtTimes(
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0), //nolint:gosec // G115: Hour (0-23) and Minute (0-59) are bounded, no overflow possible
			)),
			gocron.NewTask(jobFunc),
		)

	case ScheduleTypeWeekly:
		gocronJob, err = m.scheduler.NewJob(
			gocron.WeeklyJob(1, gocron.NewWeekdays(entry.schedule.DayOfWeek), gocron.NewAtTimes(
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0), //nolint:gosec // G115: Hour (0-23) and Minute (0-59) are bounded, no overflow possible
			)),
			gocron.NewTask(jobFunc),
		)

	case ScheduleTypeHourly:
		gocronJob, err = m.scheduler.NewJob(
			gocron.DurationJob(time.Hour),
			gocron.NewTask(jobFunc),
		)

	case ScheduleTypeMonthly:
		gocronJob, err = m.scheduler.NewJob(
			gocron.MonthlyJob(1, gocron.NewDaysOfTheMonth(entry.schedule.DayOfMonth), gocron.NewAtTimes(
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0), //nolint:gosec // G115: Hour (0-23) and Minute (0-59) are bounded, no overflow possible
			)),
			gocron.NewTask(jobFunc),
		)

	default:
		return nil, fmt.Errorf("unknown schedule type: %s", entry.schedule.Type)
	}

	return gocronJob, err
}

// createJobWrapper creates the execution wrapper for a job.
// This wrapper handles:
// - JobContext creation with multi-tenant resource resolution
// - Overlapping execution prevention per FR-026, FR-027
// - Panic recovery per FR-021
// - Observability (traces, metrics, logs) per FR-017-FR-020
// - Graceful shutdown handling per FR-024
func (m *SchedulerModule) createJobWrapper(entry *jobEntry) func() {
	return func() {
		// Check for shutdown
		select {
		case <-m.shutdownCtx.Done():
			m.logger.Warn().
				Str("jobID", entry.metadata.JobID).
				Msg("Job trigger skipped - scheduler is shutting down")
			return
		default:
		}

		// Overlapping execution prevention per FR-026
		if !entry.tryLock() {
			m.logger.Warn().
				Str("jobID", entry.metadata.JobID).
				Str("triggerType", "scheduled").
				Msg("Job trigger skipped - job is already running")
			entry.metadata.incrementSkipped()
			return
		}

		// Track in-flight execution for graceful shutdown
		m.wg.Add(1)

		// Ensure cleanup happens
		defer func() {
			entry.unlock()
			m.wg.Done()
		}()

		// Create execution context with cancellation for graceful shutdown
		ctx, cancel := context.WithCancel(m.shutdownCtx)
		defer cancel()

		// Create JobContext with multi-tenant resolvers
		jobCtx := newJobContext(
			ctx,
			entry.metadata.JobID,
			"scheduled",
			m.logger,
			func() types.Interface {
				db, err := m.getDB(ctx)
				if err != nil {
					m.logger.Error().Err(err).Msg("Failed to get DB for job execution")
					return nil
				}
				return db
			},
			func() messaging.Client {
				msg, err := m.getMessaging(ctx)
				if err != nil {
					m.logger.Error().Err(err).Msg("Failed to get Messaging for job execution")
					return nil
				}
				return msg
			},
			m.config,
		)

		// Execute job with panic recovery per FR-021
		m.executeJob(entry, jobCtx)
	}
}

// executeJob executes the job with panic recovery and metadata updates.
// Per FR-021: Recover panics, log with stack trace, mark as failed.
func (m *SchedulerModule) executeJob(entry *jobEntry, ctx JobContext) {
	start := time.Now()

	// Panic recovery per FR-021
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error().
				Str("jobID", entry.metadata.JobID).
				Interface("panic", r).
				Msg("Job panicked - recovered and marked as failed")
			entry.metadata.incrementFailed()
		}
	}()

	// Execute the job
	err := entry.job.Execute(ctx)

	duration := time.Since(start)

	// Update metadata based on result
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("jobID", entry.metadata.JobID).
			Dur("duration", duration).
			Msg("Job execution failed")
		entry.metadata.incrementFailed()
	} else {
		m.logger.Info().
			Str("jobID", entry.metadata.JobID).
			Dur("duration", duration).
			Msg("Job execution completed successfully")
		entry.metadata.incrementSuccess()
	}
}
