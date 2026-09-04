package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/observability"
	"github.com/gaborage/go-bricks/server"
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
	jobPanicTypeAttr    = "job.panic_type"
	jobScheduleTypeAttr = "job.schedule_type"

	// Error message for job type validation
	errJobInterfaceMsg = "must implement scheduler.Executor interface, got %T"
)

// Module implements the GoBricks app.Module interface for job scheduling.
// It provides lazy initialization per FR-016: scheduler created only when first job is registered.
//
// Example usage:
//
//	func (m *MyModule) Init(deps *app.ModuleDeps) error {
//	    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, scheduler.ParseTime("03:00"))
//	}
type Module struct {
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
	location  *time.Location   // Loaded once in Init; nil means the "-" host-local sentinel
	scheduler gocron.Scheduler // Lazy-initialized on first job registration
	jobs      map[string]*jobEntry
	mu        sync.RWMutex // Protects scheduler and jobs map

	// Shutdown coordination
	shutdownCtx    context.Context // NOSONAR: Lifecycle context for graceful shutdown coordination - NOT request context (standard Go service pattern)
	shutdownCancel context.CancelFunc
	wg             sync.WaitGroup // Tracks in-flight job executions
}

// NewModule creates a new Module instance.
// Per FR-016: The scheduler itself is lazy-initialized on first job registration.
func NewModule() *Module {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	return &Module{
		jobs:           make(map[string]*jobEntry),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}
}

// Name implements app.Module
func (m *Module) Name() string {
	return "scheduler"
}

// Init implements app.Module
// Stores framework dependencies and multi-tenant resource resolvers used for job execution.
// (The module registry, not Init, wires this Module into deps.Scheduler after Init returns,
// because Module implements JobRegistrar; see app/module_registry.go.)
func (m *Module) Init(deps *app.ModuleDeps) error {
	// The module reads scheduler.timeout.* straight from config, which only
	// carries its defaults after config.Validate. A caller that assembles
	// ModuleDeps itself (bypassing app construction) fails here rather than at
	// shutdown, where a zero shutdown budget abandons in-flight jobs to the
	// teardown of the resources they are still using.
	if deps.Config == nil {
		return errors.New("scheduler: deps.Config is required")
	}
	if deps.Config.Scheduler.Timeout.Shutdown <= 0 {
		return errors.New("scheduler: scheduler.timeout.shutdown must be positive; run the config through config.Validate")
	}
	if deps.Config.Scheduler.Timeout.SlowJob <= 0 {
		return errors.New("scheduler: scheduler.timeout.slowjob must be positive; run the config through config.Validate")
	}
	location, err := loadSchedulerLocation(deps.Config.Scheduler.Timezone)
	if err != nil {
		return err
	}
	m.location = location

	m.logger = deps.Logger
	m.config = deps.Config
	m.tracer = deps.Tracer
	m.meterProvider = deps.MeterProvider

	// Initialize OpenTelemetry instruments once (performance optimization)
	if m.meterProvider != nil {
		meter := m.meterProvider.Meter("scheduler")

		m.executionCounter, _ = meter.Int64Counter( // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
			"job.execution.total",
			metric.WithDescription("Total number of job executions by status"),
		)

		m.durationHistogram, _ = meter.Float64Histogram( // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
			"job.execution.duration",
			metric.WithDescription("Job execution duration in seconds"),
			metric.WithUnit("s"),
		)

		m.panicCounter, _ = meter.Int64Counter( // NOSONAR: OTel meter errors intentionally ignored - nil meter results in no-op operations
			"job.panic.total",
			metric.WithDescription("Total number of job panics"),
		)
	}

	// Store multi-tenant resource resolvers
	m.getDB = deps.DB
	m.getMessaging = func(ctx context.Context) (messaging.Client, error) {
		amqpClient, err := deps.Messaging(ctx)
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
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Skip route registration if parameters are nil. Load-bearing rather than
	// cosmetic: it returns before the config reads below, so a module that never
	// ran Init can still be called this way (as tests do).
	if hr == nil || r == nil {
		return
	}

	// Create CIDR middleware for /_sys/job endpoints
	// Named rather than inlined: both are []string, so a swapped pair compiles and
	// would widen /_sys/job* to the proxy ranges.
	allowlist := m.config.Scheduler.Security.CIDRAllowlist
	trustedProxies := m.config.Scheduler.Security.TrustedProxies
	cidrMiddleware := CIDRMiddleware(m.logger, allowlist, trustedProxies)

	// Create a group for system endpoints with CIDR protection
	sysGroup := r.Group("/_sys")
	sysGroup.Use(cidrMiddleware)

	server.GET(hr, sysGroup, "/job", m.listJobsHandler)
	server.POST(hr, sysGroup, "/job/:jobId", m.triggerJobHandler)
}

// Shutdown implements app.Module
// Gracefully shuts down the scheduler per FR-013, FR-014, FR-015.
func (m *Module) Shutdown() error {
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
	m.scheduler = nil
	m.mu.Unlock()

	m.logger.Info().Msg("Initiating graceful scheduler shutdown")

	// scheduler.timeout.shutdown, normalized by config.Validate: positive on
	// every config that reached a module.
	timeout := m.config.Scheduler.Timeout.Shutdown

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
func (m *Module) ensureSchedulerInitialized() error {
	if m.scheduler != nil {
		return nil // Already initialized
	}

	s, err := gocron.NewScheduler(m.schedulerLocationOptions()...)
	if err != nil {
		return fmt.Errorf("scheduler: failed to create gocron scheduler: %w", err)
	}

	m.scheduler = s

	m.scheduler.Start()

	m.logger.Info().
		Str("timezone", m.timezoneLabel()).
		Msg("Scheduler initialized and started")

	return nil
}

// loadSchedulerLocation resolves scheduler.timezone once, at Init. An empty
// value means the config never passed config.Validate (which defaults it to
// UTC), so it is refused like a zero timeout. The "-" sentinel yields nil so
// gocron keeps its time.Local default (host-local). Any other value must load
// via time.LoadLocation. The literal "Local" is refused by config.Validate
// (ADR-093), not here: a config handed straight to Init with "Local" loads
// host-local, an accepted gap for callers that skip validation (#1315).
func loadSchedulerLocation(tz string) (*time.Location, error) {
	if tz == "" {
		return nil, errors.New("scheduler: scheduler.timezone must be set; run the config through config.Validate")
	}
	if tz == config.TimezoneDisabledSentinel {
		return nil, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("scheduler: invalid timezone %q: %w", tz, err)
	}
	return loc, nil
}

// schedulerLocationOptions translates the Init-loaded location into gocron
// scheduler options. nil (the "-" sentinel) yields no option; otherwise
// gocron.WithLocation threads the zone into every wall-clock schedule type
// (daily/weekly/monthly via at-times, hourly via a CRON_TZ prefix). FixedRate is
// interval-based and unaffected.
func (m *Module) schedulerLocationOptions() []gocron.SchedulerOption {
	if m.location == nil {
		return nil
	}
	return []gocron.SchedulerOption{gocron.WithLocation(m.location)}
}

// timezoneLabel returns an operator-facing label for the effective scheduler
// timezone, used in the startup log and the /_sys/job response. The "-" sentinel
// returns "host-local" rather than time.Local.String(), which is just "Local" in
// Go and tells operators nothing useful about the host's actual zone.
func (m *Module) timezoneLabel() string {
	if m.location == nil {
		return "host-local"
	}
	return m.location.String()
}

// JobRegistrar interface implementation

// validateExecutor checks that job implements the Executor interface.
func validateExecutor(job any) (Executor, error) {
	executor, ok := job.(Executor)
	if !ok {
		return nil, &ValidationError{
			Field:   "job",
			Message: fmt.Sprintf(errJobInterfaceMsg, job),
		}
	}
	return executor, nil
}

// FixedRate implements JobRegistrar per FR-003
func (m *Module) FixedRate(jobID string, job any, interval time.Duration) error {
	schedulerJob, err := validateExecutor(job)
	if err != nil {
		return err
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

// DailyAt implements JobRegistrar per FR-004. localTime is interpreted in the
// scheduler's configured timezone (scheduler.timezone, default UTC).
func (m *Module) DailyAt(jobID string, job any, localTime time.Time) error {
	schedulerJob, err := validateExecutor(job)
	if err != nil {
		return err
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:   ScheduleTypeDaily,
		Hour:   hour,
		Minute: minute,
	})
}

// WeeklyAt implements JobRegistrar per FR-005. localTime is interpreted in the
// scheduler's configured timezone (default UTC).
func (m *Module) WeeklyAt(jobID string, job any, dayOfWeek time.Weekday, localTime time.Time) error {
	schedulerJob, err := validateExecutor(job)
	if err != nil {
		return err
	}

	hour, minute, _ := localTime.Clock()

	return m.registerJob(jobID, schedulerJob, ScheduleConfiguration{
		Type:      ScheduleTypeWeekly,
		Hour:      hour,
		Minute:    minute,
		DayOfWeek: dayOfWeek,
	})
}

// HourlyAt implements JobRegistrar per FR-006. The minute is taken within the
// scheduler's configured timezone (matters only for sub-hour-offset zones).
func (m *Module) HourlyAt(jobID string, job any, minute int) error {
	schedulerJob, err := validateExecutor(job)
	if err != nil {
		return err
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

// MonthlyAt implements JobRegistrar per FR-007. localTime is interpreted in the
// scheduler's configured timezone (default UTC).
func (m *Module) MonthlyAt(jobID string, job any, dayOfMonth int, localTime time.Time) error {
	schedulerJob, err := validateExecutor(job)
	if err != nil {
		return err
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
func (m *Module) registerJob(jobID string, job Executor, schedule ScheduleConfiguration) error {
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

	gocronJob, err := m.scheduleWithGocron(entry)
	if err != nil {
		return fmt.Errorf("scheduler: failed to schedule job '%s': %w", jobID, err)
	}

	entry.gocronJob = gocronJob

	m.jobs[jobID] = entry

	m.logger.Info().
		Str("jobID", jobID).
		Str("scheduleType", string(schedule.Type)).
		Msg("Job registered successfully")

	return nil
}

// scheduleWithGocron creates a gocron job based on the schedule configuration.
// Must be called with m.mu lock held.
func (m *Module) scheduleWithGocron(entry *jobEntry) (gocron.Job, error) {
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
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0),
			)),
			gocron.NewTask(jobFunc),
		)

	case ScheduleTypeWeekly:
		gocronJob, err = m.scheduler.NewJob(
			gocron.WeeklyJob(1, gocron.NewWeekdays(entry.schedule.DayOfWeek), gocron.NewAtTimes(
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0),
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
				gocron.NewAtTime(uint(entry.schedule.Hour), uint(entry.schedule.Minute), 0),
			)),
			gocron.NewTask(jobFunc),
		)

	default:
		return nil, fmt.Errorf("unknown schedule type: %s", entry.schedule.Type)
	}

	return gocronJob, err
}

// registerManualTrigger atomically checks for shutdown and registers one in-flight
// manual job under m.mu. Returns false (WITHOUT calling wg.Add) when the scheduler
// is shutting down. Because Shutdown() also cancels under m.mu, the wg.Add(1) here
// provably happens-before Shutdown's wg.Wait(): either Wait observes the Add and
// waits for the job's Done, or the check sees the cancellation and no Add occurs.
//
// CONTRACT: a true return MUST be paired with exactly one wg.Done() — supplied by
// the spawned executeManualJob — and the caller MUST NOT return or panic between
// this call and `go m.executeManualJob(entry)`, or the Add would leak and hang
// Shutdown's Wait.
func (m *Module) registerManualTrigger() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.shutdownCtx.Done():
		return false
	default:
	}
	m.wg.Add(1)
	return true
}

// createJobWrapper creates the execution wrapper for a job.
// This wrapper handles:
// - JobContext creation with multi-tenant resource resolution
// - Overlapping execution prevention per FR-026, FR-027
// - Panic recovery per FR-021
// - Observability (traces, metrics, logs) per FR-017-FR-020
// - Graceful shutdown handling per FR-024
func (m *Module) createJobWrapper(entry *jobEntry) func() {
	return func() {
		m.wg.Add(1)
		defer m.wg.Done()
		m.runJobBody(entry, "scheduled")
	}
}

// runJobBody is the shared execution body for both the scheduled wrapper
// (createJobWrapper) and the manual trigger (executeManualJob). The CALLER owns
// the in-flight WaitGroup registration — scheduled: the gocron-invoked closure;
// manual: registerManualTrigger + executeManualJob's deferred Done — so runJobBody
// itself does NOT touch m.wg and is safe to call directly (e.g. from tests).
// triggerType ("scheduled"/"manual") distinguishes them in logs and the JobContext.
// The post-entry shutdown re-check still bails before tryLock/execute so a job
// registered just before shutdown never runs against tearing-down managers.
func (m *Module) runJobBody(entry *jobEntry, triggerType string) {
	// Check for shutdown.
	select {
	case <-m.shutdownCtx.Done():
		m.logger.Warn().
			Str("jobID", entry.metadata.JobID).
			Str("triggerType", triggerType).
			Msg("Job trigger skipped - scheduler is shutting down")
		return
	default:
	}

	// Overlapping execution prevention per FR-026.
	if !entry.tryLock() {
		m.logger.Warn().
			Str("jobID", entry.metadata.JobID).
			Str("triggerType", triggerType).
			Msg("Job trigger skipped - job is already running")
		entry.metadata.incrementSkipped()
		return
	}
	// Release the per-job lock when the job body returns.
	defer entry.unlock()

	// Create execution context with cancellation for graceful shutdown.
	ctx, cancel := context.WithCancel(m.shutdownCtx)
	defer cancel()

	// Install the per-job lease scope (ADR-032): per-tenant handles the job borrows via
	// JobContext.DB()/Messaging() — including the per-tenant fan-out in outbox relay and
	// inbox cleanup, whose SetTenant children inherit this scope — are released when the
	// job run completes, so a handle evicted mid-job is not closed under it.
	ctx, scope := leasescope.Install(ctx)
	defer scope.ReleaseAll()

	// Create JobContext with multi-tenant resolvers.
	jobCtx := newJobContext(
		ctx,
		entry.metadata.JobID,
		triggerType,
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

	// Execute job with panic recovery per FR-021.
	m.executeJob(entry, jobCtx)
}

// executeJob executes the job with panic recovery, metadata updates, and observability instrumentation.
// Per FR-021: Recover panics, log with stack trace, mark as failed.
// Per FR-017 to FR-020: Create spans, record metrics, propagate trace context.
// The parameter is the concrete *jobContextImpl, not the exported JobContext: the
// tracing path needs withContext, and a foreign JobContext implementation would
// otherwise panic inside the very function whose job is to contain panics.
func (m *Module) executeJob(entry *jobEntry, ctx *jobContextImpl) {
	// Create OpenTelemetry span for job execution (FR-017) if tracer is configured.
	// Propagate the traced context so child spans and WithContext(ctx) logs
	// nest under this "job.execute" span.
	var span trace.Span
	if m.tracer != nil {
		var tracedCtx context.Context
		tracedCtx, span = m.tracer.Start(
			ctx,
			"job.execute",
			trace.WithAttributes(
				attribute.String(jobIDAttr, entry.metadata.JobID),
				attribute.String("job.trigger", ctx.TriggerType()),
			),
		)
		defer span.End()
		ctx = ctx.withContext(tracedCtx)
	}

	start := time.Now()
	var executionStatus string

	// Panic recovery per FR-021
	defer func() {
		duration := time.Since(start)

		if r := recover(); r != nil {
			executionStatus = "panic"
			// SECURITY: the panic value's TYPE only. panicErr reaches two sinks the
			// filter does not touch, and the worse one leaves the platform:
			// the span sink ships it to the tracing backend as an exception
			// event, with that vendor's retention, access model and export path.
			// The summary line's Err() is the second, and the guarded report below is
			// the third — running it through the sensitive-data filter is NOT
			// protection, because that filter masks by FIELD NAME and the field is
			// `panic`, which is no needle. A bare `panic("secret")` has no inner
			// field name to match, and a map key the needle list does not name is
			// emitted in clear. So every one of the three reports names the TYPE.
			// Same rule, and the same reason, as httpclient's Do recovery.
			panicType := fmt.Sprintf("%T", r)
			panicErr := fmt.Errorf("panic (type: %s)", panicType)

			// The logger is consumer-supplied and its write can panic; `%T` cannot.
			// This defer has already spent its recover(), so guard the log call
			// alone — otherwise that panic escapes and skips the accounting below,
			// leaving the job counted as neither success nor failure.
			func() {
				defer func() { _ = recover() }()
				m.logger.WithContext(ctx).Error().
					Str("jobID", entry.metadata.JobID).
					Str("panic_type", panicType).
					Str("stackTrace", string(debug.Stack())).
					Msg("Job panicked - recovered and marked as failed")
			}()

			// Record panic in span (if span exists)
			if span != nil {
				observability.RecordErrorByType(span, panicErr)
				// panicErr is framework-built, so its own Go type says nothing;
				// panicType is the datum, and it is already %T-rendered.
				span.SetStatus(codes.Error, "panic")
				span.SetAttributes(
					attribute.String(jobStatusAttr, "panic"),
					attribute.String(jobPanicTypeAttr, panicType),
				)
			}

			entry.metadata.incrementFailed()

			m.recordMetrics(ctx, entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)

			// Emit the structured action log so the panic path also gets the
			// advertised 100% job-execution sampling (the normal-return call
			// below never runs when Execute panics).
			m.logJobResultSummary(ctx, entry.metadata.JobID, entry.metadata.ScheduleType, ctx.TriggerType(), duration, panicErr, span)
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
			// SECURITY: a consumer job's error may carry anything it read — type
			// only on both span sinks; the summary line keeps the message (ADR-083).
			observability.RecordErrorByType(span, err)
			span.SetAttributes(attribute.String(jobStatusAttr, "failure"))
		}

		entry.metadata.incrementFailed()
		m.recordMetrics(ctx, entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)
	} else {
		executionStatus = "success"

		// Record success in span (if span exists)
		if span != nil {
			span.SetStatus(codes.Ok, "completed")
			span.SetAttributes(attribute.String(jobStatusAttr, "success"))
		}

		entry.metadata.incrementSuccess()
		m.recordMetrics(ctx, entry.metadata.JobID, executionStatus, entry.metadata.ScheduleType, duration)
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
//
// ctx must be the traced job context: the SDK's default exemplar filter attaches
// an exemplar only when the recording context carries a sampled span.
func (m *Module) recordMetrics(ctx context.Context, jobID, status, scheduleType string, duration time.Duration) {
	if m.executionCounter != nil {
		m.executionCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobStatusAttr, status),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}

	if m.durationHistogram != nil {
		m.durationHistogram.Record(ctx, duration.Seconds(),
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobStatusAttr, status),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}

	if status == "panic" && m.panicCounter != nil {
		m.panicCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String(jobIDAttr, jobID),
				attribute.String(jobScheduleTypeAttr, scheduleType),
			),
		)
	}
}

// logJobResultSummary emits a structured action log for job execution with OpenTelemetry conventions.
// Similar to HTTP request logging, this provides 100% sampling of job executions with operational counters.
func (m *Module) logJobResultSummary(
	ctx JobContext,
	jobID, scheduleType, trigger string,
	duration time.Duration,
	err error,
	span trace.Span,
) {
	contextLog := m.logger.WithContext(ctx)

	logLevel, resultCode := m.determineJobSeverity(duration, err)

	event := createJobLogEvent(contextLog, logLevel)
	if err != nil {
		event = event.Err(err)
	}

	// Tenant context
	if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" { // NOSONAR: Error intentionally ignored - empty tenant ID is valid fallback for single-tenant apps
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
		Str(logger.FieldCorrelationID, traceID).
		Str("traceparent", traceparent).
		Int64("db_queries", dbCount).
		Int64("db_elapsed", dbElapsed).
		Int64("amqp_published", amqpCount).
		Int64("amqp_elapsed", amqpElapsed).
		Msg(createJobMessage(jobID, duration, err))
}

// determineJobSeverity calculates log severity and result_code based on execution result and duration.
func (m *Module) determineJobSeverity(duration time.Duration, err error) (logLevel, resultCode string) {
	// ERROR: Job failed
	if err != nil {
		return "error", "ERROR"
	}

	// WARN: Slow job (succeeded but exceeded scheduler.timeout.slowjob)
	if duration > m.config.Scheduler.Timeout.SlowJob {
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
