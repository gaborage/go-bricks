package migration

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
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
// has its own remedy — the DSN for the runtime, flyway.conf for the migration —
// so it gets its own sentinel,
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

// ErrInvalidMigrationPort rejects a database.port outside the TCP range. Zero is
// the documented "unset" case — urlAuthority omits the port and the driver takes
// its default — but a NEGATIVE port took that same branch, so a config that
// clearly meant a port silently connected to pgjdbc's 5432 instead, and nothing
// observed a port above 65535 either. config.validateOptionalDatabasePort applies
// the same `< 0 || > 65535` rule with the same zero-is-unset carve-out; this is
// the migrator's own copy, for the per-tenant configs that never passed it. The
// error names the field but never echoes the value. Match with errors.Is.
var ErrInvalidMigrationPort = errors.New("migration: database.port must be between 1 and 65535, or 0 for the driver default")

// supportedMigrationTLSModes mirrors config's unexported pgSSLModes — the libpq
// sslmode set, which is what ADR-062 validates database.tls.mode against. The
// list is shared rather than coincidental: pgx accepts these six and so does
// pgjdbc, which is the driver on the other end of the URL built here, so
// mirroring the runtime's set is correct for a JDBC URL and not merely close
// enough. The mirror extends to the NORMALIZATION, not just the set:
// validateVendorSpecificFields TrimSpaces the mode before its own exact match,
// so ` require` is a config the runtime accepts. Rejecting it here would make
// the migrator stricter than the thing it claims to mirror — a real config,
// whitespace off a templated secret, failing only its migration. Case is NOT
// folded on either side, so `Require` is rejected in both.
const (
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"
)

var supportedMigrationTLSModes = []string{"disable", "allow", "prefer", "require", sslModeVerifyCA, sslModeVerifyFull}

// ErrInvalidMigrationTLSMode rejects a database.tls.mode that is not one of the
// libpq set once surrounding whitespace is trimmed.
// buildPostgresJDBCURL copies the mode onto the URL verbatim, so an unsupported
// one reached Flyway and failed inside the driver — or, worse, was ignored by it
// — instead of failing here as a typed error. config.Validate already refuses it
// (ADR-062); this is the migrator's own copy, for the per-tenant configs from a
// dynamic DBConfigProvider or the CLI's tenants.yaml that never passed it, the
// same reason the host, port and connectionstring rules have one. The offending
// MODE is echoed — it is a fixed keyword, not caller data — but nothing else
// from the config is, since a database block carries a password and a DSN.
// Match with errors.Is.
var ErrInvalidMigrationTLSMode = errors.New("migration: database.tls.mode is not a supported sslmode")

// validateMigrationTLSMode accepts an unset mode and any of the libpq six, and
// returns the mode trimmed — the spelling that must reach the URL, since an
// untrimmed one would percent-encode its padding into the sslmode parameter.
// Returning the normalized value follows the query builder's identifier
// validators, which hand back what validation actually judged.
func validateMigrationTLSMode(mode string) (string, error) {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" || slices.Contains(supportedMigrationTLSModes, trimmed) {
		return trimmed, nil
	}
	return "", fmt.Errorf("%w: %q (supported: %s)", ErrInvalidMigrationTLSMode, trimmed, strings.Join(supportedMigrationTLSModes, ", "))
}

// validateMigrationPort accepts the unset zero and any port in the TCP range.
func validateMigrationPort(port int) error {
	if port < 0 || port > 65535 {
		return ErrInvalidMigrationPort
	}
	return nil
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
		"so the TLS settings cannot reach the migration connection; remove the database.tls block, putting its " +
		"settings in the connection string for the RUNTIME pool, and set the migration's own TLS parameters on " +
		"the JDBC url in flyway.conf, which owns the migration connection for a connectionstring config; that " +
		"url must also name the same host and database as the connection string, or the migration is encrypted " +
		"but applied to the wrong target")

// ErrMigrationTLSCARequiresVerify rejects a database.tls.ca that cannot actually
// authenticate the server. pgjdbc reads `sslrootcert` only under `verify-ca` and
// `verify-full`; `require`, `allow` and `prefer` all use a NON-VALIDATING socket
// factory, and an unset mode is `prefer`, which also falls back to plaintext. So
// a config naming a CA under any of those got an unverified — possibly
// unencrypted — migration while reading as though it pinned one.
//
// This is deliberately STRICTER than config.Validate, which admits `require`
// beside a ca (ADR-062): pgx treats `require` + ca as verify-ca, a documented
// libpq inheritance, so the RUNTIME really does verify there. pgjdbc does not,
// and the migrator answers for pgjdbc. The divergence is the point — the same
// config is honored at runtime and refused for migration rather than silently
// unverified. Match with errors.Is.
var ErrMigrationTLSCARequiresVerify = errors.New(
	"migration: database.tls.ca requires database.tls.mode verify-ca or verify-full: pgjdbc reads sslrootcert " +
		"only under those two modes, so under require/allow/prefer (or an unset mode, which is prefer) the CA " +
		"would be ignored and the migration would not authenticate the server")

// ErrMigrationTLSCASystemUnsupported rejects the `ca: system` sentinel for
// migrations. It is a libpq/pgx spelling meaning "the platform trust store", and
// pgjdbc has no equivalent: LibPQFactory special-cases nothing and treats the
// value as a FILE PATH, so `sslrootcert=system` names a file that does not exist
// (verified against pgjdbc REL42.7.12). Mapping it to the JVM's own default
// trust store would not be equivalent either — `cacerts` is a different trust set
// from the one pgx consults, so the migration would authenticate against CAs the
// runtime does not, which is the silent divergence ADR-085 exists to remove.
// Name a real CA file for the migration instead. Match with errors.Is.
var ErrMigrationTLSCASystemUnsupported = errors.New(
	"migration: database.tls.ca: system is not supported for Flyway migrations: it is a libpq/pgx sentinel for " +
		"the platform trust store and pgjdbc has no equivalent, treating the value as a file path; point " +
		"database.tls.ca at the CA certificate file itself for the migration configuration")

// tlsCASystemSentinel is libpq's spelling for "use the platform trust store".
const tlsCASystemSentinel = "system"

// validateMigrationTLSCA checks the CA against the mode it will be used with and
// returns it trimmed, for the same reason the mode validator does: the trimmed
// spelling is what must reach sslrootcert, since padding would percent-encode
// into the URL and name a different file. config trims this field too. The mode
// arrives already normalized by validateMigrationTLSMode.
func validateMigrationTLSCA(caFile, normalizedMode string) (string, error) {
	trimmed := strings.TrimSpace(caFile)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == tlsCASystemSentinel {
		return "", ErrMigrationTLSCASystemUnsupported
	}
	if normalizedMode != sslModeVerifyCA && normalizedMode != sslModeVerifyFull {
		return "", ErrMigrationTLSCARequiresVerify
	}
	return trimmed, nil
}

// usesFrameworkOwnedURL reports whether this run gets a framework-built `-url=`.
// Only PostgreSQL discrete-field configs qualify. Oracle is excluded because it
// has no SUPPORTED `database.tls` — the shared config carries the field and
// validation rejects it for Oracle (ADR-062) — and no JDBC URL builder here; a
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
func buildPostgresJDBCURL(db *config.DatabaseConfig, appName, tlsMode, caFile string) (string, error) {
	// tlsMode and caFile arrive validated and trimmed from urlArgs; cert/key are read
	// raw because this is a presence check, which trimming cannot change — the pair is
	// refused outright, never rendered.
	if db.TLS.CertFile != "" || db.TLS.KeyFile != "" {
		return "", ErrMigrationMTLSUnsupported
	}

	jdbcURL := jdbcPostgresScheme + urlAuthority(db.Host, db.Port) + "/" + urlDatabaseSegment(db.Database)

	params := url.Values{}
	if appName != "" {
		params.Set(urlParamApplicationName, appName)
	}
	if tlsMode != "" {
		params.Set(urlParamSSLMode, tlsMode)
	}
	if caFile != "" {
		params.Set(urlParamSSLRootCert, caFile)
	}
	if encoded := params.Encode(); encoded != "" {
		jdbcURL += "?" + encoded
	}

	return jdbcURL, nil
}

// urlDatabaseSegment renders the database name as a JDBC path segment. url.PathEscape
// leaves "+" literal, but pgjdbc decodes the database segment with URLDecoder, which
// reads "+" as a space — so "bill+ing" would target "bill ing". Escaping it as %2B
// survives that decode as a literal plus.
func urlDatabaseSegment(database string) string {
	return strings.ReplaceAll(url.PathEscape(database), "+", "%2B")
}

// urlAuthority renders host[:port], omitting the port when it is zero so the
// driver takes its default. It runs downstream of validateMigrationPort, so a
// non-positive port here is zero and never a negative one. It brackets an IPv6
// literal so the colons in the address are not read as the port separator. Brackets are stripped first and
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
	if err := validateMigrationPort(db.Port); err != nil {
		return nil, err
	}
	tlsMode, err := validateMigrationTLSMode(db.TLS.Mode)
	if err != nil {
		return nil, err
	}
	caFile, err := validateMigrationTLSCA(db.TLS.CAFile, tlsMode)
	if err != nil {
		return nil, err
	}
	jdbcURL, err := buildPostgresJDBCURL(db, appName, tlsMode, caFile)
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
