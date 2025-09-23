package fixtures

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/testing/mocks"
)

// DatabaseFixtures provides helper functions for creating pre-configured database mocks
// and SQL result builders for consistent testing.

// NewHealthyDatabase creates a mock database that responds positively to health checks
// and basic operations. This is useful for testing happy path scenarios.
func NewHealthyDatabase() *mocks.MockDatabase {
	mockDB := &mocks.MockDatabase{}

	// Setup healthy responses
	mockDB.ExpectHealthCheck(true)
	mockDB.ExpectDatabaseType("mock")
	mockDB.ExpectStats(map[string]any{
		"open_connections": 1,
		"in_use":           0,
		"idle":             1,
	}, nil)

	return mockDB
}

// NewFailingDatabase creates a mock database that fails health checks and operations.
// This is useful for testing error scenarios and failure handling.
func NewFailingDatabase(err error) *mocks.MockDatabase {
	if err == nil {
		err = sql.ErrConnDone
	}

	mockDB := &mocks.MockDatabase{}

	// Setup failing responses
	mockDB.ExpectHealthCheck(false)
	mockDB.On("Query", mock.Anything, mock.Anything, mock.Anything).Return((*sql.Rows)(nil), err)
	mockDB.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(nil, err)
	mockDB.On("Begin", mock.Anything).Return((*mocks.MockTx)(nil), err)

	return mockDB
}

// NewDatabaseWithData creates a mock database pre-configured with data responses.
// The data map keys are SQL queries (can be partial matches) and values are the rows to return.
//
// Example:
//
//	data := map[string][]interface{}{
//	  "SELECT * FROM users": {
//	    []interface{}{1, "John", "john@example.com"},
//	    []interface{}{2, "Jane", "jane@example.com"},
//	  },
//	}
//	mockDB := fixtures.NewDatabaseWithData(data)
func NewDatabaseWithData(data map[string][]interface{}) *mocks.MockDatabase {
	mockDB := NewHealthyDatabase()

	for query, rowData := range data {
		if len(rowData) == 0 {
			continue
		}
		// Assume first row defines column structure
		firstRow := rowData[0].([]interface{})
		columns := make([]string, len(firstRow))
		for i := range firstRow {
			columns[i] = "col" + string(rune('A'+i)) // Generate column names like "colA", "colB"
		}

		// Convert the data to the correct format
		rowsData := make([][]interface{}, len(rowData))
		for i, row := range rowData {
			rowsData[i] = row.([]interface{})
		}
		rows := NewMockRows(columns, rowsData)
		if rows.Err() != nil {
			panic(rows.Err()) // This should never happen in tests
		}
		mockDB.ExpectQuery(query, rows, nil)
	}

	return mockDB
}

// NewReadOnlyDatabase creates a mock database that only allows read operations.
// Write operations will fail with an appropriate error.
func NewReadOnlyDatabase() *mocks.MockDatabase {
	mockDB := NewHealthyDatabase()

	// Allow read operations
	mockDB.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&sql.Rows{}, nil)
	mockDB.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(&sql.Row{})

	// Fail write operations
	readOnlyErr := errors.New("database is read-only")
	mockDB.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(nil, readOnlyErr)
	mockDB.On("Begin", mock.Anything).Return((*mocks.MockTx)(nil), readOnlyErr)

	return mockDB
}

// SQL Result Builders

// NewMockRows creates sql.Rows for testing with the provided columns and data.
// This is useful when you need to return specific data from Query operations.
//
// Example:
//
//	rows := fixtures.NewMockRows(
//	  []string{"id", "name", "email"},
//	  [][]interface{}{
//	    {1, "John", "john@example.com"},
//	    {2, "Jane", "jane@example.com"},
//	  },
//	)
func NewMockRows(columns []string, rows [][]interface{}) *sql.Rows {
	// Create a mock database connection
	db, sqlMock, err := sqlmock.New()
	if err != nil {
		panic(err) // This should never happen in tests
	}
	defer db.Close()

	// Create the expected rows
	sqlRows := sqlmock.NewRows(columns)
	for _, row := range rows {
		// Convert to driver.Value slice
		driverValues := make([]driver.Value, len(row))
		for i, val := range row {
			driverValues[i] = val
		}
		sqlRows.AddRow(driverValues...)
	}

	// Setup the expectation
	sqlMock.ExpectQuery(".*").WillReturnRows(sqlRows)

	// Execute the query to get the actual rows
	result, err := db.QueryContext(context.Background(), "SELECT")
	if err != nil {
		panic(err) // This should never happen in tests
	}
	return result
}

// NewMockResult creates sql.Result for testing Exec operations.
// This is useful when you need to simulate INSERT, UPDATE, or DELETE operations.
//
// Example:
//
//	result := fixtures.NewMockResult(1, 5) // lastInsertId=1, rowsAffected=5
func NewMockResult(lastInsertID, rowsAffected int64) sql.Result {
	return &mockResult{
		lastInsertID: lastInsertID,
		rowsAffected: rowsAffected,
	}
}

// NewErrorResult creates sql.Result that returns errors for testing error scenarios.
func NewErrorResult(err error) sql.Result {
	return &mockResult{
		err: err,
	}
}

// mockResult implements sql.Result for testing
type mockResult struct {
	lastInsertID int64
	rowsAffected int64
	err          error
}

func (r *mockResult) LastInsertId() (int64, error) {
	return r.lastInsertID, r.err
}

func (r *mockResult) RowsAffected() (int64, error) {
	return r.rowsAffected, r.err
}

// Transaction Helpers

// NewSuccessfulTransaction creates a mock transaction that commits successfully.
func NewSuccessfulTransaction() *mocks.MockTx {
	mockTx := &mocks.MockTx{}
	mockTx.ExpectSuccessfulTransaction()

	// Allow operations within the transaction
	mockTx.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&sql.Rows{}, nil)
	mockTx.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(NewMockResult(1, 1), nil)

	return mockTx
}

// NewFailedTransaction creates a mock transaction that fails on commit.
func NewFailedTransaction(commitErr error) *mocks.MockTx {
	if commitErr == nil {
		commitErr = errors.New("transaction commit failed")
	}

	mockTx := &mocks.MockTx{}
	mockTx.ExpectFailedTransaction(commitErr)

	// Allow operations within the transaction
	mockTx.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&sql.Rows{}, nil)
	mockTx.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(NewMockResult(1, 1), nil)

	return mockTx
}

// Database Type Helpers

// NewPostgreSQLDatabase creates a mock database that behaves like PostgreSQL.
func NewPostgreSQLDatabase() *mocks.MockDatabase {
	mockDB := &mocks.MockDatabase{}

	// Setup healthy responses for PostgreSQL
	mockDB.ExpectHealthCheck(true)
	mockDB.ExpectDatabaseType("postgres")
	mockDB.ExpectStats(map[string]any{
		"open_connections": 1,
		"in_use":           0,
		"idle":             1,
	}, nil)
	mockDB.On("GetMigrationTable").Return("schema_migrations")

	return mockDB
}

// NewOracleDatabase creates a mock database that behaves like Oracle.
func NewOracleDatabase() *mocks.MockDatabase {
	mockDB := &mocks.MockDatabase{}

	// Setup healthy responses for Oracle
	mockDB.ExpectHealthCheck(true)
	mockDB.ExpectDatabaseType("oracle")
	mockDB.ExpectStats(map[string]any{
		"open_connections": 1,
		"in_use":           0,
		"idle":             1,
	}, nil)
	mockDB.On("GetMigrationTable").Return("SCHEMA_MIGRATIONS")

	return mockDB
}

// NewMongoDatabase creates a mock database that behaves like MongoDB (via the adapter).
func NewMongoDatabase() *mocks.MockDatabase {
	mockDB := &mocks.MockDatabase{}

	// Setup healthy responses for MongoDB
	mockDB.ExpectHealthCheck(true)
	mockDB.ExpectDatabaseType("mongodb")
	mockDB.ExpectStats(map[string]any{
		"open_connections": 1,
		"in_use":           0,
		"idle":             1,
	}, nil)
	mockDB.On("GetMigrationTable").Return("schema_migrations")

	return mockDB
}
