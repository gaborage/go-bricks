package mongodb

import (
	"context"
	"database/sql"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// Transaction implements the database.Tx interface for MongoDB
type Transaction struct {
	session   *mongo.Session
	database  *mongo.Database
	logger    logger.Logger
	parentCtx context.Context //nolint:S8242 // NOSONAR: Context required by MongoDB Session API (CommitTransaction, AbortTransaction, EndSession)
}

// Query executes a query within the transaction (not applicable for MongoDB)
func (t *Transaction) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("SQL query operations not supported for MongoDB transactions")
}

// QueryRow executes a query returning a single row within the transaction (not applicable)
func (t *Transaction) QueryRow(_ context.Context, _ string, _ ...any) types.Row {
	// MongoDB doesn't support SQL queries, this is for interface compatibility
	return nil
}

// Exec executes a command within the transaction (not applicable for MongoDB)
func (t *Transaction) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, fmt.Errorf("SQL exec operations not supported for MongoDB transactions")
}

// Prepare creates a prepared statement within the transaction (not applicable)
func (t *Transaction) Prepare(_ context.Context, _ string) (database.Statement, error) {
	return nil, fmt.Errorf("prepared statements not supported for MongoDB transactions")
}

// Commit commits the MongoDB transaction
func (t *Transaction) Commit() error {
	// In v2, pass the context directly to transaction methods
	err := t.session.CommitTransaction(t.parentCtx)
	if err != nil {
		t.logger.Error().Err(err).Msg("Failed to commit MongoDB transaction")
		// Still need to end session even if commit fails
		t.session.EndSession(t.parentCtx)
		return fmt.Errorf("failed to commit MongoDB transaction: %w", err)
	}

	// End session after successful commit
	t.session.EndSession(t.parentCtx)
	t.logger.Debug().Msg("MongoDB transaction committed successfully")
	return nil
}

// Rollback rolls back the MongoDB transaction
func (t *Transaction) Rollback() error {
	// In v2, pass the context directly to transaction methods
	err := t.session.AbortTransaction(t.parentCtx)
	if err != nil {
		t.logger.Error().Err(err).Msg("Failed to rollback MongoDB transaction")
		// Continue with session cleanup even if abort fails
	}

	// Always end session, even if abort failed
	t.session.EndSession(t.parentCtx)
	if err != nil {
		return fmt.Errorf("failed to rollback MongoDB transaction: %w", err)
	}

	t.logger.Debug().Msg("MongoDB transaction rolled back successfully")
	return nil
}

// Session returns the underlying MongoDB session for document operations
func (t *Transaction) Session() *mongo.Session {
	return t.session
}

// Database returns the database instance for use within the transaction
func (t *Transaction) Database() *mongo.Database {
	return t.database
}
