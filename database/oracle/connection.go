package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// Connection implements the database.Interface for Oracle
type Connection struct {
	db     *sql.DB
	config *config.DatabaseConfig
	logger logger.Logger
}

var (
	openOracleDB = func(dsn string) (*sql.DB, error) {
		return sql.Open("oracle", dsn)
	}
	pingOracleDB = func(ctx context.Context, db *sql.DB) error {
		return db.PingContext(ctx)
	}
)

// NewConnection creates a new Oracle connection
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (database.Interface, error) {
	var dsn string
	if cfg.ConnectionString != "" {
		dsn = cfg.ConnectionString
	} else {
		// Build Oracle DSN
		if cfg.ServiceName != "" {
			dsn = go_ora.BuildUrl(cfg.Host, cfg.Port, cfg.ServiceName, cfg.Username, cfg.Password, nil)
		} else if cfg.SID != "" {
			urlOpts := map[string]string{"SID": cfg.SID}
			dsn = go_ora.BuildUrl(cfg.Host, cfg.Port, "", cfg.Username, cfg.Password, urlOpts)
		} else {
			dsn = go_ora.BuildUrl(cfg.Host, cfg.Port, cfg.Database, cfg.Username, cfg.Password, nil)
		}
	}

	// Open Oracle connection
	db, err := openOracleDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open Oracle connection: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(int(cfg.MaxConns))
	db.SetMaxIdleConns(int(cfg.MaxIdleConns))
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pingOracleDB(ctx, db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close Oracle database connection after ping failure")
		}
		return nil, fmt.Errorf("failed to ping Oracle database: %w", err)
	}

	ev := log.Info().
		Str("host", cfg.Host).
		Int("port", cfg.Port)
	if cfg.ServiceName != "" {
		ev = ev.Str("service_name", cfg.ServiceName)
	} else if cfg.SID != "" {
		ev = ev.Str("sid", cfg.SID)
	} else {
		ev = ev.Str("database", cfg.Database)
	}
	ev.Msg("Connected to Oracle database")

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
	c.logger.Info().Msg("Closing Oracle database connection")
	return c.db.Close()
}

// DatabaseType returns the database type
func (c *Connection) DatabaseType() string {
	return "oracle"
}

// GetMigrationTable returns the migration table name for Oracle
func (c *Connection) GetMigrationTable() string {
	return "FLYWAY_SCHEMA_HISTORY"
}

// CreateMigrationTable creates the migration table if it doesn't exist
func (c *Connection) CreateMigrationTable(ctx context.Context) error {
	query := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE FLYWAY_SCHEMA_HISTORY (
				installed_rank NUMBER NOT NULL,
				version VARCHAR2(50),
				description VARCHAR2(200) NOT NULL,
				type VARCHAR2(20) NOT NULL,
				script VARCHAR2(1000) NOT NULL,
				checksum NUMBER,
				installed_by VARCHAR2(100) NOT NULL,
				installed_on TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
				execution_time NUMBER NOT NULL,
				success NUMBER(1) NOT NULL,
				CONSTRAINT flyway_schema_history_pk PRIMARY KEY (installed_rank)
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN -- Table already exists
					RAISE;
				END IF;
		END;
	`

	_, err := c.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create Oracle migration table: %w", err)
	}

	// Create index
	indexQuery := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE INDEX flyway_schema_history_s_idx ON FLYWAY_SCHEMA_HISTORY (success)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN -- Index already exists
					RAISE;
				END IF;
		END;
	`

	_, err = c.Exec(ctx, indexQuery)
	return err
}
