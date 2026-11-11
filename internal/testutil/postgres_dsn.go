package testutil

import (
	"os"
	"testing"
)

const (
	// socketHost is the unix-socket directory these fixtures resolve to.
	socketHost        = "/var/run/postgresql"
	tcpHost           = "db.example.com"
	userPair          = " user=u"
	sslModeKey        = " sslmode="
	tcpHostUserDSN    = "host=" + tcpHost + userPair
	socketUserDSN     = "host=" + socketHost + userPair
	envPGHOST         = "PGHOST"
	envPGSSLMODE      = "PGSSLMODE"
	envPGSSLCERT      = "PGSSLCERT"
	sslModeRequire    = "require"
	sslModeVerifyFull = "verify-full"
)

// UntokenizablePostgresDSNs are connection strings pgx v5 refuses to parse, so a scanner
// that cannot tokenize them may pass them through unjudged.
var UntokenizablePostgresDSNs = []string{
	"host='" + socketHost + sslModeKey + sslModeRequire,
	`host='/a\' user=u`,
	`host='/a\`,
	"host",
	"host=a user",
	"=a",
	"ho st=a",
	"host=a\x00",
	"POSTGRES://h/db",
	"postgres://[::1/db",
	"postgres://[]/db",
	"postgres://[::1]x/db",
	"postgres://%zz/db",
	"postgres://h%00/db",
	"postgresql://a b/db",
	"postgres://h\x00/db",
	"postgres://h/db?sslmode",
	"postgres://h/db?sslmode=a=b",
	"postgres://h/db?&sslmode=" + sslModeRequire,
	"postgres://h/db?ssl%zz=1",
	"postgres://h/db?sslmode=re%zz",
	"postgres://h/db?host=a b",
}

// PostgresDSNHostCase pins one DSN's host resolution, shared between the config package's
// own scanner unit test and database/postgresql's pgconn.ParseConfig oracle test, so the
// mirror cannot silently drift from a pgx bump. Two exclusions shape the fixture list: every
// entry names a SINGLE host, because pgconn.ParseConfig resolves only fallbacks[0] into
// Config.Host and a comma-joined host string cannot be compared against it; and every entry
// carries an explicit user, so parsing never depends on the OS account running the test.
type PostgresDSNHostCase struct {
	Name    string
	DSN     string
	Host    string
	HostSet bool
}

var PostgresDSNHostCases = []PostgresDSNHostCase{
	{Name: "keyword_tcp_host", DSN: tcpHostUserDSN, Host: tcpHost, HostSet: true},
	{Name: "keyword_socket_host_tls_material_never_read", DSN: "host=" + socketHost + sslModeKey + sslModeVerifyFull + userPair, Host: socketHost, HostSet: true},
	{Name: "keyword_socket_host_no_claim", DSN: socketUserDSN, Host: socketHost, HostSet: true},
	// pgx drops the unescaped backslash, so the host is TCP "C:pg", not a socket path.
	{Name: "keyword_windows_drive_tcp_host", DSN: `host=C:\pg sslmode=` + sslModeRequire + userPair, Host: "C:pg", HostSet: true},
	// The escaped backslash survives as one literal backslash, so pgx treats this as a socket path.
	{Name: "keyword_windows_drive_socket_host", DSN: `host=C:\\pg sslmode=` + sslModeRequire + userPair, Host: `C:\pg`, HostSet: true},
	{Name: "uri_tcp_host", DSN: "postgres://u@" + tcpHost + "/db", Host: tcpHost, HostSet: true},
	{Name: "uri_percent_encoded_socket_host", DSN: "postgres://u@%2Fvar%2Frun%2Fpostgresql/db?sslmode=" + sslModeRequire, Host: socketHost, HostSet: true},
	{Name: "uri_ipv6_host", DSN: "postgres://u@[::1]/db", Host: "::1", HostSet: true},
	{Name: "uri_query_host_overrides_authority", DSN: "postgres://u@a/db?host=c", Host: "c", HostSet: true},
	// pgx files an unknown but well-shaped key under RuntimeParams rather than rejecting it,
	// which is why keyword-form inference tests the key's shape and not libpq's vocabulary.
	{Name: "keyword_unknown_key_is_a_runtime_param", DSN: "foo=1 host=h user=u", Host: "h", HostSet: true},
}

// PostgresSSLEnvTLSCase is one DSN+env combination the [C66.1] rule and pgx
// ParseConfig must agree on: a socket host with a TLS claim is refused here and
// yields TLSConfig == nil from pgx; a TCP host with a claim pgx honors yields a
// non-nil TLSConfig. Env entries are applied with t.Setenv on a hermetic PG* env.
type PostgresSSLEnvTLSCase struct {
	Name       string
	DSN        string
	Env        [][2]string
	Refuse     bool
	WantPgxTLS bool
}

// PostgresSSLEnvTLSCases is shared between config's rule-2 tests and
// database/postgresql's pgconn.ParseConfig oracle so the env-under-DSN merge
// cannot silently drift from a pgx bump.
var PostgresSSLEnvTLSCases = []PostgresSSLEnvTLSCase{
	{Name: "socket_pgsslmode_verify_full", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeVerifyFull}}, Refuse: true},
	{Name: "socket_pgsslmode_require", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeRequire}}, Refuse: true},
	{Name: "socket_pgsslmode_verify_ca", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, "verify-ca"}}, Refuse: true},
	{Name: "socket_pgsslrootcert", DSN: socketUserDSN, Env: [][2]string{{"PGSSLROOTCERT", "/etc/pg/ca.crt"}}, Refuse: true},
	// Material is judged by presence, so the `system` trust-store sentinel claims like a path.
	{Name: "socket_pgsslrootcert_system", DSN: socketUserDSN, Env: [][2]string{{"PGSSLROOTCERT", "system"}}, Refuse: true},
	{Name: "socket_pgsslcert", DSN: socketUserDSN, Env: [][2]string{{envPGSSLCERT, "/etc/pg/client.crt"}}, Refuse: true},
	{Name: "socket_pgsslkey", DSN: socketUserDSN, Env: [][2]string{{"PGSSLKEY", "/etc/pg/client.key"}}, Refuse: true},
	{Name: "socket_pgsslnegotiation_direct", DSN: socketUserDSN, Env: [][2]string{{"PGSSLNEGOTIATION", "direct"}}, Refuse: true},
	{Name: "socket_pgsslmode_prefer", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, "prefer"}}},
	{Name: "socket_pgsslmode_allow", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, certModeAllow}}},
	{Name: "socket_pgsslmode_disable", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, certModeDisable}}},
	{Name: "socket_empty_pgsslmode", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, ""}}},
	{Name: "socket_empty_pgsslcert", DSN: socketUserDSN, Env: [][2]string{{envPGSSLCERT, ""}}},
	{Name: "dsn_sslmode_disable_shadows_pgsslmode", DSN: "host=" + socketHost + " sslmode=disable user=u", Env: [][2]string{{envPGSSLMODE, sslModeVerifyFull}}},
	{Name: "dsn_empty_sslcert_shadows_pgsslcert", DSN: "host=" + socketHost + " sslcert='' user=u", Env: [][2]string{{envPGSSLCERT, "/x"}}},
	{Name: "tcp_pgsslmode_require", DSN: tcpHostUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeRequire}}, WantPgxTLS: true},
	{Name: "tcp_pgsslmode_verify_full", DSN: tcpHostUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeVerifyFull}}, WantPgxTLS: true},
	{Name: "tcp_pgsslnegotiation_direct", DSN: tcpHostUserDSN, Env: [][2]string{{"PGSSLNEGOTIATION", "direct"}}, WantPgxTLS: true},
	{Name: "pghost_socket_pgsslmode_require", DSN: "user=u", Env: [][2]string{{envPGHOST, socketHost}, {envPGSSLMODE, sslModeRequire}}, Refuse: true},
	// sslcertmode governs the client certificate only, so no valid value of it claims TLS.
	{Name: "socket_pgsslcertmode_require", DSN: socketUserDSN, Env: [][2]string{{EnvPGSSLCertMode, sslModeRequire}}},
	{Name: "socket_pgsslcertmode_disable", DSN: socketUserDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeDisable}}},
	{Name: "socket_dsn_sslcertmode_require", DSN: socketUserDSN + certModeKey + sslModeRequire},
	{Name: "socket_dsn_sslcertmode_allow", DSN: socketUserDSN + certModeKey + certModeAllow},
	{Name: "socket_dsn_sslcertmode_disable", DSN: socketUserDSN + certModeKey + certModeDisable},
}

// EnvPGSSLCertMode is the one PGSSL* variable pgx honors when set but empty, so a hermetic
// test must unset it rather than set it to "".
const EnvPGSSLCertMode = "PGSSLCERTMODE"

const (
	certModeKey     = " sslcertmode="
	certModeEmpty   = " sslcertmode=''"
	uriUserAtTCP    = "postgres://u@" + tcpHost
	certModeDSNKey  = "sslcertmode"
	tcpRequireDSN   = tcpHostUserDSN + sslModeKey + sslModeRequire
	clientCertPair  = " sslcert=/etc/pg/client.crt sslkey=/etc/pg/client.key"
	tcpCertDSN      = tcpRequireDSN + clientCertPair
	uriCertBase     = uriUserAtTCP + "/db?sslmode=" + sslModeRequire + "&sslcert=/etc/pg/client.crt&sslkey=/etc/pg/client.key"
	certModeDisable = "disable"
	certModeAllow   = "allow"
	certModeInvalid = "bogus"
	envPGSSLKEY     = "PGSSLKEY"
	envCertPath     = "/etc/pg/env.crt"
	envKeyPath      = "/etc/pg/env.key"
)

// PostgresSSLCertModeCase is one DSN+env combination whose effective sslcertmode the config
// seam judges. RefusedBy is the source the refusal names (sslcertmode or PGSSLCERTMODE), empty
// when the seam accepts. Invalid marks a refusal of the value itself, which pgx fails to parse;
// any other refusal is disable beside a client certificate, which pgx then loads none of. Every
// disable row dials a TCP host under sslmode=require, so pgx builds a TLS config to inspect.
type PostgresSSLCertModeCase struct {
	Name      string
	DSN       string
	Env       [][2]string
	RefusedBy string
	Invalid   bool
}

// PostgresSSLCertModeCases is shared between config's sslcertmode tests and
// database/postgresql's pgconn.ParseConfig oracle, so the merge cannot drift from a pgx bump.
var PostgresSSLCertModeCases = []PostgresSSLCertModeCase{
	{Name: "keyword_disable_with_cert", DSN: tcpCertDSN + certModeKey + certModeDisable, RefusedBy: certModeDSNKey},
	{Name: "uri_disable_with_cert", DSN: uriCertBase + "&sslcertmode=" + certModeDisable, RefusedBy: certModeDSNKey},
	{Name: "disable_with_sslcert_only", DSN: tcpRequireDSN + " sslcert=/etc/pg/client.crt" + certModeKey + certModeDisable, RefusedBy: certModeDSNKey},
	{Name: "disable_with_sslkey_only", DSN: tcpRequireDSN + " sslkey=/etc/pg/client.key" + certModeKey + certModeDisable, RefusedBy: certModeDSNKey},
	{Name: "disable_with_pgsslcert_and_pgsslkey", DSN: tcpRequireDSN + certModeKey + certModeDisable, Env: [][2]string{{envPGSSLCERT, envCertPath}, {envPGSSLKEY, envKeyPath}}, RefusedBy: certModeDSNKey},
	{Name: "disable_with_pgsslcert_only", DSN: tcpRequireDSN + certModeKey + certModeDisable, Env: [][2]string{{envPGSSLCERT, envCertPath}}, RefusedBy: certModeDSNKey},
	{Name: "disable_with_pgsslkey_only", DSN: tcpRequireDSN + certModeKey + certModeDisable, Env: [][2]string{{envPGSSLKEY, envKeyPath}}, RefusedBy: certModeDSNKey},
	{Name: "pgsslcertmode_disable_with_dsn_cert", DSN: tcpCertDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeDisable}}, RefusedBy: EnvPGSSLCertMode},
	{Name: "pgsslcertmode_disable_with_env_cert", DSN: tcpRequireDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeDisable}, {envPGSSLCERT, envCertPath}, {envPGSSLKEY, envKeyPath}}, RefusedBy: EnvPGSSLCertMode},
	{Name: "dsn_allow_shadows_pgsslcertmode_disable", DSN: tcpCertDSN + certModeKey + certModeAllow, Env: [][2]string{{EnvPGSSLCertMode, certModeDisable}}},
	{Name: "dsn_require_with_cert", DSN: tcpCertDSN + certModeKey + sslModeRequire},
	{Name: "pgsslcertmode_allow_with_cert", DSN: tcpCertDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeAllow}}},
	{Name: "unset_with_cert", DSN: tcpCertDSN},
	{Name: "disable_without_cert", DSN: tcpRequireDSN + certModeKey + certModeDisable},
	{Name: "pgsslcertmode_disable_without_cert", DSN: tcpRequireDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeDisable}}},
	// An empty DSN key shadows the variable, so no client certificate is in effect.
	{Name: "disable_with_empty_dsn_cert_shadowing_env", DSN: tcpRequireDSN + " sslcert='' sslkey=''" + certModeKey + certModeDisable, Env: [][2]string{{envPGSSLCERT, envCertPath}, {envPGSSLKEY, envKeyPath}}},
	{Name: "disable_with_empty_pgsslcert", DSN: tcpRequireDSN + certModeKey + certModeDisable, Env: [][2]string{{envPGSSLCERT, ""}, {envPGSSLKEY, ""}}},
	{Name: "pgsslcertmode_empty", DSN: tcpHostUserDSN, Env: [][2]string{{EnvPGSSLCertMode, ""}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	{Name: "pgsslcertmode_empty_with_cert", DSN: tcpCertDSN, Env: [][2]string{{EnvPGSSLCertMode, ""}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	{Name: "pgsslcertmode_bogus", DSN: tcpHostUserDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeInvalid}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	{Name: "pgsslcertmode_bogus_with_cert", DSN: tcpCertDSN, Env: [][2]string{{EnvPGSSLCertMode, certModeInvalid}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	// pgx compares exactly: no case folding, no trimming.
	{Name: "pgsslcertmode_capitalized", DSN: tcpHostUserDSN, Env: [][2]string{{EnvPGSSLCertMode, "Disable"}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	{Name: "pgsslcertmode_padded", DSN: tcpHostUserDSN, Env: [][2]string{{EnvPGSSLCertMode, " disable"}}, RefusedBy: EnvPGSSLCertMode, Invalid: true},
	{Name: "keyword_empty", DSN: tcpHostUserDSN + certModeEmpty, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "keyword_empty_with_cert", DSN: tcpCertDSN + certModeEmpty, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "keyword_bogus", DSN: tcpHostUserDSN + certModeKey + certModeInvalid, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "keyword_bogus_with_cert", DSN: tcpCertDSN + certModeKey + certModeInvalid, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "uri_empty", DSN: uriUserAtTCP + "/db?sslcertmode=", RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "uri_bogus_with_cert", DSN: uriCertBase + "&sslcertmode=" + certModeInvalid, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "socket_keyword_bogus", DSN: socketUserDSN + certModeKey + certModeInvalid, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "dsn_allow_shadows_pgsslcertmode_bogus", DSN: tcpHostUserDSN + certModeKey + certModeAllow, Env: [][2]string{{EnvPGSSLCertMode, certModeInvalid}}},
	{Name: "dsn_empty_shadows_pgsslcertmode_allow", DSN: tcpHostUserDSN + certModeEmpty, Env: [][2]string{{EnvPGSSLCertMode, certModeAllow}}, RefusedBy: certModeDSNKey, Invalid: true},
	{Name: "unset_without_cert", DSN: tcpHostUserDSN},
}

const (
	envPGSERVICE       = "PGSERVICE"
	serviceDSNSource   = "service="
	serviceName        = "svc"
	serviceClaimDSN    = "service=" + serviceName + sslModeKey + sslModeRequire
	serviceEnvClaimDSN = "user=u sslmode=" + sslModeRequire
)

// PostgresServiceHost and PostgresServicePort are what PostgresServiceFileBody's service sets:
// a unix-socket host, so a DSN that resolves through it dials the socket with TLS skipped, and a
// port no fixture DSN names, so pgx's port shows whether it read the service at all.
const (
	PostgresServiceHost = socketHost
	PostgresServicePort = 6543
)

// PostgresServiceFileBody is the service file the pgx oracle writes: a named service, and an
// unnamed [] section, which pgservicefile files under "" and so answers an empty service=.
const PostgresServiceFileBody = "[" + serviceName + "]\nhost=" + PostgresServiceHost + "\nport=6543\nuser=u\n" +
	"[]\nhost=" + PostgresServiceHost + "\nport=6543\nuser=u\n"

// PostgresServiceCase is one DSN+env combination whose libpq service resolution pgx and the
// config seam must agree on. RefusedBy is the source the config refusal names as carrying the
// service (service= or PGSERVICE), empty when the seam accepts; every row pgx resolves through
// a service is refused. PgxHost is the host pgconn.ParseConfig resolves under
// PostgresServiceFileBody, empty when pgx refuses the DSN. Env entries are applied with SetEnv
// on a hermetic PG* env.
type PostgresServiceCase struct {
	Name      string
	DSN       string
	Env       [][2]string
	RefusedBy string
	PgxHost   string
}

// PostgresServiceCases is shared between config's service-rule tests and
// database/postgresql's pgconn.ParseConfig oracle, so the service merge cannot silently
// drift from a pgx bump.
var PostgresServiceCases = []PostgresServiceCase{
	{Name: "dsn_service_without_pghost", DSN: serviceClaimDSN, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	{Name: "dsn_service_over_tcp_pghost", DSN: serviceClaimDSN, Env: [][2]string{{envPGHOST, tcpHost}}, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	{Name: "pgservice_without_pghost", DSN: serviceEnvClaimDSN, Env: [][2]string{{envPGSERVICE, serviceName}}, RefusedBy: envPGSERVICE, PgxHost: PostgresServiceHost},
	{Name: "pgservice_over_tcp_pghost", DSN: serviceEnvClaimDSN, Env: [][2]string{{envPGSERVICE, serviceName}, {envPGHOST, tcpHost}}, RefusedBy: envPGSERVICE, PgxHost: PostgresServiceHost},
	// The DSN's own service= is the carrier; PGSERVICE behind it is shadowed, not named.
	{Name: "dsn_service_beside_pgservice", DSN: serviceClaimDSN, Env: [][2]string{{envPGSERVICE, "other"}}, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	{Name: "uri_query_service", DSN: "postgres:///db?service=" + serviceName + "&sslmode=" + sslModeRequire, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	// The DSN host wins, but the service still supplies whatever the DSN leaves unset.
	{Name: "dsn_host_beside_dsn_service", DSN: "host=" + tcpHost + " service=" + serviceName, RefusedBy: serviceDSNSource, PgxHost: tcpHost},
	// pgx resolves a service on the key's presence: an empty service= reads the unnamed [] section.
	{Name: "empty_dsn_service_over_tcp_pghost", DSN: "service='' sslmode=" + sslModeRequire, Env: [][2]string{{envPGHOST, tcpHost}}, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	{Name: "uri_empty_query_service_over_tcp_pghost", DSN: "postgres:///db?service=&sslmode=" + sslModeRequire, Env: [][2]string{{envPGHOST, tcpHost}}, RefusedBy: serviceDSNSource, PgxHost: PostgresServiceHost},
	// The empty DSN key shadows PGSERVICE, so the DSN is the carrier named.
	{Name: "empty_dsn_service_shadows_pgservice", DSN: tcpHostUserDSN + " service=''", Env: [][2]string{{envPGSERVICE, serviceName}}, RefusedBy: serviceDSNSource, PgxHost: tcpHost},
	// Negative pin: pgx skips whitespace after '=', so the next pair becomes the service name.
	{Name: "empty_service_swallows_next_pair", DSN: "service= " + tcpHostUserDSN, Env: [][2]string{{envPGSERVICE, serviceName}}, RefusedBy: serviceDSNSource},
	{Name: "dsn_servicefile_without_service", DSN: "servicefile=/x/pg_service.conf " + tcpHostUserDSN, PgxHost: tcpHost},
	{Name: "pgservicefile_without_service", DSN: tcpHostUserDSN, Env: [][2]string{{"PGSERVICEFILE", "/x"}}, PgxHost: tcpHost},
}

// UnsetEnv unsets each variable for the test's duration, restoring it afterwards.
func UnsetEnv(t testing.TB, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// SetEnv applies each fixture env pair with t.Setenv.
func SetEnv(t testing.TB, env [][2]string) {
	t.Helper()
	for _, e := range env {
		t.Setenv(e[0], e[1])
	}
}
