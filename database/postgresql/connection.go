package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/tracking"
	"github.com/gaborage/go-bricks/database/internal/wrapper"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// vendorName is the human-readable vendor name used in log messages,
// errors, and wrapper.Connection.Name.
const vendorName = "PostgreSQL"

// Connection implements the types.Interface for PostgreSQL. Embeds the
// vendor-agnostic wrapper.Connection which carries the byte-identical
// delegation methods (Query, Exec, Prepare, Begin, Health, Stats, Close, etc.)
// — see database/internal/wrapper. PostgreSQL-only methods (DatabaseType,
// MigrationTable, CreateMigrationTable DDL) stay defined here.
type Connection struct {
	*wrapper.Connection
}

var (
	openPostgresDB = func(cfg *pgx.ConnConfig) *sql.DB {
		return stdlib.OpenDB(*cfg)
	}
	pingPostgresDB = func(ctx context.Context, db *sql.DB) error {
		return db.PingContext(ctx)
	}
)

// makeKeepAliveDialer creates a custom dialer with TCP keep-alive enabled.
// This prevents NAT gateways, load balancers, and firewalls from dropping
// idle connections by sending periodic TCP probes.
func makeKeepAliveDialer(keepAliveCfg config.PoolKeepAliveConfig, log logger.Logger) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			KeepAlive: keepAliveCfg.Interval,
			Timeout:   30 * time.Second,
		}

		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		// Explicitly enable TCP keep-alive and set the period
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			if setErr := tcpConn.SetKeepAlive(true); setErr != nil {
				log.Warn().Err(setErr).Msg("Failed to enable TCP keep-alive on PostgreSQL connection")
			}
			if setErr := tcpConn.SetKeepAlivePeriod(keepAliveCfg.Interval); setErr != nil {
				log.Warn().Err(setErr).Msg("Failed to set TCP keep-alive period on PostgreSQL connection")
			}
		}

		return conn, nil
	}
}

// quoteDSN returns a DSN-safe representation of value, wrapping it in single quotes
// and escaping backslashes and single quotes when value contains characters other
// than letters, digits, dot (.), underscore (_) or hyphen (-). If value is empty
// it returns two single quotes `”`.
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

// NewConnection creates and configures a PostgreSQL Connection using cfg and log.
// It validates cfg, builds or uses the provided DSN, sets pool options, ensures connectivity with a ping, logs success, and returns the wrapped Connection or an error.
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (types.Interface, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration is required")
	}
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

		if cfg.TLS.Mode != "" {
			parts = append(parts, fmt.Sprintf("sslmode=%s", cfg.TLS.Mode))
		}

		dsn = strings.Join(parts, " ")
	}

	// Parse config for pgx
	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL config: %w", err)
	}

	// Apply session-level timezone via pgx RuntimeParams. Empty Timezone is
	// normalized to config.DefaultDatabaseTimezone here so callers that bypass
	// config.Validate (tests, internal helpers) still receive the documented
	// default-UTC behavior. Only the explicit opt-out sentinel skips injection.
	// Config wins over any timezone embedded in the DSN.
	if cfg.Timezone != config.TimezoneDisabledSentinel {
		timezone := cfg.Timezone
		if timezone == "" {
			timezone = config.DefaultDatabaseTimezone
		}
		if pgxConfig.RuntimeParams == nil {
			pgxConfig.RuntimeParams = make(map[string]string)
		}
		pgxConfig.RuntimeParams["timezone"] = timezone
	}

	// Configure TCP keep-alive if enabled (prevents NAT/LB idle connection drops)
	if cfg.Pool.KeepAlive.Enabled {
		pgxConfig.DialFunc = makeKeepAliveDialer(cfg.Pool.KeepAlive, log)
		log.Debug().
			Dur("keepalive_interval", cfg.Pool.KeepAlive.Interval).
			Msg("TCP keep-alive enabled for PostgreSQL connections")
	}

	// Create connection using pgx driver
	db := openPostgresDB(pgxConfig)

	// Configure connection pool
	db.SetMaxOpenConns(int(cfg.Pool.Max.Connections))
	db.SetMaxIdleConns(int(cfg.Pool.Idle.Connections))
	db.SetConnMaxLifetime(cfg.Pool.Lifetime.Max)
	db.SetConnMaxIdleTime(cfg.Pool.Idle.Time)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pingPostgresDB(ctx, db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close PostgreSQL database connection after ping failure")
		}
		return nil, fmt.Errorf("failed to ping PostgreSQL database: %w", err)
	}

	ev := log.Info().
		Str("host", cfg.Host).
		Int("port", cfg.Port).
		Str("database", cfg.Database)
	wrapper.AppendPoolFields(ev, cfg).Msg("Connected to PostgreSQL database")

	conn := &Connection{
		Connection: &wrapper.Connection{
			DB:     db,
			Config: cfg,
			Logger: log,
			Name:   vendorName,
		},
	}

	// Register connection pool metrics for observability with server metadata
	// Store cleanup function on the embedded wrapper.Connection so its Close()
	// can invoke it during shutdown.
	namespace := tracking.BuildPostgreSQLNamespace(cfg.Database, cfg.PostgreSQL.Schema)
	conn.MetricsCleanup = tracking.RegisterConnectionPoolMetrics(conn, "postgresql", cfg.Host, cfg.Port, namespace)

	return conn, nil
}

// PostgreSQL re-exports the vendor-agnostic wrappers; see database/internal/wrapper.
type Statement = wrapper.Statement
type Transaction = wrapper.Transaction

// DatabaseType returns the database type
func (c *Connection) DatabaseType() string {
	return types.PostgreSQL
}

// MigrationTable returns the migration table name for PostgreSQL
func (c *Connection) MigrationTable() string {
	return "flyway_schema_history"
}

// CreateMigrationTable creates the migration table if it doesn't exist
func (c *Connection) CreateMigrationTable(ctx context.Context) error {
	tableQuery := `
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
	`

	if _, err := c.Exec(ctx, tableQuery); err != nil {
		return err
	}

	indexQuery := `CREATE INDEX IF NOT EXISTS flyway_schema_history_s_idx ON flyway_schema_history (success);`
	if _, err := c.Exec(ctx, indexQuery); err != nil {
		return err
	}

	return nil
}
