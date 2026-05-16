package app

import (
	"context"
	"crypto/rsa"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Module defines the core interface that all application modules must implement.
// It provides hooks for initialization and cleanup. Route registration and messaging
// declaration are optional — implement RouteRegisterer and/or MessagingDeclarer
// only if your module needs them.
type Module interface {
	Name() string
	Init(deps *ModuleDeps) error
	Shutdown() error
}

// RouteRegisterer is an optional interface that modules can implement to register HTTP routes.
// Modules that implement this interface will have RegisterRoutes called automatically
// during application startup.
type RouteRegisterer interface {
	RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
}

// MessagingDeclarer is an optional interface that modules can implement to declare
// AMQP exchanges, queues, bindings, publishers, and consumers.
// Modules that implement this interface will have DeclareMessaging called automatically
// during application startup.
type MessagingDeclarer interface {
	DeclareMessaging(decls *messaging.Declarations)
}

// JobRegistrar defines the interface for scheduling jobs.
// This interface is defined here to avoid circular imports between app and scheduler packages.
// The scheduler package implements this interface via its Module type.
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

// OutboxPublisher defines the interface for writing events to the transactional outbox.
// This interface is defined here to avoid circular imports between app and outbox packages.
// The outbox package implements this interface via its Module type.
//
// Events are written to the outbox table within the caller's database transaction,
// ensuring atomic consistency with business data. A background relay publishes
// them to the message broker after the transaction commits.
type OutboxPublisher interface {
	// Publish writes an event to the outbox table within the given transaction.
	// Returns the generated event ID (UUID) for correlation and idempotency.
	Publish(ctx context.Context, tx dbtypes.Tx, event *OutboxEvent) (string, error)
}

// OutboxEvent represents a domain event to be reliably published via the outbox pattern.
// This type is defined here to avoid circular imports. The outbox package provides
// additional utilities for working with events.
type OutboxEvent struct {
	// EventType identifies the kind of event (e.g., "order.created").
	EventType string

	// AggregateID identifies the entity this event relates to (e.g., "order-123").
	AggregateID string

	// Payload is the event data. If []byte, stored as-is. Otherwise, JSON-marshaled.
	Payload any

	// Headers are optional AMQP headers propagated to the published message.
	Headers map[string]any

	// Exchange is the target AMQP exchange. If empty, uses the default from outbox config.
	Exchange string

	// RoutingKey overrides the default routing key. If empty, uses EventType.
	RoutingKey string
}

// OutboxProvider is an optional interface that modules can implement to provide
// an OutboxPublisher for dependency injection into other modules.
// When a module implements this interface, the ModuleRegistry automatically
// wires its OutboxPublisher into ModuleDeps.Outbox.
//
// This follows the same pattern as JobRegistrar for the scheduler module.
type OutboxProvider interface {
	OutboxPublisher() OutboxPublisher
}

// KeyStore provides access to named RSA key pairs loaded at startup.
// Keys are loaded from DER files or base64-encoded values during module initialization.
// All methods are safe for concurrent use (the store is read-only after init).
// This interface is defined here to avoid circular imports between app and keystore packages.
type KeyStore interface {
	// PublicKey returns the parsed RSA public key for the given certificate name.
	// Returns an error if the name is not configured.
	PublicKey(name string) (*rsa.PublicKey, error)

	// PrivateKey returns the parsed RSA private key for the given certificate name.
	// Returns an error if the name is not configured or no private key was provided.
	PrivateKey(name string) (*rsa.PrivateKey, error)

	// Secret returns a defensive copy of the raw symmetric key material for the
	// given name (HMAC/CMAC key, HKDF input). The caller owns the returned slice
	// and may zeroize it after use. Returns an error if the name is not
	// configured or the entry holds an RSA pair rather than a secret.
	Secret(name string) ([]byte, error)
}

// KeyStoreProvider is an optional interface that modules can implement to provide
// a KeyStore for dependency injection into other modules.
// When a module implements this interface, the ModuleRegistry automatically
// wires its KeyStore into ModuleDeps.KeyStore.
type KeyStoreProvider interface {
	KeyStore() KeyStore
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
// If no scheduler module is registered, this method will not be called.
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
	// This field is nil if no scheduler module is registered.
	// Example: deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, time.Date(0, 0, 0, 3, 0, 0, 0, time.Local))
	Scheduler JobRegistrar

	// Outbox provides transactional event publishing.
	// Events are written to the outbox table atomically with business data,
	// then reliably delivered to the message broker by a background relay.
	// This field is nil if no outbox module is registered or outbox.enabled is false.
	// Example: deps.Outbox.Publish(ctx, tx, &app.OutboxEvent{EventType: "order.created", ...})
	Outbox OutboxPublisher

	// KeyStore provides access to named RSA key pairs for encryption/signing.
	// Keys are loaded at startup from DER files or base64-encoded values.
	// This field is nil if no KeyStoreModule is registered or no keys are configured.
	// Example: key, err := deps.KeyStore.PrivateKey("signing")
	KeyStore KeyStore

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
