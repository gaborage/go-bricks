package testutil

// UntokenizablePostgresDSNs are connection strings pgx v5 refuses to parse, so a scanner
// that cannot tokenize them may pass them through unjudged.
var UntokenizablePostgresDSNs = []string{
	"host='/var/run/postgresql sslmode=require",
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
