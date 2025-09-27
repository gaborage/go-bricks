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
	NewTrackedDB         = tracking.NewDB
	NewTrackedConnection = tracking.NewConnection
	TrackDBOperation     = tracking.TrackDBOperation
	NewTrackingSettings  = tracking.NewSettings
)

// Re-export internal constants
const (
	DefaultSlowQueryThreshold = tracking.DefaultSlowQueryThreshold
	DefaultMaxQueryLength     = tracking.DefaultMaxQueryLength
)
