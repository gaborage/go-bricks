// Package types contains the core database interface definitions for go-bricks.
//
//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"context"
	"database/sql"
)

// Querier defines the core query execution operations that represent 80% of typical database usage.
// This interface follows the Single Responsibility Principle by focusing solely on query execution,
// separate from transaction management, health checks, and migration support.
//
// Querier is designed for easy mocking in unit tests, requiring only 4 methods instead of the
// full 13 methods in the Interface type. Most business logic only needs query execution capabilities.
//
// The database.Interface type embeds Querier, so all existing code continues to work unchanged.
//
// Usage in tests:
//
//	// Simple mock implementation
//	type mockQuerier struct {
//	    queryFunc    func(ctx context.Context, query string, args ...any) (*sql.Rows, error)
//	    queryRowFunc func(ctx context.Context, query string, args ...any) Row
//	    execFunc     func(ctx context.Context, query string, args ...any) (sql.Result, error)
//	}
//
//	// Inject via ModuleDeps.GetDB
//	deps := &app.ModuleDeps{
//	    GetDB: func(ctx context.Context) (database.Interface, error) {
//	        return mockQuerier, nil
//	    },
//	}
//
// For comprehensive testing utilities, see the database/testing package which provides
// TestDB with fluent API for setting up query expectations and assertions.
type Querier interface {
	// Query executes a SQL query that returns rows, typically a SELECT statement.
	// The caller is responsible for closing the returned rows.
	//
	// The query should use vendor-specific placeholders:
	//   - PostgreSQL: $1, $2, $3
	//   - Oracle: :1, :2, :3
	//
	// For vendor-agnostic query construction, use the QueryBuilder.
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)

	// QueryRow executes a SQL query that is expected to return at most one row.
	// QueryRow always returns a non-nil value. Errors are deferred until Row's Scan method is called.
	//
	// For vendor-agnostic query construction, use the QueryBuilder.
	QueryRow(ctx context.Context, query string, args ...any) Row

	// Exec executes a SQL statement that doesn't return rows, typically INSERT, UPDATE, or DELETE.
	// The returned sql.Result provides RowsAffected and LastInsertId (if supported by the vendor).
	//
	// For vendor-agnostic query construction, use the QueryBuilder.
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// DatabaseType returns the vendor identifier for this database connection.
	// Valid values are defined as constants: PostgreSQL, Oracle.
	//
	// This is included in Querier (despite being metadata) because the query builder
	// requires vendor information for placeholder generation and identifier quoting.
	// Including it here prevents forcing all test mocks to implement additional interfaces.
	DatabaseType() string
}
