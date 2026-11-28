package testutil

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gaborage/go-bricks/database/identifier"
)

type fakeLedgerStore struct{}

func fakeLedgerConstructor(refuseHash bool) func(string) (*fakeLedgerStore, error) {
	return func(name string) (*fakeLedgerStore, error) {
		if strings.Contains(name, " ") {
			return nil, errors.New("fake: table name part contains invalid identifier characters")
		}
		if refuseHash && strings.Contains(name, "#") {
			return nil, fmt.Errorf("fake: table name %q is not valid for postgresql: %w", name, identifier.ErrIdentifierCharset)
		}
		return &fakeLedgerStore{}, nil
	}
}

func TestLedgerStoreConstructorsAcceptAVendorSplitConstructorPair(t *testing.T) {
	c := LedgerStoreConstructors[*fakeLedgerStore]{
		Module:   "fake",
		Postgres: fakeLedgerConstructor(true),
		Oracle:   fakeLedgerConstructor(false),
	}

	c.AssertTableNameCharset(t, []LedgerTableNameCase{
		{Name: "postgresql_refuses_hash", Table: "ev#ents", Refused: true},
		{Name: "postgresql_accepts_dollar", Table: "ev$ents"},
		{Name: "oracle_accepts_hash", Oracle: true, Table: "ev#ents"},
	})
	c.AssertOracleGrammarRefusal(t)
}

func TestAssertConfigurationFaultAcceptsACharsetRefusal(t *testing.T) {
	err := fmt.Errorf("fake: failed to create store: fake: table name %q is not valid for postgresql: %w",
		"ev#ents", identifier.ErrIdentifierCharset)

	AssertConfigurationFault(t, err, "fake", `fake: table name "ev#ents" is not valid for postgresql`)
}
