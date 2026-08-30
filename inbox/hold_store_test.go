package inbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
