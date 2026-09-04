package inbox

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// updateGoldens regenerates testdata/sql/*.golden — the SQL each inbox and hold
// store method emits, captured BEFORE the #1255 port and diffed after it. A
// change there is a deliberate one the commit body names, never a side effect.
var updateGoldens = flag.Bool("update", false, "regenerate the store SQL goldens")

// golden renders statements the way testdata/sql pins them; fixedAt is the
// fixture clock every deterministic argument carries.
var (
	fixedAt = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	golden  = dbtesting.SQLGolden{FixedClock: fixedAt}
)

// permissiveDB answers every statement: empty rows for queries, one affected
// row for execs, on the pool and on one transaction.
func permissiveDB(vendor string) (*dbtesting.TestDB, *dbtesting.TestTx) {
	db := dbtesting.NewTestDB(vendor)
	empty := dbtesting.NewRowSet("c")
	// Stats reads one row of three aggregates through QueryRow; first match wins,
	// so its expectation precedes the catch-all.
	db.ExpectQuery("COUNT(*)").WillReturnRows(dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(0), int64(0), nil))
	db.ExpectQuery("").WillReturnRows(empty)
	db.ExpectExec("").WillReturnRowsAffected(1)
	tx := db.ExpectTransaction()
	tx.ExpectQuery("").WillReturnRows(empty)
	tx.ExpectExec("").WillReturnRowsAffected(1)
	return db, tx
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	dbtesting.AssertGolden(t, filepath.Join("testdata", "sql", name+".golden"), got, *updateGoldens)
}

// TestStoreSQLGolden pins the inbox ledger store's SQL per vendor.
func TestStoreSQLGolden(t *testing.T) {
	cases := []struct {
		vendor string
		build  func() (Store, error)
	}{
		{dbtypes.PostgreSQL, func() (Store, error) { return NewPostgresStore("gobricks_inbox") }},
		{dbtypes.Oracle, func() (Store, error) { return NewOracleStore("gobricks_inbox") }},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			store, err := tc.build()
			require.NoError(t, err)
			ctx := context.Background()
			db, tx := permissiveDB(tc.vendor)
			var out strings.Builder
			step := func(name string, fn func() error) {
				t.Helper()
				require.NoError(t, fn(), name)
				fmt.Fprintf(&out, "== %s\n", name)
			}
			step("CreateTable", func() error { return store.CreateTable(ctx, db) })
			step("MarkProcessed", func() error {
				_, err := store.MarkProcessed(ctx, tx, Record{TenantID: "acme", EventID: "evt-1", ProcessedAt: fixedAt})
				return err
			})
			step("MarkProcessed_single_tenant", func() error {
				_, err := store.MarkProcessed(ctx, tx, Record{TenantID: "", EventID: "evt-2", ProcessedAt: fixedAt})
				return err
			})
			step("DeleteProcessed", func() error { _, err := store.DeleteProcessed(ctx, db, fixedAt); return err })
			compareGolden(t, "inbox_"+tc.vendor, out.String()+golden.Render(db, tx))
		})
	}
}

// TestHoldStoreSQLGolden pins the hold ledger store's SQL per vendor: every
// HoldStore method, driven with fixed fixtures.
