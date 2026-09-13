package testutil

// socketHost is the unix-socket directory these fixtures resolve to.
const socketHost = "/var/run/postgresql"

// UntokenizablePostgresDSNs are connection strings pgx v5 refuses to parse, so a scanner
// that cannot tokenize them may pass them through unjudged.
var UntokenizablePostgresDSNs = []string{
	"host='" + socketHost + " sslmode=require",
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
	"postgres://h/db?&sslmode=require",
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
	{Name: "keyword_tcp_host", DSN: "host=db.example.com user=u", Host: "db.example.com", HostSet: true},
	{Name: "keyword_socket_host_tls_material_never_read", DSN: "host=" + socketHost + " sslmode=verify-full user=u", Host: socketHost, HostSet: true},
	{Name: "keyword_socket_host_no_claim", DSN: "host=" + socketHost + " user=u", Host: socketHost, HostSet: true},
	// pgx drops the unescaped backslash, so the host is TCP "C:pg", not a socket path.
	{Name: "keyword_windows_drive_tcp_host", DSN: `host=C:\pg sslmode=require user=u`, Host: "C:pg", HostSet: true},
	// The escaped backslash survives as one literal backslash, so pgx treats this as a socket path.
	{Name: "keyword_windows_drive_socket_host", DSN: `host=C:\\pg sslmode=require user=u`, Host: `C:\pg`, HostSet: true},
	{Name: "uri_tcp_host", DSN: "postgres://u@db.example.com/db", Host: "db.example.com", HostSet: true},
	{Name: "uri_percent_encoded_socket_host", DSN: "postgres://u@%2Fvar%2Frun%2Fpostgresql/db?sslmode=require", Host: socketHost, HostSet: true},
	{Name: "uri_ipv6_host", DSN: "postgres://u@[::1]/db", Host: "::1", HostSet: true},
	{Name: "uri_query_host_overrides_authority", DSN: "postgres://u@a/db?host=c", Host: "c", HostSet: true},
	// pgx files an unknown but well-shaped key under RuntimeParams rather than rejecting it,
	// which is why keyword-form inference tests the key's shape and not libpq's vocabulary.
	{Name: "keyword_unknown_key_is_a_runtime_param", DSN: "foo=1 host=h user=u", Host: "h", HostSet: true},
}
