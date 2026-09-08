package inbox

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database"
	dbident "github.com/gaborage/go-bricks/database/identifier"
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

// TestInboxTableNameByteCapsAreVendorDerived pins the two derived bounds by
// value: gremlins does not mutate a const declaration, so only an assertion on
// the numbers keeps a future edit to the derivation honest.
func TestInboxTableNameByteCapsAreVendorDerived(t *testing.T) {
	require.Equal(t, 14, inboxLongestDerivedAffix)
	require.Equal(t, 114, maxTableNameLen, "Oracle's 128-byte cap less the derived index affix")
	require.Equal(t, 49, maxPostgresTableNameLen, "PostgreSQL's 63-byte cap less the derived index affix")
}

// TestValidateTableNameForVendorBounds walks both sides of both vendors'
// boundaries through the store constructors, which are the only callers of the
// vendor-aware check.
func TestValidateTableNameForVendorBounds(t *testing.T) {
	tests := []struct {
		name      string
		newStore  func(string) (Store, error)
		nameLen   int
		wantError bool
	}{
		{"postgres_at_cap_accepted", NewPostgresStore, maxPostgresTableNameLen, false},
		{"postgres_one_over_cap_refused", NewPostgresStore, maxPostgresTableNameLen + 1, true},
		{"postgres_oracle_length_refused", NewPostgresStore, maxTableNameLen, true},
		{"oracle_accepts_postgres_over_cap", NewOracleStore, maxPostgresTableNameLen + 1, false},
		{"oracle_at_cap_accepted", NewOracleStore, maxTableNameLen, false},
		{"oracle_one_over_cap_refused", NewOracleStore, maxTableNameLen + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := tt.newStore(strings.Repeat("a", tt.nameLen))
			if !tt.wantError {
				require.NoError(t, err)
				require.NotNil(t, store)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), "too long")
		})
	}
}

// TestValidateTableNameForVendorErrorNamesCapAndVendor pins the refusal text:
// an operator reading it must learn which vendor refused and at what length.
func TestValidateTableNameForVendorErrorNamesCapAndVendor(t *testing.T) {
	// A name that boots on Oracle today but exceeds PostgreSQL's budget.
	_, err := NewPostgresStore(strings.Repeat("a", maxTableNameLen))
	require.Error(t, err)
	require.Contains(t, err.Error(), "49")
	require.Contains(t, err.Error(), dbtypes.PostgreSQL)
	require.Contains(t, err.Error(), "63-byte cap")

	// Oracle's own bound equals the vendor-blind one, so an over-long Oracle
	// name is refused by the shared check first — with the shared text.
	_, err = NewOracleStore(strings.Repeat("a", maxTableNameLen+1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "114")
	require.Contains(t, err.Error(), fmt.Sprintf("derived Oracle identifiers must fit %d chars", dbident.MaxOracleBytes))
}

// TestValidateTableNameConfigBoundUnchanged pins the vendor-blind, config-time
// path at Oracle's bound, so a later edit cannot silently tighten what an
// operator may configure.
func TestValidateTableNameConfigBoundUnchanged(t *testing.T) {
	require.NoError(t, validateTableName(strings.Repeat("a", maxTableNameLen)))

	err := validateTableName(strings.Repeat("a", maxTableNameLen+1))
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("derived Oracle identifiers must fit %d chars", dbident.MaxOracleBytes))
}

// TestStoreMarkProcessedBuildRefusalIsABuildStageExecError pins #1521's premise
// for the ledger's one build site: a table name the validator accepts but the
// builder refuses reports the build stage, sentinel still reachable.
func TestStoreMarkProcessedBuildRefusalIsABuildStageExecError(t *testing.T) {
	store, err := NewPostgresStore("ev#ents")
	require.NoError(t, err, "the name validator accepts this name; only the builder refuses it")
	ctx := context.Background()
	_, tx := permissiveDB(dbtypes.PostgreSQL)

	_, err = store.MarkProcessed(ctx, tx, Record{TenantID: "acme", EventID: "e-1", ProcessedAt: fixedAt})

	require.Error(t, err)
	var execErr *database.ExecError
	require.ErrorAs(t, err, &execErr)
	assert.Equal(t, database.StageBuild, execErr.Stage)
	assert.Equal(t, "inbox postgres: build mark processed failed", execErr.Op)
	assert.ErrorIs(t, err, dbident.ErrIdentifierCharset)
}
