package outbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTableNameValid(t *testing.T) {
	tests := []struct {
		name  string
		table string
	}{
		{name: "simple", table: "gobricks_outbox"},
		{name: "underscore_prefix", table: "_outbox"},
		{name: "with_numbers", table: "outbox_v2"},
		{name: "schema_qualified", table: "myschema.outbox_events"},
		{name: "with_dollar", table: "outbox$events"},
		{name: "with_hash", table: "outbox#events"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, validateTableName(tt.table))
		})
	}
}

func TestValidateTableNameInvalid(t *testing.T) {
	tests := []struct {
		name  string
		table string
		want  string
	}{
		{name: "empty", table: "", want: "must not be empty"},
		{name: "sql_injection_semicolon", table: "users; DROP TABLE users", want: "dangerous SQL"},
		{name: "sql_comment_dash", table: "users--comment", want: "dangerous SQL"},
		{name: "sql_comment_block", table: "users/*comment*/", want: "dangerous SQL"},
		{name: "starts_with_number", table: "1table", want: "invalid identifier"},
		{name: "contains_space", table: "my table", want: "invalid identifier"},
		{name: "contains_quote", table: `my"table`, want: "invalid identifier"},
		{name: "too_many_dots", table: "a.b.c", want: "too many dot-separated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTableName(tt.table)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestNewPostgresStoreValidTableName(t *testing.T) {
	store, err := NewPostgresStore("gobricks_outbox")
	assert.NoError(t, err)
	assert.NotNil(t, store)
}

func TestNewPostgresStoreInvalidTableName(t *testing.T) {
	_, err := NewPostgresStore("invalid;table")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dangerous SQL")
}

func TestNewOracleStoreValidTableName(t *testing.T) {
	store, err := NewOracleStore("gobricks_outbox")
	assert.NoError(t, err)
	assert.NotNil(t, store)
}

func TestNewOracleStoreInvalidTableName(t *testing.T) {
	_, err := NewOracleStore("invalid;table")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dangerous SQL")
}

// TestValidateTableNameLengthBound pins headroom for every identifier the store DERIVES
// from the table's segment under PostgreSQL's 63-byte truncation. The longest is
// "idx_<segment>_published" (+14), not the "<segment>_leader" companion (+7), so the bound
// is 49. Two silent failures sit past it: a 63-byte name collapses onto its own companion
// (CreateTable skips the leader table and seeds the ledger), and a 50-to-56-byte name
// truncates the pending and published index names into each other. Measured on the table
// segment so a schema prefix does not spend the budget, and applied to both vendors because
// PostgreSQL's limit is the binding one (Oracle allows 128).
func TestValidateTableNameLengthBound(t *testing.T) {
	tests := []struct {
		name  string
		table string
		want  string
	}{
		{name: "at_the_bound_49_bytes", table: strings.Repeat("a", 49)},
		{name: "one_over_the_bound_50_bytes", table: strings.Repeat("a", 50), want: "50"},
		{name: "index_names_would_collide_at_52_bytes", table: strings.Repeat("a", 52), want: "49"},
		{name: "collides_with_itself_at_63_bytes", table: strings.Repeat("a", 63), want: "49"},
		{name: "schema_prefix_does_not_count", table: "averylongschemaname." + strings.Repeat("a", 49)},
		{name: "long_table_under_a_schema_still_rejected", table: "s." + strings.Repeat("a", 50), want: "49"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTableName(tt.table)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Contains(t, err.Error(), "_published")
		})
	}
}
