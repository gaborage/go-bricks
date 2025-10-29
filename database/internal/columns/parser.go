package columns

import (
	"fmt"
	"reflect"
	"strings"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// oracleReservedWords contains Oracle SQL reserved words that must be quoted when used as identifiers.
// This list is replicated from database/internal/builder/oracle.go to avoid circular dependencies.
// Keep in sync with the builder package's reserved word list.
var oracleReservedWords = map[string]struct{}{
	"ACCESS": {}, "ADD": {}, "ALL": {}, "ALTER": {}, "AND": {}, "ANY": {}, "AS": {}, "ASC": {},
	"BEGIN": {}, "BETWEEN": {}, "BY": {}, "CASE": {}, "CHECK": {}, "COLUMN": {}, "COMMENT": {},
	"CONNECT": {}, "CREATE": {}, "CURRENT": {}, "DELETE": {}, "DESC": {}, "DISTINCT": {},
	"DROP": {}, "ELSE": {}, "EXCLUDE": {}, "EXISTS": {}, "FOR": {}, "FROM": {}, "GRANT": {},
	"GROUP": {}, "HAVING": {}, "IN": {}, "INDEX": {}, "INSERT": {}, "INTERSECT": {}, "INTO": {},
	"IS": {}, "LEVEL": {}, "LIKE": {}, "LOCK": {}, "MINUS": {}, "MODE": {}, "NOCOMPRESS": {},
	"NOT": {}, "NULL": {}, "NUMBER": {}, "OF": {}, "ON": {}, "OPTION": {}, "OR": {}, "ORDER": {},
	"ROW": {}, "ROWNUM": {}, "SELECT": {}, "SET": {}, "SHARE": {}, "SIZE": {}, "START": {},
	"TABLE": {}, "THEN": {}, "TO": {}, "TRIGGER": {}, "UNION": {}, "UNIQUE": {}, "UPDATE": {},
	"VALUES": {}, "VIEW": {}, "WHEN": {}, "WHERE": {}, "WITH": {},
}

// isOracleReservedWord checks if an identifier is an Oracle reserved word.
func isOracleReservedWord(identifier string) bool {
	if identifier == "" {
		return false
	}
	_, ok := oracleReservedWords[strings.ToUpper(identifier)]
	return ok
}

// oracleQuoteIdentifier applies Oracle-specific quoting rules to an identifier.
// Reserved words are quoted with double quotes.
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
	if isOracleReservedWord(trimmed) {
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

	rt := rv.Elem().Type()
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("parseStruct expects a pointer to struct, got pointer to %s", rt.Kind())
	}

	metadata := &ColumnMetadata{
		TypeName:       rt.Name(),
		Columns:        make([]Column, 0, rt.NumField()),
		columnsByField: make(map[string]*Column),
	}

	// Iterate through struct fields and extract db tags
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Extract db tag
		dbTag := field.Tag.Get("db")

		// Skip fields without db tag or with db:"-" (explicit ignore)
		if dbTag == "" || dbTag == "-" {
			continue
		}

		// SECURITY: Validate tag format to prevent SQL injection
		if err := validateDBTag(dbTag, rt.Name(), field.Name); err != nil {
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
		metadata.columnsByField[field.Name] = &metadata.Columns[len(metadata.Columns)-1]
	}

	// Fail-fast if no db-tagged fields found
	if len(metadata.Columns) == 0 {
		return nil, fmt.Errorf("no fields with `db` tags found in struct %s", rt.Name())
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
