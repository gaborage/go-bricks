package outbox

import (
	"context"
	"fmt"
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

	// FetchPending retrieves up to batchSize pending events ordered by creation time.
	// Selection is status-gated only: parking is driven by the "failed" status
	// (set by MarkDeadLettered), NOT by retry_count, so an outage-inflated count can
	// never freeze a healthy pending event.
	FetchPending(ctx context.Context, db dbtypes.Interface, batchSize int) ([]Record, error)

	// MarkPublished updates the event status to published with a timestamp.
	MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error

	// MarkFailed increments retry count and records the error, leaving the event
	// "pending" so the relay retries it on a later cycle.
	MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error

	// MarkDeadLettered increments retry count, records the error, and sets the event
	// status to "failed" — a terminal state the relay stops retrying. Used when a
	// poison event (broker-rejected or corrupt) exhausts MaxRetries.
	MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error

	// DeletePublished removes events that were published before the given time.
	// Returns the number of rows deleted.
	DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)

	// CreateTable creates the outbox table if it does not exist.
	// Used for auto-migration when outbox.autocreatetable is true.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}
