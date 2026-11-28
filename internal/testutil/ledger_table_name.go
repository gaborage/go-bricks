package testutil

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database/identifier"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// LedgerTableNameCase is one table name handed to a ledger store constructor.
type LedgerTableNameCase struct {
	Name    string
	Oracle  bool // build with the Oracle constructor instead of the PostgreSQL one
	Table   string
	Refused bool
}

// LedgerStoreConstructors are the outbox's or the inbox's two store constructors.
type LedgerStoreConstructors[S any] struct {
	Module   string // error prefix, e.g. "outbox"
	Postgres func(string) (S, error)
	Oracle   func(string) (S, error)
}

// AssertTableNameCharset runs each case through its constructor. A refused name
// returns a nil store and an error matching identifier.ErrIdentifierCharset that
// names the module, the table and PostgreSQL; an accepted one returns a store.
func (c LedgerStoreConstructors[S]) AssertTableNameCharset(t *testing.T, cases []LedgerTableNameCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			newStore := c.Postgres
			if tc.Oracle {
				newStore = c.Oracle
			}
			store, err := newStore(tc.Table)
			if !tc.Refused {
				require.NoError(t, err)
				assert.NotNil(t, store)
				return
			}
			require.ErrorIs(t, err, identifier.ErrIdentifierCharset)
			assert.Nil(t, store)
			assert.Contains(t, err.Error(),
				fmt.Sprintf("%s: table name %q is not valid for %s", c.Module, tc.Table, dbtypes.PostgreSQL))
		})
	}
}

// AssertOracleGrammarRefusal pins that an Oracle-invalid name keeps the shared
// sqlid refusal, which carries no identifier sentinel.
func (c LedgerStoreConstructors[S]) AssertOracleGrammarRefusal(t *testing.T) {
	t.Helper()
	store, err := c.Oracle("ev ents")
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "invalid identifier characters")
	assert.NotErrorIs(t, err, identifier.ErrIdentifierCharset)
}

// AssertConfigurationFault pins a table-name refusal at module Init: it carries
// want, matches identifier.ErrIdentifierCharset, and offers none of the
// database-state hints (migrations, auto-create, "is not usable").
func AssertConfigurationFault(t *testing.T, err error, module, want string) {
	t.Helper()
	require.Error(t, err)
	assert.Contains(t, err.Error(), want)
	assert.NotContains(t, err.Error(), "run migrations")
	assert.NotContains(t, err.Error(), module+".autocreatetable")
	assert.NotContains(t, err.Error(), "is not usable")
	assert.ErrorIs(t, err, identifier.ErrIdentifierCharset)
}
