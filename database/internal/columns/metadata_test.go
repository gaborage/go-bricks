package columns

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestColumnMetadataCol tests the Col method for successful retrieval
func TestColumnMetadataCol(t *testing.T) {
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
			got := metadata.Col(tt.fieldName)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColumnMetadataColPanic tests that Col panics on invalid field names
func TestColumnMetadataColPanic(t *testing.T) {
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
		metadata.Col("NonExistent")
	}, "Col should panic on non-existent field name")
}

// TestColumnMetadataCols tests the Cols method for bulk retrieval
func TestColumnMetadataCols(t *testing.T) {
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
		want       []string
	}{
		{
			name:       "get multiple fields",
			fieldNames: []string{"ID", "Name", "Email"},
			want:       []string{`"ID"`, "name", "email"},
		},
		{
			name:       "get single field",
			fieldNames: []string{"Level"},
			want:       []string{`"LEVEL"`},
		},
		{
			name:       "get all fields in custom order",
			fieldNames: []string{"Email", "ID", "Level"},
			want:       []string{"email", `"ID"`, `"LEVEL"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadata.Cols(tt.fieldNames...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColumnMetadataColsPanic tests that Cols panics if any field is invalid
func TestColumnMetadataColsPanic(t *testing.T) {
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
		metadata.Cols("ID", "NonExistent", "AnotherBad")
	}, "Cols should panic if any field name is invalid")
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
	want := []string{`"ID"`, `"NUMBER"`, "status"}

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

// TestColumnMetadataColPanicMessageQuality tests panic message contains helpful information
func TestColumnMetadataColPanicMessageQuality(t *testing.T) {
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
		require.NotNil(t, r, "Col should panic")

		msg := r.(string)
		assert.Contains(t, msg, "BadField", "Panic message should contain the invalid field name")
		assert.Contains(t, msg, "User", "Panic message should contain the struct type name")
		assert.Contains(t, msg, "ID", "Panic message should list available fields")
		assert.Contains(t, msg, "Name", "Panic message should list available fields")
	}()

	metadata.Col("BadField")
}

// TestColumnMetadataColPanicWithAlias tests that Col panic includes alias information
func TestColumnMetadataColPanicWithAlias(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "User",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: `"ID"`, FieldIndex: 0},
			{FieldName: "Name", DBColumn: "name", QuotedColumn: "name", FieldIndex: 1},
			{FieldName: "Email", DBColumn: "email", QuotedColumn: "email", FieldIndex: 2},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	// Create aliased instance
	aliased := metadata.As("u")

	defer func() {
		r := recover()
		require.NotNil(t, r, "Col should panic on non-existent field")

		msg := r.(string)
		assert.Contains(t, msg, "NonExistent", "Panic message should contain the invalid field name")
		assert.Contains(t, msg, "User", "Panic message should contain the struct type name")
		assert.Contains(t, msg, `with alias "u"`, "Panic message should mention the alias")
		assert.Contains(t, msg, "ID", "Panic message should list available fields")
		assert.Contains(t, msg, "Name", "Panic message should list available fields")
		assert.Contains(t, msg, "Email", "Panic message should list available fields")
	}()

	aliased.Col("NonExistent")
}

// TestColumnMetadataColPanicNoAlias tests that non-aliased Col panic does NOT include alias mention
func TestColumnMetadataColPanicNoAlias(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "Product",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: "id", FieldIndex: 0},
			{FieldName: "Price", DBColumn: "price", QuotedColumn: "price", FieldIndex: 1},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	defer func() {
		r := recover()
		require.NotNil(t, r, "Col should panic on non-existent field")

		msg := r.(string)
		assert.Contains(t, msg, "BadField", "Panic message should contain the invalid field name")
		assert.Contains(t, msg, "Product", "Panic message should contain the struct type name")
		assert.NotContains(t, msg, "alias", "Panic message should NOT mention alias for non-aliased instance")
		assert.Contains(t, msg, "ID", "Panic message should list available fields")
		assert.Contains(t, msg, "Price", "Panic message should list available fields")
	}()

	metadata.Col("BadField")
}

// TestColumnMetadataColsPanicWithAlias tests that Cols panic includes alias information
func TestColumnMetadataColsPanicWithAlias(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "Order",
		Columns: []Column{
			{FieldName: "ID", DBColumn: "id", QuotedColumn: `"ID"`, FieldIndex: 0},
			{FieldName: "Total", DBColumn: "total", QuotedColumn: "total", FieldIndex: 1},
		},
		columnsByField: map[string]*Column{},
	}

	// Populate columnsByField map
	for i := range metadata.Columns {
		metadata.columnsByField[metadata.Columns[i].FieldName] = &metadata.Columns[i]
	}

	// Create aliased instance
	aliased := metadata.As("o")

	defer func() {
		r := recover()
		require.NotNil(t, r, "Cols should panic when any field is non-existent")

		msg := r.(string)
		assert.Contains(t, msg, "InvalidField", "Panic message should contain the invalid field name")
		assert.Contains(t, msg, "Order", "Panic message should contain the struct type name")
		assert.Contains(t, msg, `with alias "o"`, "Panic message should mention the alias")
		assert.Contains(t, msg, "ID", "Panic message should list available fields")
		assert.Contains(t, msg, "Total", "Panic message should list available fields")
	}()

	aliased.Cols("ID", "InvalidField", "Total")
}

// TestColumnMetadataMultipleAliasesPanic tests that different aliases show distinct error messages
func TestColumnMetadataMultipleAliasesPanic(t *testing.T) {
	metadata := &ColumnMetadata{
		TypeName: "Employee",
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

	// Create two different aliases
	emp := metadata.As("emp")
	mgr := metadata.As("mgr")

	// Test first alias
	t.Run("emp alias", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "Col should panic")

			msg := r.(string)
			assert.Contains(t, msg, `with alias "emp"`, "Panic message should mention 'emp' alias")
			assert.NotContains(t, msg, `with alias "mgr"`, "Panic message should NOT mention 'mgr' alias")
		}()

		emp.Col("BadField")
	})

	// Test second alias
	t.Run("mgr alias", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "Col should panic")

			msg := r.(string)
			assert.Contains(t, msg, `with alias "mgr"`, "Panic message should mention 'mgr' alias")
			assert.NotContains(t, msg, `with alias "emp"`, "Panic message should NOT mention 'emp' alias")
		}()

		mgr.Col("BadField")
	})
}

// TestAsPanicsOnEmptyAlias pins the documented fail-fast behaviour: the
// As() helper panics rather than returning an unaliased clone, so a
// caller passing an empty string sees the bug at development time.
func TestAsPanicsOnEmptyAlias(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	assert.PanicsWithValue(t, "alias cannot be empty", func() {
		cm.As("")
	})
}

func TestAliasReturnsEmptyWhenUnaliased(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)
	assert.Empty(t, cm.Alias(), "fresh metadata is unaliased")
}

func TestAliasReturnsSetValueAfterAs(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	aliased := cm.As("u")
	assert.Equal(t, "u", aliased.Alias())
	assert.Empty(t, cm.Alias(), "original metadata must remain unaliased — As() returns a new instance")
}

func TestAllReturnsQualifiedColumnsWhenAliased(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	aliased := cm.As("u")
	assert.Equal(t, []string{"u.id", "u.name", "u.email"}, aliased.All())
	// Sanity: original returns unqualified columns.
	assert.Equal(t, []string{"id", "name", "email"}, cm.All())
}

func TestFieldMapUnaliasedReturnsValues(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 7, Name: "alice", Email: "alice@example.com"}
	got := cm.FieldMap(&user)

	require.Len(t, got, 3)
	assert.Equal(t, int64(7), got["id"])
	assert.Equal(t, "alice", got["name"])
	assert.Equal(t, "alice@example.com", got["email"])
}

func TestFieldMapAliasedQualifiesKeys(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 42, Name: "bob"}
	got := cm.As("u").FieldMap(&user)

	require.Contains(t, got, "u.id")
	require.Contains(t, got, "u.name")
	require.Contains(t, got, "u.email")
	assert.Equal(t, int64(42), got["u.id"])
	assert.Equal(t, "bob", got["u.name"])
}

func TestFieldMapAcceptsStructByValue(t *testing.T) {
	// extractStructValue handles both struct values and pointers — exercise
	// the value path here so the dereference branch isn't the only one
	// covered. parseStruct itself requires a pointer (existing contract),
	// so we build metadata via the pointer and then call FieldMap with the
	// value.
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	got := cm.FieldMap(ValidUser{ID: 1, Name: "x", Email: "x@y"})
	assert.Equal(t, int64(1), got["id"])
}

func TestAllFieldsUnaliasedReturnsColsAndValues(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 9, Name: "carol", Email: "carol@example.com"}
	cols, vals := cm.AllFields(&user)

	assert.Equal(t, []string{"id", "name", "email"}, cols)
	assert.Equal(t, []any{int64(9), "carol", "carol@example.com"}, vals)
}

func TestAllFieldsAliasedQualifiesColumns(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 11}
	cols, _ := cm.As("u").AllFields(&user)
	assert.Equal(t, []string{"u.id", "u.name", "u.email"}, cols)
}

func TestExtractStructValuePanicsOnNil(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	// An untyped nil reaches reflect.ValueOf as an invalid Value — that's
	// the path we want covered (distinct from a typed-nil pointer).
	assert.Panics(t, func() {
		cm.FieldMap(nil)
	}, "nil instance must panic with a clear error")
}

func TestExtractStructValuePanicsOnNilPointer(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	var u *ValidUser // typed nil pointer
	assert.Panics(t, func() {
		cm.FieldMap(u)
	}, "typed-nil pointer must panic distinctly from untyped nil")
}

func TestExtractStructValuePanicsOnNonStruct(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	assert.Panics(t, func() {
		cm.FieldMap("not a struct")
	}, "non-struct kinds must panic")
}

func TestExtractStructValuePanicsOnTypeMismatch(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	// A different struct type with the same field shape must still be
	// rejected — metadata is keyed by reflect.Type identity, not by
	// structural match.
	assert.Panics(t, func() {
		cm.FieldMap(&NoDBTags{})
	}, "passing a different struct type must panic with a type-mismatch error")
}
