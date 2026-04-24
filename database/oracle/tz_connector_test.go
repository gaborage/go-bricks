package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/sijms/go-ora/v2/configurations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// fakeConn captures the SQL executed via ExecContext so tests can assert that
// the tzConnector wrapped its Connect() with the expected ALTER SESSION.
type fakeConn struct {
	execed     []string
	execErr    error
	closed     bool
	supportsEx bool // when false, the conn is presented WITHOUT ExecerContext
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }
func (c *fakeConn) Close() error                        { c.closed = true; return nil }

// fakeExecConn embeds fakeConn and implements driver.ExecerContext.
type fakeExecConn struct{ *fakeConn }

func (c *fakeExecConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.execed = append(c.execed, query)
	if c.execErr != nil {
		return nil, c.execErr
	}
	return driver.RowsAffected(0), nil
}

// fakeConnector returns a configurable fakeConn (with or without ExecerContext)
// to exercise both happy and failure paths through tzConnector.Connect.
type fakeConnector struct {
	conn       *fakeConn
	connectErr error
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	if c.connectErr != nil {
		return nil, c.connectErr
	}
	if c.conn.supportsEx {
		return &fakeExecConn{fakeConn: c.conn}, nil
	}
	return c.conn, nil
}

func (c *fakeConnector) Driver() driver.Driver { return nil }

func TestTzConnectorRunsAlterSession(t *testing.T) {
	fake := &fakeConn{supportsEx: true}
	wrapped := newTzConnector(&fakeConnector{conn: fake}, "Asia/Tokyo")

	conn, err := wrapped.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.False(t, fake.closed, "happy path must not close the connection")

	require.Len(t, fake.execed, 1, "exactly one ALTER SESSION must execute per Connect")
	assert.Equal(t, "ALTER SESSION SET TIME_ZONE = 'Asia/Tokyo'", fake.execed[0])
}

func TestTzConnectorClosesConnOnExecFailure(t *testing.T) {
	execErr := errors.New("ORA-00942: table or view does not exist")
	fake := &fakeConn{supportsEx: true, execErr: execErr}
	wrapped := newTzConnector(&fakeConnector{conn: fake}, "UTC")

	conn, err := wrapped.Connect(context.Background())
	require.Error(t, err, "exec failure must bubble up")
	assert.Nil(t, conn, "no conn must be returned on failure")
	assert.True(t, fake.closed, "failed connection must be closed to avoid pool leak")
	assert.ErrorIs(t, err, execErr, "wrapped error must preserve the underlying cause")
}

func TestTzConnectorErrorsOnMissingExecerContext(t *testing.T) {
	// A driver.Conn that does NOT implement ExecerContext can't run ALTER SESSION;
	// we close it and surface a clear error rather than silently skip the setting.
	fake := &fakeConn{supportsEx: false}
	wrapped := newTzConnector(&fakeConnector{conn: fake}, "UTC")

	conn, err := wrapped.Connect(context.Background())
	require.Error(t, err)
	assert.Nil(t, conn)
	assert.True(t, fake.closed)
	assert.Contains(t, err.Error(), "ExecerContext")
}

func TestTzConnectorPropagatesInnerConnectError(t *testing.T) {
	innerErr := errors.New("network unreachable")
	wrapped := newTzConnector(&fakeConnector{connectErr: innerErr}, "UTC")

	conn, err := wrapped.Connect(context.Background())
	assert.Nil(t, conn)
	assert.ErrorIs(t, err, innerErr, "inner Connect errors must surface unchanged (no ALTER attempt)")
}

func TestNewConnectionRoutesThroughTimezoneAwarePathWhenSet(t *testing.T) {
	// Verify NewConnection dispatches through openOracleDBWithConnector (the
	// timezone-aware seam) when cfg.Timezone is set. Legacy paths (openOracleDB,
	// openOracleDBWithDialer) must NOT be used.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	var connectorPathUsed bool
	var legacyPathUsed bool

	originalConnector := openOracleDBWithConnector
	originalOpen := openOracleDB
	originalOpenWithDialer := openOracleDBWithDialer
	originalPing := pingOracleDB

	openOracleDBWithConnector = func(c driver.Connector) *sql.DB {
		connectorPathUsed = true
		// The connector passed in MUST be a tzConnector wrapping the go-ora connector,
		// otherwise the per-pool-member ALTER SESSION guarantee is broken.
		_, ok := c.(*tzConnector)
		assert.True(t, ok, "connector handed to openOracleDBWithConnector must be a *tzConnector wrapper")
		return db
	}
	openOracleDB = func(string) (*sql.DB, error) {
		legacyPathUsed = true
		return db, nil
	}
	openOracleDBWithDialer = func(string, configurations.DialerContext) *sql.DB {
		// Should never be invoked when timezone is set.
		return db
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithConnector = originalConnector
		openOracleDB = originalOpen
		openOracleDBWithDialer = originalOpenWithDialer
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "u",
		Password: "p",
		Timezone: "Asia/Tokyo",
		Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1"}},
		Pool: config.PoolConfig{
			Max:      config.PoolMaxConfig{Connections: 5},
			Idle:     config.PoolIdleConfig{Connections: 2, Time: time.Minute},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
		},
	}

	mock.ExpectClose()
	conn, err := NewConnection(cfg, newTestLogger())
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.True(t, connectorPathUsed, "timezone-set config must route through openOracleDBWithConnector")
	assert.False(t, legacyPathUsed, "legacy openOracleDB must NOT be called when timezone is set")

	require.NoError(t, conn.Close())
}

func TestNewConnectionUsesLegacyPathOnDashSentinel(t *testing.T) {
	// When timezone is opted out via "-", legacy paths must be used so existing
	// tests and behaviors stay backward compatible.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	var connectorPathUsed bool
	var legacyPathUsed bool

	originalConnector := openOracleDBWithConnector
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithConnector = func(driver.Connector) *sql.DB {
		connectorPathUsed = true
		return db
	}
	openOracleDB = func(string) (*sql.DB, error) {
		legacyPathUsed = true
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithConnector = originalConnector
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "u",
		Password: "p",
		Timezone: "-",
		Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1"}},
		Pool: config.PoolConfig{
			Max:      config.PoolMaxConfig{Connections: 5},
			Idle:     config.PoolIdleConfig{Connections: 2, Time: time.Minute},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
		},
	}

	mock.ExpectClose()
	conn, err := NewConnection(cfg, newTestLogger())
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.False(t, connectorPathUsed, "dash sentinel must skip the timezone-aware connector path")
	assert.True(t, legacyPathUsed, "dash sentinel must route through legacy openOracleDB")

	require.NoError(t, conn.Close())
}

func TestTzConnectorEscapesSingleQuoteDefensively(t *testing.T) {
	// Validation should reject any value containing a quote, but as belt-and-braces
	// the wrapper escapes single quotes so a regression in validation can't smuggle
	// an injected statement past us.
	fake := &fakeConn{supportsEx: true}
	wrapped := newTzConnector(&fakeConnector{conn: fake}, "UTC'; DROP TABLE foo; --")

	_, err := wrapped.Connect(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.execed, 1)
	assert.Equal(t, "ALTER SESSION SET TIME_ZONE = 'UTC''; DROP TABLE foo; --'", fake.execed[0],
		"single quotes must be doubled to keep the value as a literal")
}
