// Package database provides performance tracking for database operations
package database

import (
	"github.com/gaborage/go-bricks/database/internal/tracking"
)

// Re-export the internal tracking implementation as the public API
type (
	TrackedConnection = tracking.Connection
)

// Re-export internal functions as public API
var (
	NewTrackedConnection = tracking.NewConnection

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

	// WithExpectedError declares errors matching expected as an anticipated outcome
	// of the database operations run with the returned context. Tracking then logs a
	// matching failure at DEBUG (not ERROR), leaves its span status Unset and does not
	// escalate request severity; the caller still receives the error unchanged.
	// Scope it to the single statement whose failure is expected:
	//
	//	lockCtx := database.WithExpectedError(ctx, database.IsLockNotAvailable)
	//	err := tx.QueryRow(lockCtx, "SELECT ... FOR UPDATE NOWAIT").Scan(&id)
	//
	// Do not declare it on blocking statements: a PostgreSQL 55P03 raised by an
	// expired lock_timeout is a real failure. Nested declarations compose — an error
	// is expected when either predicate matches.
	WithExpectedError = tracking.WithExpectedError
)
