package outbox

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// updateGoldens regenerates testdata/sql/*.golden. The files are the proof the
// #1255 port asks for: the SQL each store method emits, captured BEFORE the
// port from the hand-written implementations and diffed after it. A change
// there is a deliberate one the commit body names, never a side effect.
var updateGoldens = flag.Bool("update", false, "regenerate the store SQL goldens")

// goldenStatement is one statement the store handed the database, rendered the
// way the golden file records it: the SQL verbatim, then each argument with its
// Go type so a placeholder-order change or a re-bound argument is visible.
func goldenStatement(kind, sql string, args []any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", kind, sql)
	for i, a := range args {
		fmt.Fprintf(&b, "  arg[%d] %T = %v\n", i, a, goldenArg(a))
	}
	return b.String()
}

// fixedAt is the fixture clock every deterministic argument carries; it prints
// verbatim so a wrong binding fails the golden. The one non-deterministic
// argument — MarkPublished's time.Now() — prints as a marker instead.
var fixedAt = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// goldenArg keeps the file stable and the fixture visible: byte slices print
// as text, the fixture time prints as RFC3339, any other clock value prints as
// <time> (the position and type are what a wall-clock argument pins).
func goldenArg(a any) any {
	switch v := a.(type) {
	case []byte:
		return string(v)
	case time.Time:
		if v.Equal(fixedAt) {
			return v.UTC().Format(time.RFC3339)
		}
		return "<time>"
	case *time.Time:
		if v != nil && v.Equal(fixedAt) {
			return v.UTC().Format(time.RFC3339)
		}
		return "<time>"
	default:
		return v
	}
}

// permissiveDB answers every statement the store can issue, so a run records
// the SQL of every method without a per-statement expectation: an empty row set
// for queries, one affected row for execs, on the transaction and the pool alike.
func permissiveDB(vendor string) (db *dbtesting.TestDB, leaderTx, tx *dbtesting.TestTx) {
	db = dbtesting.NewTestDB(vendor)
	empty := dbtesting.NewRowSet("id")
	db.ExpectQuery("").WillReturnRows(empty)
	db.ExpectExec("").WillReturnRowsAffected(1)
	// Begin pops queued transactions in order and only Lead calls Begin — Insert
	// is handed its transaction directly — so the leader's is queued first.
	leaderTx = db.ExpectTransaction()
	leaderTx.ExpectQuery("").WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))
	leaderTx.ExpectExec("").WillReturnRowsAffected(0)
	tx = db.ExpectTransaction()
	tx.ExpectQuery("").WillReturnRows(empty)
	tx.ExpectExec("").WillReturnRowsAffected(1)
	return db, leaderTx, tx
}

// captureStoreSQL drives every Store method with fixed fixtures and returns the
// statements in call order. Lead's SELECT ... FOR UPDATE NOWAIT and the leader
// probe are driven too: leadRow runs on its own transaction, so the fixture
// expects two transactions.
func captureStoreSQL(t *testing.T, vendor string, store Store) string {
	t.Helper()
	ctx := context.Background()
	db, leaderTx, tx := permissiveDB(vendor)

	record := &Record{
		ID:          "11111111-2222-4333-8444-555555555555",
		EventType:   "order.created",
		AggregateID: "order-42",
		Payload:     []byte(`{"id":42}`),
		Headers:     []byte(`{"x-tenant-id":"acme"}`),
		Exchange:    "orders",
		RoutingKey:  "order.created",
		Lane:        LaneAMQP,
		Status:      "pending",
		CreatedAt:   fixedAt,
	}

	var out strings.Builder
	step := func(name string, fn func() error) {
		t.Helper()
		before := len(db.ExecLog()) + len(db.QueryLog()) + len(tx.ExecLog()) + len(tx.QueryLog()) + len(leaderTx.QueryLog()) + len(leaderTx.ExecLog())
		require.NoError(t, fn(), name)
		fmt.Fprintf(&out, "== %s\n", name)
		after := len(db.ExecLog()) + len(db.QueryLog()) + len(tx.ExecLog()) + len(tx.QueryLog()) + len(leaderTx.QueryLog()) + len(leaderTx.ExecLog())
		require.Greater(t, after, before, "%s issued no statement", name)
	}

	step("CreateTable", func() error { return store.CreateTable(ctx, db) })
	step("Insert", func() error { return store.Insert(ctx, tx, record) })
	step("FetchPending", func() error { _, err := store.FetchPending(ctx, db, 25); return err })
	step("MarkPublished", func() error { return store.MarkPublished(ctx, db, record.ID) })
	step("MarkFailed", func() error { return store.MarkFailed(ctx, db, record.ID, "broker down") })
	step("MarkDeadLettered", func() error { return store.MarkDeadLettered(ctx, db, record.ID, "poison") })
	step("DeletePublished", func() error { _, err := store.DeletePublished(ctx, db, fixedAt); return err })
	step("Lead+Probe+Release", func() error {
		lead, err := store.Lead(ctx, db)
		if err != nil {
			return err
		}
		if err := lead.Probe(ctx); err != nil {
			return err
		}
		return lead.Release(ctx)
	})

	// Statements are recorded per connection, in call order within each; the
	// golden lists them grouped by the surface they went through.
	var body strings.Builder
	body.WriteString("# pool queries\n")
	for _, q := range db.QueryLog() {
		body.WriteString(goldenStatement("QUERY", q.SQL, q.Args))
	}
	body.WriteString("# pool execs\n")
	for _, e := range db.ExecLog() {
		body.WriteString(goldenStatement("EXEC", e.SQL, e.Args))
	}
	body.WriteString("# tx queries\n")
	for _, q := range tx.QueryLog() {
		body.WriteString(goldenStatement("QUERY", q.SQL, q.Args))
	}
	body.WriteString("# tx execs\n")
	for _, e := range tx.ExecLog() {
		body.WriteString(goldenStatement("EXEC", e.SQL, e.Args))
	}
	body.WriteString("# leader tx queries\n")
	for _, q := range leaderTx.QueryLog() {
		body.WriteString(goldenStatement("QUERY", q.SQL, q.Args))
	}
	body.WriteString("# leader tx execs\n")
	for _, e := range leaderTx.ExecLog() {
		body.WriteString(goldenStatement("EXEC", e.SQL, e.Args))
	}
	return out.String() + body.String()
}

// TestStoreSQLGolden pins the SQL every store emits per vendor. Run with
// -update to regenerate; review the diff as the port's proof.
func TestStoreSQLGolden(t *testing.T) {
	cases := []struct {
		vendor string
		build  func() (Store, error)
	}{
		{dbtypes.PostgreSQL, func() (Store, error) { return NewPostgresStore("gobricks_outbox") }},
		{dbtypes.Oracle, func() (Store, error) { return NewOracleStore("gobricks_outbox") }},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			store, err := tc.build()
			require.NoError(t, err)

			got := captureStoreSQL(t, tc.vendor, store)
			path := filepath.Join("testdata", "sql", "outbox_"+tc.vendor+".golden")
			if *updateGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "golden missing — run go test ./outbox -run TestStoreSQLGolden -update")
			require.Equal(t, string(want), got, "store SQL drifted from testdata/sql/%s; regenerate with -update only for a deliberate change named in the commit body", filepath.Base(path))
		})
	}
}
