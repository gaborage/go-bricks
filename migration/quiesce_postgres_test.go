package migration

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostgresQuiesceControllerRejectsNilDB(t *testing.T) {
	_, err := NewPostgresQuiesceController(nil, "")
	require.Error(t, err)
}

func TestNewPostgresQuiesceControllerRejectsBadTableName(t *testing.T) {
	for _, bad := range []string{"bad name!", "a.b.c", "1leading", ""} {
		// "" resolves to the default, so only the truly-bad names must fail.
		if bad == "" {
			continue
		}
		_, err := NewPostgresQuiesceController(new(sql.DB), bad)
		require.ErrorIs(t, err, ErrInvalidQuiesceTable, "name %q must be rejected", bad)
	}
}

func TestNewPostgresQuiesceControllerDefaultsTableName(t *testing.T) {
	c, err := NewPostgresQuiesceController(new(sql.DB), "")
	require.NoError(t, err)
	assert.Equal(t, DefaultQuiesceTable, c.tableName)
	assert.Contains(t, c.quotedTable, DefaultQuiesceTable)
}

func TestNewPostgresQuiesceControllerAcceptsSchemaQualified(t *testing.T) {
	c, err := NewPostgresQuiesceController(new(sql.DB), "ops.quiesce_flags")
	require.NoError(t, err)
	assert.Equal(t, `"ops"."quiesce_flags"`, c.quotedTable)
}

func TestPostgresQuiesceTableDDLSubstitutesIdentifierOnly(t *testing.T) {
	// Security precedent: the DDL has exactly one %s (the validated identifier);
	// every value-side input flows through $N placeholders, never %s.
	assert.Equal(t, 1, strings.Count(PostgresQuiesceTableDDL, "%s"))
}
