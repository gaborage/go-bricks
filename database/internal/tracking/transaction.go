package tracking

import (
	"context"
	"database/sql"
	"time"

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
}

// NewTransaction creates a Transaction wrapper that tracks performance for database transactions.
// It wraps the provided transaction and records execution metrics for all operations.
func NewTransaction(tx types.Tx, log logger.Logger, vendor string, settings Settings) types.Tx {
	return &Transaction{
		tx:       tx,
		logger:   log,
		vendor:   vendor,
		settings: settings,
	}
}

// Query executes a query within a transaction with performance tracking
func (tx *Transaction) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tx.tx.Query(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, err)

	return rows, err
}

// QueryRow executes a single row query within a transaction with performance tracking
func (tx *Transaction) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := tx.tx.QueryRow(ctx, query, args...)

	// Track performance metrics (error will be checked when row is scanned)
	tx.trackTx(ctx, query, args, start, nil)

	return row
}

// Exec executes a query within a transaction without returning rows with performance tracking
func (tx *Transaction) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tx.tx.Exec(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, err)

	return result, err
}

// Prepare prepares a statement within a transaction with performance tracking
func (tx *Transaction) Prepare(ctx context.Context, query string) (types.Statement, error) {
	start := time.Now()
	stmt, err := tx.tx.Prepare(ctx, query)

	// Track performance metrics
	tx.trackTx(ctx, "TX_PREPARE: "+query, nil, start, err)

	if err != nil {
		return nil, err
	}

	return NewStatement(stmt, tx.logger, tx.vendor, query, tx.settings), nil
}

// Commit commits the transaction
func (tx *Transaction) Commit() error {
	return tx.tx.Commit()
}

// Rollback rolls back the transaction
func (tx *Transaction) Rollback() error {
	return tx.tx.Rollback()
}

// trackTx tracks transaction operation performance
func (tx *Transaction) trackTx(ctx context.Context, query string, args []any, start time.Time, err error) {
	tc := &Context{
		Logger:   tx.logger,
		Vendor:   tx.vendor,
		Settings: tx.settings,
	}
	TrackDBOperation(ctx, tc, query, args, start, err)
}
