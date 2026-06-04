// Package sqlid validates SQL identifiers (table names) before they are
// interpolated into DDL/DML, guarding against SQL identifier injection.
// The rules mirror the historical outbox validator and
// database/internal/columns/parser.go for consistency across the framework.
package sqlid

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// validIdentifierPattern matches safe SQL identifiers (letters, digits,
// underscore, $, #). A leading digit is rejected.
var validIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`)

// ValidateTableName checks that name is a safe SQL identifier.
// Supports optional schema-qualified names (e.g., "myschema.table").
// Returns a descriptive error when name is empty, contains dangerous SQL
// fragments, has more than two dot-separated parts, or any part is not a
// valid identifier. The error message has no package prefix; callers wrap it
// with their own prefix (e.g. fmt.Errorf("outbox: %w", err)).
func ValidateTableName(name string) error {
	if name == "" {
		return errors.New("table name must not be empty")
	}

	for _, dangerous := range []string{";", "--", "/*", "*/"} {
		if strings.Contains(name, dangerous) {
			return fmt.Errorf("table name %q contains dangerous SQL characters", name)
		}
	}

	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return fmt.Errorf("table name %q has too many dot-separated parts (expected schema.table or table)", name)
	}

	for _, part := range parts {
		if !validIdentifierPattern.MatchString(part) {
			return fmt.Errorf("table name part %q contains invalid identifier characters", part)
		}
	}

	return nil
}
