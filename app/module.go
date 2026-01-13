package app

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Module defines the interface that all application modules must implement.
// It provides hooks for initialization, route registration, messaging setup, and cleanup.
type Module interface {
	Name() string
	Init(deps *ModuleDeps) error
	RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
	DeclareMessaging(decls *messaging.Declarations)
	Shutdown() error
}

// JobRegistrar defines the interface for scheduling jobs.
// This interface is defined here to avoid circular imports between app and scheduler packages.
// The scheduler package implements this interface via SchedulerModule.
type JobRegistrar interface {
	// FixedRate schedules a job to run every interval duration
	FixedRate(jobID string, job any, interval time.Duration) error

	// DailyAt schedules a job to run daily at the specified local time
	DailyAt(jobID string, job any, localTime time.Time) error

	// WeeklyAt schedules a job to run weekly on the specified day and time
	WeeklyAt(jobID string, job any, dayOfWeek time.Weekday, localTime time.Time) error

	// HourlyAt schedules a job to run hourly at the specified minute
	HourlyAt(jobID string, job any, minute int) error

	// MonthlyAt schedules a job to run monthly on the specified day and time
	MonthlyAt(jobID string, job any, dayOfMonth int, localTime time.Time) error
}

// JobProvider is an optional interface that modules can implement to register scheduled jobs.
// Modules implementing this interface will have RegisterJobs() called automatically after
// all module Init() methods have completed, making module registration order irrelevant.
//
// Example:
//
//	type JobsModule struct{}
//
//	func (m *JobsModule) RegisterJobs(scheduler JobRegistrar) error {
//	    scheduler.FixedRate("cleanup", &CleanupJob{}, 30*time.Minute)
//	    scheduler.DailyAt("report", &ReportJob{}, scheduler.ParseTime("03:00"))
//	    return nil
//	}
//
// The scheduler parameter is guaranteed to be non-nil when this method is called.
// If no SchedulerModule is registered, this method will not be called.
type JobProvider interface {
	RegisterJobs(JobRegistrar) error
}

// ModuleDeps contains the dependencies that are injected into each module.
// It provides access to core services like database, logging, messaging, observability, and job scheduling.
// All modules must use DB() and Messaging() functions for resource access.
type ModuleDeps struct {
	Logger logger.Logger
	Config *config.Config

	// Tracer provides distributed tracing capabilities.
	// Creates spans for tracking operations across services.
	// This is a no-op tracer if observability is disabled.
	Tracer trace.Tracer

	// MeterProvider provides metrics collection capabilities.
	// Use this to create custom meters for application-specific metrics.
	// This is a no-op provider if observability is disabled.
	MeterProvider metric.MeterProvider

	// Scheduler provides job scheduling capabilities.
	// Modules can register jobs using methods like FixedRate, DailyAt, WeeklyAt, etc.
	// This field is nil if no SchedulerModule is registered.
	// Example: deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, time.Date(0, 0, 0, 3, 0, 0, 0, time.Local))
	Scheduler JobRegistrar

	// DB returns a database interface for the current context.
	// In single-tenant mode, returns the global database instance.
	// In multi-tenant mode, resolves tenant from context and returns tenant-specific database.
	DB func(_ context.Context) (database.Interface, error)

	// DBByName returns a named database interface for explicit database selection.
	// Use this when working with multiple databases in single-tenant mode.
	// The name must match a key in the 'databases:' config section.
	// Example: db, err := deps.DBByName(ctx, "legacy") for databases.legacy config.
	// Named databases are shared across all tenants in multi-tenant mode.
	DBByName func(ctx context.Context, name string) (database.Interface, error)

	// Messaging returns a messaging client for the current context.
	// In single-tenant mode, returns the global messaging client.
	// In multi-tenant mode, resolves tenant from context and returns tenant-specific client.
	Messaging func(_ context.Context) (messaging.AMQPClient, error)

	// Cache returns a cache instance for the current context.
	// In single-tenant mode, returns the global cache instance.
	// In multi-tenant mode, resolves tenant from context and returns tenant-specific cache.
	Cache func(_ context.Context) (cache.Cache, error)
}
