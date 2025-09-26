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

	expectedSQL := "INSERT INTO users (id,name,updated_at) VALUES ($1,$2,$3) ON CONFLICT (id, tenant_id) DO UPDATE SET name = EXCLUDED.name, updated_at = EXCLUDED.updated_at"
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL generated: %s", sql)
	}

	require.Len(t, args, 3)
	if args[0] != 42 || args[1] != "alice" || args[2] != "2023-05-01" {
		t.Fatalf("unexpected argument ordering: %v", args)
	}
}
