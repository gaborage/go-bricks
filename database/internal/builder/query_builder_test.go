package builder

import (
	"testing"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCaseInsensitiveLikePostgreSQL(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "name ILIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestBuildCaseInsensitiveLikeOracle(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "UPPER(name) LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%JOHN%", args[0])
}

func TestBuildCaseInsensitiveLikeDefault(t *testing.T) {
	qb := NewQueryBuilder("mysql")

	sql, args := toSQL(t, qb.BuildCaseInsensitiveLike("name", "john"))

	assert.Equal(t, "name LIKE ?", sql)
	require.Len(t, args, 1)
	assert.Equal(t, "%john%", args[0])
}

func TestBuildLimitOffsetSkipsZeroValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.BuildLimitOffset(qb.Select("*").From("users"), 0, 0)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "LIMIT")
	assert.NotContains(t, sql, "OFFSET")
}

func TestBuildLimitOffsetAppliesPositiveValues(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	query := qb.BuildLimitOffset(qb.Select("*").From("users"), 5, 10)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 5")
	assert.Contains(t, sql, "OFFSET 10")
}

func TestBuildLimitOffsetOracleUsesSuffix(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	query := qb.BuildLimitOffset(qb.Select("*").From("users"), 5, 0)

	sql, _, err := query.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "FETCH NEXT 5 ROWS ONLY")
	assert.NotContains(t, sql, "LIMIT")
}

func toSQL(t *testing.T, sqlizer squirrel.Sqlizer) (sql string, args []any) {
	t.Helper()

	sql, args, err := sqlizer.ToSql()
	require.NoError(t, err)

	return sql, args
}

func TestBuildCurrentTimestampByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, expected: "NOW()"},
		{vendor: dbtypes.Oracle, expected: "SYSDATE"},
		{vendor: "sqlite", expected: "NOW()"},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildCurrentTimestamp(); got != tt.expected {
			t.Fatalf("unexpected timestamp for %s: %s", tt.vendor, got)
		}
	}
}

func TestBuildUUIDGenerationByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, expected: "gen_random_uuid()"},
		{vendor: dbtypes.Oracle, expected: "SYS_GUID()"},
		{vendor: "mysql", expected: "UUID()"},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildUUIDGeneration(); got != tt.expected {
			t.Fatalf("unexpected UUID function for %s: %s", tt.vendor, got)
		}
	}
}

func TestBuildBooleanValueByVendor(t *testing.T) {
	cases := []struct {
		vendor string
		input  bool
		expect any
	}{
		{vendor: dbtypes.PostgreSQL, input: true, expect: true},
		{vendor: dbtypes.Oracle, input: true, expect: 1},
		{vendor: dbtypes.Oracle, input: false, expect: 0},
		{vendor: "sqlite", input: false, expect: false},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.BuildBooleanValue(tt.input); got != tt.expect {
			t.Fatalf("unexpected boolean mapping for %s: %v", tt.vendor, got)
		}
	}
}

func TestEscapeIdentifierByVendor(t *testing.T) {
	cases := []struct {
		vendor   string
		name     string
		expected string
	}{
		{vendor: dbtypes.PostgreSQL, name: "foo", expected: `"foo"`},
		{vendor: dbtypes.Oracle, name: "bar", expected: `"bar"`},
		{vendor: "sqlite", name: "baz", expected: `"baz"`},
	}

	for _, tt := range cases {
		qb := NewQueryBuilder(tt.vendor)
		if got := qb.EscapeIdentifier(tt.name); got != tt.expected {
			t.Fatalf("unexpected escaped identifier for %s: %s", tt.vendor, got)
		}
	}
}
