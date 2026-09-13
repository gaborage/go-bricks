package tracking

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/database/types"
)

// Transaction wraps types.Tx to provide performance tracking for database transactions.
// It intercepts all transaction operations and logs performance metrics,
// slow queries, and errors using structured logging.
// Query/QueryRow/Exec come from the embedded stmtTracker, shared with Session.
// The tracking Context (stmtTracker.tc) is the single source of logger, vendor
// and settings — Prepare reads them through it rather than from copies.
type Transaction struct {
	stmtTracker
	tx types.Tx
}

// NewTransaction creates a Transaction wrapper around the provided tx that records execution
// metrics for all transaction operations. The wrapper delegates calls to the given tx while
// capturing timing and error information. tc is the caller's tracking Context — pass the
// BEGINNING connection's or session's context (see Connection.trackingContext) so transaction
// statements carry the same server.address / server.port / db.namespace attributes as the
// BEGIN span.
func NewTransaction(tx types.Tx, tc *Context) types.Tx {
	return &Transaction{
		stmtTracker: stmtTracker{q: tx, tc: tc},
		tx:          tx,
	}
}

// Compile-time check
var _ types.Tx = (*Transaction)(nil)

// Prepare prepares a statement within a transaction with performance tracking
func (tx *Transaction) Prepare(ctx context.Context, query string) (types.Statement, error) {
	start := time.Now()
	stmt, err := tx.tx.Prepare(ctx, query)

	tx.trackTx(ctx, "TX_PREPARE: "+query, nil, start, 0, err) // Prepare doesn't affect rows

	if err != nil {
		return nil, err
	}

	return NewStatement(stmt, tx.tc.Logger, tx.tc.Vendor, query, tx.tc.Settings), nil
}

// Commit commits the transaction
func (tx *Transaction) Commit(ctx context.Context) error {
	start := time.Now()
	err := tx.tx.Commit(ctx)

	tx.trackTx(ctx, "TX_COMMIT", nil, start, 0, err) // COMMIT doesn't affect rows

	return err
}

// Rollback rolls back the transaction
func (tx *Transaction) Rollback(ctx context.Context) error {
	start := time.Now()
	err := tx.tx.Rollback(ctx)

	tx.trackTx(ctx, "TX_ROLLBACK", nil, start, 0, err) // ROLLBACK doesn't affect rows

	return err
}

func (tx *Transaction) trackTx(ctx context.Context, query string, args []any, start time.Time, rowsAffected int64, err error) {
	TrackDBOperation(ctx, tx.tc, query, args, start, rowsAffected, err)
}
