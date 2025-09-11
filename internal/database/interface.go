package database

import (
	"context"
	"database/sql"
)

// Interface defines the common database operations supported by the framework
type Interface interface {
	// Query execution
	Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row
	Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error)

	// Prepared statements
	Prepare(ctx context.Context, query string) (*sql.Stmt, error)

	// Transaction support
	Begin(ctx context.Context) (*sql.Tx, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

	// Health and diagnostics
	Health(ctx context.Context) error
	Stats() (map[string]interface{}, error)

	// Connection management
	Close() error

	// Database-specific features
	DatabaseType() string

	// Migration support
	GetMigrationTable() string
	CreateMigrationTable(ctx context.Context) error
}
