// Package database provides performance tracking for database operations
package database

import (
	"github.com/gaborage/go-bricks/database/internal/tracking"
)

// Re-export the internal tracking implementation as the public API
type (
	TrackedDB          = tracking.DB
	TrackedConnection  = tracking.Connection
	TrackingContext    = tracking.Context
	TrackedStatement   = tracking.Statement
	TrackedStmt        = tracking.Statement
	TrackedTransaction = tracking.Transaction
	TrackedTx          = tracking.Transaction
)

// Re-export internal functions as public API
var (
	NewTrackedDB                  = tracking.NewDB
	NewTrackedConnection          = tracking.NewConnection
	TrackDBOperation              = tracking.TrackDBOperation
	NewTrackingSettings           = tracking.NewSettings
	RegisterConnectionPoolMetrics = tracking.RegisterConnectionPoolMetrics

	// SetObservabilityEnabled gates DB-operation OpenTelemetry span/metric emission.
	// Called once at app bootstrap from the resolved observability.enabled value so
	// that, when observability is disabled, the tracking layer builds no span/metric
	// attributes (honoring the no-op provider's zero-overhead contract).
	SetObservabilityEnabled = tracking.SetObservabilityEnabled

	// WithRepositoryMethod records the business-operation (repository) method name
	// on ctx so the tracking layer emits it as the `repository.method` attribute on
	// the db.client.operation.duration metric. Pass the resulting context to the
	// database call:
	//
	//	ctx = database.WithRepositoryMethod(ctx, "GetCustomer")
	//	rows, err := db.Query(ctx, query, args...)
	//
	// The method name must be a static, low-cardinality identifier.
	WithRepositoryMethod = tracking.WithRepositoryMethod
	// RepositoryMethodFromContext returns the repository method name stored on ctx
	// by WithRepositoryMethod, and whether one was set.
	RepositoryMethodFromContext = tracking.RepositoryMethodFromContext
)

// Re-export internal constants
const (
	DefaultSlowQueryThreshold = tracking.DefaultSlowQueryThreshold
	DefaultMaxQueryLength     = tracking.DefaultMaxQueryLength
)
