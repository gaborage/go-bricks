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
	dbtesting.Compare(t, filepath.Join("testdata", "sql", name+".golden"), got, *updateGoldens)
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
func TestHoldStoreSQLGolden(t *testing.T) {
	cases := []struct {
		vendor string
		build  func() (HoldStore, error)
	}{
		{dbtypes.PostgreSQL, func() (HoldStore, error) { return NewPostgresHoldStore("gobricks_inbox_hold") }},
		{dbtypes.Oracle, func() (HoldStore, error) { return NewOracleHoldStore("gobricks_inbox_hold") }},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			store, err := tc.build()
			require.NoError(t, err)
			ctx := context.Background()
			db, tx := permissiveDB(tc.vendor)
			row := &HoldRow{
				Consumer: "orders", Stream: "orders-s", Offset: 7, TenantID: "acme",
				Data: []byte(`{"id":1}`), Properties: []byte(`{"k":"v"}`), HeldAt: fixedAt,
			}
			var out strings.Builder
			step := func(name string, fn func() error) {
				t.Helper()
				require.NoError(t, fn(), name)
				fmt.Fprintf(&out, "== %s\n", name)
			}
			step("CreateTable", func() error { return store.CreateTable(ctx, db) })
			step("Park", func() error { _, err := store.Park(ctx, tx, row); return err })
			step("HeldTenants", func() error { _, err := store.HeldTenants(ctx, db, "orders"); return err })
			step("ListTenants", func() error { _, err := store.ListTenants(ctx, db, "orders"); return err })
			step("DueTenants", func() error { _, err := store.DueTenants(ctx, db, "orders", 10); return err })
			step("AcquireLease", func() error {
				_, err := store.AcquireLease(ctx, db, "orders", "acme", "owner-1", 30*time.Second)
				return err
			})
			step("NextRows", func() error { _, err := store.NextRows(ctx, db, "orders", "acme", 5); return err })
			step("DeleteRow", func() error {
				_, err := store.DeleteRow(ctx, db, "orders", "orders-s", 7, "acme", "owner-1")
				return err
			})
			step("Defer", func() error {
				_, err := store.Defer(ctx, db, "orders", "acme", "owner-1", 45*time.Second, "boom")
				return err
			})
			step("ReleaseLease", func() error { return store.ReleaseLease(ctx, db, "orders", "acme", "owner-1") })
			step("Release", func() error { _, err := store.Release(ctx, db, "orders", "acme", "owner-1"); return err })
			step("Stats", func() error { _, err := store.Stats(ctx, db, "orders"); return err })
			compareGolden(t, "hold_"+tc.vendor, out.String()+golden.Render(db, tx))
		})
	}
}
