package testutil

const (
	// socketHost is the unix-socket directory these fixtures resolve to.
	socketHost        = "/var/run/postgresql"
	tcpHostUserDSN    = "host=db.example.com user=u"
	socketUserDSN     = "host=" + socketHost + " user=u"
	envPGHOST         = "PGHOST"
	envPGSSLMODE      = "PGSSLMODE"
	envPGSSLCERT      = "PGSSLCERT"
	sslModeRequire    = "require"
	sslModeVerifyFull = "verify-full"
)

// UntokenizablePostgresDSNs are connection strings pgx v5 refuses to parse, so a scanner
// that cannot tokenize them may pass them through unjudged.
var UntokenizablePostgresDSNs = []string{
	"host='" + socketHost + " sslmode=" + sslModeRequire,
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
	{Name: "keyword_tcp_host", DSN: tcpHostUserDSN, Host: "db.example.com", HostSet: true},
	{Name: "keyword_socket_host_tls_material_never_read", DSN: "host=" + socketHost + " sslmode=" + sslModeVerifyFull + " user=u", Host: socketHost, HostSet: true},
	{Name: "keyword_socket_host_no_claim", DSN: socketUserDSN, Host: socketHost, HostSet: true},
	// pgx drops the unescaped backslash, so the host is TCP "C:pg", not a socket path.
	{Name: "keyword_windows_drive_tcp_host", DSN: `host=C:\pg sslmode=` + sslModeRequire + ` user=u`, Host: "C:pg", HostSet: true},
	// The escaped backslash survives as one literal backslash, so pgx treats this as a socket path.
	{Name: "keyword_windows_drive_socket_host", DSN: `host=C:\\pg sslmode=` + sslModeRequire + ` user=u`, Host: `C:\pg`, HostSet: true},
	{Name: "uri_tcp_host", DSN: "postgres://u@db.example.com/db", Host: "db.example.com", HostSet: true},
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
	{Name: "socket_pgsslmode_allow", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, "allow"}}},
	{Name: "socket_pgsslmode_disable", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, "disable"}}},
	{Name: "socket_empty_pgsslmode", DSN: socketUserDSN, Env: [][2]string{{envPGSSLMODE, ""}}},
	{Name: "socket_empty_pgsslcert", DSN: socketUserDSN, Env: [][2]string{{envPGSSLCERT, ""}}},
	{Name: "dsn_sslmode_disable_shadows_pgsslmode", DSN: "host=" + socketHost + " sslmode=disable user=u", Env: [][2]string{{envPGSSLMODE, sslModeVerifyFull}}},
	{Name: "dsn_empty_sslcert_shadows_pgsslcert", DSN: "host=" + socketHost + " sslcert='' user=u", Env: [][2]string{{envPGSSLCERT, "/x"}}},
	{Name: "tcp_pgsslmode_require", DSN: tcpHostUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeRequire}}, WantPgxTLS: true},
	{Name: "tcp_pgsslmode_verify_full", DSN: tcpHostUserDSN, Env: [][2]string{{envPGSSLMODE, sslModeVerifyFull}}, WantPgxTLS: true},
	{Name: "tcp_pgsslnegotiation_direct", DSN: tcpHostUserDSN, Env: [][2]string{{"PGSSLNEGOTIATION", "direct"}}, WantPgxTLS: true},
	{Name: "pghost_socket_pgsslmode_require", DSN: "user=u", Env: [][2]string{{envPGHOST, socketHost}, {envPGSSLMODE, sslModeRequire}}, Refuse: true},
}

const (
	envPGSERVICE     = "PGSERVICE"
	serviceDSNSource = "service="
	serviceTCPHost   = "db.internal"
	serviceTCPDSN    = "host=" + serviceTCPHost + " user=u"
	serviceClaimDSN  = "service=" + PostgresServiceName + " sslmode=" + sslModeRequire
)

// PostgresServiceName is the one service PostgresServiceFileBody defines.
const PostgresServiceName = "svc"

// PostgresServiceFileBody is the service file the pgx oracle writes. Its service names a
// unix-socket host, so a DSN that resolves through it dials the socket with TLS skipped.
const PostgresServiceFileBody = "[" + PostgresServiceName + "]\nhost=" + socketHost + "\nuser=u\n"

// PgxServiceOutcome is what pgconn.ParseConfig makes of a PostgresServiceCase when
// PostgresServiceFileBody is the service file in effect.
type PgxServiceOutcome int

const (
	// PgxServiceSocket: the service's socket host wins, over PGHOST too, and TLSConfig is nil.
	PgxServiceSocket PgxServiceOutcome = iota + 1
	// PgxServiceTCPHost: pgx dials the DSN's own TCP host.
	PgxServiceTCPHost
	// PgxServiceUnresolved: pgx looks up a service the file does not define and refuses the DSN.
	PgxServiceUnresolved
)

// PostgresServiceCase is one DSN+env combination whose libpq service resolution pgx and the
// config seam must agree on. Refuse is the source the config refusal names as carrying the
// service (service= or PGSERVICE), empty when the seam accepts. Env entries are applied with
// t.Setenv on a hermetic PG* env.
type PostgresServiceCase struct {
	Name   string
	DSN    string
	Env    [][2]string
	Refuse string
	Pgx    PgxServiceOutcome
}

// PostgresServiceCases is shared between config's service-rule tests and
// database/postgresql's pgconn.ParseConfig oracle, so the service merge cannot silently
// drift from a pgx bump.
var PostgresServiceCases = []PostgresServiceCase{
	{Name: "dsn_service_without_pghost", DSN: serviceClaimDSN, Refuse: serviceDSNSource, Pgx: PgxServiceSocket},
	{Name: "dsn_service_over_tcp_pghost", DSN: serviceClaimDSN, Env: [][2]string{{envPGHOST, serviceTCPHost}}, Refuse: serviceDSNSource, Pgx: PgxServiceSocket},
	{Name: "pgservice_without_pghost", DSN: "user=u sslmode=" + sslModeRequire, Env: [][2]string{{envPGSERVICE, PostgresServiceName}}, Refuse: envPGSERVICE, Pgx: PgxServiceSocket},
	{Name: "pgservice_over_tcp_pghost", DSN: "user=u sslmode=" + sslModeRequire, Env: [][2]string{{envPGSERVICE, PostgresServiceName}, {envPGHOST, serviceTCPHost}}, Refuse: envPGSERVICE, Pgx: PgxServiceSocket},
	// The DSN's own service= is the carrier; PGSERVICE behind it is shadowed, not named.
	{Name: "dsn_service_beside_pgservice", DSN: serviceClaimDSN, Env: [][2]string{{envPGSERVICE, "other"}}, Refuse: serviceDSNSource, Pgx: PgxServiceSocket},
	{Name: "uri_query_service", DSN: "postgres:///db?service=" + PostgresServiceName + "&sslmode=" + sslModeRequire, Refuse: serviceDSNSource, Pgx: PgxServiceSocket},
	// The DSN host wins, but the service still supplies whatever the DSN leaves unset.
	{Name: "dsn_host_beside_dsn_service", DSN: "host=" + serviceTCPHost + " service=" + PostgresServiceName, Refuse: serviceDSNSource, Pgx: PgxServiceTCPHost},
	// A present DSN key, empty included, shadows PGSERVICE; pgx then looks up the empty name.
	{Name: "empty_dsn_service_shadows_pgservice", DSN: serviceTCPDSN + " service=''", Env: [][2]string{{envPGSERVICE, PostgresServiceName}}, Pgx: PgxServiceUnresolved},
	// Negative pin: pgx skips whitespace after '=', so the next pair becomes the service name.
	{Name: "empty_service_swallows_next_pair", DSN: "service= " + serviceTCPDSN, Env: [][2]string{{envPGSERVICE, PostgresServiceName}}, Refuse: serviceDSNSource, Pgx: PgxServiceUnresolved},
	{Name: "dsn_servicefile_without_service", DSN: "servicefile=/x/pg_service.conf " + serviceTCPDSN, Pgx: PgxServiceTCPHost},
	{Name: "pgservicefile_without_service", DSN: serviceTCPDSN, Env: [][2]string{{"PGSERVICEFILE", "/x"}}, Pgx: PgxServiceTCPHost},
}
