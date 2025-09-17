package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// Connection implements the database.Interface for PostgreSQL
type Connection struct {
	db     *sql.DB
	config *config.DatabaseConfig
	logger logger.Logger
}

var (
	openPostgresDB = func(cfg *pgx.ConnConfig) *sql.DB {
		return stdlib.OpenDB(*cfg)
	}
	pingPostgresDB = func(ctx context.Context, db *sql.DB) error {
		return db.PingContext(ctx)
	}
)

// quoteDSN quotes a DSN value according to libpq rules:
// - Returns double single quotes for empty strings (empty value)
// - Escapes backslashes and single quotes
// - Wraps in single quotes when value contains non-alphanumeric/._- characters
func quoteDSN(value string) string {
	if value == "" {
		return "''"
	}

	// Check if quoting is needed (contains spaces or special characters)
	needsQuoting := false
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			needsQuoting = true
			break
		}
	}

	if !needsQuoting {
		return value
	}

	// Escape backslashes and single quotes
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")

	return "'" + escaped + "'"
}

// NewConnection creates a new PostgreSQL connection
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (database.Interface, error) {
	var dsn string
	if cfg.ConnectionString != "" {
		dsn = cfg.ConnectionString
	} else {
		parts := []string{
			fmt.Sprintf("host=%s", quoteDSN(cfg.Host)),
			fmt.Sprintf("port=%d", cfg.Port),
			fmt.Sprintf("user=%s", quoteDSN(cfg.Username)),
			fmt.Sprintf("password=%s", quoteDSN(cfg.Password)),
			fmt.Sprintf("dbname=%s", quoteDSN(cfg.Database)),
		}

		if cfg.SSLMode != "" {
			parts = append(parts, fmt.Sprintf("sslmode=%s", cfg.SSLMode))
		}

		dsn = strings.Join(parts, " ")
	}

	// Parse config for pgx
	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL config: %w", err)
	}

	// Create connection using pgx driver
	db := openPostgresDB(pgxConfig)

	// Configure connection pool
	db.SetMaxOpenConns(int(cfg.MaxConns))
	db.SetMaxIdleConns(int(cfg.MaxIdleConns))
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pingPostgresDB(ctx, db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close PostgreSQL database connection after ping failure")
		}
		return nil, fmt.Errorf("failed to ping PostgreSQL database: %w", err)
	}

	log.Info().
		Str("host", cfg.Host).
		Int("port", cfg.Port).
		Str("database", cfg.Database).
		Msg("Connected to PostgreSQL database")

	return &Connection{
		db:     db,
		config: cfg,
		logger: log,
	}, nil
}

// Statement wraps sql.Stmt to implement database.Statement
type Statement struct {
	stmt *sql.Stmt
}

// Query executes a prepared query with arguments
func (s *Statement) Query(ctx context.Context, args ...interface{}) (*sql.Rows, error) {
	return s.stmt.QueryContext(ctx, args...)
}

// QueryRow executes a prepared query that returns a single row
func (s *Statement) QueryRow(ctx context.Context, args ...interface{}) *sql.Row {
	return s.stmt.QueryRowContext(ctx, args...)
}

// Exec executes a prepared statement with arguments
func (s *Statement) Exec(ctx context.Context, args ...interface{}) (sql.Result, error) {
	return s.stmt.ExecContext(ctx, args...)
}

// Close closes the prepared statement
func (s *Statement) Close() error {
	return s.stmt.Close()
}

// Transaction wraps sql.Tx to implement database.Tx
type Transaction struct {
	tx *sql.Tx
}

// Query executes a query within the transaction
func (t *Transaction) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns a single row within the transaction
func (t *Transaction) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// Exec executes a query without returning rows within the transaction
func (t *Transaction) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement within the transaction
func (t *Transaction) Prepare(ctx context.Context, query string) (database.Statement, error) {
	stmt, err := t.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &Statement{stmt: stmt}, nil
}

// Commit commits the transaction
func (t *Transaction) Commit() error {
	return t.tx.Commit()
}

// Rollback rolls back the transaction
func (t *Transaction) Rollback() error {
	return t.tx.Rollback()
}

// Query executes a query that returns rows
func (c *Connection) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns at most one row
func (c *Connection) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// Exec executes a query without returning any rows
func (c *Connection) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement for later queries or executions
func (c *Connection) Prepare(ctx context.Context, query string) (database.Statement, error) {
	stmt, err := c.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &Statement{stmt: stmt}, nil
}

// Begin starts a transaction
func (c *Connection) Begin(ctx context.Context) (database.Tx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Transaction{tx: tx}, nil
}

// BeginTx starts a transaction with options
func (c *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	tx, err := c.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Transaction{tx: tx}, nil
}

// Health checks database connectivity
func (c *Connection) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return c.db.PingContext(ctx)
}

// Stats returns database connection statistics
func (c *Connection) Stats() (map[string]interface{}, error) {
	stats := c.db.Stats()
	return map[string]interface{}{
		"max_open_connections": stats.MaxOpenConnections,
		"open_connections":     stats.OpenConnections,
		"in_use":               stats.InUse,
		"idle":                 stats.Idle,
		"wait_count":           stats.WaitCount,
		"wait_duration":        stats.WaitDuration.String(),
		"max_idle_closed":      stats.MaxIdleClosed,
		"max_idle_time_closed": stats.MaxIdleTimeClosed,
		"max_lifetime_closed":  stats.MaxLifetimeClosed,
	}, nil
}

// Close closes the database connection
func (c *Connection) Close() error {
	c.logger.Info().Msg("Closing PostgreSQL database connection")
	return c.db.Close()
}

// DatabaseType returns the database type
func (c *Connection) DatabaseType() string {
	return "postgresql"
}

// GetMigrationTable returns the migration table name for PostgreSQL
func (c *Connection) GetMigrationTable() string {
	return "flyway_schema_history"
}

// CreateMigrationTable creates the migration table if it doesn't exist
func (c *Connection) CreateMigrationTable(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS flyway_schema_history (
			installed_rank INTEGER NOT NULL,
			version VARCHAR(50),
			description VARCHAR(200) NOT NULL,
			type VARCHAR(20) NOT NULL,
			script VARCHAR(1000) NOT NULL,
			checksum INTEGER,
			installed_by VARCHAR(100) NOT NULL,
			installed_on TIMESTAMP NOT NULL DEFAULT NOW(),
			execution_time INTEGER NOT NULL,
			success BOOLEAN NOT NULL,
			CONSTRAINT flyway_schema_history_pk PRIMARY KEY (installed_rank)
		);
		
		CREATE INDEX IF NOT EXISTS flyway_schema_history_s_idx ON flyway_schema_history (success);
	`

	_, err := c.Exec(ctx, query)
	return err
}
