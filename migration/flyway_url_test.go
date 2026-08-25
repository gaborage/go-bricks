package migration

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const testAppName = "billing-api"

func pgURLConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{
		Type: config.PostgreSQL, Host: "db.internal", Port: 5432,
		Username: "migrator", Password: "long-enough-password", Database: "billing",
	}
}

func TestBuildPostgresJDBCURLCarriesTLSParams(t *testing.T) {
	db := pgURLConfig()
	db.TLS.Mode = "verify-full"
	db.TLS.CAFile = "/etc/ssl/ca.pem"

	got, err := buildPostgresJDBCURL(db, testAppName, db.TLS.Mode)

	require.NoError(t, err)
	assert.Equal(t,
		"jdbc:postgresql://db.internal:5432/billing?ApplicationName=billing-api&sslmode=verify-full&sslrootcert=%2Fetc%2Fssl%2Fca.pem",
		got)
}

func TestBuildPostgresJDBCURLWithoutTLS(t *testing.T) {
	got, err := buildPostgresJDBCURL(pgURLConfig(), testAppName, "")

	require.NoError(t, err)
	assert.Equal(t, "jdbc:postgresql://db.internal:5432/billing?ApplicationName=billing-api", got)
	assert.NotContains(t, got, urlParamSSLMode)
	assert.NotContains(t, got, urlParamSSLRootCert)
}

func TestBuildPostgresJDBCURLEncodesApplicationName(t *testing.T) {
	got, err := buildPostgresJDBCURL(pgURLConfig(), "billing api/v2 & co", "")

	require.NoError(t, err)
	assert.Contains(t, got, "ApplicationName=billing+api%2Fv2+%26+co")
}

func TestBuildPostgresJDBCURLOmitsApplicationNameWhenUnset(t *testing.T) {
	got, err := buildPostgresJDBCURL(pgURLConfig(), "", "")

	require.NoError(t, err)
	assert.Equal(t, "jdbc:postgresql://db.internal:5432/billing", got)
}

// argv is world-readable in the process list, so the URL must never be a second
// home for the credentials the environment already carries.
func TestBuildPostgresJDBCURLCarriesNoCredentials(t *testing.T) {
	db := pgURLConfig()
	db.Username = "migrator-user"
	db.Password = "sup3r-secret-password"
	db.TLS.Mode = "require"

	got, err := buildPostgresJDBCURL(db, testAppName, db.TLS.Mode)

	require.NoError(t, err)
	assert.NotContains(t, got, db.Password)
	assert.NotContains(t, got, db.Username)
	assert.NotContains(t, got, "@")
}

func TestBuildPostgresJDBCURLRejectsMTLS(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*config.DatabaseConfig)
	}{
		{name: "certfile_only", apply: func(c *config.DatabaseConfig) { c.TLS.CertFile = "/etc/ssl/client.crt" }},
		{name: "keyfile_only", apply: func(c *config.DatabaseConfig) { c.TLS.KeyFile = "/etc/ssl/client.key" }},
		{name: "both", apply: func(c *config.DatabaseConfig) {
			c.TLS.CertFile = "/etc/ssl/client.crt"
			c.TLS.KeyFile = "/etc/ssl/client.key"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := pgURLConfig()
			db.TLS.Mode = "verify-full"
			tt.apply(db)

			_, err := buildPostgresJDBCURL(db, testAppName, db.TLS.Mode)

			require.ErrorIs(t, err, ErrMigrationMTLSUnsupported)
			// The message must name the ACTIONABLE fact — that the framework does not
			// forward the pair — rather than a pgjdbc format rule, which is not why
			// this fails.
			assert.Contains(t, err.Error(), "sslcert/sslkey")
		})
	}
}

func TestBuildPostgresJDBCURLAuthority(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{name: "host_and_port", host: "db.internal", port: 5432, want: "jdbc:postgresql://db.internal:5432/billing"},
		{name: "port_omitted", host: "db.internal", port: 0, want: "jdbc:postgresql://db.internal/billing"},
		{name: "ipv6_with_port", host: "::1", port: 5432, want: "jdbc:postgresql://[::1]:5432/billing"},
		{name: "ipv6_without_port", host: "::1", port: 0, want: "jdbc:postgresql://[::1]/billing"},
		{name: "ipv6_prebracketed", host: "[::1]", port: 0, want: "jdbc:postgresql://[::1]/billing"},
		// A bracketed literal WITH a port: net.JoinHostPort brackets anything holding a
		// colon, so passing it through unstripped emitted `[[::1]]:5432`.
		{name: "ipv6_prebracketed_with_port", host: "[::1]", port: 5432, want: "jdbc:postgresql://[::1]:5432/billing"},
		{name: "ipv6_prebracketed_long_with_port", host: "[2001:db8::1]", port: 5432, want: "jdbc:postgresql://[2001:db8::1]:5432/billing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := pgURLConfig()
			db.Host, db.Port = tt.host, tt.port

			got, err := buildPostgresJDBCURL(db, "", db.TLS.Mode)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A negative port took urlAuthority's zero branch and silently connected to
// pgjdbc's default 5432; nothing observed a port above the TCP range either.
// Zero stays the documented unset case.
func TestURLArgsRejectsPortOutsideTCPRange(t *testing.T) {
	for _, port := range []int{-1, -5432, 65536, 99999} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			db := pgURLConfig()
			db.Port = port

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.ErrorIs(t, err, ErrInvalidMigrationPort)
			assert.Nil(t, args)
			assert.NotContains(t, err.Error(), strconv.Itoa(port),
				"the error names the field, never the value")
		})
	}

	// The boundaries that must still pass, so the comparison cannot be widened
	// without a test noticing.
	for _, port := range []int{0, 1, 65535} {
		t.Run(fmt.Sprintf("accepts_port_%d", port), func(t *testing.T) {
			db := pgURLConfig()
			db.Port = port

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.NoError(t, err)
			require.Len(t, args, 1)
		})
	}

	// Zero is unset, not "port 0": the authority carries no port at all.
	t.Run("zero_omits_the_port", func(t *testing.T) {
		db := pgURLConfig()
		db.Port = 0

		args, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.Contains(t, args[0], jdbcPostgresScheme+"db.internal/billing")
	})
}

// buildPostgresJDBCURL copied the mode onto the URL verbatim, so an unsupported
// one reached Flyway instead of failing here. The six accepted are the libpq set
// config.Validate uses, compared exactly — no trimming, no case folding.
func TestURLArgsValidatesTLSMode(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run("accepts_"+mode, func(t *testing.T) {
			db := pgURLConfig()
			db.TLS.Mode = mode

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.NoError(t, err)
			require.Len(t, args, 1)
			assert.Contains(t, args[0], urlParamSSLMode+"="+url.QueryEscape(mode))
		})
	}

	// An unset mode is not a rejected one — it simply puts no sslmode on the URL.
	t.Run("accepts_unset_and_omits_the_param", func(t *testing.T) {
		db := pgURLConfig()
		db.TLS.Mode = ""

		args, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.NotContains(t, args[0], urlParamSSLMode+"=")
	})

	// Whitespace is TRIMMED, not rejected: validateVendorSpecificFields trims the
	// mode before its own exact match, so ` require` is a config the runtime
	// accepts — rejecting it here would make the migrator stricter than the thing
	// it mirrors. The trimmed spelling is what must reach the URL, since an
	// untrimmed one would percent-encode its padding into the sslmode parameter.
	for _, mode := range []string{" require", "require ", "\tverify-full\t"} {
		t.Run("trims_"+strings.TrimSpace(mode), func(t *testing.T) {
			db := pgURLConfig()
			db.TLS.Mode = mode

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.NoError(t, err)
			assert.Contains(t, args[0], urlParamSSLMode+"="+url.QueryEscape(strings.TrimSpace(mode)))
			assert.NotContains(t, args[0], "+"+strings.TrimSpace(mode))
			assert.NotContains(t, args[0], "%20")
		})
	}

	// A mode carrying CR/LF is rejected by validateEnvFields, which runs first and
	// owns that class for every field formatted into argv — not by the mode rule.
	t.Run("newline_belongs_to_the_env_field_guard", func(t *testing.T) {
		db := pgURLConfig()
		db.TLS.Mode = "require\n"

		_, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.ErrorIs(t, err, ErrEnvFieldHasControlChar)
		assert.NotErrorIs(t, err, ErrInvalidMigrationTLSMode)
	})

	// Whitespace-only trims to unset, which is legal and simply omits the param.
	t.Run("whitespace_only_is_unset", func(t *testing.T) {
		db := pgURLConfig()
		db.TLS.Mode = "   "

		args, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.NotContains(t, args[0], urlParamSSLMode+"=")
	})

	// Case is NOT folded on either side, so these stay rejected.
	rejected := []struct{ name, mode string }{
		{name: "bogus", mode: "bogus"},
		{name: "case_variant", mode: "Require"},
		{name: "upper", mode: "VERIFY-FULL"},
		{name: "padded_case_variant", mode: " Require "},
		// pgjdbc splits the query at the first `?`, so a mode smuggling its own
		// separators must not reach the URL either.
		{name: "param_injection", mode: "disable&sslrootcert=/tmp/evil"},
	}
	for _, tt := range rejected {
		t.Run("rejects_"+tt.name, func(t *testing.T) {
			db := pgURLConfig()
			db.TLS.Mode = tt.mode

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.ErrorIs(t, err, ErrInvalidMigrationTLSMode)
			assert.Nil(t, args)
			// The mode is a fixed keyword and is echoed to make the error actionable;
			// nothing else from the config may ride along.
			assert.Contains(t, err.Error(), strings.TrimSpace(tt.mode))
			assert.NotContains(t, err.Error(), db.Password)
			assert.NotContains(t, err.Error(), db.Host)
		})
	}
}

func TestMigrateForRefusesUnsupportedTLSMode(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	for _, mode := range []string{"bogus", "Require", " Require "} {
		t.Run(mode, func(t *testing.T) {
			db := pgURLConfig()
			db.TLS.Mode = mode

			require.ErrorIs(t, runMigrateExpectingRefusal(t, db), ErrInvalidMigrationTLSMode)
		})
	}
}

func TestMigrateForRefusesPortOutsideTCPRange(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	for _, port := range []int{-1, 65536} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			db := pgURLConfig()
			db.Port = port

			require.ErrorIs(t, runMigrateExpectingRefusal(t, db), ErrInvalidMigrationPort)
		})
	}
}

func TestBuildPostgresJDBCURLEscapesDatabaseName(t *testing.T) {
	db := pgURLConfig()
	db.Database = "bill ing?x"

	got, err := buildPostgresJDBCURL(db, "", db.TLS.Mode)

	require.NoError(t, err)
	assert.Equal(t, "jdbc:postgresql://db.internal:5432/bill%20ing%3Fx", got)
}

func TestUsesFrameworkOwnedURL(t *testing.T) {
	tests := []struct {
		name   string
		vendor string
		db     *config.DatabaseConfig
		apply  func(*config.DatabaseConfig)
		want   bool
	}{
		{name: "postgres_discrete_fields", vendor: config.PostgreSQL, db: pgURLConfig(), want: true},
		{name: "nil_config", vendor: config.PostgreSQL, db: nil, want: false},
		{name: "oracle", vendor: config.Oracle, db: &config.DatabaseConfig{
			Type: config.Oracle, Host: "h", Port: 1521, Database: "PDB1",
		}, want: false},
		{name: "unknown_vendor", vendor: "", db: pgURLConfig(), want: false},
		{
			name: "connectionstring", vendor: config.PostgreSQL, db: pgURLConfig(), want: false,
			apply: func(c *config.DatabaseConfig) { c.ConnectionString = "postgres://u:p@h:5432/d" },
		},
		{
			name: "missing_host", vendor: config.PostgreSQL, db: pgURLConfig(), want: false,
			apply: func(c *config.DatabaseConfig) { c.Host = "" },
		},
		{
			name: "missing_database", vendor: config.PostgreSQL, db: pgURLConfig(), want: false,
			apply: func(c *config.DatabaseConfig) { c.Database = "" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.apply != nil {
				tt.apply(tt.db)
			}
			assert.Equal(t, tt.want, usesFrameworkOwnedURL(tt.db, tt.vendor))
		})
	}
}

func TestURLArgsRejectsControlCharacters(t *testing.T) {
	db := pgURLConfig()
	db.Host = "db.internal\n-url=jdbc:postgresql://evil/db"

	_, err := urlArgs(db, config.PostgreSQL, testAppName)

	require.ErrorIs(t, err, ErrEnvFieldHasControlChar)
}

func TestAppNameToleratesNilConfig(t *testing.T) {
	fm := &FlywayMigrator{}
	assert.Empty(t, fm.appName())
}

// newURLTestMigrator is newFlywayMigratorForTest with an app name, which the
// built URL sends as ApplicationName (pg_stat_activity's application_name).
func newURLTestMigrator(t *testing.T) *FlywayMigrator {
	t.Helper()
	fm := newFlywayMigratorForTest(t)
	fm.config.App.Name = testAppName
	return fm
}

func runMigrateCapturingArgs(t *testing.T, fm *FlywayMigrator, db *config.DatabaseConfig) string {
	t.Helper()
	stub, capturePath := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "flyway.conf")
	migrationPath := filepath.Join(tempDir, "migrations")
	require.NoError(t, os.WriteFile(configPath, []byte("flyway.url=jdbc:postgresql://stale/db\n"), 0o644))
	require.NoError(t, os.MkdirAll(migrationPath, 0o755))

	_, err := fm.MigrateFor(context.Background(), db, &Config{
		FlywayPath:    stub,
		ConfigPath:    configPath,
		MigrationPath: migrationPath,
		Timeout:       10 * time.Second,
		Environment:   "test",
	})
	require.NoError(t, err)

	captured, readErr := os.ReadFile(capturePath)
	require.NoError(t, readErr)
	return string(captured)
}

// The acceptance case for #1047: a conf whose URL lacks TLS params can no longer
// produce a cleartext migration, because the framework's -url= outranks it.
func TestMigrateForAppendsFrameworkURLWithTLS(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	db := pgURLConfig()
	db.TLS.Mode = "verify-full"
	db.TLS.CAFile = "/etc/ssl/ca.pem"

	captured := runMigrateCapturingArgs(t, newURLTestMigrator(t), db)

	assert.Contains(t, captured, flagURL+"jdbc:postgresql://db.internal:5432/billing?")
	assert.Contains(t, captured, "sslmode=verify-full")
	assert.Contains(t, captured, "sslrootcert=%2Fetc%2Fssl%2Fca.pem")
	assert.Contains(t, captured, "ApplicationName=billing-api")
	assert.NotContains(t, captured, db.Password)
}

func TestMigrateForAppendsFrameworkURLWithoutTLS(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	captured := runMigrateCapturingArgs(t, newURLTestMigrator(t), pgURLConfig())

	assert.Contains(t, captured, flagURL+"jdbc:postgresql://db.internal:5432/billing?ApplicationName=billing-api")
	assert.NotContains(t, captured, "sslmode=")
}

func TestMigrateForOmitsURLForConfConfigs(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	t.Run("oracle", func(t *testing.T) {
		db := &config.DatabaseConfig{
			Type: config.Oracle, Host: "ora.internal", Port: 1521,
			Username: "u", Password: "long-enough-password", Database: "PDB1",
		}
		captured := runMigrateCapturingArgs(t, newURLTestMigrator(t), db)
		assert.NotContains(t, captured, flagURL)
	})

	t.Run("connectionstring", func(t *testing.T) {
		db := pgURLConfig()
		db.ConnectionString = "postgres://migrator@db.internal:5432/billing"
		captured := runMigrateCapturingArgs(t, newURLTestMigrator(t), db)
		assert.NotContains(t, captured, flagURL)
	})
}

// runMigrateExpectingRefusal drives the per-tenant door with an empty conf and
// returns the error MigrateFor refuses with. The point of going through
// MigrateFor rather than urlArgs is that this door takes a *config.DatabaseConfig
// straight from a dynamic DBConfigProvider or the CLI's tenants.yaml, so it never
// necessarily passed config.Validate.
func runMigrateExpectingRefusal(t *testing.T, db *config.DatabaseConfig) error {
	t.Helper()
	stub, _ := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "flyway.conf")
	migrationPath := filepath.Join(tempDir, "migrations")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(migrationPath, 0o755))

	_, err := newURLTestMigrator(t).MigrateFor(context.Background(), db, &Config{
		FlywayPath:    stub,
		ConfigPath:    configPath,
		MigrationPath: migrationPath,
		Timeout:       10 * time.Second,
		Environment:   "test",
	})
	return err
}

func TestMigrateForRefusesMTLS(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	db := pgURLConfig()
	db.TLS.Mode = "verify-full"
	db.TLS.CertFile = "/etc/ssl/client.crt"
	db.TLS.KeyFile = "/etc/ssl/client.key"

	require.ErrorIs(t, runMigrateExpectingRefusal(t, db), ErrMigrationMTLSUnsupported)
}

// tlsWithConnectionStringShapes are the database.tls spellings that must not
// reach a DSN-owned migration silently. Each field is isolated so a flipped
// disjunct in hasTLSSettings is caught rather than masked by its neighbors.
// dsnConfigWithTLS is the shape both connectionstring+TLS suites drive: a valid
// PostgreSQL block whose discrete fields are overridden by a DSN, plus one TLS
// spelling from the table.
func dsnConfigWithTLS(apply func(*config.DatabaseConfig)) *config.DatabaseConfig {
	db := pgURLConfig()
	db.ConnectionString = "postgres://migrator@db.internal:5432/billing"
	apply(db)
	return db
}

var tlsWithConnectionStringShapes = []struct {
	name  string
	apply func(*config.DatabaseConfig)
}{
	{name: "mode_only", apply: func(c *config.DatabaseConfig) { c.TLS.Mode = "verify-full" }},
	{name: "ca_only", apply: func(c *config.DatabaseConfig) { c.TLS.CAFile = "/etc/ssl/ca.pem" }},
	{name: "cert_only", apply: func(c *config.DatabaseConfig) { c.TLS.CertFile = "/etc/ssl/client.crt" }},
	{name: "key_only", apply: func(c *config.DatabaseConfig) { c.TLS.KeyFile = "/etc/ssl/client.key" }},
	{name: "cert_and_key", apply: func(c *config.DatabaseConfig) {
		c.TLS.CertFile = "/etc/ssl/client.crt"
		c.TLS.KeyFile = "/etc/ssl/client.key"
	}},
}

// config.Validate already refuses database.tls beside a connectionstring
// (ADR-062), but urlArgs must not depend on the caller having run it: the
// per-tenant configs are caller-supplied. Without this the pair fell through to
// the conf-owned deferral and Flyway ran on the DSN with the TLS fields dropped.
func TestURLArgsRejectsTLSWithConnectionString(t *testing.T) {
	for _, tt := range tlsWithConnectionStringShapes {
		t.Run(tt.name, func(t *testing.T) {
			args, err := urlArgs(dsnConfigWithTLS(tt.apply), config.PostgreSQL, testAppName)

			require.ErrorIs(t, err, ErrMigrationTLSWithConnectionString)
			assert.Nil(t, args)
		})
	}

	// The unchanged boundary: a DSN with no database.tls block still defers to the
	// conf-owned URL rather than failing.
	t.Run("connection_string_alone_defers", func(t *testing.T) {
		db := dsnConfigWithTLS(func(*config.DatabaseConfig) {})

		args, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.Nil(t, args)
	})
}

func TestMigrateForRefusesTLSWithConnectionString(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	for _, tt := range tlsWithConnectionStringShapes {
		t.Run(tt.name, func(t *testing.T) {
			err := runMigrateExpectingRefusal(t, dsnConfigWithTLS(tt.apply))

			require.ErrorIs(t, err, ErrMigrationTLSWithConnectionString)
		})
	}
}

// Per-tenant URL construction: the runner hands each tenant its own
// *config.DatabaseConfig, so distinct hosts must reach Flyway as distinct URLs.
func TestMigrateAllBuildsPerTenantURLs(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub, capturePath := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
	fm := newURLTestMigrator(t)
	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: config.PostgreSQL, Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: config.PostgreSQL, Host: "h2", Port: 5433, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
	})

	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: []string{"t1", "t2"}},
		provider,
		ActionMigrate,
		MigrateAllOptions{BaseConfig: makeBaseConfig(t, stub)},
	)

	require.NoError(t, err)
	require.Empty(t, res.Failed())

	captured, readErr := os.ReadFile(capturePath)
	require.NoError(t, readErr)
	lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, string(captured), flagURL+"jdbc:postgresql://h1:5432/d1?ApplicationName=billing-api")
	assert.Contains(t, string(captured), flagURL+"jdbc:postgresql://h2:5433/d2?ApplicationName=billing-api")
}

// An unescaped host escapes the URL authority: `h/?sslmode=disable&x=` ends the
// authority early, and pgjdbc then reads the injected parameters — turning a
// verify-full config into a cleartext connection to a host of the value's
// choosing. The host cannot be percent-encoded (it must stay routable), so it is
// validated instead.
func TestURLArgsRejectsHostThatEscapesTheAuthority(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{name: "query_injection", host: "evilhost/?sslmode=disable&x="},
		{name: "path_separator", host: "evilhost/billing"},
		{name: "bare_question_mark", host: "evilhost?sslmode=disable"},
		{name: "ampersand", host: "evilhost&sslmode=disable"},
		{name: "userinfo", host: "user:pw@evilhost"},
		{name: "fragment", host: "evilhost#x"},
		{name: "space", host: "evil host"},
		{name: "empty_after_brackets", host: "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := pgURLConfig()
			db.Host = tt.host
			db.TLS.Mode = "verify-full"

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.ErrorIs(t, err, ErrInvalidMigrationHost)
			assert.Nil(t, args)
			assert.NotContains(t, err.Error(), tt.host, "error must not echo the host: it can hold a whole DSN")
		})
	}
}

func TestValidateMigrationHostAcceptsRealHosts(t *testing.T) {
	for _, host := range []string{
		"db.internal", "localhost", "db-1.eu-west-1.rds.amazonaws.com", "db.internal.",
		// Underscore is not a URL delimiter, and internal DNS / Docker hostnames use it.
		"my_db_host", "pg_primary.svc.cluster.local",
		"10.0.0.5", "::1", "[::1]", "2001:db8::1", "[2001:db8::1]",
	} {
		t.Run(host, func(t *testing.T) {
			assert.NoError(t, validateMigrationHost(host))
		})
	}
}

// The gate that skips URL construction is three-way. A PARTIALLY filled block (a
// target broken in transit) and any TLS the framework cannot put on a URL both
// fail closed; a block naming nothing at all is conf-owned by construction and
// still defers, alongside Oracle and connectionstring.
func TestURLArgsFailsClosedOnIncompleteTarget(t *testing.T) {
	tests := []struct {
		name    string
		withTLS bool
		apply   func(*config.DatabaseConfig)
	}{
		{name: "partial_empty_host_with_tls", withTLS: true, apply: func(c *config.DatabaseConfig) { c.Host = "" }},
		{name: "partial_empty_database_with_tls", withTLS: true, apply: func(c *config.DatabaseConfig) { c.Database = "" }},
		{name: "partial_empty_host_without_tls", apply: func(c *config.DatabaseConfig) { c.Host = "" }},
		{name: "partial_empty_database_without_tls", apply: func(c *config.DatabaseConfig) { c.Database = "" }},
		// Each of the three URL-target fields is isolated as the ONLY one set, so a
		// flipped comparison on any single disjunct of namesURLTarget is caught: with
		// two of them set, the other two disjuncts mask the mutation.
		{name: "host_only_without_tls", apply: func(c *config.DatabaseConfig) {
			c.Database, c.Port, c.Username, c.Password = "", 0, "", ""
		}},
		{name: "database_only_without_tls", apply: func(c *config.DatabaseConfig) {
			c.Host, c.Port, c.Username, c.Password = "", 0, "", ""
		}},
		{name: "port_only_without_tls", apply: func(c *config.DatabaseConfig) {
			c.Host, c.Database, c.Username, c.Password = "", "", "", ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := pgURLConfig()
			if tt.withTLS {
				db.TLS.Mode = "verify-full"
				db.TLS.CAFile = "/etc/ssl/ca.pem"
			}
			tt.apply(db)

			args, err := urlArgs(db, config.PostgreSQL, testAppName)

			require.ErrorIs(t, err, ErrIncompleteMigrationTarget)
			assert.Nil(t, args)
		})
	}

	// A bare block naming no target at all, but asking for TLS: there is no shape in
	// which a TLS setting silently fails to reach the connection (#1047).
	t.Run("bare_block_with_tls_fails", func(t *testing.T) {
		db := &config.DatabaseConfig{Type: config.PostgreSQL}
		db.TLS.Mode = "verify-full"

		_, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.ErrorIs(t, err, ErrIncompleteMigrationTarget)
	})

	// ADR-085's third boundary: a block carrying only `type` is conf-owned by
	// construction — nothing to build a URL from, and no TLS guarantee to lose.
	t.Run("bare_block_without_tls_defers", func(t *testing.T) {
		args, err := urlArgs(&config.DatabaseConfig{Type: config.PostgreSQL}, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.Nil(t, args)
	})

	// A DSN with no database.tls block is conf-owned and defers, even with the
	// discrete host blanked. With a TLS block it does NOT — see
	// TestURLArgsRejectsTLSWithConnectionString.
	t.Run("connectionstring_without_tls_defers", func(t *testing.T) {
		db := pgURLConfig()
		db.ConnectionString = "postgres://u:p@h:5432/d"
		db.Host = ""

		args, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.NoError(t, err)
		assert.Nil(t, args)
	})

	t.Run("oracle_defers", func(t *testing.T) {
		db := &config.DatabaseConfig{Type: config.Oracle, Host: "", Database: "PDB1"}
		db.TLS.Mode = "verify-full"

		args, err := urlArgs(db, config.Oracle, testAppName)

		require.NoError(t, err)
		assert.Nil(t, args)
	})

	t.Run("error_does_not_echo_config", func(t *testing.T) {
		db := pgURLConfig()
		db.Password = "sup3r-secret-password"
		db.Database = ""

		_, err := urlArgs(db, config.PostgreSQL, testAppName)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), db.Password)
	})
}

func TestNamesURLTarget(t *testing.T) {
	assert.False(t, namesURLTarget(nil))
	assert.False(t, namesURLTarget(&config.DatabaseConfig{Type: config.PostgreSQL}))

	tests := []struct {
		name  string
		apply func(*config.DatabaseConfig)
		want  bool
	}{
		{name: "host", apply: func(c *config.DatabaseConfig) { c.Host = "h" }, want: true},
		{name: "database", apply: func(c *config.DatabaseConfig) { c.Database = "d" }, want: true},
		{name: "port", apply: func(c *config.DatabaseConfig) { c.Port = 5432 }, want: true},
		// Credentials are env-delivered and never reach the URL, so they name no
		// target: a config supplying only a password while flyway.conf owns the URL
		// is the conf-owned shape, not a broken one.
		{name: "username_alone_names_nothing", apply: func(c *config.DatabaseConfig) { c.Username = "u" }},
		{name: "password_alone_names_nothing", apply: func(c *config.DatabaseConfig) { c.Password = "p" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &config.DatabaseConfig{Type: config.PostgreSQL}
			tt.apply(db)
			assert.Equal(t, tt.want, namesURLTarget(db))
		})
	}
}

// The shape 7 existing tests pin: credentials (and maybe a schema) with the URL
// left to flyway.conf. It must still defer, not fail.
func TestURLArgsDefersOnCredentialOnlyConfig(t *testing.T) {
	db := &config.DatabaseConfig{
		Type: config.PostgreSQL, Password: "longenough-pw",
		PostgreSQL: config.PostgreSQLConfig{Schema: "tenant_a"},
	}

	args, err := urlArgs(db, config.PostgreSQL, testAppName)

	require.NoError(t, err)
	assert.Nil(t, args)
}
