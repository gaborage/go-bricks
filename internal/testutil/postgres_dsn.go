package testutil

const (
	// socketHost is the unix-socket directory these fixtures resolve to.
	socketHost        = "/var/run/postgresql"
	tcpHostUserDSN    = "host=db.example.com user=u"
	socketUserDSN     = "host=" + socketHost + " user=u"
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
	{Name: "pghost_socket_pgsslmode_require", DSN: "user=u", Env: [][2]string{{"PGHOST", socketHost}, {envPGSSLMODE, sslModeRequire}}, Refuse: true},
}
