package testing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// SQLGolden renders what a TestDB and its transactions recorded into the text
// a golden file pins: each statement verbatim, then every bound argument with
// its Go type, so a placeholder-order change or a re-bound argument is visible.
// It is the proof a store port is judged by (#1255): capture before, diff after.
//
// FixedClock is the fixture time the test binds deliberately; it prints as
// RFC3339 so a wrong binding fails the golden. Any other time value — a
// store's own time.Now() — prints as "<time>", since only its position and
// type are stable.
type SQLGolden struct {
	FixedClock time.Time
}

// Render returns the golden text for db and, in order, each tx: pool queries,
// pool execs, then per transaction its queries and execs.
func (g SQLGolden) Render(db *TestDB, txs ...*TestTx) string {
	var b strings.Builder
	b.WriteString("# pool queries\n")
	for _, q := range db.QueryLog() {
		b.WriteString(g.Statement("QUERY", q.SQL, q.Args))
	}
	b.WriteString("# pool execs\n")
	for _, e := range db.ExecLog() {
		b.WriteString(g.Statement("EXEC", e.SQL, e.Args))
	}
	for i, tx := range txs {
		fmt.Fprintf(&b, "# tx[%d] queries\n", i)
		for _, q := range tx.QueryLog() {
			b.WriteString(g.Statement("QUERY", q.SQL, q.Args))
		}
		fmt.Fprintf(&b, "# tx[%d] execs\n", i)
		for _, e := range tx.ExecLog() {
			b.WriteString(g.Statement("EXEC", e.SQL, e.Args))
		}
	}
	return b.String()
}

// Statement renders one statement and its arguments.
func (g SQLGolden) Statement(kind, sql string, args []any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", kind, sql)
	for i, a := range args {
		fmt.Fprintf(&b, "  arg[%d] %T = %v\n", i, a, g.arg(a))
	}
	return b.String()
}

func (g SQLGolden) arg(a any) any {
	switch v := a.(type) {
	case []byte:
		return string(v)
	case time.Time:
		return g.clock(v)
	case *time.Time:
		if v == nil {
			return "<nil>"
		}
		return g.clock(*v)
	default:
		return v
	}
}

func (g SQLGolden) clock(v time.Time) string {
	if v.Equal(g.FixedClock) {
		return v.UTC().Format(time.RFC3339)
	}
	return "<time>"
}

// Compare pins got against the golden at path. With update true the file is
// (re)written instead — only for a deliberate change the commit body names.
func Compare(t *testing.T, path, got string, update bool) {
	t.Helper()
	require.NoError(t, compareGolden(path, got, update))
}

// compareGolden is Compare's testable core: nil when got matches the file (or
// was written to it), an error naming the file otherwise.
func compareGolden(path, got string, update bool) error {
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(got), 0o600)
	}
	want, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- a test golden under testdata, path built from literals by the calling test
	if err != nil {
		return fmt.Errorf("golden %s missing — regenerate with -update: %w", path, err)
	}
	if string(want) != got {
		return fmt.Errorf("SQL drifted from %s; regenerate with -update only for a deliberate change named in the commit body\n--- want\n%s\n--- got\n%s", path, want, got)
	}
	return nil
}
