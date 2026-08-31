package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// holdModule is an initialized module with the hold on, whose shared resolver
// hands out the given database.
func holdModule(t *testing.T, db dbtypes.Interface) *Module {
	t.Helper()
	m := NewModule()
	m.SetSharedResolvers(func(context.Context) (dbtypes.Interface, error) { return db, nil }, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{
			Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyShared,
			Hold: config.InboxHoldConfig{Enabled: true},
		},
	}
	require.NoError(t, m.Init(deps))
	return m
}

// holdReadyDB answers the startup probes: the inbox's own, then the hold's.
func holdReadyDB() *dbtesting.TestDB {
	db := probeReadyDB()
	db.ExpectQuery(`SELECT tenant_id`).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
	return db
}

// TestHoldLedgerIsNilUntilTheHoldIsOn pins that the port exists only for a module
// that actually holds — the streams manager reads a nil as "no hold configured".
func TestHoldLedgerIsNilUntilTheHoldIsOn(t *testing.T) {
	m := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyPerTenant},
	}
	require.NoError(t, m.Init(deps))

	assert.Nil(t, m.HoldLedger(), "a module without a hold offers no ledger")
}

// TestHoldLedgerParksThroughTheSharedResolver pins where a park lands: the
// control-plane database, in one transaction, with the properties encoded.
func TestHoldLedgerParksThroughTheSharedResolver(t *testing.T) {
	db := holdReadyDB()
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO gobricks_inbox_hold_tenant`).WillReturnRowsAffected(1)
	tx.ExpectExec(`INSERT INTO gobricks_inbox_hold`).WillReturnRowsAffected(1)
	m := holdModule(t, db)

	ledger := m.HoldLedger()
	require.NotNil(t, ledger)

	err := ledger.Park(t.Context(), &streams.HeldMessage{
		Consumer:   "orders-processor",
		Stream:     "orders-0",
		Offset:     41,
		TenantID:   "tenant-a",
		Data:       []byte(`{"id":1}`),
		Properties: map[string]any{"x-tenant-id": "tenant-a"},
		HeldAt:     time.Now(),
	})

	require.NoError(t, err)
	execs := tx.ExecLog()
	require.Len(t, execs, 2, "one park is the tenant marker and its row")
	// Located rather than indexed: the row insert is built by the query builder,
	// which orders columns its own way. The row's DATA is JSON too, so the
	// properties are identified by what they decode to, not by shape.
	assert.True(t, boundJSONHas(execs[1].Args, "x-tenant-id", "tenant-a"),
		"the properties reach the column as the JSON a replay decodes back")
}

// TestHoldLedgerParksWithoutProperties pins the nil-map case: a message with no
// application properties stores a NULL rather than the four bytes of "null".
func TestHoldLedgerParksWithoutProperties(t *testing.T) {
	db := holdReadyDB()
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO gobricks_inbox_hold_tenant`).WillReturnRowsAffected(1)
	tx.ExpectExec(`INSERT INTO gobricks_inbox_hold`).WillReturnRowsAffected(1)
	m := holdModule(t, db)

	err := m.HoldLedger().Park(t.Context(), &streams.HeldMessage{
		Consumer: "orders-processor", Stream: "orders-0", Offset: 41, TenantID: "tenant-a",
	})

	require.NoError(t, err)
	execs := tx.ExecLog()
	require.Len(t, execs, 2)
	assert.False(t, boundJSONHas(execs[1].Args, "x-tenant-id", "tenant-a"),
		"no properties means no encoded map at all")
	for _, arg := range execs[1].Args {
		assert.NotEqual(t, []byte("null"), arg, "and not the four bytes of an encoded nil")
	}
}

// TestHoldLedgerReadsTheHeldTenants pins the other half of the port.
func TestHoldLedgerReadsTheHeldTenants(t *testing.T) {
	// One expectation serves both readers: TestDB matches first-registered-wins and
	// never consumes an expectation, so the startup probe and the ledger call below
	// answer from the same rows.
	db := probeReadyDB()
	db.ExpectQuery(`SELECT tenant_id`).
		WillReturnRows(dbtesting.NewRowSet("tenant_id").AddRow("acme"))
	m := holdModule(t, db)

	tenants, err := m.HoldLedger().HeldTenants(t.Context(), "orders-processor")

	require.NoError(t, err)
	assert.Equal(t, []string{"acme"}, tenants)
}

// TestInitProbesTheHoldTable pins that a hold whose table is missing fails
// startup rather than at the first park — the same fail-fast the inbox ledger has.
func TestInitProbesTheHoldTable(t *testing.T) {
	db := probeReadyDB()
	db.ExpectQuery(`SELECT tenant_id`).WillReturnError(errors.New("relation does not exist"))
	m := NewModule()
	m.SetSharedResolvers(func(context.Context) (dbtypes.Interface, error) { return db, nil }, nil)
	deps := testDeps()
	deps.Config = &config.Config{
		Inbox: config.InboxConfig{
			Enabled: true, RetentionPeriod: time.Hour, Tenancy: config.TenancyShared,
			Hold: config.InboxHoldConfig{Enabled: true},
		},
	}

	err := m.Init(deps)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gobricks_inbox_hold")
}

// boundJSONHas reports whether any bound argument is a JSON object carrying
// key=value. Identifying the properties by content rather than by position keeps
// the assertion honest when the query builder reorders an insert's columns.
func boundJSONHas(args []any, key, value string) bool {
	for _, arg := range args {
		encoded, ok := arg.([]byte)
		if !ok {
			continue
		}
		var decoded map[string]any
		if json.Unmarshal(encoded, &decoded) != nil {
			continue
		}
		if decoded[key] == value {
			return true
		}
	}
	return false
}
