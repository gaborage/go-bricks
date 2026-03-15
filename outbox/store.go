package outbox

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// validIdentifierPattern matches safe SQL identifiers (letters, digits, underscore, $, #).
// Mirrors the pattern in database/internal/columns/parser.go for consistency.
var validIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`)

// validateTableName checks that name is a safe SQL identifier.
// Supports optional schema-qualified names (e.g., "myschema.outbox_events").
func validateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("outbox: table name must not be empty")
	}

	// Check for dangerous SQL fragments
	for _, dangerous := range []string{";", "--", "/*", "*/"} {
		if strings.Contains(name, dangerous) {
			return fmt.Errorf("outbox: table name %q contains dangerous SQL characters", name)
		}
	}

	// Split on "." for optional schema.table format
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return fmt.Errorf("outbox: table name %q has too many dot-separated parts (expected schema.table or table)", name)
	}

	for _, part := range parts {
		if !validIdentifierPattern.MatchString(part) {
			return fmt.Errorf("outbox: table name part %q contains invalid identifier characters", part)
		}
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
