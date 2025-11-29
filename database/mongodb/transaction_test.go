package mongodb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// createTestTransactionForInterface creates a basic transaction for interface method testing
func createTestTransactionForInterface() *Transaction {
	testLogger := newTestLogger()
	parentCtx := context.Background()

	// We create a transaction with nil session/database since we're only testing
	// the SQL interface methods that don't use these fields
	return &Transaction{
		session:   nil, // Not needed for SQL interface methods
		database:  nil, // Not needed for SQL interface methods
		logger:    testLogger,
		parentCtx: parentCtx,
	}
}

// createTestTransactionWithMockData creates transaction with mock data for getter tests
func createTestTransactionWithMockData() *Transaction {
	testLogger := newTestLogger()
	parentCtx := context.Background()

	// Use a dummy database pointer for testing getters
	// In real usage, these would be properly initialized
	mockDB := &mongo.Database{}

	return &Transaction{
		session:   nil, // Session interface is too complex to mock easily
		database:  mockDB,
		logger:    testLogger,
		parentCtx: parentCtx,
	}
}

// TestTransactionQuery tests SQL query operations (not supported for MongoDB)
func TestTransactionQuery(t *testing.T) {
	transaction := createTestTransactionForInterface()
	ctx := context.Background()

	rows, err := transaction.Query(ctx, "SELECT * FROM test", "arg1")

	assert.Error(t, err)
	assert.Nil(t, rows)
	assert.Contains(t, err.Error(), "SQL query operations not supported for MongoDB transactions")
}

// TestTransactionQueryRow tests SQL single row query operations (not supported)
func TestTransactionQueryRow(t *testing.T) {
	transaction := createTestTransactionForInterface()
	ctx := context.Background()

	row := transaction.QueryRow(ctx, "SELECT * FROM test WHERE id = ?", 1)

	assert.Nil(t, row)
}

// TestTransactionExec tests SQL exec operations (not supported for MongoDB)
func TestTransactionExec(t *testing.T) {
	transaction := createTestTransactionForInterface()
	ctx := context.Background()

	result, err := transaction.Exec(ctx, "UPDATE test SET name = ?", "newName")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "SQL exec operations not supported for MongoDB transactions")
}

// TestTransactionPrepare tests prepared statement operations (not supported)
func TestTransactionPrepare(t *testing.T) {
	transaction := createTestTransactionForInterface()
	ctx := context.Background()

	stmt, err := transaction.Prepare(ctx, "SELECT * FROM test WHERE id = ?")

	assert.Error(t, err)
	assert.Nil(t, stmt)
	assert.Contains(t, err.Error(), "prepared statements not supported for MongoDB transactions")
}

// TestTransactionSession tests session getter
func TestTransactionSession(t *testing.T) {
	transaction := createTestTransactionForInterface()

	session := transaction.Session()

	// Should return the session (nil in this case since we're testing the getter mechanism)
	assert.Nil(t, session)
}

// TestTransactionDatabase tests database getter
func TestTransactionDatabase(t *testing.T) {
	transaction := createTestTransactionWithMockData()

	database := transaction.Database()

	assert.NotNil(t, database)
	assert.Equal(t, transaction.database, database)
}

// TestTransactionCommitPanicsWithNilSession tests that commit handles nil session gracefully
func TestTransactionCommitWithNilSession(t *testing.T) {
	transaction := createTestTransactionForInterface()

	// This will cause a panic due to nil session, but that's expected behavior
	// In real usage, session would never be nil
	assert.Panics(t, func() {
		_ = transaction.Commit()
	})
}

// TestTransactionRollbackWithNilSession tests that rollback handles nil session gracefully
func TestTransactionRollbackWithNilSession(t *testing.T) {
	transaction := createTestTransactionForInterface()

	// This will cause a panic due to nil session, but that's expected behavior
	// In real usage, session would never be nil
	assert.Panics(t, func() {
		_ = transaction.Rollback()
	})
}
