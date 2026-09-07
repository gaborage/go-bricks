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
	assert.NotNil(t, store)
	assert.NoError(t, err)
}

func TestNewPostgresStoreInvalidTableName(t *testing.T) {
	_, err := NewPostgresStore("invalid;table")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dangerous SQL")
}

func TestNewOracleStoreValidTableName(t *testing.T) {
	store, err := NewOracleStore("gobricks_outbox")
	assert.NotNil(t, store)
	assert.NoError(t, err)
}

func TestNewOracleStoreInvalidTableName(t *testing.T) {
	_, err := NewOracleStore("invalid;table")
	require.Error(t, err)
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

// TestNewPostgresStoreSchemaSegmentBound pins the schema segment against PostgreSQL's raw
// identifier cap at construction. No derived name decorates the schema — IndexBaseName
// strips it and LeaderTableName appends to the table segment — so the whole 63 bytes are
// spendable, and one byte more is refused here instead of inside the first ToSQL.
func TestNewPostgresStoreSchemaSegmentBound(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   bool
	}{
		{name: "schema_at_the_postgresql_cap", schema: strings.Repeat("s", 63)},
		{name: "schema_one_byte_over_the_postgresql_cap", schema: strings.Repeat("s", 64), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewPostgresStore(tt.schema + ".gobricks_outbox")
			if !tt.want {
				require.NoError(t, err)
				assert.NotNil(t, store)
				return
			}
			require.Error(t, err)
			assert.Nil(t, store)
			assert.Contains(t, err.Error(), tt.schema)
			assert.Contains(t, err.Error(), "64")
			assert.Contains(t, err.Error(), "63")
		})
	}
}

// TestNewOracleStoreSchemaSegmentBound pins the other side of the vendor split: Oracle
// spends 128 bytes on the schema segment, so a 64-byte schema that PostgreSQL refuses
// still boots here, and only 129 is over the cap.
func TestNewOracleStoreSchemaSegmentBound(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   bool
	}{
		{name: "schema_over_the_postgresql_cap_is_fine_on_oracle", schema: strings.Repeat("s", 64)},
		{name: "schema_at_the_oracle_cap", schema: strings.Repeat("s", 128)},
		{name: "schema_one_byte_over_the_oracle_cap", schema: strings.Repeat("s", 129), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewOracleStore(tt.schema + ".gobricks_outbox")
			if !tt.want {
				require.NoError(t, err)
				assert.NotNil(t, store)
				return
			}
			require.Error(t, err)
			assert.Nil(t, store)
			assert.Contains(t, err.Error(), "128")
		})
	}
}
