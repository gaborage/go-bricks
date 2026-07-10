package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/configurations"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/tracking"
	"github.com/gaborage/go-bricks/database/internal/wrapper"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// vendorName is the human-readable vendor name used in log messages,
// errors, and wrapper.Connection.Name.
const vendorName = "Oracle"

// Connection implements the types.Interface for Oracle. Embeds the
// vendor-agnostic wrapper.Connection which carries the byte-identical
// delegation methods (Query, Exec, Prepare, Begin, Health, Stats, Close, etc.)
// — see database/internal/wrapper. Oracle-only methods (DatabaseType,
// MigrationTable, CreateMigrationTable PL/SQL block) stay defined here.
type Connection struct {
	*wrapper.Connection
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
	// openOracleDBWithConnector opens a *sql.DB from an arbitrary driver.Connector.
	// Used by the timezone-aware path so a tzConnector wrapper can intercept every
	// new physical connection. Test seam: override to return a fake *sql.DB.
	openOracleDBWithConnector = sql.OpenDB
	pingOracleDB              = func(ctx context.Context, db *sql.DB) error {
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
// When cfg.Pool.KeepAlive.IsEnabled() is true, a custom TCP dialer is used to enable keep-alive probes.
// Unless cfg.Timezone is the disabled sentinel ("-"), every new physical connection runs
// ALTER SESSION SET TIME_ZONE (defaulting to UTC when Timezone is unset) before being handed
// to the pool, guaranteeing pool-wide consistency.
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (types.Interface, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration is nil")
	}

	dsn := buildOracleDSN(cfg)

	db, err := openOracleConnection(dsn, cfg, log)
	if err != nil {
		return nil, err
	}

	configureConnectionPool(db, cfg)

	if err := verifyConnection(db, log); err != nil {
		return nil, err
	}

	logConnectionSuccess(log, cfg)

	conn := &Connection{
		Connection: &wrapper.Connection{
			DB:     db,
			Config: cfg,
			Logger: log,
			Name:   vendorName,
		},
	}

	namespace := tracking.BuildOracleNamespace(cfg.Oracle.Service.Name, cfg.Oracle.Service.SID, cfg.Database)
	conn.MetricsCleanup = tracking.RegisterConnectionPoolMetrics(conn, "oracle", cfg.Host, cfg.Port, namespace)

	return conn, nil
}

// openOracleConnection dispatches to the timezone-aware connector path for any
// non-opt-out configuration, falling back to the legacy DSN-based open paths
// only when the caller has explicitly opted out via the disabled sentinel.
// An empty Timezone is normalized to config.DefaultDatabaseTimezone here so
// callers that bypass config.Validate (e.g. tests, internal helpers) still
// receive the documented default-UTC behavior. The legacy paths are preserved
// so existing tests that stub openOracleDB / openOracleDBWithDialer can opt in
// to the no-timezone path explicitly via Timezone == TimezoneDisabledSentinel.
func openOracleConnection(dsn string, cfg *config.DatabaseConfig, log logger.Logger) (*sql.DB, error) {
	if cfg.Timezone == config.TimezoneDisabledSentinel {
		return openLegacyOracleDB(dsn, cfg, log)
	}
	timezone := cfg.Timezone
	if timezone == "" {
		timezone = config.DefaultDatabaseTimezone
	}
	cfgCopy := *cfg
	cfgCopy.Timezone = timezone
	return openTimezoneAwareOracleDB(dsn, &cfgCopy, log), nil
}

// openTimezoneAwareOracleDB builds a connector wrapped with tzConnector so every
// new physical connection runs ALTER SESSION SET TIME_ZONE. Optional keep-alive
// is applied to the underlying connector before wrapping.
func openTimezoneAwareOracleDB(dsn string, cfg *config.DatabaseConfig, log logger.Logger) *sql.DB {
	base := go_ora.NewConnector(dsn)
	applyOracleKeepAlive(base, cfg, log)
	return openOracleDBWithConnector(newTzConnector(base, cfg.Timezone))
}

// applyOracleKeepAlive installs the TCP keep-alive dialer on the go-ora connector
// when enabled. If the go-ora connector type changes (API drift), the assertion
// fails and we log a warning rather than abort — the connector is still usable
// without keep-alive.
func applyOracleKeepAlive(connector driver.Connector, cfg *config.DatabaseConfig, log logger.Logger) {
	if !cfg.Pool.KeepAlive.IsEnabled() {
		return
	}
	oc, ok := connector.(*go_ora.OracleConnector)
	if !ok {
		log.Warn().Msg("Keep-alive dialer setup failed (go-ora API change?), continuing without keep-alive")
		return
	}
	oc.Dialer(newKeepAliveDialer(cfg.Pool.KeepAlive.Interval, log))
	log.Debug().
		Dur("keepalive_interval", cfg.Pool.KeepAlive.Interval).
		Msg("TCP keep-alive enabled for Oracle connections")
}

// openLegacyOracleDB preserves the original DSN-based open paths for the
// no-timezone and timezone-opt-out cases. Routes through openOracleDBWithDialer
// when keep-alive is enabled (with graceful fallback to openOracleDB on go-ora
// API changes), or through openOracleDB directly when keep-alive is disabled.
func openLegacyOracleDB(dsn string, cfg *config.DatabaseConfig, log logger.Logger) (*sql.DB, error) {
	if cfg.Pool.KeepAlive.IsEnabled() {
		dialer := newKeepAliveDialer(cfg.Pool.KeepAlive.Interval, log)
		db := openOracleDBWithDialer(dsn, dialer)
		if db == nil {
			log.Warn().Msg("Keep-alive dialer setup failed (go-ora API change?), falling back to standard connection")
			fallback, err := openOracleDB(dsn)
			if err != nil {
				return nil, fmt.Errorf("failed to open Oracle connection: %w", err)
			}
			return fallback, nil
		}
		log.Debug().
			Dur("keepalive_interval", cfg.Pool.KeepAlive.Interval).
			Msg("TCP keep-alive enabled for Oracle connections")
		return db, nil
	}

	db, err := openOracleDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open Oracle connection: %w", err)
	}
	return db, nil
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

	wrapper.AppendPoolFields(ev, cfg).Msg("Connected to Oracle database")
}

// Oracle re-exports the vendor-agnostic wrappers; see database/internal/wrapper.
type Statement = wrapper.Statement
type Transaction = wrapper.Transaction

// Query, QueryRow, Exec, Prepare, Begin, BeginTx, Health, Stats, Close are
// inherited from the embedded *wrapper.Connection — see database/internal/wrapper.

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
	logEvent := c.Logger.Info().
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName)

	if arrayTypeName != "" {
		logEvent.Msg("Registering Oracle UDT with collection type")
	} else {
		logEvent.Msg("Registering Oracle UDT (object type only)")
	}

	err := go_ora.RegisterType(c.DB, typeName, arrayTypeName, typeObj)
	if err != nil {
		c.Logger.Error().
			Err(err).
			Str("type_name", typeName).
			Str("array_type_name", arrayTypeName).
			Msg("Failed to register Oracle UDT")
		return fmt.Errorf("failed to register Oracle type %s: %w", typeName, err)
	}

	c.Logger.Debug().
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
	logEvent := c.Logger.Info().
		Str("owner", owner).
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName)

	if arrayTypeName != "" {
		logEvent.Msg("Registering Oracle UDT with owner and collection type")
	} else {
		logEvent.Msg("Registering Oracle UDT with owner (object type only)")
	}

	err := go_ora.RegisterTypeWithOwner(c.DB, owner, typeName, arrayTypeName, typeObj)
	if err != nil {
		c.Logger.Error().
			Err(err).
			Str("owner", owner).
			Str("type_name", typeName).
			Str("array_type_name", arrayTypeName).
			Msg("Failed to register Oracle UDT with owner")
		return fmt.Errorf("failed to register Oracle type %s.%s: %w", owner, typeName, err)
	}

	c.Logger.Debug().
		Str("owner", owner).
		Str("type_name", typeName).
		Str("array_type_name", arrayTypeName).
		Msg("Successfully registered Oracle UDT with owner")

	return nil
}
