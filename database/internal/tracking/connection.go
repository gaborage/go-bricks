package tracking

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/rowtracker"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// DB wraps sql.DB to provide request-scoped performance tracking.
// It intercepts all database operations and logs performance metrics,
// slow queries, and errors using structured logging.
type DB struct {
	*sql.DB
	logger   logger.Logger
	vendor   string
	settings Settings
}

// NewDB creates a DB wrapper that tracks performance for the provided *sql.DB.
// It records query and execution metrics (durations, truncated queries, optional parameter logging).
// Uses log for emitting structured logs, vendor to identify the database type, and
// NewDB returns a DB wrapper around the provided *sql.DB that records per-request
// performance metrics, slow queries, and errors using the provided logger.
// The wrapper is configured for the given vendor and derives per-connection
// tracking settings from cfg when provided.
func NewDB(db *sql.DB, log logger.Logger, vendor string, cfg *config.DatabaseConfig) *DB {
	return &DB{
		DB:       db,
		logger:   log,
		vendor:   vendor,
		settings: NewSettings(cfg),
	}
}

// QueryContext executes a query with context and tracks performance
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := db.DB.QueryContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, args, start, 0, err) // Read operations don't have rows affected

	return rows, err
}

// QueryRowContext executes a single row query with context and tracks performance

func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) types.Row {
	start := time.Now()
	row := types.NewRowFromSQL(db.DB.QueryRowContext(ctx, query, args...))

	return rowtracker.Wrap(row, func(err error) {
		db.trackQuery(ctx, query, args, start, 0, err) // Read operations don't have rows affected
	})
}

// ExecContext executes a query without returning rows and tracks performance
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := db.DB.ExecContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, args, start, extractRowsAffected(result, err), err)

	return result, err
}

// PrepareContext prepares a statement with context and tracks performance
func (db *DB) PrepareContext(ctx context.Context, query string) (types.Statement, error) {
	start := time.Now()
	stmt, err := db.DB.PrepareContext(ctx, query)

	// Track performance metrics
	db.trackQuery(ctx, "PREPARE: "+query, nil, start, 0, err) // Prepare doesn't affect rows

	if err != nil {
		return nil, err
	}
	base := &BasicStatement{Stmt: stmt}

	return NewStatement(base, db.logger, db.vendor, query, db.settings), nil
}

// trackQuery tracks database query performance and logs the results
func (db *DB) trackQuery(ctx context.Context, query string, args []any, start time.Time, rowsAffected int64, err error) {
	tc := &Context{
		Logger:   db.logger,
		Vendor:   db.vendor,
		Settings: db.settings,
	}
	TrackDBOperation(ctx, tc, query, args, start, rowsAffected, err)
}

// Connection wraps database.Interface to provide comprehensive performance tracking.
// It delegates all operations to the wrapped connection while intercepting calls
// to log performance metrics, detect slow queries, and track errors.
type Connection struct {
	conn     types.Interface
	logger   logger.Logger
	vendor   string
	settings Settings

	// Server connection metadata for OTel attributes
	serverAddress string
	serverPort    int
	namespace     string
}

// NewConnection returns a database.Interface that wraps conn and records query/operation
// metrics and logs. The wrapper delegates all calls to the provided conn, uses conn.DatabaseType()
// NewConnection returns a types.Interface that wraps conn and records per-operation
// performance metrics, slow queries, and errors using the provided logger.
//
// The wrapper uses conn.DatabaseType() as the vendor identifier and derives
// per-connection tracking settings from cfg via NewSettings.
func NewConnection(conn types.Interface, log logger.Logger, cfg *config.DatabaseConfig) types.Interface {
	return &Connection{
		conn:     conn,
		logger:   log,
		vendor:   conn.DatabaseType(),
		settings: NewSettings(cfg),
	}
}

// Query executes a query with performance tracking
func (tc *Connection) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tc.conn.Query(ctx, query, args...)

	tc.trackOperation(ctx, query, args, start, 0, err) // Read operations don't have rows affected
	return rows, err
}

// QueryRow executes a single row query with performance tracking

func (tc *Connection) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	start := time.Now()
	row := tc.conn.QueryRow(ctx, query, args...)

	return rowtracker.Wrap(row, func(err error) {
		tc.trackOperation(ctx, query, args, start, 0, err) // Read operations don't have rows affected
	})
}

// Exec executes a query without returning rows with performance tracking
func (tc *Connection) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tc.conn.Exec(ctx, query, args...)

	tc.trackOperation(ctx, query, args, start, extractRowsAffected(result, err), err)
	return result, err
}

// Prepare prepares a statement with performance tracking
func (tc *Connection) Prepare(ctx context.Context, query string) (types.Statement, error) {
	start := time.Now()
	stmt, err := tc.conn.Prepare(ctx, query)

	tc.trackOperation(ctx, "PREPARE: "+query, nil, start, 0, err) // Prepare doesn't affect rows

	if err != nil {
		return nil, err
	}

	return NewStatement(stmt, tc.logger, tc.vendor, query, tc.settings), nil
}

// Begin starts a transaction with performance tracking
func (tc *Connection) Begin(ctx context.Context) (types.Tx, error) {
	start := time.Now()
	tx, err := tc.conn.Begin(ctx)
	tc.trackOperation(ctx, "BEGIN", nil, start, 0, err) // BEGIN doesn't affect rows
	if err != nil {
		return nil, err
	}

	return NewTransaction(tx, tc.logger, tc.vendor, tc.settings), nil
}

// BeginTx starts a transaction with options and performance tracking
func (tc *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	start := time.Now()
	tx, err := tc.conn.BeginTx(ctx, opts)
	tc.trackOperation(ctx, "BEGIN_TX", nil, start, 0, err) // BEGIN_TX doesn't affect rows
	if err != nil {
		return nil, err
	}

	return NewTransaction(tx, tc.logger, tc.vendor, tc.settings), nil
}

// Health checks database connection health (no tracking needed)
func (tc *Connection) Health(ctx context.Context) error {
	return tc.conn.Health(ctx)
}

// Stats returns database connection statistics (no tracking needed)
func (tc *Connection) Stats() (map[string]any, error) {
	return tc.conn.Stats()
}

// Close closes the database connection (no tracking needed)
func (tc *Connection) Close() error {
	return tc.conn.Close()
}

// DatabaseType returns the database type (no tracking needed)
func (tc *Connection) DatabaseType() string {
	return tc.conn.DatabaseType()
}

// GetMigrationTable returns the migration table name (no tracking needed)
func (tc *Connection) GetMigrationTable() string {
	return tc.conn.GetMigrationTable()
}

// CreateMigrationTable creates the migration table if it doesn't exist with tracking
func (tc *Connection) CreateMigrationTable(ctx context.Context) error {
	start := time.Now()
	err := tc.conn.CreateMigrationTable(ctx)

	tc.trackOperation(ctx, "CREATE_MIGRATION_TABLE", nil, start, 0, err) // DDL doesn't report rows affected
	return err
}

// trackOperation tracks database operation performance
func (tc *Connection) trackOperation(ctx context.Context, query string, args []any, start time.Time, rowsAffected int64, err error) {
	trackingCtx := &Context{
		Logger:        tc.logger,
		Vendor:        tc.vendor,
		Settings:      tc.settings,
		ServerAddress: tc.serverAddress,
		ServerPort:    tc.serverPort,
		Namespace:     tc.namespace,
	}
	TrackDBOperation(ctx, trackingCtx, query, args, start, rowsAffected, err)
}

// SetServerInfo sets the server connection metadata for OTel attributes.
// This should be called after creating the Connection to populate server address, port, and namespace.
func (tc *Connection) SetServerInfo(address string, port int, namespace string) {
	tc.serverAddress = address
	tc.serverPort = port
	tc.namespace = namespace
}
