package wrapper

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// healthCheckTimeout is the per-call timeout applied to Health()'s PingContext.
// Matches the pre-W4-C hardcoded value in both vendor connections.
const healthCheckTimeout = 5 * time.Second

// Connection holds the byte-identical fields and delegation methods that
// previously lived in both postgresql.Connection and oracle.Connection.
// Vendor packages embed this struct (typically by pointer) and add only the
// vendor-specific bits (DatabaseType / MigrationTable / CreateMigrationTable
// DDL / dialer plumbing).
//
// Fields are exported so vendor packages can construct via struct literal in
// their NewConnection, and so the metrics-registration callback can flip
// MetricsCleanup after wiring it up. The Name field flows into Close()'s
// "Closing X database connection" log message — the only previously
// vendor-specific bit in those nine methods.
type Connection struct {
	DB             *sql.DB
	Config         *config.DatabaseConfig
	Logger         logger.Logger
	MetricsCleanup func()
	Name           string // vendor display name, e.g. "PostgreSQL", "Oracle"
}

// Query executes a query that returns rows.
func (c *Connection) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.DB.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns at most one row.
func (c *Connection) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	return types.NewRowFromSQL(c.DB.QueryRowContext(ctx, query, args...))
}

// Exec executes a query without returning any rows.
func (c *Connection) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.DB.ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement for later queries or executions.
func (c *Connection) Prepare(ctx context.Context, query string) (types.Statement, error) {
	stmt, err := c.DB.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return NewStatement(stmt), nil
}

// Begin starts a transaction with default options.
func (c *Connection) Begin(ctx context.Context) (types.Tx, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return NewTransaction(tx), nil
}

// BeginTx starts a transaction with the given options.
func (c *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	tx, err := c.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return NewTransaction(tx), nil
}

// Health checks database connectivity with a 5s timeout. The caller's context
// is honored if it has a shorter deadline.
func (c *Connection) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	return c.DB.PingContext(ctx)
}

// Stats returns database connection statistics in a map suitable for logging
// or metrics tracking. When Config is non-nil, the configured idle-connection
// target is included for visibility (Go's sql.DB doesn't enforce a minimum
// idle count; this is purely a configured target).
func (c *Connection) Stats() (map[string]any, error) {
	stats := c.DB.Stats()
	result := map[string]any{
		"max_open_connections": stats.MaxOpenConnections,
		"open_connections":     stats.OpenConnections,
		"in_use":               stats.InUse,
		"idle":                 stats.Idle,
		"wait_count":           stats.WaitCount,
		"wait_duration":        stats.WaitDuration.String(),
		"max_idle_closed":      stats.MaxIdleClosed,
		"max_idle_time_closed": stats.MaxIdleTimeClosed,
		"max_lifetime_closed":  stats.MaxLifetimeClosed,
	}

	if c.Config != nil {
		result["max_idle_connections"] = int(c.Config.Pool.Idle.Connections)
		result["configured_idle_connections"] = int(c.Config.Pool.Idle.Connections)
	}

	return result, nil
}

// Close closes the underlying *sql.DB, unregisters the metrics callback (if
// any), and logs the shutdown. The Name field is used to render the vendor
// name into the log message.
func (c *Connection) Close() error {
	if c.Logger != nil {
		c.Logger.Info().Msgf("Closing %s database connection", c.Name)
	}

	if c.MetricsCleanup != nil {
		c.MetricsCleanup()
	}

	return c.DB.Close()
}
