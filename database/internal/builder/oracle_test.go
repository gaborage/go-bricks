package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestQuoteOracleColumnHandlesReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	if got := qb.quoteOracleColumn("number"); got != `"number"` {
		t.Fatalf("expected reserved word to be quoted, got %s", got)
	}
	if got := qb.quoteOracleColumn("name"); got != "name" {
		t.Fatalf("expected non-reserved word to remain unchanged")
	}
}

func TestQuoteOracleColumnsForDMLUppercasesReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	cols := qb.quoteOracleColumnsForDML("id", "number")
	if cols[0] != "id" || cols[1] != `"NUMBER"` {
		t.Fatalf("unexpected quoting result: %v", cols)
	}
}

func TestBuildOraclePaginationClause(t *testing.T) {
	if clause := buildOraclePaginationClause(0, 0); clause != "" {
		t.Fatalf("expected empty clause, got %s", clause)
	}
	if clause := buildOraclePaginationClause(5, 0); clause != "FETCH NEXT 5 ROWS ONLY" {
		t.Fatalf("unexpected clause: %s", clause)
	}
	if clause := buildOraclePaginationClause(5, 10); clause != "OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY" {
		t.Fatalf("unexpected clause with offset: %s", clause)
	}
}

func TestBuildUpsertOracleGeneratesMergeStatement(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	insertColumns := map[string]any{
		"id":   1,
		"name": "alice",
	}
	updateColumns := map[string]any{
		"name": "bob",
	}
	conflictColumns := []string{"id"}

	sql, args, err := qb.BuildUpsert("users", conflictColumns, insertColumns, updateColumns)
	require.NoError(t, err)

	if !strings.HasPrefix(sql, "MERGE INTO users") {
		t.Fatalf("expected MERGE statement, got %s", sql)
	}
	if !strings.Contains(sql, "SELECT :1 AS \"ID\", :2 AS \"NAME\" FROM dual") {
		t.Fatalf("expected using clause with positional binds, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN MATCHED THEN UPDATE SET \"NAME\" = :3") {
		t.Fatalf("expected update clause, got %s", sql)
	}
	if !strings.Contains(sql, "WHEN NOT MATCHED THEN INSERT (\"ID\", \"NAME\") VALUES (source.\"ID\", source.\"NAME\")") {
		t.Fatalf("expected insert clause, got %s", sql)
	}

	require.Len(t, args, 3)
	if args[0] != 1 || args[1] != "alice" {
		t.Fatalf("unexpected using clause args: %v", args)
	}
}

func TestBuildUpsertOracleRequiresConflictColumns(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)
	_, _, err := qb.BuildUpsert("users", nil, map[string]any{"id": 1}, nil)
	if err == nil {
		t.Fatalf("expected error when conflict columns missing")
	}
}

func TestBuildUpsertNonOracleFallsBack(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)
	sql, args, err := qb.BuildUpsert("users", []string{"id"}, map[string]any{"id": 1}, map[string]any{"name": "bob"})
	require.NoError(t, err)
	if !strings.Contains(sql, "ON CONFLICT") {
		t.Fatalf("expected PostgreSQL fallback, got %s", sql)
	}
	require.NotEmpty(t, args)
}
