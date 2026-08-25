package migration

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/config"
)

// jdbcPostgresScheme prefixes every framework-built PostgreSQL JDBC URL.
const jdbcPostgresScheme = "jdbc:postgresql://"

// URL query parameters the framework sets on the built JDBC URL. These are
// pgjdbc's spellings: `sslmode` and `sslrootcert` match libpq, but the
// application name does NOT — pgjdbc's property is `ApplicationName`
// (PGProperty.APPLICATION_NAME), and its startup-parameter assembly reads a
// WHITELIST of known PGProperty keys, so a libpq-spelled `application_name` in
// the URL is silently dropped rather than forwarded. The server-side column it
// ends up in is still named `application_name`.
const (
	urlParamApplicationName = "ApplicationName"
	urlParamSSLMode         = "sslmode"
	urlParamSSLRootCert     = "sslrootcert"
)

// safeMigrationHostname is the DNS-name grammar a host may use when it is not an
// IP literal: dot-separated labels of letters, digits, hyphen and underscore.
// What it excludes is the point — `/ ? # & = @ : [ ]`, percent-encoding and
// whitespace are the characters that would let a host value escape the URL
// authority. Underscore is admitted deliberately though RFC 1123 omits it: it is
// not a URL delimiter, so it cannot escape anything, and internal DNS and Docker
// hostnames use it, so rejecting it would break real deployments for no gain.
var safeMigrationHostname = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*\.?$`)

// ErrInvalidMigrationHost rejects a database.host that is neither an IP literal
// nor a plain DNS name. The host is the one URL component that cannot be
// percent-encoded (it must stay a routable address), so it is validated instead:
// unescaped, a value like `h/?sslmode=disable&x=` ends the authority early and
// pgjdbc reads the injected parameters, turning a verify-full config into a
// cleartext connection to a host of the value's choosing. The error names the
// field but never echoes it — a misconfigured host can hold a whole DSN,
// password included. Match with errors.Is.
var ErrInvalidMigrationHost = errors.New("migration: database.host is not a valid hostname or IP address")

// ErrIncompleteMigrationTarget rejects a PostgreSQL config the framework cannot
// build a URL from when deferring to the conf would lose a guarantee: either the
// block is PARTIALLY filled (some identity field set, but not a usable host AND
// database — a target broken in transit, e.g. a tenant whose host arrived blank
// from a secret store), or database.tls is set and would silently fail to reach
// the connection, which is the whole of #1047. It covers the DISCRETE-FIELD shapes
// only: a database.tls block beside a connectionstring loses the same guarantee but
// has its own remedy — move the settings into the DSN — so it gets its own sentinel,
// ErrMigrationTLSWithConnectionString. A block naming NO identity field and no TLS
// is conf-owned by construction and still defers. The error names the fields but
// never echoes a value; the per-tenant caller pairs it with TenantResult.TenantID.
// Match with errors.Is.
var ErrIncompleteMigrationTarget = errors.New(
	"migration: PostgreSQL requires database.host and database.database (or database.connectionstring) " +
		"so the framework can build the Flyway JDBC URL; a partially filled database block, or any " +
		"database.tls setting the framework cannot put on a URL, is rejected rather than silently " +
		"deferring to the URL in flyway.conf, which the framework does not read")

// hasTLSSettings reports whether the config asks for TLS at all.
func hasTLSSettings(db *config.DatabaseConfig) bool {
	return db != nil &&
		(db.TLS.Mode != "" || db.TLS.CertFile != "" || db.TLS.KeyFile != "" || db.TLS.CAFile != "")
}

// namesURLTarget reports whether the block names a connection TARGET — exactly the
// three fields that become the URL (host, port, database). A block carrying none
// of them is conf-owned by construction (ADR-085's third boundary); one carrying
// some but not a usable host AND database is partially filled, which is a broken
// target rather than a deliberate hand-off.
//
// Username and password are deliberately NOT counted, though ADR-047 treats them
// as identity markers for the different question of whether a database is intended
// at all. They never appear in the URL — they are env-delivered — so credentials
// beside a conf-owned URL say nothing about where the migration points, and
// counting them would reject the long-standing shape of a config that supplies only
// a password (and perhaps postgresql.schema) while flyway.conf owns the target.
func namesURLTarget(db *config.DatabaseConfig) bool {
	return db != nil &&
		(db.Host != "" || db.Database != "" || db.Port != 0)
}

// validateMigrationHost accepts an IP literal (bracketed or bare) or a plain DNS
// name, and rejects everything else.
func validateMigrationHost(host string) error {
	bare := unbracket(host)
	if bare == "" {
		return ErrInvalidMigrationHost
	}
	if net.ParseIP(bare) != nil {
		return nil
	}
	if safeMigrationHostname.MatchString(host) {
		return nil
	}
	return ErrInvalidMigrationHost
}

// ErrMigrationMTLSUnsupported rejects a migration whose config asks for
// PostgreSQL client-certificate authentication. The limit is OURS, not pgjdbc's:
// the framework does not forward `database.tls.cert`/`key` as the JDBC
// `sslcert`/`sslkey` parameters, so it refuses rather than migrating without the
// client certificate the config asked for. Match with errors.Is.
var ErrMigrationMTLSUnsupported = errors.New(
	"migration: PostgreSQL client-certificate TLS (database.tls.cert/database.tls.key) is not supported for Flyway migrations: " +
		"the framework does not forward them as the JDBC sslcert/sslkey parameters, so it refuses rather than " +
		"migrating without the client certificate; use database.tls.mode + database.tls.ca for server-authenticated TLS, " +
		"or migrate outside the framework")

// ErrMigrationTLSWithConnectionString rejects a PostgreSQL config that sets both
// `database.connectionstring` and `database.tls.*`. The framework does not parse
// DSNs, so it cannot lift the TLS material onto a URL it builds; deferring to the
// conf would run the migration on the DSN with the configured TLS silently
// dropped. config.Validate already refuses this shape (ADR-062), but a per-tenant
// DatabaseConfig can reach MigrateFor without ever passing through it — a dynamic
// DBConfigProvider, or the CLI's tenants.yaml — so the migrator fails closed on
// its own rather than trusting the caller to have validated. Match with errors.Is.
var ErrMigrationTLSWithConnectionString = errors.New(
	"migration: database.tls is set alongside database.connectionstring: the framework does not parse DSNs, " +
		"so the TLS settings cannot reach the migration connection; put sslmode/sslrootcert/sslcert/sslkey in the " +
		"connection string itself and remove the database.tls block")

// usesFrameworkOwnedURL reports whether this run gets a framework-built `-url=`.
// Only PostgreSQL discrete-field configs qualify. Oracle is excluded because it
// has no `database.tls` at all (ADR-062) and no JDBC URL builder here; a
// `connectionstring` config is excluded because the framework does not parse
// DSNs. Both keep the conf-owned URL.
func usesFrameworkOwnedURL(db *config.DatabaseConfig, vendor string) bool {
	return db != nil &&
		vendor == config.PostgreSQL &&
		db.ConnectionString == "" &&
		db.Host != "" &&
		db.Database != ""
}

// buildPostgresJDBCURL assembles the JDBC URL Flyway connects with. It carries
// only the connection target, TLS material, and application_name — never the
// username or password, which stay env-delivered, because argv is world-readable
// in the process list.
func buildPostgresJDBCURL(db *config.DatabaseConfig, appName string) (string, error) {
	if db.TLS.CertFile != "" || db.TLS.KeyFile != "" {
		return "", ErrMigrationMTLSUnsupported
	}

	jdbcURL := jdbcPostgresScheme + urlAuthority(db.Host, db.Port) + "/" + url.PathEscape(db.Database)

	params := url.Values{}
	if appName != "" {
		params.Set(urlParamApplicationName, appName)
	}
	if db.TLS.Mode != "" {
		params.Set(urlParamSSLMode, db.TLS.Mode)
	}
	if db.TLS.CAFile != "" {
		params.Set(urlParamSSLRootCert, db.TLS.CAFile)
	}
	if encoded := params.Encode(); encoded != "" {
		jdbcURL += "?" + encoded
	}

	return jdbcURL, nil
}

// urlAuthority renders host[:port], bracketing an IPv6 literal so the colons in
// the address are not read as the port separator. Brackets are stripped first and
// re-added once: a config may spell the literal either way, and net.JoinHostPort
// brackets anything containing a colon, so passing an already-bracketed host
// straight through would emit `[[::1]]:5432`.
func urlAuthority(host string, port int) string {
	host = unbracket(host)
	if port > 0 {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// unbracket removes one matched pair of surrounding brackets from an IPv6 literal.
func unbracket(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

// urlArgs returns the `-url=` flag for runs the framework owns the URL for, or
// nil for the documented conf-owned cases (Oracle, bare connectionstring). Host and
// database reach argv from here, so validateEnvFields runs before they do — the
// same guard buildEnvironmentVariables applies to the subprocess environment.
func urlArgs(db *config.DatabaseConfig, vendor, appName string) ([]string, error) {
	if !usesFrameworkOwnedURL(db, vendor) {
		// Two ways a conf-owned PostgreSQL run still fails rather than defers. A DSN
		// carries its own TLS, so a database.tls block beside one cannot reach the
		// connection; and a partially filled block is a broken target. Everything else
		// — Oracle, a bare DSN, a block naming no target and no TLS — defers.
		if vendor == config.PostgreSQL && db != nil {
			switch {
			case db.ConnectionString != "":
				if hasTLSSettings(db) {
					return nil, ErrMigrationTLSWithConnectionString
				}
			case namesURLTarget(db) || hasTLSSettings(db):
				return nil, ErrIncompleteMigrationTarget
			}
		}
		return nil, nil
	}
	if err := validateEnvFields(db); err != nil {
		return nil, err
	}
	if err := validateMigrationHost(db.Host); err != nil {
		return nil, err
	}
	jdbcURL, err := buildPostgresJDBCURL(db, appName)
	if err != nil {
		return nil, fmt.Errorf("build flyway jdbc url: %w", err)
	}
	return []string{flagURL + jdbcURL}, nil
}

// appName is the value the built URL sends as ApplicationName, so a DBA watching
// pg_stat_activity sees the migrating service by name in its application_name.
func (fm *FlywayMigrator) appName() string {
	if fm.config == nil {
		return ""
	}
	return fm.config.App.Name
}
