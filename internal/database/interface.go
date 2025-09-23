package database

import (
	"context"
	"database/sql"
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

// Interface defines the common database operations supported by the framework
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
