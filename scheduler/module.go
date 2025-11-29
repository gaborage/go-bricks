package scheduler

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
	"github.com/go-co-op/gocron/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// OpenTelemetry attribute names for job scheduler observability.
// These follow OpenTelemetry naming conventions (https://opentelemetry.io/docs/specs/semconv/general/naming/):
// - Lowercase with dot-separated namespacing (e.g., job.id, job.status)
// - Snake_case for multi-word components (e.g., schedule_type)
//
// Note: As of 2025, OpenTelemetry has no official semantic conventions for scheduled/batch jobs.
// These naming choices follow general OTel guidelines and community patterns for custom metrics.
const (
	jobIDAttr           = "job.id"
	jobStatusAttr       = "job.status"
	jobScheduleTypeAttr = "job.schedule_type"

	// Error message for job type validation
	errJobInterfaceMsg = "must implement scheduler.Job interface, got %T"
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

	// OpenTelemetry instruments (pre-created for performance)
	executionCounter  metric.Int64Counter
	durationHistogram metric.Float64Histogram
	panicCounter      metric.Int64Counter

	// Scheduler state
	scheduler gocron.Scheduler // Lazy-initialized on first job registration
	jobs      map[string]*jobEntry
	mu        sync.RWMutex // Protects scheduler and jobs map

	// Shutdown coordination
	//nolint:S8242 // NOSONAR: Lifecycle context for graceful shutdown coordination - NOT request context (standard Go service pattern)
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

	// Initialize OpenTelemetry instruments once (performance optimization)
	if m.meterProvider != nil {
		meter := m.meterProvider.Meter("scheduler")

		// Create execution counter
		//nolint:S8148 // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
		m.executionCounter, _ = meter.Int64Counter(
			"job.execution.total",
			metric.WithDescription("Total number of job executions by status"),
		)

		// Create duration histogram
		//nolint:S8148 // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
		m.durationHistogram, _ = meter.Float64Histogram(
			"job.execution.duration",
			metric.WithDescription("Job execution duration in seconds"),
			metric.WithUnit("s"),
		)

		// Create panic counter
		//nolint:S8148 // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
		m.panicCounter, _ = meter.Int64Counter(
			"job.panic.total",
			metric.WithDescription("Total number of job panics"),
		)
	}

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
	var trustedProxies []string
	if m.config != nil {
		allowlist = m.config.Scheduler.Security.CIDRAllowlist
		trustedProxies = m.config.Scheduler.Security.TrustedProxies
	}
	cidrMiddleware := CIDRMiddleware(allowlist, trustedProxies)

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
	// Signal shutdown to all job wrappers
	m.shutdownCancel()

	// If scheduler not initialized, nothing to do
	if m.scheduler == nil {
		m.mu.Unlock()
		m.logger.Info().Msg("Scheduler not initialized, nothing to shut down")
		return nil
	}

	scheduler := m.scheduler
	m.mu.Unlock()

	m.logger.Info().Msg("Initiating graceful scheduler shutdown")

	// Get shutdown timeout from config (default 30s per ASSUME-010)
	timeout := 30 * time.Second
	if m.config != nil && m.config.Scheduler.Timeout.Shutdown > 0 {
		timeout = m.config.Scheduler.Timeout.Shutdown
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
func (m *SchedulerModule) FixedRate(jobID string, job any, interval time.Duration) error {
	// Validate job implements scheduler.Job interface
	schedulerJob, ok := job.(Job)
	if !ok {
		return &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}

	// Validate parameters per FR-023
	if interval <= 0 {
		return &ValidationError{
			Field:   "interval",
			Message: "must be positive. Choose a duration greater than 0.",
		}
	}

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:     ScheduleTypeFixedRate,
		Interval: interval,
	})
}

// DailyAt implements JobRegistrar per FR-004
func (m *SchedulerModule) DailyAt(jobID string, job any, localTime time.Time) error {
	// Validate job implements scheduler.Job interface
	schedulerJob, ok := job.(Job)
	if !ok {
		return &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:   ScheduleTypeDaily,
		Hour:   hour,
		Minute: minute,
	})
}

// WeeklyAt implements JobRegistrar per FR-005
func (m *SchedulerModule) WeeklyAt(jobID string, job any, dayOfWeek time.Weekday, localTime time.Time) error {
	// Validate job implements scheduler.Job interface
	schedulerJob, ok := job.(Job)
	if !ok {
		return &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:      ScheduleTypeWeekly,
		Hour:      hour,
		Minute:    minute,
		DayOfWeek: dayOfWeek,
	})
}

// HourlyAt implements JobRegistrar per FR-006
func (m *SchedulerModule) HourlyAt(jobID string, job any, minute int) error {
	// Validate job implements scheduler.Job interface
	schedulerJob, ok := job.(Job)
	if !ok {
		return &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}

	// Validate parameters per FR-023
	if minute < 0 || minute > 59 {
		return &ValidationError{
			Field:   "minute",
			Message: "must be 0-59. Choose a valid minute value.",
		}
	}

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:   ScheduleTypeHourly,
		Minute: minute,
	})
}

// MonthlyAt implements JobRegistrar per FR-007
func (m *SchedulerModule) MonthlyAt(jobID string, job any, dayOfMonth int, localTime time.Time) error {
	// Validate job implements scheduler.Job interface
	schedulerJob, ok := job.(Job)
	if !ok {
		return &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}

	// Validate parameters per FR-023
	if dayOfMonth < 1 || dayOfMonth > 31 {
		return &ValidationError{
			Field:   "day",
			Message: "must be 1-31. Choose a valid day of the month.",
		}
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
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

	// Check if shutdown has been initiated
	select {
	case <-m.shutdownCtx.Done():
		return fmt.Errorf("scheduler: cannot register job '%s' - scheduler is shutting down", jobID)
	default:
	}

	// Validate unique job ID per FR-022
	if _, exists := m.jobs[jobID]; exists {
		return &ValidationError{
			Field:   "jobID",
			Message: fmt.Sprintf("'%s' already registered. Choose a unique identifier.", jobID),
		}
	}

	// Validate schedule configuration (defensive check)
	// This provides defense in depth - public methods validate at API boundary,
	// and this validates before scheduling with gocron
	if err := schedule.Validate(time.Now()); err != nil {
		return err // Already a ValidationError from schedule.go
	}

	// Lazy-initialize scheduler per FR-016
	if err := m.ensureSchedulerInitialized(); err != nil {
		return err
	}

	// Create job entry with complete metadata
	entry := &jobEntry{
		job:      job,
		schedule: schedule,
		metadata: &JobMetadata{
			JobID:          jobID,
			ScheduleType:   string(schedule.Type),
			CronExpression: schedule.ToCronExpression(),
			HumanReadable:  schedule.ToHumanReadable(),
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
			gocron.CronJob(fmt.Sprintf("%d * * * *", entry.schedule.Minute), false),
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

// executeJob executes the job with panic recovery, metadata updates, and observability instrumentation.
// Per FR-021: Recover panics, log with stack trace, mark as failed.
// Per FR-017 to FR-020: Create spans, record metrics, propagate trace context.
func (m *SchedulerModule) executeJob(entry *jobEntry, ctx JobContext) {
	// Create OpenTelemetry span for job execution (FR-017) if tracer is configured
	var span trace.Span
	if m.tracer != nil {
		_, span = m.tracer.Start(
			ctx,
			"job.execute",
			trace.WithAttributes(
				attribute.String(jobIDAttr, entry.metadata.JobID),
				attribute.String("job.trigger", ctx.TriggerType()),
			),
		)
		defer span.End()
	}

	start := time.Now()
	var executionStatus string

	// Panic recovery per FR-021
	defer func() {
		duration := time.Since(start)

		if r := recover(); r != nil {
			executionStatus = "panic"

			// Log panic with stack trace
			m.logger.Error().
				Str("jobID", entry.metadata.JobID).
				Interface("panic", r).
				Str("stackTrace", string(debug.Stack())).
				Msg("Job panicked - recovered and marked as failed")

			// Record panic in span (if span exists)
			if span != nil {
				span.RecordError(fmt.Errorf("panic: %v", r))
				span.SetStatus(codes.Error, "panic")
				span.SetAttributes(attribute.String(jobStatusAttr, "panic"))
			}

			// Update metadata
			entry.metadata.incrementFailed()

			// Record metrics
			m.recordMetrics(entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)
		}
	}()

	// Execute the job with the original JobContext (span is already linked via context)
	err := entry.job.Execute(ctx)

	duration := time.Since(start)

	// Emit structured action log with operational counters and correlation
	m.logJobResultSummary(ctx, entry.metadata.JobID, entry.metadata.ScheduleType, ctx.TriggerType(), duration, err, span)

	// Update metadata and record observability based on result
	if err != nil {
		executionStatus = "failure"

		// Record error in span (if span exists)
		if span != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(attribute.String(jobStatusAttr, "failure"))
		}

		entry.metadata.incrementFailed()
		m.recordMetrics(entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)
	} else {
		executionStatus = "success"

		// Record success in span (if span exists)
		if span != nil {
			span.SetStatus(codes.Ok, "completed")
			span.SetAttributes(attribute.String(jobStatusAttr, "success"))
		}

		entry.metadata.incrementSuccess()
		m.recordMetrics(entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)
	}
}

// recordMetrics records OpenTelemetry metrics for job execution.
// Per FR-018: Emit metrics for execution count, success/failure counts, and duration.
// Includes schedule_type attribute for analysis by scheduling pattern.
//
// Metric naming follows OpenTelemetry conventions (https://opentelemetry.io/docs/specs/semconv/general/metrics/):
// - job.execution.total (Counter) - Singular "execution" for consistency
// - job.execution.duration (Histogram) - Duration in seconds (UCUM unit "s")
// - job.panic.total (Counter) - Singular "panic" for consistency
//
// Attributes: job.id, job.status (success/failure/panic), job.schedule_type (fixed_rate/daily/weekly/hourly/monthly)
func (m *SchedulerModule) recordMetrics(jobID, status, scheduleType string, duration time.Duration) {
	// Record job execution counter
	if m.executionCounter != nil {
		m.executionCounter.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobStatusAttr, status),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}

	// Record job execution duration histogram
	if m.durationHistogram != nil {
		m.durationHistogram.Record(context.Background(), duration.Seconds(),
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobStatusAttr, status),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}

	// Record panic counter if status is panic
	if status == "panic" && m.panicCounter != nil {
		m.panicCounter.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}
}

// logJobResultSummary emits a structured action log for job execution with OpenTelemetry conventions.
// Similar to HTTP request logging, this provides 100% sampling of job executions with operational counters.
func (m *SchedulerModule) logJobResultSummary(
	ctx JobContext,
	jobID, scheduleType, trigger string,
	duration time.Duration,
	err error,
	span trace.Span,
) {
	contextLog := m.logger.WithContext(ctx)

	// Determine severity and result_code
	logLevel, resultCode := m.determineJobSeverity(duration, err)

	// Create log event
	event := createJobLogEvent(contextLog, logLevel)
	if err != nil {
		event = event.Err(err)
	}

	// Tenant context
	//nolint:S8148 // NOSONAR: Error intentionally ignored - empty tenant ID is valid fallback for single-tenant apps
	if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" {
		event = event.Str("tenant", tenantID)
	}

	// Operational counters
	dbCount := logger.GetDBCounter(ctx)
	dbElapsed := logger.GetDBElapsed(ctx)
	amqpCount := logger.GetAMQPCounter(ctx)
	amqpElapsed := logger.GetAMQPElapsed(ctx)

	// Trace correlation
	traceID := ""
	traceparent := ""
	if span != nil {
		traceID = span.SpanContext().TraceID().String()
		traceparent = fmt.Sprintf("00-%s-%s-01",
			span.SpanContext().TraceID().String(),
			span.SpanContext().SpanID().String())
	}

	// Emit structured action log
	event.
		Str("log.type", "action").
		Str("job.id", jobID).
		Str("job.schedule_type", scheduleType).
		Str("job.trigger", trigger).
		Int64("job.execution.duration", duration.Nanoseconds()).
		Str("job.status", jobStatusFromError(err)).
		Str("result_code", resultCode).
		Str("correlation_id", traceID).
		Str("traceparent", traceparent).
		Int64("db_queries", dbCount).
		Int64("db_elapsed", dbElapsed).
		Int64("amqp_published", amqpCount).
		Int64("amqp_elapsed", amqpElapsed).
		Msg(createJobMessage(jobID, duration, err))
}

// determineJobSeverity calculates log severity and result_code based on execution result and duration.
func (m *SchedulerModule) determineJobSeverity(duration time.Duration, err error) (logLevel, resultCode string) {
	// ERROR: Job failed
	if err != nil {
		return "error", "ERROR"
	}

	// WARN: Slow job (succeeded but exceeded threshold)
	threshold := 30 * time.Second // default
	if m.config != nil && m.config.Scheduler.Timeout.SlowJob > 0 {
		threshold = m.config.Scheduler.Timeout.SlowJob
	}
	if threshold > 0 && duration > threshold {
		return "warn", "WARN"
	}

	// INFO: Normal successful job
	return "info", "INFO"
}

// createJobLogEvent creates a log event with the specified severity.
func createJobLogEvent(log logger.Logger, level string) logger.LogEvent {
	switch level {
	case "error":
		return log.Error()
	case "warn":
		return log.Warn()
	default:
		return log.Info()
	}
}

// jobStatusFromError returns "success" or "failure" based on error.
func jobStatusFromError(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}

// createJobMessage generates a human-readable message for job logs.
func createJobMessage(jobID string, duration time.Duration, err error) string {
	if err != nil {
		return fmt.Sprintf("Job '%s' failed after %s", jobID, duration)
	}
	return fmt.Sprintf("Job '%s' completed successfully in %s", jobID, duration)
}
