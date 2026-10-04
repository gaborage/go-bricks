package testing

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestAssertTxOptions(t *testing.T) {
	serializableReadOnly := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}
	tests := []struct {
		name     string
		recorded *sql.TxOptions
		want     *sql.TxOptions
		wantFail bool
	}{
		{name: "match_passes", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}},
		{name: "both_nil_passes"},
		{name: "nil_vs_non_nil_fails", want: &sql.TxOptions{}, wantFail: true},
		{name: "non_nil_vs_nil_fails", recorded: &sql.TxOptions{}, wantFail: true},
		{name: "isolation_mismatch_fails", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true}, wantFail: true},
		{name: "read_only_mismatch_fails", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelSerializable}, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := NewTestDB(dbtypes.PostgreSQL)
			db.ExpectTransaction()
			tx, err := db.BeginTx(t.Context(), tt.recorded)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()

			recorder := &testing.T{}
			AssertTxOptions(recorder, tx.(*TestTx), tt.want)

			assert.Equal(t, tt.wantFail, recorder.Failed())
		})
	}
}
