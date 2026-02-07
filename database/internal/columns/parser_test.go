package columns

import (
	"reflect"
	"testing"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	dangerousSQLCharsErrMsg = "dangerous SQL characters"
	invalidIdentifierErrMsg = "invalid identifier characters"
)

// Test structs for parser tests
type ValidUser struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

type OracleReservedWords struct {
	ID     int64  `db:"id"`
	Number string `db:"number"` // Oracle reserved word
	Level  int    `db:"level"`  // Oracle reserved word
	Size   string `db:"size"`   // Oracle reserved word
}

type MixedExport struct {
	ID           int64  `db:"id"`
	Name         string `db:"name"`
	unexported   string `db:"hidden"` //nolint:unused // Should be ignored (unexported)
	NoTag        string
	ExplicitSkip string `db:"-"` // Explicit ignore
}

type EmptyStruct struct{}

type NoDBTags struct {
	ID   int64
	Name string
}

type DangerousTags struct {
	ID         int64  `db:"id; DROP TABLE users--"`
	Name       string `db:"name"`
	SQLComment string `db:"col/* comment */"`
}

type QuotedTags struct {
	ID         int64  `db:"\"id\""` // Pre-quoted (invalid)
	Name       string `db:"'name'"` // Single quoted (invalid)
	SingleChar string `db:"x"`
}

// TestParseStructValidStruct tests successful parsing of a valid struct
func TestParseStructValidStruct(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})

	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, "ValidUser", metadata.TypeName)
	assert.Len(t, metadata.Columns, 3)

	// Verify first column
	assert.Equal(t, "ID", metadata.Columns[0].FieldName)
	assert.Equal(t, "id", metadata.Columns[0].DBColumn)
	assert.Equal(t, "id", metadata.Columns[0].QuotedColumn) // PostgreSQL: no quoting
	assert.Equal(t, 0, metadata.Columns[0].FieldIndex)
	assert.Equal(t, reflect.TypeOf(int64(0)), metadata.Columns[0].FieldType)

	// Verify second column
	assert.Equal(t, "Name", metadata.Columns[1].FieldName)
	assert.Equal(t, "name", metadata.Columns[1].DBColumn)

	// Verify third column
	assert.Equal(t, "Email", metadata.Columns[2].FieldName)
	assert.Equal(t, "email", metadata.Columns[2].DBColumn)

	// Verify columnsByField map
	assert.Len(t, metadata.columnsByField, 3)
	assert.NotNil(t, metadata.columnsByField["ID"])
	assert.NotNil(t, metadata.columnsByField["Name"])
	assert.NotNil(t, metadata.columnsByField["Email"])
}

// TestParseStructNilPointer tests parsing with a nil pointer value
func TestParseStructNilPointer(t *testing.T) {
	var user *ValidUser

	metadata, err := parseStruct(dbtypes.PostgreSQL, user)

	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, "ValidUser", metadata.TypeName)
}

// TestParseStructOracleReservedWords tests Oracle-specific quoting
func TestParseStructOracleReservedWords(t *testing.T) {
	metadata, err := parseStruct(dbtypes.Oracle, &OracleReservedWords{})

	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Len(t, metadata.Columns, 4)

	// ID: not a reserved word, no quoting
	assert.Equal(t, "id", metadata.Columns[0].DBColumn)
	assert.Equal(t, "id", metadata.Columns[0].QuotedColumn)

	// Number: Oracle reserved word, should be quoted
	assert.Equal(t, "number", metadata.Columns[1].DBColumn)
	assert.Equal(t, `"number"`, metadata.Columns[1].QuotedColumn)

	// Level: Oracle reserved word, should be quoted
	assert.Equal(t, "level", metadata.Columns[2].DBColumn)
	assert.Equal(t, `"level"`, metadata.Columns[2].QuotedColumn)

	// Size: Oracle reserved word, should be quoted
	assert.Equal(t, "size", metadata.Columns[3].DBColumn)
	assert.Equal(t, `"size"`, metadata.Columns[3].QuotedColumn)
}

// TestParseStructPostgreSQL tests PostgreSQL vendor (no quoting)
func TestParseStructPostgreSQL(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, &OracleReservedWords{})

	require.NoError(t, err)
	require.NotNil(t, metadata)

	// PostgreSQL doesn't quote reserved words in this implementation
	for _, col := range metadata.Columns {
		assert.Equal(t, col.DBColumn, col.QuotedColumn, "PostgreSQL should not quote column names")
	}
}

// TestParseStructMixedExport tests handling of unexported fields and explicit skips
func TestParseStructMixedExport(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, &MixedExport{})

	require.NoError(t, err)
	require.NotNil(t, metadata)

	// Should only have 2 columns (ID and Name)
	// unexported, NoTag, and ExplicitSkip should be ignored
	assert.Len(t, metadata.Columns, 2)

	assert.Equal(t, "ID", metadata.Columns[0].FieldName)
	assert.Equal(t, "Name", metadata.Columns[1].FieldName)
}

// TestParseStructEmptyStruct tests struct with no db-tagged fields
func TestParseStructEmptyStruct(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, &EmptyStruct{})

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "no fields with `db` tags found")
}

// TestParseStructNoDBTags tests struct with fields but no db tags
func TestParseStructNoDBTags(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, &NoDBTags{})

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "no fields with `db` tags found")
}

// TestParseStructNotPointer tests error handling for non-pointer input
func TestParseStructNotPointer(t *testing.T) {
	metadata, err := parseStruct(dbtypes.PostgreSQL, ValidUser{}) // Not a pointer

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "expects a pointer to struct")
}

// TestParseStructPointerToNonStruct tests error handling for pointer to non-struct
func TestParseStructPointerToNonStruct(t *testing.T) {
	str := "not a struct"
	metadata, err := parseStruct(dbtypes.PostgreSQL, &str)

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "expects a pointer to struct")
}

// TestParseStructDangerousTags tests security validation for SQL injection
func TestParseStructDangerousTags(t *testing.T) {
	// Test semicolon
	type SemicolonTag struct {
		ID int64 `db:"id; DROP TABLE users--"`
	}
	metadata, err := parseStruct(dbtypes.PostgreSQL, &SemicolonTag{})
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), dangerousSQLCharsErrMsg)

	// Test SQL comment
	type CommentTag struct {
		ID int64 `db:"col/* comment */"`
	}
	metadata, err = parseStruct(dbtypes.PostgreSQL, &CommentTag{})
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), dangerousSQLCharsErrMsg)

	// Test double dash
	type DashTag struct {
		ID int64 `db:"col--"`
	}
	metadata, err = parseStruct(dbtypes.PostgreSQL, &DashTag{})
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), dangerousSQLCharsErrMsg)
}

// TestParseStructInvalidIdentifier tests validation for invalid identifier characters
func TestParseStructInvalidIdentifier(t *testing.T) {
	type InvalidIdentifier struct {
		ID string `db:"id || 1=1"`
	}

	metadata, err := parseStruct(dbtypes.PostgreSQL, &InvalidIdentifier{})

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), invalidIdentifierErrMsg)
}

// TestParseStructQuotedTags tests that pre-quoted tags are rejected
func TestParseStructQuotedTags(t *testing.T) {
	// Test double quotes
	type DoubleQuoteTag struct {
		ID int64 `db:"\"id\""`
	}
	metadata, err := parseStruct(dbtypes.PostgreSQL, &DoubleQuoteTag{})
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "contains quotes")

	// Test single quotes
	type SingleQuoteTag struct {
		Name string `db:"'name'"`
	}
	metadata, err = parseStruct(dbtypes.PostgreSQL, &SingleQuoteTag{})
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "contains quotes")
}

// TestValidateDBTag tests the tag validation function
func TestValidateDBTag(t *testing.T) {
	tests := []struct {
		name        string
		tag         string
		structName  string
		fieldName   string
		expectError bool
	}{
		{
			name:        "valid simple tag",
			tag:         "user_id",
			structName:  "User",
			fieldName:   "ID",
			expectError: false,
		},
		{
			name:        "valid tag with underscores",
			tag:         "created_at",
			structName:  "User",
			fieldName:   "CreatedAt",
			expectError: false,
		},
		{
			name:        "invalid semicolon",
			tag:         "id; DROP TABLE",
			structName:  "User",
			fieldName:   "ID",
			expectError: true,
		},
		{
			name:        "invalid SQL comment",
			tag:         "id/* comment */",
			structName:  "User",
			fieldName:   "ID",
			expectError: true,
		},
		{
			name:        "invalid double dash",
			tag:         "id--",
			structName:  "User",
			fieldName:   "ID",
			expectError: true,
		},
		{
			name:        "invalid double quotes",
			tag:         `"id"`,
			structName:  "User",
			fieldName:   "ID",
			expectError: true,
		},
		{
			name:        "invalid single quotes",
			tag:         "'id'",
			structName:  "User",
			fieldName:   "ID",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDBTag(tt.tag, tt.structName, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestIsOracleReservedWord tests the Oracle reserved word detection from sqllex package
func TestIsOracleReservedWord(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		want       bool
	}{
		{"NUMBER is reserved", "NUMBER", true},
		{"number is reserved (case-insensitive)", "number", true},
		{"Level is reserved", "level", true},
		{"SIZE is reserved", "SIZE", true},
		{"id is not reserved", "id", false},
		{"user_id is not reserved", "user_id", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqllex.IsOracleReservedWord(tt.identifier)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestOracleQuoteIdentifier tests Oracle identifier quoting logic
func TestOracleQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name   string
		column string
		want   string
	}{
		{
			name:   "simple column",
			column: "id",
			want:   "id",
		},
		{
			name:   "reserved word NUMBER",
			column: "number",
			want:   `"number"`,
		},
		{
			name:   "reserved word LEVEL",
			column: "level",
			want:   `"level"`,
		},
		{
			name:   "reserved word SIZE",
			column: "size",
			want:   `"size"`,
		},
		{
			name:   "already quoted",
			column: `"ID"`,
			want:   `"ID"`,
		},
		{
			name:   "empty string",
			column: "",
			want:   "",
		},
		{
			name:   "whitespace trimmed",
			column: "  id  ",
			want:   "id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oracleQuoteIdentifier(tt.column)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestApplyVendorQuoting tests vendor-specific quoting dispatch
func TestApplyVendorQuoting(t *testing.T) {
	tests := []struct {
		name   string
		vendor string
		column string
		want   string
	}{
		{
			name:   "Oracle reserved word",
			vendor: dbtypes.Oracle,
			column: "number",
			want:   `"number"`,
		},
		{
			name:   "Oracle simple column",
			vendor: dbtypes.Oracle,
			column: "id",
			want:   "id",
		},
		{
			name:   "PostgreSQL reserved word (no quoting)",
			vendor: dbtypes.PostgreSQL,
			column: "number",
			want:   "number",
		},
		{
			name:   "Unknown vendor",
			vendor: "unknown",
			column: "id",
			want:   "id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyVendorQuoting(tt.vendor, tt.column)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseStructFieldIndexCorrect tests that FieldIndex is correctly set
func TestParseStructFieldIndexCorrect(t *testing.T) {
	type TestStruct struct {
		Ignored1   string // No db tag
		ID         int64  `db:"id"`         // Should be index 1
		Ignored2   string `db:"-"`          // Explicit ignore
		Name       string `db:"name"`       // Should be index 3
		unexported string `db:"unexported"` //nolint:unused // Should be ignored
		Email      string `db:"email"`      // Should be index 5
	}

	metadata, err := parseStruct(dbtypes.PostgreSQL, &TestStruct{})

	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Len(t, metadata.Columns, 3)

	// Verify field indices match struct field positions
	assert.Equal(t, 1, metadata.Columns[0].FieldIndex) // ID at position 1
	assert.Equal(t, 3, metadata.Columns[1].FieldIndex) // Name at position 3
	assert.Equal(t, 5, metadata.Columns[2].FieldIndex) // Email at position 5
}
