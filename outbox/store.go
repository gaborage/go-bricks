package outbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// validateTableName checks that name is a safe SQL identifier, delegating to the
// shared sqlid validator and wrapping its error with the outbox package prefix.
// Supports optional schema-qualified names (e.g., "myschema.outbox_events").
func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}

// indexBaseName returns the unqualified table name (the last dot-separated
// segment), used to derive index names. An index name cannot itself be
// schema-qualified, so a schema-qualified table like "myschema.outbox_events"
// must derive its index names from "outbox_events" while the index still targets
// the fully-qualified table in its ON clause.
func indexBaseName(tableName string) string {
	if i := strings.LastIndex(tableName, "."); i >= 0 {
		return tableName[i+1:]
	}
	return tableName
}

// Record represents a single row in the outbox table.
// Records are created by Publisher.Publish() and consumed by the relay job.
type Record struct {
	ID          string     // UUID, generated on insert
	EventType   string     // Event type for routing
	AggregateID string     // Aggregate identifier for correlation
	Payload     []byte     // JSON-encoded event payload
	Headers     []byte     // JSON-encoded AMQP headers (nullable)
	Exchange    string     // Target AMQP exchange
	RoutingKey  string     // AMQP routing key
	Status      string     // "pending", "published", or "failed"
	RetryCount  int        // Number of publish attempts
	Error       string     // Last error message (empty on success)
	CreatedAt   time.Time  // When the event was created
	PublishedAt *time.Time // When the event was successfully published (nil if pending)
}

// Event status constants.
const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusFailed    = "failed"
)

// Store abstracts outbox table operations for vendor-agnostic SQL.
// Implementations exist for PostgreSQL and Oracle with vendor-specific
// placeholder styles and DDL.
type Store interface {
	// Insert writes an event row to the outbox table within the given transaction.
	Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error

	// FetchPending retrieves up to batchSize unpublished events ordered by creation time.
	// Events with retry count exceeding maxRetries are skipped.
	FetchPending(ctx context.Context, db dbtypes.Interface, batchSize, maxRetries int) ([]Record, error)

	// MarkPublished updates the event status to published with a timestamp.
	MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error

	// MarkFailed increments retry count and records the error.
	MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error

	// DeletePublished removes events that were published before the given time.
	// Returns the number of rows deleted.
	DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)

	// CreateTable creates the outbox table if it does not exist.
	// Used for auto-migration when outbox.auto_create_table is true.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}
