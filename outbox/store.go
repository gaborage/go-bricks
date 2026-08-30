package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// maxTableNameLen bounds the outbox table's own segment so every identifier the store
// DERIVES from it stays distinct under PostgreSQL's 63-byte truncation. The longest
// decoration wins: "idx_" + segment + "_published" (14 bytes) beats the leader table's
// "_leader" (7). Without the bound a 63-byte name collapses onto its own companion —
// CreateTable would skip creating the leader table and aim the seed at the ledger — and a
// 50-to-56-byte name truncates the two index names into each other. Applied for both
// vendors because PostgreSQL's limit is the binding one; Oracle allows 128.
const maxTableNameLen = 63 - len(longestDerivedPrefix) - len(longestDerivedSuffix)

const (
	leaderSuffix         = "_leader"
	longestDerivedPrefix = "idx_"
	longestDerivedSuffix = "_published"
)

// validateTableName checks that name is a safe SQL identifier, delegating to the
// shared sqlid validator and wrapping its error with the outbox package prefix.
// Supports optional schema-qualified names (e.g., "myschema.outbox_events").
func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	// Measure the table segment only: a schema prefix is a separate identifier and does
	// not spend the table's byte budget.
	if segment := sqlid.IndexBaseName(name); len(segment) > maxTableNameLen {
		return fmt.Errorf("outbox: table name segment %q is %d bytes; the maximum is %d so the derived %q%s%s index and %q%s companion table stay distinct identifiers under PostgreSQL's 63-byte truncation",
			segment, len(segment), maxTableNameLen,
			longestDerivedPrefix, "<name>", longestDerivedSuffix, "<name>", leaderSuffix)
	}
	return nil
}

// Record represents a single row in the outbox table.
// Records are created by Publisher.Publish() and consumed by the relay job.
type Record struct {
	ID           string     // UUID, generated on insert
	EventType    string     // Event type for routing
	AggregateID  string     // Aggregate identifier for correlation
	Payload      []byte     // Event payload; JSON-encoded unless the caller supplied []byte, which is stored as-is
	Headers      []byte     // JSON-encoded AMQP headers (nullable)
	Exchange     string     // Target AMQP exchange
	RoutingKey   string     // AMQP routing key
	Lane         string     // LaneAMQP or LaneStream; the store fills an empty lane with LaneAMQP
	Stream       string     // Stream-lane target super stream (empty on the AMQP lane)
	PartitionKey string     // Stream-lane partition key: the row's tenant stamp
	Seq          int64      // Per-ledger sequence assigned by the database at insert; zero before insert, never written by Insert
	Status       string     // "pending", "published", or "failed"
	RetryCount   int        // Number of failed publish attempts so far (not incremented on eventual success)
	Error        string     // Last recorded failure message; NOT cleared on a later successful publish
	CreatedAt    time.Time  // When the event was created
	PublishedAt  *time.Time // When the event was successfully published (nil if pending)
}

// Lane constants name the transport a row is dispatched on.
const (
	LaneAMQP   = "amqp"
	LaneStream = "stream"
)

// leadership holds the transaction whose row lock IS the claim: the lock lives for
// the transaction's lifetime, so Release is a rollback (there is nothing to commit)
// and Probe is a trivial statement that fails once the transaction is gone.
type leadership struct {
	tx        dbtypes.Tx
	probeStmt string
}

func (l *leadership) Probe(ctx context.Context) error {
	_, err := l.tx.Exec(ctx, l.probeStmt)
	return err
}

func (l *leadership) Release(ctx context.Context) error {
	return l.tx.Rollback(ctx)
}

// leadRow is the whole of Lead except the two things that genuinely differ by vendor: the
// word in its errors and the statement Probe uses. The claim query is common SQL, not a
// dialect difference, so it lives here rather than being mirrored under the stores'
// file-level dupl exemption, which exists for real dialect divergence.
func leadRow(ctx context.Context, db dbtypes.Interface, leaderTable, vendor, probeStmt string) (Leadership, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("outbox %s: begin leader transaction failed: %w", vendor, err)
	}

	var id int64
	query := fmt.Sprintf(`SELECT id FROM %s WHERE id = 1 FOR UPDATE NOWAIT`, leaderTable)
	if err := tx.QueryRow(ctx, query).Scan(&id); err != nil {
		_ = tx.Rollback(ctx)
		switch {
		case database.IsLockNotAvailable(err):
			return nil, ErrNotLeader
		case errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("outbox %s: leader row missing in %s; run the documented migration", vendor, leaderTable)
		default:
			return nil, fmt.Errorf("outbox %s: take leader row failed: %w", vendor, err)
		}
	}

	return &leadership{tx: tx, probeStmt: probeStmt}, nil
}

// laneOrDefault fills an unset lane with LaneAMQP, so no persisted row carries an
// empty lane even when a caller hand-builds a Record.
func laneOrDefault(lane string) string {
	if lane == "" {
		return LaneAMQP
	}
	return lane
}

// Event status constants.
const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusFailed    = "failed"
)

// Leadership is a held claim on a ledger's leader row. Probe reports whether the
// claim still stands; Release gives it up.
type Leadership interface {
	// Probe fails once the claim is gone (statement timeout, recycled connection,
	// partition). The caller must stop draining on the first failed probe.
	Probe(ctx context.Context) error

	// Release gives up the claim. It is safe to defer.
	Release(ctx context.Context) error
}

// Store abstracts outbox table operations for vendor-agnostic SQL.
// Implementations exist for PostgreSQL and Oracle with vendor-specific
// placeholder styles and DDL.
type Store interface {
	// Insert writes an event row to the outbox table within the given transaction.
	Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error

	// FetchPending retrieves up to batchSize pending events in ledger sequence order.
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
	// status to "failed" — a terminal state the relay stops retrying. Used ONLY for
	// poison events (undecodable headers, or a destination the AMQP frame can never carry)
	// that exhaust MaxRetries. Connectivity failures
	// (broker down, NACK, confirmation timeout) must never call this — they advance
	// retry_count via MarkFailed and keep retrying indefinitely.
	MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error

	// DeletePublished removes events that were published before the given time.
	// Returns the number of rows deleted.
	DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)

	// Lead takes the ledger's leader row FOR UPDATE NOWAIT in a transaction it holds
	// until Release. ErrNotLeader when another instance holds it. Probe fails once the
	// transaction is gone (timeout, recycled connection, partition), and the caller
	// must stop draining on the first failed probe.
	Lead(ctx context.Context, db dbtypes.Interface) (Leadership, error)

	// CreateTable creates the outbox table, its indexes, and the companion leader
	// table with its single row, if they do not exist.
	// Used for auto-migration when outbox.autocreatetable is true.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}
