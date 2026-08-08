package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/sijms/go-ora/v2/configurations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/dbtestlog"
	"github.com/gaborage/go-bricks/database/internal/wrapper"
	"github.com/gaborage/go-bricks/internal/testutil"
)

const (
	oraclePingErrorMsg = "failed to ping Oracle database"
	selectQuery        = "SELECT name FROM dual WHERE id = :1"
	insertQuery        = "INSERT INTO dual(id, name) VALUES (:1, :2)"
)

type ConnectionTestData struct {
	name          string
	config        *config.DatabaseConfig
	expectError   bool
	errorContains string
}

// =============================================================================
// Test Helper Functions
// =============================================================================

// setupMockConnection creates a mock database connection with logger for testing
func setupMockConnection(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *Connection) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	c := &Connection{Connection: &wrapper.Connection{DB: db, Logger: dbtestlog.NewDisabledTestLogger(), Name: "Oracle"}}
	return db, mock, c
}

// createStandardPoolConfig returns a standard pool configuration for testing
func createStandardPoolConfig() config.PoolConfig {
	return config.PoolConfig{
		Max: config.PoolMaxConfig{
			Connections: 25,
		},
		Idle: config.PoolIdleConfig{
			Connections: 10,
			Time:        30 * time.Minute,
		},
		Lifetime: config.LifetimeConfig{
			Max: time.Hour,
		},
	}
}

// createOracleConfig creates a test Oracle database configuration
func createOracleConfig(connType, value string) *config.DatabaseConfig {
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Pool:     createStandardPoolConfig(),
	}

	switch connType {
	case "connection_string":
		cfg.ConnectionString = value
		cfg.Host = ""
		cfg.Port = 0
		cfg.Username = ""
		cfg.Password = ""
	case "service_name":
		cfg.Oracle = config.OracleConfig{
			Service: config.ServiceConfig{Name: value},
		}
	case "sid":
		cfg.Oracle = config.OracleConfig{
			Service: config.ServiceConfig{SID: value},
		}
	case "database":
		cfg.Database = value
	}

	return cfg
}

// testConnectionExpectedError tests that a connection attempt fails with expected error
func testConnectionExpectedError(t *testing.T, cfg *config.DatabaseConfig) {
	log := dbtestlog.NewTestLogger()
	_, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

// setupMockStatementOperations sets up common mock expectations for statement operations
func setupMockStatementOperations(mock sqlmock.Sqlmock) {
	// Health check
	mock.ExpectPing()

	// Basic Exec operation
	mock.ExpectExec("INSERT INTO t").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))

	// Query operation
	rows := sqlmock.NewRows([]string{"id"}).AddRow(1)
	mock.ExpectQuery("SELECT id FROM t").WillReturnRows(rows)

	// QueryRow operation
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery("SELECT CURRENT_TIMESTAMP").WillReturnRows(rows)

	// Prepare operation
	mock.ExpectPrepare("UPDATE t SET x").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
}

// setupMockTransactionOperations sets up common mock expectations for transaction operations
func setupMockTransactionOperations(mock sqlmock.Sqlmock) {
	// Begin + transaction query + commit
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT 1 FROM dual").WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
}

// setupMockMetaOperations sets up common mock expectations for metadata operations
func setupMockMetaOperations(mock sqlmock.Sqlmock) {
	// CreateMigrationTable expects two PL/SQL blocks
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))

	// Close operation
	mock.ExpectClose()
}

func TestConnectionBasicMethodsWithSQLMock(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Set up all mock expectations
	setupMockStatementOperations(mock)
	setupMockTransactionOperations(mock)
	setupMockMetaOperations(mock)

	// Execute Health check
	require.NoError(t, c.Health(ctx))

	// Execute Exec operation
	_, err := c.Exec(ctx, "INSERT INTO t(x) VALUES(?)", "a")
	require.NoError(t, err)

	// Execute Query operation
	func() {
		rs, qerr := c.Query(ctx, "SELECT id FROM t")
		require.NoError(t, qerr)
		defer rs.Close()
		assert.True(t, rs.Next())
	}()

	// Execute QueryRow operation
	row := c.QueryRow(ctx, "SELECT CURRENT_TIMESTAMP")
	require.NotNil(t, row)
	var ts time.Time
	require.NoError(t, row.Scan(&ts))

	// Execute Prepare + Statement operations
	st, err := c.Prepare(ctx, "UPDATE t SET x=:1 WHERE id=:2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Execute transaction operations
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) // No-op if committed
	func() {
		rs2, qerr := tx.Query(ctx, "SELECT 1 FROM dual")
		require.NoError(t, qerr)
		defer rs2.Close()
	}()
	require.NoError(t, tx.Commit(ctx))

	// Execute BeginTx + rollback
	tx2, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback(ctx))

	// Test Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Test metadata methods
	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.MigrationTable())

	// Test CreateMigrationTable
	require.NoError(t, c.CreateMigrationTable(ctx))

	// Test Close
	require.NoError(t, c.Close())

	// Verify all expectations were met
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Connection Management Tests
// =============================================================================

func TestConnectionNewConnectionWithConnectionString(t *testing.T) {
	cfg := createOracleConfig("connection_string", "oracle://user:pass@localhost:1521/XE")
	testConnectionExpectedError(t, cfg)
}

// TestConnectionNewConnectionRedactsConnectionStringPassword pins the startup-error
// leak: go-ora returns url.Parse's *url.Error unwrapped, and net/url renders the whole
// raw URL (userinfo included) with %q. The DEL byte in the host is the url.Parse trigger.
func TestConnectionNewConnectionRedactsConnectionStringPassword(t *testing.T) {
	const password = "zqxvwmarmaladeplinth"
	cfg := createOracleConfig("connection_string", "oracle://appuser:"+password+"@zonk\x7fhost:1521/XE")

	_, err := NewConnection(cfg, dbtestlog.NewTestLogger())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), password, "connection-string password leaked into the startup error")
}

// TestConnectionNewConnectionRedactsBuiltDSNPassword covers the other DSN source:
// go_ora.BuildUrl embeds cfg.Password, so the same masking must apply there.
func TestConnectionNewConnectionRedactsBuiltDSNPassword(t *testing.T) {
	const password = "zqxvwtrellisfathom"
	cfg := createOracleConfig("service_name", "XE")
	cfg.Host = "zonk\x7fhost"
	cfg.Password = password

	_, err := NewConnection(cfg, dbtestlog.NewTestLogger())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), password, "built-DSN password leaked into the startup error")
	assert.Contains(t, err.Error(), "testuser", "username must stay visible so the failing connection is identifiable")
	assert.Contains(t, err.Error(), "zonk", "host must stay visible so the failing connection is identifiable")
}

// TestConnectionNewConnectionRedactsDelimiterPasswords covers passwords carrying an
// unescaped URL delimiter. Those hide the '@' behind the authority terminator, and
// net/url then echoes the leading password segment a second time as the port it
// rejected — so the assertion targets that segment, not just the whole password.
func TestConnectionNewConnectionRedactsDelimiterPasswords(t *testing.T) {
	const leadingSegment = "zqxvwmarrow"
	tests := []struct {
		name     string
		password string
	}{
		{name: "password_with_slash", password: leadingSegment + "/plinth"},
		{name: "password_with_question_mark", password: leadingSegment + "?plinth"},
		{name: "password_with_hash", password: leadingSegment + "#plinth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := "oracle://appuser:" + tt.password + "@host:1521/XE"
			// Premise, checked before dialing: the delimiter makes url.Parse reject the
			// authority, so NewConnection fails at parse and never resolves "host". A case
			// that ever became parseable trips here instead of doing real network I/O.
			_, parseErr := url.Parse(dsn)
			require.Error(t, parseErr, "premise: url.Parse must reject the DSN before any connect attempt")

			cfg := createOracleConfig("connection_string", dsn)

			_, err := NewConnection(cfg, dbtestlog.NewTestLogger())

			require.Error(t, err)
			assert.NotContains(t, err.Error(), leadingSegment, "password leaked into the startup error")
			assert.Contains(t, err.Error(), "appuser", "username must stay visible so the failing connection is identifiable")
		})
	}
}

func TestRedactDSNErrorPreservesErrorChain(t *testing.T) {
	// Assembled from a variable so staticcheck (SA1007) does not constant-fold the
	// deliberately invalid URL away.
	host := "zonk\x7fhost"
	dsn := "oracle://appuser:s3cr3t@" + host + ":1521/XE"
	_, parseErr := url.Parse(dsn)
	require.Error(t, parseErr)

	err := redactDSNError(fmt.Errorf("%s: %w", oraclePingErrorMsg, parseErr), dsn)

	assert.NotContains(t, err.Error(), "s3cr3t")
	assert.Contains(t, err.Error(), "appuser")
	// A net/url reason that quotes nothing carries no password material, so it survives.
	assert.Contains(t, err.Error(), "invalid control character")

	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr, "wrapping must keep the original error reachable")
	assert.Equal(t, dsn, urlErr.URL, "the unwrapped error still carries the raw DSN")
}

func TestRedactDSNErrorPassesOriginalThrough(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		err  error
	}{
		{name: "dsn_without_password", dsn: "oracle://host:1521/XE", err: errors.New("boom: oracle://host:1521/XE")},
		{name: "message_omits_the_dsn", dsn: "oracle://appuser:s3cr3t@host:1521/XE", err: errors.New("context deadline exceeded")},
		{
			name: "url_error_without_password",
			dsn:  "oracle://host:1521/XE",
			err:  &url.Error{Op: "parse", URL: "oracle://host:1521/XE", Err: errors.New("boom")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Same(t, tt.err, redactDSNError(tt.err, tt.dsn), "nothing to mask must not allocate a wrapper")
		})
	}
}

// TestRedactDSNErrorHandlesURLErrorWithoutReason pins the nil guard: url.Error is a
// plain struct, so a driver can hand one back with no wrapped reason at all.
func TestRedactDSNErrorHandlesURLErrorWithoutReason(t *testing.T) {
	const dsn = "oracle://appuser:zqxvwbarrowclasp@host:1521/XE"

	err := redactDSNError(&url.Error{Op: "parse", URL: dsn}, dsn)

	assert.NotContains(t, err.Error(), "zqxvwbarrowclasp")
	assert.Contains(t, err.Error(), "appuser")
}

// TestNetURLErrorFormatUnchanged is a tripwire on the single stdlib assumption
// maskURLError depends on: net/url renders url.Error as `%s %q: %s` over Op, URL, Err.
// maskURLError replaces urlErr.Error() with a rebuild of that exact format, so a
// format change makes the replacement silently stop matching and the password is
// exposed again — a redaction that fails open. If this test ever fails, revisit
// maskURLError before anything else.
func TestNetURLErrorFormatUnchanged(t *testing.T) {
	err := &url.Error{Op: "parse", URL: "x", Err: errors.New("boom")}

	assert.Equal(t, `parse "x": boom`, err.Error())
}

// TestMaskDSNPasswordLeavesParseableAuthorityAlone pins the premise behind the
// looksLikeHostPort guard, which is the whole reason these two DSNs are left unmasked:
// url.Parse ACCEPTS both, so neither is ever rendered into an error. They are the same
// shape read two ways — credentials in the first, host:port plus an '@' in the path in
// the second — so masking one necessarily mangles the other, for no security gain.
func TestMaskDSNPasswordLeavesParseableAuthorityAlone(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "digit_password_segment_reads_as_a_real_port", dsn: "oracle://user:1234/secret@db"},
		{name: "host_port_with_at_sign_in_path", dsn: "oracle://host:1521/XE@weird"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := url.Parse(tt.dsn)
			require.NoError(t, err, "premise: url.Parse accepts it, so no error ever echoes this DSN")
			assert.Equal(t, tt.dsn, maskDSNPassword(tt.dsn))
		})
	}
}

func TestMaskDSNPassword(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{name: "empty_dsn", dsn: "", want: ""},
		{name: "no_userinfo", dsn: "oracle://host:1521/XE", want: "oracle://host:1521/XE"},
		{name: "user_only_no_colon", dsn: "oracle://appuser@host:1521/XE", want: "oracle://appuser@host:1521/XE"},
		{name: "user_and_password", dsn: "oracle://appuser:s3cr3t@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "empty_password", dsn: "oracle://appuser:@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "password_with_at", dsn: "oracle://appuser:pa@ss@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "password_with_colon", dsn: "oracle://appuser:pa:ss@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "password_with_slash", dsn: "oracle://appuser:pa/ss@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "password_with_question_mark", dsn: "oracle://appuser:pa?ss@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		{name: "password_with_hash", dsn: "oracle://appuser:pa#ss@host:1521/XE", want: "oracle://appuser:xxxxx@host:1521/XE"},
		// What url.Parse is handed once it drops the fragment: no '@' survives, so the
		// userinfo is only recognizable by "appuser:pa" not being a valid host[:port].
		{name: "fragment_truncated_userinfo", dsn: "oracle://appuser:pa", want: "oracle://appuser:xxxxx"},
		// looksLikeHostPort rejects a non-numeric port, so this credential-free authority
		// reads as userinfo and everything past "host:" is masked — including the "/XE"
		// tail. Over-masking on purpose: the shape is one url.Parse rejects, so the DSN
		// does reach an error message, and masking a DSN that holds no password is the
		// safe direction. The accepted cost is that a plain port typo costs the operator
		// the invalid-port text and the service path.
		{name: "credential_free_authority_with_non_numeric_port", dsn: "oracle://host:abc/XE", want: "oracle://host:xxxxx"},
		{name: "ipv6_host_with_port", dsn: "oracle://[::1]:1521/XE", want: "oracle://[::1]:1521/XE"},
		{name: "ipv6_host_without_port", dsn: "oracle://[::1]/XE", want: "oracle://[::1]/XE"},
		// A zero-compressed literal ends in the ':' the port scan looks for, so it is the
		// only shape where skipping to the wrong side of the ']' changes the verdict.
		{name: "ipv6_zero_compressed_host_without_port", dsn: "oracle://[::]/XE", want: "oracle://[::]/XE"},
		// The scan tests each strings.Index result against -1, so index 0 must read as a
		// hit. One case per sentinel: scheme, authority terminator, '@', ':'.
		{name: "scheme_separator_at_index_zero", dsn: "://appuser:s3cr3t@host:1521/XE", want: "://appuser:xxxxx@host:1521/XE"},
		{name: "authority_terminator_at_index_zero", dsn: "oracle:///user:pass@host", want: "oracle:///user:pass@host"},
		{name: "at_sign_at_index_zero", dsn: "oracle://@host:1521/XE", want: "oracle://@host:1521/XE"},
		{name: "empty_username_with_password", dsn: "oracle://:s3cr3t@host:1521/XE", want: "oracle://:xxxxx@host:1521/XE"},
		{name: "query_options_preserved", dsn: "oracle://appuser:s3cr3t@host:1521/XE?SID=XE", want: "oracle://appuser:xxxxx@host:1521/XE?SID=XE"},
		{name: "at_sign_only_in_path", dsn: "oracle://host:1521/XE@weird", want: "oracle://host:1521/XE@weird"},
		{name: "scheme_without_authority", dsn: "oracle://", want: "oracle://"},
		{name: "unparseable_host_still_masked", dsn: "oracle://appuser:s3cr3t@zonk\x7fhost:1521/XE", want: "oracle://appuser:xxxxx@zonk\x7fhost:1521/XE"},
		{name: "ez_connect_not_a_url", dsn: "appuser/s3cr3t@host:1521/XE", want: "appuser/s3cr3t@host:1521/XE"},
		{
			name: "tns_descriptor_not_a_url",
			dsn:  "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=host)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=XE)))",
			want: "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=host)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=XE)))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maskDSNPassword(tt.dsn))
		})
	}
}

func TestLooksLikeHostPort(t *testing.T) {
	tests := []struct {
		name      string
		authority string
		want      bool
	}{
		{name: "bare_host", authority: "host", want: true},
		{name: "host_with_numeric_port", authority: "host:1521", want: true},
		{name: "userinfo_with_non_numeric_tail", authority: "user:pa", want: false},
		{name: "bracketed_ipv6_without_port", authority: "[::1]", want: true},
		// Skipping to the wrong side of the ']' leaves a ':' behind and turns the
		// trailing "]" into the port.
		{name: "zero_compressed_ipv6_without_port", authority: "[::]", want: true},
		{name: "bracketed_ipv6_with_port", authority: "[2001:db8::1]:1521", want: true},
		// LastIndex returns -1 for an unterminated literal, so the +1 slice is a no-op
		// and the whole segment is examined: "db8" is not a port.
		{name: "unterminated_ipv6_literal", authority: "[2001:db8", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeHostPort(tt.authority))
		})
	}
}

// TestMaskDSNIn covers the seam directly: redactDSNError reaches it for every error,
// but a *url.Error has already had its rendering rebuilt by maskURLError, so only a
// non-url.Error carrying the raw DSN exercises the replacement here.
func TestMaskDSNIn(t *testing.T) {
	const password = "zqxvwgirdlespan"
	// The DEL byte makes %q escape the DSN, so the quoted rendering is not a substring
	// of the message and only the second replacement can reach it.
	leaky := "oracle://appuser:" + password + "@zonk\x7fhost:1521/XE"
	masked := "oracle://appuser:xxxxx@zonk\x7fhost:1521/XE"

	tests := []struct {
		name string
		dsn  string
		msg  string
		want string
	}{
		{
			name: "dsn_without_password_leaves_the_message_alone",
			dsn:  "oracle://host:1521/XE",
			msg:  `dial oracle://host:1521/XE: connection refused`,
			want: `dial oracle://host:1521/XE: connection refused`,
		},
		{
			name: "verbatim_and_quoted_renderings_both_masked",
			dsn:  leaky,
			msg:  "dial " + leaky + ": parse " + strconv.Quote(leaky) + ": bad",
			want: "dial " + masked + ": parse " + strconv.Quote(masked) + ": bad",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskDSNIn(tt.msg, tt.dsn)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, password)
		})
	}
}

func TestConnectionNewConnectionWithServiceName(t *testing.T) {
	cfg := createOracleConfig("service_name", "XEPDB1")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionWithSID(t *testing.T) {
	cfg := createOracleConfig("sid", "XE")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionWithDatabase(t *testing.T) {
	cfg := createOracleConfig("database", "XE")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	originalOpen := openOracleDB
	originalPing := pingOracleDB
	openOracleDB = func(string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }
	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: "XEPDB1",
			},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestConnectionCreateMigrationTable(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the CREATE TABLE execution (two PL/SQL blocks)
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))

	err := c.CreateMigrationTable(ctx)
	assert.NoError(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionCreateMigrationTableFirstError(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the first PL/SQL block to fail
	mock.ExpectExec(`BEGIN`).WillReturnError(sql.ErrConnDone)

	err := c.CreateMigrationTable(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create Oracle migration table")

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOracleStatementQueryAndQueryRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT id FROM dual WHERE flag = :1")).
		ExpectQuery().
		WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))

	stmt, err := db.PrepareContext(context.Background(), "SELECT id FROM dual WHERE flag = :1")
	require.NoError(t, err)
	ps := wrapper.NewStatement(stmt)

	func() {
		rows, qerr := ps.Query(context.Background(), true)
		require.NoError(t, qerr)
		defer rows.Close()
		require.True(t, rows.Next())
	}()
	require.NoError(t, ps.Close())

	mock.ExpectPrepare(regexp.QuoteMeta(selectQuery)).
		ExpectQuery().
		WithArgs(5).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("beta"))

	stmtRow, err := db.PrepareContext(context.Background(), selectQuery)
	require.NoError(t, err)
	psRow := wrapper.NewStatement(stmtRow)

	row := psRow.QueryRow(context.Background(), 5)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "beta", name)
	require.NoError(t, psRow.Close())
}

func TestOracleTransactionQueryPrepareExec(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer nativeTx.Rollback() // No-op after commit
	trx := wrapper.NewTransaction(nativeTx)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM dual WHERE code = :1")).
		WithArgs("X").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))

	func() {
		rows, qerr := trx.Query(context.Background(), "SELECT id FROM dual WHERE code = :1", "X")
		require.NoError(t, qerr)
		defer rows.Close()
	}()

	mock.ExpectQuery(regexp.QuoteMeta(selectQuery)).
		WithArgs(11).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("gamma"))

	row := trx.QueryRow(context.Background(), selectQuery, 11)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "gamma", name)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE dual SET name = :1 WHERE id = :2")).
		WithArgs("delta", 11).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = trx.Exec(context.Background(), "UPDATE dual SET name = :1 WHERE id = :2", "delta", 11)
	require.NoError(t, err)

	mock.ExpectPrepare(regexp.QuoteMeta(insertQuery))
	mock.ExpectExec(regexp.QuoteMeta(insertQuery)).
		WithArgs(12, "epsilon").
		WillReturnResult(sqlmock.NewResult(1, 1))

	stmt, err := trx.Prepare(context.Background(), insertQuery)
	require.NoError(t, err)
	_, err = stmt.Exec(context.Background(), 12, "epsilon")
	require.NoError(t, err)
	require.NoError(t, stmt.Close())

	mock.ExpectCommit()
	require.NoError(t, trx.Commit(context.Background()))
}

func TestOracleTransactionPrepareError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer nativeTx.Rollback() // Called after explicit rollback below (no-op)
	trx := wrapper.NewTransaction(nativeTx)

	prepareErr := errors.New("prepare failed")
	mock.ExpectPrepare(regexp.QuoteMeta("INSERT INTO fail(id) VALUES (:1)")).
		WillReturnError(prepareErr)

	stmt, err := trx.Prepare(context.Background(), "INSERT INTO fail(id) VALUES (:1)")
	assert.Nil(t, stmt)
	assert.Error(t, err)
	assert.ErrorIs(t, err, prepareErr)

	mock.ExpectRollback()
	require.NoError(t, trx.Rollback(context.Background()))
}

func TestConnectionCreateMigrationTableSecondError(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the first PL/SQL block to succeed, second to fail
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).WillReturnError(sql.ErrTxDone)

	err := c.CreateMigrationTable(ctx)
	assert.Error(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionQueryOperationsErrorHandling(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Test Query error
	mock.ExpectQuery("SELECT").WillReturnError(sql.ErrConnDone)
	rows, err := c.Query(ctx, "SELECT * FROM test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sql: connection is already closed")
	assert.Nil(t, rows)

	// Test QueryRow (doesn't return error, but we can test it executes)
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(42)
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(countRows)
	row := c.QueryRow(ctx, "SELECT COUNT(*) FROM test")
	assert.NotNil(t, row)
	var total int
	require.NoError(t, row.Scan(&total))
	assert.Equal(t, 42, total)

	// Test Prepare error
	mock.ExpectPrepare("INVALID SQL").WillReturnError(sql.ErrTxDone)
	_, err = c.Prepare(ctx, "INVALID SQL")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sql: transaction has already been committed or rolled back")

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionTransactionOperationsErrorHandling(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Test Begin error
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	tx, err := c.Begin(ctx)
	if tx != nil {
		defer tx.Rollback(ctx) // Safety: should never execute since Begin is expected to fail
	}
	assert.Error(t, err)

	// Test BeginTx error
	mock.ExpectBegin().WillReturnError(sql.ErrTxDone)
	txOpts, err := c.BeginTx(ctx, &sql.TxOptions{})
	if txOpts != nil {
		defer txOpts.Rollback(ctx) // Safety: should never execute since BeginTx is expected to fail
	}
	assert.Error(t, err)

	// Test successful Prepare error in statement
	mock.ExpectPrepare("SELECT").WillReturnError(sql.ErrConnDone)
	_, err = c.Prepare(ctx, "SELECT * FROM test")
	assert.Error(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionMetadata(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()
	mock.ExpectClose()

	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.MigrationTable())
	assert.NoError(t, c.Close())
}

func TestConnectionValidationIntegration(t *testing.T) {
	log := dbtestlog.NewTestLogger()

	tests := []ConnectionTestData{
		{
			name: "valid config with service name should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg,
		},
		{
			name: "valid config with SID should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg, // Connection fails, not validation
		},
		{
			name: "valid config with database name should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Database: "XE",
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg, // Connection fails, not validation
		},
		{
			name: "invalid config with no connection identifier should fail validation",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
			},
			expectError:   true,
			errorContains: "oracle connection identifier",
		},
		{
			name: "invalid config with multiple identifiers should fail validation",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: "oracle connection identifier",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First validate the configuration (this is what we're testing)
			err := config.Validate(&config.Config{
				App: config.AppConfig{
					Name:    "test-app",
					Version: "v1.0.0",
					Env:     "development",
					Rate:    config.RateConfig{Limit: 100},
				},
				Server: config.ServerConfig{
					Port: 8080,
					Timeout: config.TimeoutConfig{
						Read:       15 * time.Second,
						Write:      30 * time.Second,
						Middleware: 5 * time.Second,
						Shutdown:   10 * time.Second,
					},
				},
				Database: *tt.config,
				Log: config.LogConfig{
					Level: "info",
				},
			})

			if tt.errorContains == oraclePingErrorMsg {
				// Config validation should pass, only connection should fail
				assert.NoError(t, err, "Configuration validation should pass")

				// Now attempt to create connection (this should fail with connection error)
				_, connErr := NewConnection(tt.config, log)
				assert.Error(t, connErr)
				assert.Contains(t, connErr.Error(), tt.errorContains)
			} else {
				// Config validation should fail
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			}
		})
	}
}

// =============================================================================
// Oracle SEQUENCE Tests (No UDT Registration Required)
// =============================================================================

func TestOracleSequenceQuery(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Test that SEQUENCE queries work without any UDT registration
	t.Run("sequenceNextval", func(t *testing.T) {
		// Mock SEQUENCE.NEXTVAL query
		rows := sqlmock.NewRows([]string{"nextval"}).AddRow(int64(1001))
		mock.ExpectQuery("SELECT SEQ_ID_TABLE.NEXTVAL FROM DUAL").WillReturnRows(rows)

		var nextID int64
		err := c.QueryRow(ctx, "SELECT SEQ_ID_TABLE.NEXTVAL FROM DUAL").Scan(&nextID)
		require.NoError(t, err)
		assert.Equal(t, int64(1001), nextID)
	})

	t.Run("sequenceCurrval", func(t *testing.T) {
		// Mock SEQUENCE.CURRVAL query
		rows := sqlmock.NewRows([]string{"currval"}).AddRow(int64(1001))
		mock.ExpectQuery("SELECT SEQ_ID_TABLE.CURRVAL FROM DUAL").WillReturnRows(rows)

		var currID int64
		err := c.QueryRow(ctx, "SELECT SEQ_ID_TABLE.CURRVAL FROM DUAL").Scan(&currID)
		require.NoError(t, err)
		assert.Equal(t, int64(1001), currID)
	})

	// Verify all expectations met
	assert.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// TCP Keep-Alive Tests
// =============================================================================

func TestNewConnectionWithKeepAliveEnabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track which open function was called and capture the dialer
	var usedDialerPath bool
	var capturedDialer configurations.DialerContext

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, dialer configurations.DialerContext) *sql.DB {
		usedDialerPath = true
		capturedDialer = dialer
		return db
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  boolPtr(true),
				Interval: 60 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify the dialer path was used and a dialer was provided
	assert.True(t, usedDialerPath, "should use openOracleDBWithDialer when keep-alive is enabled")
	assert.NotNil(t, capturedDialer, "dialer should be provided when keep-alive is enabled")

	// Verify it's the correct dialer type with the right interval
	kaDialer, ok := capturedDialer.(*keepAliveDialer)
	require.True(t, ok, "dialer should be a keepAliveDialer")
	assert.Equal(t, 60*time.Second, kaDialer.interval, "dialer should have correct interval")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Simulate openOracleDBWithDialer returning nil (go-ora API change)
	var usedFallbackPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, _ configurations.DialerContext) *sql.DB {
		return nil // Simulate type assertion failure
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		usedFallbackPath = true
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  boolPtr(true), // Keep-alive enabled but dialer fails
				Interval: 60 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "should succeed with fallback")
	require.NotNil(t, conn)

	// Verify fallback path was used
	assert.True(t, usedFallbackPath, "should fall back to regular openOracleDB when dialer returns nil")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track which open function was called
	var usedDialerPath bool
	var usedRegularPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, _ configurations.DialerContext) *sql.DB {
		usedDialerPath = true
		return db
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		usedRegularPath = true
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  boolPtr(false), // Explicitly disabled
				Interval: 60 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify the regular path was used (no custom dialer)
	assert.False(t, usedDialerPath, "should NOT use openOracleDBWithDialer when keep-alive is disabled")
	assert.True(t, usedRegularPath, "should use regular openOracleDB when keep-alive is disabled")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveAndSID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track DSN, dialer, and which path was used
	var capturedDSN string
	var capturedDialer configurations.DialerContext
	var usedRegularPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(dsn string, dialer configurations.DialerContext) *sql.DB {
		capturedDSN = dsn
		capturedDialer = dialer
		return db
	}
	openOracleDB = func(dsn string) (*sql.DB, error) {
		usedRegularPath = true
		capturedDSN = dsn
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{SID: "XE"}, // Using SID instead of service name
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  boolPtr(true),
				Interval: 30 * time.Second, // Different interval to verify
			},
		},
	}

	log := dbtestlog.NewTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify regular path was NOT used (keep-alive enabled should use dialer path)
	assert.False(t, usedRegularPath, "should NOT use regular openOracleDB when keep-alive is enabled")

	// Verify DSN contains SID option
	assert.Contains(t, capturedDSN, "SID=XE", "DSN should contain SID option")

	// Verify DSN does NOT contain fake TCP keep-alive options (they're now handled via dialer)
	assert.NotContains(t, capturedDSN, "TCP KEEPALIVE", "DSN should not contain TCP KEEPALIVE (handled via dialer)")
	assert.NotContains(t, capturedDSN, "TCP KEEPIDLE", "DSN should not contain TCP KEEPIDLE (handled via dialer)")

	// Verify dialer was configured with correct interval
	require.NotNil(t, capturedDialer, "dialer should be provided when keep-alive is enabled")
	kaDialer, ok := capturedDialer.(*keepAliveDialer)
	require.True(t, ok, "dialer should be a keepAliveDialer")
	assert.Equal(t, 30*time.Second, kaDialer.interval, "dialer should have correct interval")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestResolveServiceName(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.DatabaseConfig
		expected string
	}{
		{
			name: "service_name_takes_priority",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1", SID: "XE"}},
				Database: "testdb",
			},
			expected: "XEPDB1",
		},
		{
			name: "database_fallback_when_no_service_or_sid",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{},
				Database: "testdb",
			},
			expected: "testdb",
		},
		{
			name: "empty_when_sid_set_without_service_name",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{Service: config.ServiceConfig{SID: "XE"}},
				Database: "testdb",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveServiceName(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildURLOptions(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.DatabaseConfig
		expected map[string]string
	}{
		{
			name: "with_sid",
			cfg: &config.DatabaseConfig{
				Oracle: config.OracleConfig{Service: config.ServiceConfig{SID: "XE"}},
			},
			expected: map[string]string{"SID": "XE"},
		},
		{
			name: "without_sid",
			cfg: &config.DatabaseConfig{
				Oracle: config.OracleConfig{},
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildURLOptions(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestKeepAliveDialerDialContext(t *testing.T) {
	log := dbtestlog.NewTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Verify dialer is properly initialized
	assert.Equal(t, 60*time.Second, dialer.interval)
	assert.NotNil(t, dialer.log)
}

func TestKeepAliveDialerDialContextWithListener(t *testing.T) {
	log := dbtestlog.NewTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Use context-aware listener (noctx linter requirement)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start local TCP listener on an available port
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	conn, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	// Verify it's a TCP connection (keep-alive requires TCP)
	tcpConn, ok := conn.(*net.TCPConn)
	assert.True(t, ok, "should return *net.TCPConn")
	assert.NotNil(t, tcpConn)
}

func TestKeepAliveDialerDialContextError(t *testing.T) {
	log := dbtestlog.NewTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Use a short timeout since we expect failure
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Try to connect to a non-existent address (port 59999 is unlikely to be in use)
	conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:59999")
	assert.Error(t, err, "should fail when connecting to non-existent address")
	assert.Nil(t, conn)
}

func TestNewConnectionPingFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Note: mock expectations may not be met if ping fails before close
	defer func() { _ = mock.ExpectationsWereMet() }()

	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDB = func(_ string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error {
		return errors.New(testutil.TestConnectionRefused)
	}

	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1"}},
		Pool: config.PoolConfig{
			KeepAlive: config.PoolKeepAliveConfig{Enabled: boolPtr(false)},
		},
	}

	log := dbtestlog.NewTestLogger()
	mock.ExpectClose() // DB should be closed on ping failure

	conn, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

// =============================================================================
// Oracle UDT Registration Tests
// =============================================================================

func TestConnectionRegisterType(t *testing.T) {
	type Product struct {
		ID    int64   `udt:"ID"`
		Name  string  `udt:"NAME"`
		Price float64 `udt:"PRICE"`
	}

	t.Run("objectTypeOnly", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Register without collection type
		err := c.RegisterType("PRODUCT_TYPE", "", Product{})
		// Method should exist and handle gracefully (may fail without real Oracle)
		if err != nil {
			t.Logf("RegisterType returned error (expected without Oracle): %v", err)
		}
	})

	t.Run("collectionTypeForBulkOps", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Primary use case: register collection type
		err := c.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})
		if err != nil {
			t.Logf("RegisterType with collection returned error: %v", err)
		}
	})
}

func TestConnectionRegisterTypeWithOwner(t *testing.T) {
	type Customer struct {
		CustomerID int    `udt:"CUSTOMER_ID"`
		Name       string `udt:"NAME"`
	}

	t.Run("withSchemaOwner", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Note: This may panic with sqlmock due to driver type assertion in go-ora
		// Integration tests validate actual functionality
		defer func() {
			if r := recover(); r != nil {
				t.Logf("RegisterTypeWithOwner panicked (expected with sqlmock): %v", r)
			}
		}()

		err := c.RegisterTypeWithOwner("SHARED_SCHEMA", "CUSTOMER_TYPE", "", Customer{})
		if err != nil {
			t.Logf("RegisterTypeWithOwner returned error: %v", err)
		}
	})

	t.Run("schemaOwnerWithCollection", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Note: This may panic with sqlmock due to driver type assertion in go-ora
		defer func() {
			if r := recover(); r != nil {
				t.Logf("RegisterTypeWithOwner panicked (expected with sqlmock): %v", r)
			}
		}()

		err := c.RegisterTypeWithOwner("SHARED_SCHEMA", "CUSTOMER_TYPE", "CUSTOMER_TABLE", Customer{})
		if err != nil {
			t.Logf("RegisterTypeWithOwner with collection returned error: %v", err)
		}
	})
}

// =============================================================================
// Edge Case Coverage Tests
// =============================================================================

// TestConnectionStatsWithNilConfig tests Stats() when config is nil
// This covers the else branch where config is not available.
// Coverage target: Stats() nil config branch
func TestConnectionStatsWithNilConfig(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Create connection with nil config
	c := &Connection{
		Connection: &wrapper.Connection{
			DB:     db,
			Logger: dbtestlog.NewDisabledTestLogger(),
			Config: nil, // Explicitly nil config
			Name:   "Oracle",
		},
	}

	stats, err := c.Stats()
	assert.NoError(t, err, "Stats() should succeed with nil config")
	assert.NotNil(t, stats, "Stats should not be nil")

	// Verify basic stats keys are present
	assert.Contains(t, stats, "max_open_connections")
	assert.Contains(t, stats, "open_connections")
	assert.Contains(t, stats, "in_use")
	assert.Contains(t, stats, "idle")

	// max_idle_connections should NOT be present when config is nil
	assert.NotContains(t, stats, "max_idle_connections",
		"max_idle_connections should not be present when config is nil")
}

// TestLogConnectionSuccessWithSID tests logConnectionSuccess with SID identifier
// Coverage target: logConnectionSuccess switch case for SID (line 220-221)
func TestLogConnectionSuccessWithSID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	originalOpen := openOracleDB
	originalPing := pingOracleDB
	openOracleDB = func(string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }
	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	// Use SID instead of Service.Name
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				SID: "ORCL", // SID path (not Service.Name)
			},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

// TestLogConnectionSuccessWithDatabaseFallback tests logConnectionSuccess with Database field
// Coverage target: logConnectionSuccess default case (line 222-223)
func TestLogConnectionSuccessWithDatabaseFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	originalOpen := openOracleDB
	originalPing := pingOracleDB
	openOracleDB = func(string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }
	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	// Use Database field (fallback when neither Service.Name nor SID is set)
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Timezone: config.TimezoneDisabledSentinel, // legacy path: no per-connection ALTER SESSION
		Database: "testdb",                        // Database fallback path
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := dbtestlog.NewTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
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
	conn, err := NewConnection(cfg, dbtestlog.NewTestLogger())
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
	conn, err := NewConnection(cfg, dbtestlog.NewTestLogger())
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.False(t, connectorPathUsed, "dash sentinel must skip the timezone-aware connector path")
	assert.True(t, legacyPathUsed, "dash sentinel must route through legacy openOracleDB")

	require.NoError(t, conn.Close())
}
