package columns

import (
	"fmt"
	"reflect"
	"strings"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Column represents metadata for a single database column extracted from a struct field.
type Column struct {
	// FieldName is the Go struct field name (e.g., "UserID")
	FieldName string

	// DBColumn is the raw database column name from the db tag (e.g., "user_id")
	DBColumn string

	// QuotedColumn is the vendor-specific quoted column name (e.g., `"USER_ID"` for Oracle)
	QuotedColumn string

	// FieldIndex is the index of this field in the struct (for reflection-based value extraction)
	FieldIndex int

	// FieldType is the reflect.Type of the struct field
	FieldType reflect.Type
}

// ColumnMetadata represents cached metadata for a struct type, including all database columns
// extracted from `db:"column_name"` tags.
//
// This struct is cached per vendor to ensure vendor-specific column quoting (e.g., Oracle reserved words)
// is applied once at registration time, eliminating per-query reflection overhead.
//
// v2.4 Enhancement: Supports immutable aliasing via As() method for zero-copy table qualification.
type ColumnMetadata struct {
	// TypeName is the name of the struct type (e.g., "User")
	TypeName string

	// Columns is the ordered list of all columns extracted from the struct
	Columns []Column

	// columnsByField is an internal map for O(1) field name lookups
	columnsByField map[string]*Column

	// structType is the reflect.Type of the struct (for value extraction)
	structType reflect.Type

	// alias is the table alias for qualified column names (empty if unaliased)
	// This field is set via As() and used by Col(), Cols(), All() for automatic qualification.
	alias string
}

// As returns a new ColumnMetadata instance bound to the specified table alias.
// The returned instance shares the underlying column metadata (zero-copy).
// This method is immutable - the original ColumnMetadata instance remains unchanged.
//
// Example:
//
//	cols := qb.Columns(&User{})
//	u := cols.As("u")
//	p := cols.As("p")
//	u.Col("ID")    // "u.id"
//	p.Col("ID")    // "p.id"
//	cols.Col("ID") // "id" (original unaffected)
//
// Panics if alias is empty (fail-fast for development-time errors).
func (cm *ColumnMetadata) As(alias string) dbtypes.Columns {
	if alias == "" {
		panic("alias cannot be empty")
	}
	return &ColumnMetadata{
		TypeName:       cm.TypeName,
		Columns:        cm.Columns,        // Shared slice (immutable)
		columnsByField: cm.columnsByField, // Shared map (immutable)
		structType:     cm.structType,     // Shared type
		alias:          alias,             // New alias
	}
}

// Alias returns the current table alias, or empty string if unaliased.
//
// Example:
//
//	cols.Alias()         // ""
//	cols.As("u").Alias() // "u"
func (cm *ColumnMetadata) Alias() string {
	return cm.alias
}

// Col retrieves the vendor-quoted column name for the given struct field name.
// If aliased via As(), returns qualified column (e.g., "u.id").
//
// Example (unaliased):
//
//	type User struct {
//	    ID    int64  `db:"id"`
//	    Level string `db:"level"` // Oracle reserved word
//	}
//
//	cols := qb.Columns(&User{})
//	cols.Col("ID")    // Returns: "id" (PostgreSQL) or "ID" (Oracle)
//	cols.Col("Level") // Returns: "level" (PostgreSQL) or "LEVEL" (Oracle, quoted)
//
// Example (aliased):
//
//	u := cols.As("u")
//	u.Col("ID")       // Returns: "u.id" (PostgreSQL) or "u.\"ID\"" (Oracle)
//	u.Col("Level")    // Returns: "u.\"LEVEL\"" (Oracle reserved word with table qualification)
//
// Panics if the field name is not found (fail-fast for development-time typos).
func (cm *ColumnMetadata) Col(fieldName string) string {
	col, ok := cm.columnsByField[fieldName]
	if !ok {
		panic(fmt.Sprintf("column field %q not found in type %s (available fields: %s)",
			fieldName, cm.TypeName, cm.availableFieldsForError()))
	}

	// If aliased, return qualified column (alias.column)
	if cm.alias != "" {
		return cm.alias + "." + col.QuotedColumn
	}

	return col.QuotedColumn
}

// Cols retrieves vendor-quoted column names for multiple struct field names.
// If aliased, returns qualified columns.
//
// Example (unaliased):
//
//	cols := qb.Columns(&User{})
//	cols.Cols("ID", "Name", "Email") // ["id", "name", "email"]
//
// Example (aliased):
//
//	u := cols.As("u")
//	u.Cols("ID", "Name") // ["u.id", "u.name"]
//
// Panics if any field name is not found (fail-fast).
func (cm *ColumnMetadata) Cols(fieldNames ...string) []string {
	result := make([]string, len(fieldNames))
	for i, fieldName := range fieldNames {
		result[i] = cm.Col(fieldName) // Reuse Col() for panic behavior + aliasing
	}
	return result
}

// All returns vendor-quoted column names for all columns in the struct, in the order
// they were declared in the struct definition.
//
// Example (unaliased):
//
//	cols := qb.Columns(&User{})
//	cols.All() // ["id", "name", "email"]
//
// Example (aliased):
//
//	u := cols.As("u")
//	u.All() // ["u.id", "u.name", "u.email"]
func (cm *ColumnMetadata) All() []string {
	result := make([]string, len(cm.Columns))
	for i, col := range cm.Columns {
		// If aliased, return qualified column (alias.column)
		if cm.alias != "" {
			result[i] = cm.alias + "." + col.QuotedColumn
		} else {
			result[i] = col.QuotedColumn
		}
	}
	return result
}

// FieldMap extracts field values from a struct instance into a map.
// The map keys are vendor-quoted column names (respecting alias if set).
// Only fields with `db` tags are included. Zero values are included.
//
// Example (unaliased):
//
//	user := User{ID: 123, Name: "Alice", Email: "alice@example.com"}
//	cols.FieldMap(&user)
//	// Returns: {"id": 123, "name": "Alice", "email": "alice@example.com"}
//
// Example (aliased):
//
//	cols.As("u").FieldMap(&user)
//	// Returns: {"u.id": 123, "u.name": "Alice", "u.email": "alice@example.com"}
//
// Panics if instance is not a struct or pointer to struct.
func (cm *ColumnMetadata) FieldMap(instance any) map[string]any {
	val := cm.extractStructValue(instance)
	result := make(map[string]any, len(cm.Columns))

	for _, col := range cm.Columns {
		fieldValue := val.Field(col.FieldIndex).Interface()
		columnName := col.QuotedColumn
		if cm.alias != "" {
			columnName = cm.alias + "." + columnName
		}
		result[columnName] = fieldValue
	}

	return result
}

// AllFields extracts all field values from a struct instance as separate slices.
// Returns (columns, values) suitable for bulk INSERT/UPDATE operations.
// Only fields with `db` tags are included. Zero values are included.
//
// Example:
//
//	user := User{ID: 123, Name: "Alice", Email: "alice@example.com"}
//	cols, vals := cols.AllFields(&user)
//	// cols: ["id", "name", "email"]
//	// vals: [123, "Alice", "alice@example.com"]
//
// Panics if instance is not a struct or pointer to struct.
func (cm *ColumnMetadata) AllFields(instance any) (columns []string, values []any) {
	val := cm.extractStructValue(instance)
	columns = make([]string, len(cm.Columns))
	values = make([]any, len(cm.Columns))

	for i, col := range cm.Columns {
		fieldValue := val.Field(col.FieldIndex).Interface()
		columnName := col.QuotedColumn
		if cm.alias != "" {
			columnName = cm.alias + "." + columnName
		}
		columns[i] = columnName
		values[i] = fieldValue
	}

	return columns, values
}

// extractStructValue extracts the reflect.Value for a struct instance.
// Handles both struct values and pointers to structs.
// Panics if instance is not a struct or pointer to struct.
func (cm *ColumnMetadata) extractStructValue(instance any) reflect.Value {
	val := reflect.ValueOf(instance)

	// Handle nil
	if !val.IsValid() {
		panic(fmt.Sprintf("cannot extract fields from nil instance (type: %s)", cm.TypeName))
	}

	// Dereference pointer if necessary
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			panic(fmt.Sprintf("cannot extract fields from nil pointer (type: %s)", cm.TypeName))
		}
		val = val.Elem()
	}

	// Validate it's a struct
	if val.Kind() != reflect.Struct {
		panic(fmt.Sprintf("expected struct or pointer to struct, got %s (type: %s)",
			val.Kind(), cm.TypeName))
	}

	// Validate type matches (prevent passing wrong struct type)
	if val.Type() != cm.structType {
		panic(fmt.Sprintf("type mismatch: expected %s, got %s",
			cm.structType.Name(), val.Type().Name()))
	}

	return val
}

// availableFieldsForError returns a comma-separated list of available field names
// for error messages.
func (cm *ColumnMetadata) availableFieldsForError() string {
	fields := make([]string, 0, len(cm.Columns))
	for _, col := range cm.Columns {
		fields = append(fields, col.FieldName)
	}
	return strings.Join(fields, ", ")
}
