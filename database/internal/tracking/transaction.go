package tracking

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/database/internal/rowtracker"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// Transaction wraps types.Tx to provide performance tracking for database transactions.
// It intercepts all transaction operations and logs performance metrics,
// slow queries, and errors using structured logging.
type Transaction struct {
	tx       types.Tx
	logger   logger.Logger
	vendor   string
	settings Settings
	tc       *Context // cached context for tracking
}

// NewTransaction creates a Transaction wrapper that tracks performance for database transactions.
// NewTransaction creates a Transaction wrapper around the provided tx that records execution
// metrics for all transaction operations. The wrapper delegates calls to the given tx while
// capturing timing and error information, stores the provided logger, vendor, and settings,
// and initializes an internal Context used for tracking.
func NewTransaction(tx types.Tx, log logger.Logger, vendor string, settings Settings) types.Tx {
	t := &Transaction{
		tx:       tx,
		logger:   log,
		vendor:   vendor,
		settings: settings,
	}
	t.tc = &Context{
		Logger:   t.logger,
		Vendor:   t.vendor,
		Settings: t.settings,
	}
	return t
}

// Compile-time check
var _ types.Tx = (*Transaction)(nil)

// Query executes a query within a transaction with performance tracking
func (tx *Transaction) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tx.tx.Query(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, 0, err) // Read operations don't have rows affected

	return rows, err
}

// QueryRow executes a single row query within a transaction with performance tracking

func (tx *Transaction) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	start := time.Now()
	row := tx.tx.QueryRow(ctx, query, args...)

	return rowtracker.Wrap(row, func(err error) {
		tx.trackTx(ctx, query, args, start, 0, err) // Read operations don't have rows affected
	})
}

// Exec executes a query within a transaction without returning rows with performance tracking
func (tx *Transaction) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tx.tx.Exec(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, extractRowsAffected(result, err), err)

	return result, err
}

// Prepare prepares a statement within a transaction with performance tracking
func (tx *Transaction) Prepare(ctx context.Context, query string) (types.Statement, error) {
	start := time.Now()
	stmt, err := tx.tx.Prepare(ctx, query)

	// Track performance metrics
	tx.trackTx(ctx, "TX_PREPARE: "+query, nil, start, 0, err) // Prepare doesn't affect rows

	if err != nil {
		return nil, err
	}

	return NewStatement(stmt, tx.logger, tx.vendor, query, tx.settings), nil
}

// Commit commits the transaction
func (tx *Transaction) Commit(ctx context.Context) error {
	start := time.Now()
	err := tx.tx.Commit(ctx)

	// Track performance metrics
	tx.trackTx(ctx, "TX_COMMIT", nil, start, 0, err) // COMMIT doesn't affect rows

	// Return the original error
	return err
}

// Rollback rolls back the transaction
func (tx *Transaction) Rollback(ctx context.Context) error {
	start := time.Now()
	err := tx.tx.Rollback(ctx)

	// Track performance metrics
	tx.trackTx(ctx, "TX_ROLLBACK", nil, start, 0, err) // ROLLBACK doesn't affect rows

	// Return the original error
	return err
}

// trackTx tracks transaction operation performance
func (tx *Transaction) trackTx(ctx context.Context, query string, args []any, start time.Time, rowsAffected int64, err error) {
	TrackDBOperation(ctx, tx.tc, query, args, start, rowsAffected, err)
}
