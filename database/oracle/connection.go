package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/configurations"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/tracking"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// Connection implements the types.Interface for Oracle
type Connection struct {
	db             *sql.DB
	config         *config.DatabaseConfig
	logger         logger.Logger
	metricsCleanup func() // Cleanup function for unregistering metrics callback
}

var (
	openOracleDB = func(dsn string) (*sql.DB, error) {
		return sql.Open("oracle", dsn)
	}
	openOracleDBWithDialer = func(dsn string, dialer configurations.DialerContext) *sql.DB {
		connector := go_ora.NewConnector(dsn)
		oracleConn, ok := connector.(*go_ora.OracleConnector)
		if !ok {
			// Return nil to signal fallback needed - caller handles graceful degradation
			return nil
		}
		oracleConn.Dialer(dialer)
		return sql.OpenDB(connector)
	}
	pingOracleDB = func(ctx context.Context, db *sql.DB) error {
		return db.PingContext(ctx)
	}
)

// keepAliveDialer implements configurations.DialerContext for TCP keep-alive connections.
// This enables TCP keep-alive probes to prevent NAT gateways and load balancers
// from dropping idle database connections in cloud environments.
type keepAliveDialer struct {
	interval time.Duration
	log      logger.Logger
}

// DialContext implements configurations.DialerContext interface.
// It creates a TCP connection with keep-alive enabled.
func (d *keepAliveDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: d.interval,
	}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}

	// Explicitly enable keep-alive and set the period for TCP connections
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if setErr := tcpConn.SetKeepAlive(true); setErr != nil {
			d.log.Warn().Err(setErr).Msg("Failed to enable TCP keep-alive on Oracle connection")
		}
		if setErr := tcpConn.SetKeepAlivePeriod(d.interval); setErr != nil {
			d.log.Warn().Err(setErr).Msg("Failed to set TCP keep-alive period on Oracle connection")
		}
	}

	return conn, nil
}

// newKeepAliveDialer creates a new keepAliveDialer with the specified interval.
func newKeepAliveDialer(interval time.Duration, log logger.Logger) *keepAliveDialer {
	return &keepAliveDialer{
		interval: interval,
		log:      log,
	}
}

// NewConnection creates and returns an Oracle-backed types.Interface using the provided database configuration and logger.
// It returns an error if cfg is nil, if the connection cannot be opened, or if an initial ping to the database fails.
// The function uses cfg.ConnectionString when present or constructs a DSN from host/port and Oracle service/SID/database,
// configures the connection pool from cfg.Pool, verifies connectivity with a 10-second timeout, and logs connection details.
// When cfg.Pool.KeepAlive.Enabled is true, a custom TCP dialer is used to enable keep-alive probes.
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (types.Interface, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration is nil")
	}

	dsn := buildOracleDSN(cfg)

	var db *sql.DB
	var err error

	if cfg.Pool.KeepAlive.Enabled {
		// Use connector with custom keep-alive dialer
		dialer := newKeepAliveDialer(cfg.Pool.KeepAlive.Interval, log)
		db = openOracleDBWithDialer(dsn, dialer)
		if db == nil {
			// Graceful fallback: go-ora connector type changed, use standard connection
			log.Warn().Msg("Keep-alive dialer setup failed (go-ora API change?), falling back to standard connection")
			db, err = openOracleDB(dsn)
			if err != nil {
				return nil, fmt.Errorf("failed to open Oracle connection: %w", err)
			}
		} else {
			log.Debug().
				Dur("keepalive_interval", cfg.Pool.KeepAlive.Interval).
				Msg("TCP keep-alive enabled for Oracle connections")
		}
	} else {
		db, err = openOracleDB(dsn)
		if err != nil {
			return nil, fmt.Errorf("failed to open Oracle connection: %w", err)
		}
	}

	configureConnectionPool(db, cfg)

	if err := verifyConnection(db, log); err != nil {
		return nil, err
	}

	logConnectionSuccess(log, cfg)

	conn := &Connection{
		db:     db,
		config: cfg,
		logger: log,
	}

	namespace := tracking.BuildOracleNamespace(cfg.Oracle.Service.Name, cfg.Oracle.Service.SID, cfg.Database)
	conn.metricsCleanup = tracking.RegisterConnectionPoolMetrics(conn, "oracle", cfg.Host, cfg.Port, namespace)

	return conn, nil
}

// buildURLOptions constructs the URL options map for Oracle connection.
// Includes SID if specified. TCP keep-alive is handled separately via custom dialer.
func buildURLOptions(cfg *config.DatabaseConfig) map[string]string {
	urlOpts := make(map[string]string)

	if cfg.Oracle.Service.SID != "" {
		urlOpts["SID"] = cfg.Oracle.Service.SID
	}

	return urlOpts
}

// resolveServiceName determines the Oracle service name from configuration.
// Priority: Service.Name > Database (when no SID) > empty (when SID is set)
func resolveServiceName(cfg *config.DatabaseConfig) string {
	if cfg.Oracle.Service.Name != "" {
		return cfg.Oracle.Service.Name
	}
	if cfg.Oracle.Service.SID == "" {
		return cfg.Database
	}
	// When SID is set, service name is empty (SID passed via URL options)
	return ""
}

// buildOracleDSN constructs an Oracle connection DSN from configuration.
// Returns the DSN string. If ConnectionString is provided, uses it directly.
func buildOracleDSN(cfg *config.DatabaseConfig) string {
	if cfg.ConnectionString != "" {
		return cfg.ConnectionString
	}

	urlOpts := buildURLOptions(cfg)
	serviceName := resolveServiceName(cfg)

	var optsArg map[string]string
	if len(urlOpts) > 0 {
		optsArg = urlOpts
	}

	return go_ora.BuildUrl(cfg.Host, cfg.Port, serviceName, cfg.Username, cfg.Password, optsArg)
}

// configureConnectionPool sets pool settings on the database connection.
func configureConnectionPool(db *sql.DB, cfg *config.DatabaseConfig) {
	db.SetMaxOpenConns(int(cfg.Pool.Max.Connections))
	db.SetMaxIdleConns(int(cfg.Pool.Idle.Connections))
	db.SetConnMaxLifetime(cfg.Pool.Lifetime.Max)
	db.SetConnMaxIdleTime(cfg.Pool.Idle.Time)
}

// verifyConnection tests the database connection with a ping.
// Closes the connection and returns an error if ping fails.
func verifyConnection(db *sql.DB, log logger.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pingOracleDB(ctx, db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close Oracle database connection after ping failure")
		}
		return fmt.Errorf("failed to ping Oracle database: %w", err)
	}
	return nil
}

// logConnectionSuccess logs successful Oracle connection with appropriate identifiers.
func logConnectionSuccess(log logger.Logger, cfg *config.DatabaseConfig) {
	ev := log.Info().
		Str("host", cfg.Host).
		Int("port", cfg.Port)

	switch {
	case cfg.Oracle.Service.Name != "":
		ev = ev.Str("service_name", cfg.Oracle.Service.Name)
	case cfg.Oracle.Service.SID != "":
		ev = ev.Str("sid", cfg.Oracle.Service.SID)
	default:
		ev = ev.Str("database", cfg.Database)
	}

	ev.Msg("Connected to Oracle database")
}

// Statement wraps sql.Stmt to implement types.Statement
type Statement struct {
	stmt *sql.Stmt
}

// Query executes a prepared query with arguments
func (s *Statement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	return s.stmt.QueryContext(ctx, args...)
}

// QueryRow executes a prepared query that returns a single row
func (s *Statement) QueryRow(ctx context.Context, args ...any) types.Row {
	return types.NewRowFromSQL(s.stmt.QueryRowContext(ctx, args...))
}

// Exec executes a prepared statement with arguments
func (s *Statement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	return s.stmt.ExecContext(ctx, args...)
}

// Close closes the prepared statement
func (s *Statement) Close() error {
	return s.stmt.Close()
}

// Transaction wraps sql.Tx to implement types.Tx
type Transaction struct {
	tx *sql.Tx
}

// Query executes a query within the transaction
func (t *Transaction) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns a single row within the transaction
func (t *Transaction) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	return types.NewRowFromSQL(t.tx.QueryRowContext(ctx, query, args...))
}

// Exec executes a query without returning rows within the transaction
func (t *Transaction) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement within the transaction
func (t *Transaction) Prepare(ctx context.Context, query string) (types.Statement, error) {
	stmt, err := t.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &Statement{stmt: stmt}, nil
}

// Commit commits the transaction
// Note: Oracle's sql.Tx.Commit doesn't accept context; it's atomic and non-cancellable.
// The context parameter maintains interface consistency for databases that support it.
func (t *Transaction) Commit(_ context.Context) error {
	return t.tx.Commit()
}

// Rollback rolls back the transaction
// Note: Oracle's sql.Tx.Rollback doesn't accept context; it's atomic and non-cancellable.
// The context parameter maintains interface consistency for databases that support it.
func (t *Transaction) Rollback(_ context.Context) error {
	return t.tx.Rollback()
}

// Query executes a query that returns rows
func (c *Connection) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns at most one row
func (c *Connection) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	return types.NewRowFromSQL(c.db.QueryRowContext(ctx, query, args...))
}

// Exec executes a query without returning any rows
func (c *Connection) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement for later queries or executions
func (c *Connection) Prepare(ctx context.Context, query string) (types.Statement, error) {
	stmt, err := c.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &Statement{stmt: stmt}, nil
}

// Begin starts a transaction
func (c *Connection) Begin(ctx context.Context) (types.Tx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Transaction{tx: tx}, nil
}

// BeginTx starts a transaction with options
func (c *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
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
func (c *Connection) Stats() (map[string]any, error) {
	stats := c.db.Stats()
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

	// Add configured idle connections if config is available
	if c.config != nil {
		result["max_idle_connections"] = int(c.config.Pool.Idle.Connections)
		// configured_idle_connections represents the target maximum idle connections setting.
		// Go's sql.DB does not enforce a minimum idle count; this is purely a configured target.
		result["configured_idle_connections"] = int(c.config.Pool.Idle.Connections)
	}

	return result, nil
}

// Close closes the database connection
func (c *Connection) Close() error {
	c.logger.Info().Msg("Closing Oracle database connection")

	// Unregister metrics callback to allow garbage collection
	if c.metricsCleanup != nil {
		c.metricsCleanup()
	}

	return c.db.Close()
}

// DatabaseType returns the database type
func (c *Connection) DatabaseType() string {
	return types.Oracle
}

// MigrationTable returns the migration table name for Oracle
func (c *Connection) MigrationTable() string {
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

// RegisterType registers an Oracle User-Defined Type (UDT) for use with
// stored procedures, functions, and queries that return custom object types.
//
// IMPORTANT: This is for custom types created with CREATE TYPE, NOT for
// Oracle SEQUENCE objects. Sequences work automatically without registration:
//
//	// Oracle SEQUENCE - works without UDT registration
//	var nextID int64
//	err := conn.QueryRow(ctx, "SELECT SEQ_ID_TABLE.NEXTVAL FROM DUAL").Scan(&nextID)
//
// PRIMARY USE CASE: Collection Types for Bulk Operations
//
// Register object and collection types for efficient bulk operations:
//
//	type Product struct {
//	    ID    int64  `udt:"ID"`
//	    Name  string `udt:"NAME"`
//	    Price float64 `udt:"PRICE"`
//	}
//
//	// Register collection type
//	conn.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})
//
//	// Use in bulk insert
//	products := []Product{
//	    {ID: 1, Name: "Widget", Price: 19.99},
//	    {ID: 2, Name: "Gadget", Price: 29.99},
//	}
//	_, err := conn.Exec(ctx, "BEGIN bulk_insert_products(:1); END;", products)
//
// Parameters:
//   - typeName: Oracle object type name (e.g., "PRODUCT_TYPE")
//   - arrayTypeName: Oracle collection type name (e.g., "PRODUCT_TABLE" for TABLE OF)
//     Use empty string "" for single object types only
//   - typeObj: Go struct instance with udt:"FIELD_NAME" tags matching Oracle type
//
// Oracle type definition example:
//
//	CREATE TYPE PRODUCT_TYPE AS OBJECT (
//	    ID    NUMBER,
//	    NAME  VARCHAR2(100),
//	    PRICE NUMBER(10,2)
//	);
//	CREATE TYPE PRODUCT_TABLE AS TABLE OF PRODUCT_TYPE;
//
// The Go struct must use udt tags matching Oracle field names (case-sensitive):
//
//	type OrderItem struct {
//	    ItemID    int64   `udt:"ITEM_ID"`    // Matches Oracle: ITEM_ID
//	    Quantity  int     `udt:"QUANTITY"`   // Matches Oracle: QUANTITY
//	    UnitPrice float64 `udt:"UNIT_PRICE"` // Matches Oracle: UNIT_PRICE
//	}
//
// Registration must occur before any queries or procedures use the type.
// Best practice: register all UDTs during application initialization.
func (c *Connection) RegisterType(typeName, arrayTypeName string, typeObj any) error {
	logEvent := c.logger.Info().
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName)

	if arrayTypeName != "" {
		logEvent.Msg("Registering Oracle UDT with collection type")
	} else {
		logEvent.Msg("Registering Oracle UDT (object type only)")
	}

	err := go_ora.RegisterType(c.db, typeName, arrayTypeName, typeObj)
	if err != nil {
		c.logger.Error().
			Err(err).
			Str("type_name", typeName).
			Str("array_type_name", arrayTypeName).
			Msg("Failed to register Oracle UDT")
		return fmt.Errorf("failed to register Oracle type %s: %w", typeName, err)
	}

	c.logger.Debug().
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName).
		Msg("Successfully registered Oracle UDT")

	return nil
}

// RegisterTypeWithOwner registers an Oracle User-Defined Type with schema owner.
//
// Use this when UDTs are defined in a specific schema (not the current user's schema).
//
//	type Customer struct {
//	    CustomerID int    `udt:"CUSTOMER_ID"`
//	    Name       string `udt:"NAME"`
//	}
//
//	// Register collection type with schema owner
//	conn.RegisterTypeWithOwner("SHARED_SCHEMA", "CUSTOMER_TYPE", "CUSTOMER_TABLE", Customer{})
//
//	// Use in bulk operations
//	customers := []Customer{{CustomerID: 1, Name: "ACME"}, ...}
//	_, err := conn.Exec(ctx, "BEGIN SHARED_SCHEMA.process_customers(:1); END;", customers)
//
// Parameters:
//   - owner: Schema owner (e.g., "MYSCHEMA", case-sensitive, typically uppercase)
//   - typeName: Oracle object type name
//   - arrayTypeName: Oracle collection type name (use "" for single objects)
//   - typeObj: Go struct instance with udt:"FIELD_NAME" tags
func (c *Connection) RegisterTypeWithOwner(owner, typeName, arrayTypeName string, typeObj any) error {
	logEvent := c.logger.Info().
		Str("owner", owner).
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName)

	if arrayTypeName != "" {
		logEvent.Msg("Registering Oracle UDT with owner and collection type")
	} else {
		logEvent.Msg("Registering Oracle UDT with owner (object type only)")
	}

	err := go_ora.RegisterTypeWithOwner(c.db, owner, typeName, arrayTypeName, typeObj)
	if err != nil {
		c.logger.Error().
			Err(err).
			Str("owner", owner).
			Str("type_name", typeName).
			Str("array_type_name", arrayTypeName).
			Msg("Failed to register Oracle UDT with owner")
		return fmt.Errorf("failed to register Oracle type %s.%s: %w", owner, typeName, err)
	}

	c.logger.Debug().
		Str("owner", owner).
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName).
		Msg("Successfully registered Oracle UDT with owner")

	return nil
}
