package builder

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestBuildPostgreSQLUpsertProducesDeterministicSql(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	insertColumns := map[string]any{
		"name":       "alice",
		"id":         42,
		"updated_at": "2023-05-01",
	}
	updateColumns := map[string]any{
		"updated_at": "2023-06-01",
		"name":       "bob",
	}
	conflictColumns := []string{"tenant_id", "id"}

	sql, args, err := qb.buildPostgreSQLUpsert("users", conflictColumns, insertColumns, updateColumns)
	require.NoError(t, err)

	// The on-conflict UPDATE must bind the caller's update values ("bob"/"2023-06-01") as
	// parameters — NOT reuse EXCLUDED (the insert values), which silently ignored them and
	// diverged from Oracle's MERGE. Update placeholders follow the insert placeholders.
	expectedSQL := "INSERT INTO users (\"id\",\"name\",\"updated_at\") VALUES ($1,$2,$3) ON CONFLICT (\"id\", \"tenant_id\") DO UPDATE SET \"name\" = $4, \"updated_at\" = $5"
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL generated: %s", sql)
	}

	// Insert values first (sorted id,name,updated_at), then update values (sorted name,updated_at).
	require.Len(t, args, 5)
	if args[0] != 42 || args[1] != "alice" || args[2] != "2023-05-01" || args[3] != "bob" || args[4] != "2023-06-01" {
		t.Fatalf("unexpected argument ordering: %v", args)
	}
}

func TestBuildPostgreSQLUpsertBindsUpdateOnlyColumnAbsentFromInsert(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	// "version" is updated but NOT inserted. Under the old EXCLUDED behavior this produced
	// EXCLUDED."version", referencing a column absent from the insert (a runtime SQL error).
	// Binding the value makes it valid.
	insertColumns := map[string]any{"id": 1, "name": "a"}
	updateColumns := map[string]any{"version": 7}

	sql, args, err := qb.buildPostgreSQLUpsert("t", []string{"id"}, insertColumns, updateColumns)
	require.NoError(t, err)
	require.NotContains(t, sql, "EXCLUDED")
	require.Contains(t, sql, "\"version\" = $3")
	require.Equal(t, []any{1, "a", 7}, args)
}

func TestBuildPostgreSQLUpsertDoNothingWhenNoUpdateColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args, err := qb.buildPostgreSQLUpsert("t", []string{"id"}, map[string]any{"id": 1}, map[string]any{})
	require.NoError(t, err)
	require.Contains(t, sql, "DO NOTHING")
	require.Equal(t, []any{1}, args, "no update values appended for DO NOTHING")
}
