package columns

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColumnMetadataGet tests the Get method for successful retrieval
func TestColumnMetadataGet(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "TestStruct",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: `"ID"`, FieldIndex: 0},
			{FieldName: "Name", DBColumn: "name", QuotedColumn: "name", FieldIndex: 1},
			{FieldName: "Level", DBColumn: "level", QuotedColumn: `"LEVEL"`, FieldIndex: 2},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	tests := []struct {
		name      string
		fieldName string
		want      string
	}{
		{
			name:      "get ID column",
			fieldName: "ID",
			want:      `"ID"`,
		},
		{
			name:      "get Name column",
			fieldName: "Name",
			want:      "name",
		},
		{
			name:      "get Level column (Oracle reserved word)",
			fieldName: "Level",
			want:      `"LEVEL"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadata.Get(tt.fieldName)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColumnMetadataGetPanic tests that Get panics on invalid field names
func TestColumnMetadataGetPanic(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "TestStruct",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
		},
		columnsByField: map[string]*Column{
			"ID": {FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
		},
	}

	assert.Panics(t, func() {
		metadata.Get("NonExistent")
	}, "Get should panic on non-existent field name")
}

// TestColumnMetadataFields tests the Fields method for bulk retrieval
func TestColumnMetadataFields(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "User",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: `"ID"`, FieldIndex: 0},
			{FieldName: "Name", DBColumn: "name", QuotedColumn: "name", FieldIndex: 1},
			{FieldName: "Email", DBColumn: "email", QuotedColumn: "email", FieldIndex: 2},
			{FieldName: "Level", DBColumn: "level", QuotedColumn: `"LEVEL"`, FieldIndex: 3},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	tests := []struct {
		name       string
		fieldNames []string
		want       []any
	}{
		{
			name:       "get multiple fields",
			fieldNames: []string{"ID", "Name", "Email"},
			want:       []any{`"ID"`, "name", "email"},
		},
		{
			name:       "get single field",
			fieldNames: []string{"Level"},
			want:       []any{`"LEVEL"`},
		},
		{
			name:       "get all fields in custom order",
			fieldNames: []string{"Email", "ID", "Level"},
			want:       []any{"email", `"ID"`, `"LEVEL"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadata.Fields(tt.fieldNames...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColumnMetadataFieldsPanic tests that Fields panics if any field is invalid
func TestColumnMetadataFieldsPanic(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "User",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
		},
		columnsByField: map[string]*Column{
			"ID": {FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
		},
	}

	assert.Panics(t, func() {
		metadata.Fields("ID", "NonExistent", "AnotherBad")
	}, "Fields should panic if any field name is invalid")
}

// TestColumnMetadataAll tests the All method
func TestColumnMetadataAll(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "Account",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: `"ID"`, FieldIndex: 0},
			{FieldName: "Number", DBColumn: "number", QuotedColumn: `"NUMBER"`, FieldIndex: 1},
			{FieldName: "Status", DBColumn: "status", QuotedColumn: "status", FieldIndex: 2},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	got := metadata.All()
	want := []any{`"ID"`, `"NUMBER"`, "status"}

	assert.Equal(t, want, got, "All should return all columns in declaration order")
}

// TestColumnMetadataAllEmptyStruct tests All with empty struct (edge case)
func TestColumnMetadataAllEmptyStruct(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName:       "EmptyStruct",
		Columns:        []Column{},
		columnsByField: map[string]*Column{},
	}

	got := metadata.All()
	assert.Empty(t, got, "All should return empty slice for struct with no db-tagged fields")
}

// TestColumnFieldType tests that FieldType is correctly stored
func TestColumnFieldType(t *testing.T) {
	col := Column{
		FieldName:    "ID",
		DBColumn:     "id",
		QuotedColumn: "id",
		FieldIndex:   0,
		FieldType:    reflect.TypeOf(int64(0)),
	}

	assert.Equal(t, reflect.TypeOf(int64(0)), col.FieldType, "FieldType should match int64")
	assert.Equal(t, "ID", col.FieldName, "FieldName should match ID")
	assert.Equal(t, "id", col.DBColumn, "DBColumn should match id")
	assert.Equal(t, "id", col.QuotedColumn, "QuotedColumn should match id")
	assert.Equal(t, 0, col.FieldIndex, "FieldIndex should match 0")
}

// TestColumnMetadataAvailableFieldsForError tests the error message helper
func TestColumnMetadataAvailableFieldsForError(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "User",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
			{FieldName: "Name", DBColumn: "name", QuotedColumn: "name", FieldIndex: 1},
			{FieldName: "Email", DBColumn: "email", QuotedColumn: "email", FieldIndex: 2},
		},
		columnsByField: map[string]*Column{},
	}

	got := metadata.availableFieldsForError()
	assert.Contains(t, got, "ID")
	assert.Contains(t, got, "Name")
	assert.Contains(t, got, "Email")
}

// TestColumnMetadataGetPanicMessageQuality tests panic message contains helpful information
func TestColumnMetadataGetPanicMessageQuality(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "User",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
			{FieldName: "Name", DBColumn: "name", QuotedColumn: "name", FieldIndex: 1},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	defer func() {
		r := recover()
		require.NotNil(t, r, "Get should panic")

		msg := r.(string)
		assert.Contains(t, msg, "BadField", "Panic message should contain the invalid field name")
		assert.Contains(t, msg, "User", "Panic message should contain the struct type name")
		assert.Contains(t, msg, "ID", "Panic message should list available fields")
		assert.Contains(t, msg, "Name", "Panic message should list available fields")
	}()

	metadata.Get("BadField")
}
