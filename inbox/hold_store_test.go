package inbox

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestValidateHoldTableNameBoundsEveryDerivedName pins the bound both vendors
// have to live with: the tenant table's name is derived from this one, and
// PostgreSQL silently TRUNCATES an identifier past 63 bytes rather than
// refusing it — which would quietly point two deployments at one table.
func TestValidateHoldTableNameBoundsEveryDerivedName(t *testing.T) {
	longest := strings.Repeat("a", maxHoldTableNameLen)

	tests := []struct {
		name    string
		table   string
		wantErr string
	}{
		{name: "plain_name_is_accepted", table: "gobricks_inbox_hold"},
		{name: "the_longest_name_is_accepted", table: longest},
		{name: "one_over_is_refused", table: longest + "a", wantErr: "too long"},
		{name: "qualified_name_is_refused", table: "schema.hold", wantErr: "hold"},
		{name: "empty_name_is_refused", table: "", wantErr: "hold"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHoldTableName(tc.table)

			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestHoldTableNameBoundLeavesRoomForEveryDerivedName pins WHY the bound is what
// it is, and it is the INDEX names that set it, not the tenant table: PostgreSQL
// truncates past 63 bytes instead of refusing, and the two index names share the
// prefix `idx_<table>_tenant_`, so a name long enough to truncate them collapses
// BOTH to the same identifier — at which point the second CREATE INDEX quietly
// does nothing and the drain's due-tenant query runs unindexed forever.
func TestHoldTableNameBoundLeavesRoomForEveryDerivedName(t *testing.T) {
	longest := strings.Repeat("a", maxHoldTableNameLen)

	derived := []string{
		longest + holdTenantTableSuffix,
		"idx_" + longest + "_tenant_order",
		"idx_" + longest + "_tenant_due",
		// PostgreSQL names a primary key <table>_pkey on its own.
		longest + holdTenantTableSuffix + "_pkey",
	}
	for _, name := range derived {
		assert.LessOrEqual(t, len(name), postgresMaxIdentifierLen,
			"%q is derived from the longest legal table name and must fit", name)
	}

	assert.Greater(t, len("idx_"+longest+"a_tenant_order"), postgresMaxIdentifierLen,
		"one byte more would not fit, which is what makes this the bound")
}

// TestHoldStoreConstructorsRefuseABadTableName pins that neither vendor's
// constructor hands back a store that would build SQL from an unusable name.
func TestHoldStoreConstructorsRefuseABadTableName(t *testing.T) {
	for _, newStore := range map[string]func(string) (HoldStore, error){
		"postgres": NewPostgresHoldStore,
		"oracle":   NewOracleHoldStore,
	} {
		store, err := newStore("schema.hold")

		require.Error(t, err)
		assert.Nil(t, store)
	}
}

// TestBoundedLimitNeverWraps pins the guard on a caller's row limit: it is a
// count, and a negative one converted to uint64 becomes a number no query should
// carry. Anything below one asks for nothing, which the smallest legal limit says
// honestly.
func TestBoundedLimitNeverWraps(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  uint64
	}{
		{name: "an_ordinary_limit_passes_through", limit: 50, want: 50},
		{name: "the_smallest_limit_passes_through", limit: 1, want: 1},
		{name: "zero_asks_for_the_smallest", limit: 0, want: 1},
		{name: "a_negative_limit_never_wraps", limit: -1, want: 1},
		{name: "the_most_negative_limit_never_wraps", limit: math.MinInt, want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, boundedLimit(tc.limit))
		})
	}
}

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
