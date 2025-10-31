package columns

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

var validDBTagPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`)

// oracleQuoteIdentifier applies Oracle-specific quoting rules to an identifier.
// Reserved words are quoted with double quotes using the canonical list from sqllex package.
func oracleQuoteIdentifier(column string) string {
	trimmed := strings.TrimSpace(column)
	if trimmed == "" {
		return trimmed
	}

	// Don't double-quote already quoted identifiers
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		return trimmed
	}

	// Quote Oracle reserved words
	if sqllex.IsOracleReservedWord(trimmed) {
		return `"` + trimmed + `"`
	}

	return trimmed
}

// parseStruct extracts column metadata from a struct type using reflection.
// It processes `db:"column_name"` tags and applies vendor-specific column quoting.
//
// Returns an error if:
//   - structPtr is not a pointer to a struct
//   - Any db tag contains dangerous SQL characters
func parseStruct(vendor string, structPtr any) (*ColumnMetadata, error) {
	// Validate input type
	rv := reflect.ValueOf(structPtr)
	if rv.Kind() != reflect.Ptr {
		return nil, fmt.Errorf("parseStruct expects a pointer to struct, got %T", structPtr)
	}

	ptrType := rv.Type()
	structType := ptrType.Elem()

	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("parseStruct expects a pointer to struct, got pointer to %s", structType.Kind())
	}

	metadata := &ColumnMetadata{
		TypeName:       structType.Name(),
		Columns:        make([]Column, 0, structType.NumField()),
		columnsByField: make(map[string]*Column),
		structType:     structType, // Store for FieldMap/AllFields value extraction
		alias:          "",         // Initialize as unaliased
	}

	// Iterate through struct fields and extract db tags
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Extract db tag
		dbTag := strings.TrimSpace(field.Tag.Get("db"))

		// Skip fields without db tag or with db:"-" (explicit ignore)
		if dbTag == "" || dbTag == "-" {
			continue
		}

		// SECURITY: Validate tag format to prevent SQL injection
		if err := validateDBTag(dbTag, structType.Name(), field.Name); err != nil {
			return nil, err
		}

		// Build column metadata
		col := Column{
			FieldName:  field.Name,
			DBColumn:   dbTag,
			FieldIndex: i,
			FieldType:  field.Type,
		}

		// Apply vendor-specific quoting
		col.QuotedColumn = applyVendorQuoting(vendor, dbTag)

		metadata.Columns = append(metadata.Columns, col)
	}

	for idx := range metadata.Columns {
		col := &metadata.Columns[idx]
		metadata.columnsByField[col.FieldName] = col
	}

	// Fail-fast if no db-tagged fields found
	if len(metadata.Columns) == 0 {
		return nil, fmt.Errorf("no fields with `db` tags found in struct %s", structType.Name())
	}

	return metadata, nil
}

// validateDBTag checks for dangerous characters in db tags that could indicate SQL injection attempts.
func validateDBTag(tag, structName, fieldName string) error {
	// Check for dangerous SQL characters
	dangerous := []string{";", "--", "/*", "*/"}
	for _, d := range dangerous {
		if strings.Contains(tag, d) {
			return fmt.Errorf(
				"invalid db tag %q in field %s.%s: contains dangerous SQL characters %q",
				tag, structName, fieldName, d,
			)
		}
	}

	// Check for quotes (column names should not be pre-quoted in tags)
	if strings.Contains(tag, `"`) || strings.Contains(tag, "'") {
		return fmt.Errorf(
			"invalid db tag %q in field %s.%s: contains quotes (vendor-specific quoting is applied automatically)",
			tag, structName, fieldName,
		)
	}

	if !validDBTagPattern.MatchString(tag) {
		return fmt.Errorf(
			"invalid db tag %q in field %s.%s: contains invalid identifier characters (allowed: letters, numbers, '_', '$', '#', must start with letter or underscore)",
			tag, structName, fieldName,
		)
	}

	return nil
}

// applyVendorQuoting applies vendor-specific column name quoting.
func applyVendorQuoting(vendor, column string) string {
	switch vendor {
	case dbtypes.Oracle:
		return oracleQuoteIdentifier(column)
	case dbtypes.PostgreSQL:
		// PostgreSQL typically doesn't need quoting for standard column names
		return column
	case dbtypes.MongoDB:
		// MongoDB uses document field names, no SQL quoting needed
		return column
	default:
		// Unknown vendor: return as-is
		return column
	}
}
