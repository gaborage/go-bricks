// Package types contains the core database interface definitions for go-bricks.
// These interfaces are separate from the main database package to avoid import cycles
// and to make them easily accessible for mocking and testing.
//
//nolint:revive // Package name "types" is intentionally generic to avoid circular
package types

import (
	"context"
	"database/sql"

	"github.com/Masterminds/squirrel"
)

// Database vendor identifiers shared across the database packages.
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
	MongoDB    = "mongodb"
)

// Statement defines the interface for prepared statements
type Statement interface {
	// Query execution
	Query(ctx context.Context, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, args ...any) *sql.Row
	Exec(ctx context.Context, args ...any) (sql.Result, error)

	// Statement management
	Close() error
}

// Tx defines the interface for database transactions
type Tx interface {
	// Query execution within transaction
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Prepared statements within transaction
	Prepare(ctx context.Context, query string) (Statement, error)

	// Transaction control
	Commit() error
	Rollback() error
}

// Interface defines the common database operations supported by the framework.
// This is the main interface that applications and modules should depend on
// for database operations, allowing for easy mocking and testing.
type Interface interface {
	// Query execution
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Prepared statements
	Prepare(ctx context.Context, query string) (Statement, error)

	// Transaction support
	Begin(ctx context.Context) (Tx, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error)

	// Health and diagnostics
	Health(ctx context.Context) error
	Stats() (map[string]any, error)

	// Connection management
	Close() error

	// Database-specific features
	DatabaseType() string

	// Migration support
	GetMigrationTable() string
	CreateMigrationTable(ctx context.Context) error
}

// QueryBuilderInterface defines the interface for vendor-specific SQL query building.
// This interface allows for dependency injection and mocking of query builders,
// enabling unit testing of business logic that constructs queries without
// actually generating SQL strings.
type QueryBuilderInterface interface {
	// Vendor information
	Vendor() string

	// Query builders
	Select(columns ...string) squirrel.SelectBuilder
	Insert(table string) squirrel.InsertBuilder
	InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder
	Update(table string) squirrel.UpdateBuilder
	Delete(table string) squirrel.DeleteBuilder

	// Vendor-specific helpers
	BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer
	BuildLimitOffset(query squirrel.SelectBuilder, limit, offset int) squirrel.SelectBuilder
	BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error)

	// Database function builders
	BuildCurrentTimestamp() string
	BuildUUIDGeneration() string
	BuildBooleanValue(value bool) any

	// Identifier escaping
	EscapeIdentifier(identifier string) string
}
