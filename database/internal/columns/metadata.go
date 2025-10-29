package columns

import (
	"fmt"
	"reflect"
	"strings"
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
type ColumnMetadata struct {
	// TypeName is the name of the struct type (e.g., "User")
	TypeName string

	// Columns is the ordered list of all columns extracted from the struct
	Columns []Column

	// columnsByField is an internal map for O(1) field name lookups
	columnsByField map[string]*Column
}

// Get retrieves the vendor-quoted column name for the given struct field name.
//
// Example:
//
//	type User struct {
//	    ID    int64  `db:"id"`
//	    Level string `db:"level"` // Oracle reserved word
//	}
//
//	cols := qb.Columns(&User{})
//	cols.Get("ID")    // Returns: "id" (PostgreSQL) or `"ID"` (Oracle)
//	cols.Get("Level") // Returns: "level" (PostgreSQL) or `"LEVEL"` (Oracle, quoted)
//
// Panics if the field name is not found (fail-fast for development-time typos).
func (cm *ColumnMetadata) Get(fieldName string) string {
	col, ok := cm.columnsByField[fieldName]
	if !ok {
		panic(fmt.Sprintf("column field %q not found in type %s (available fields: %s)",
			fieldName, cm.TypeName, cm.availableFieldsForError()))
	}
	return col.QuotedColumn
}

// Fields retrieves vendor-quoted column names for multiple struct field names.
//
// Example:
//
//	cols := qb.Columns(&User{})
//	qb.Select(cols.Fields("ID", "Name", "Email")...).From("users")
//
// Panics if any field name is not found (fail-fast).
func (cm *ColumnMetadata) Fields(fieldNames ...string) []any {
	result := make([]any, len(fieldNames))
	for i, fieldName := range fieldNames {
		result[i] = cm.Get(fieldName) // Reuse Get() for panic behavior
	}
	return result
}

// All returns vendor-quoted column names for all columns in the struct, in the order
// they were declared in the struct definition.
//
// Example:
//
//	cols := qb.Columns(&User{})
//	qb.Select(cols.All()...).From("users") // SELECT all columns
func (cm *ColumnMetadata) All() []any {
	result := make([]any, len(cm.Columns))
	for i, col := range cm.Columns {
		result[i] = col.QuotedColumn
	}
	return result
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
