// Package sqlid validates SQL identifiers (table names) before they are
// interpolated into DDL/DML, guarding against SQL identifier injection.
// It composes on database/identifier: each dot-separated part is judged by
// that package's Oracle grammar, the union alphabet (letters, digits,
// underscore, $, #) every vendor the framework supports accepts, so the
// grammar has one home and cannot drift from the renderers'.
package sqlid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gaborage/go-bricks/database/identifier"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// ValidateTableName checks that name is a safe SQL identifier.
// Supports optional schema-qualified names (e.g., "myschema.table").
// Returns a descriptive error when name is empty, contains dangerous SQL
// fragments, has more than two dot-separated parts, or any part is not a
// valid identifier or exceeds identifier.MaxOracleBytes. The error message has
// no package prefix; callers wrap it with their own prefix (e.g.
// fmt.Errorf("outbox: %w", err)).
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
		// Oracle is the union alphabet, not a vendor choice: it is the widest
		// grammar the framework supports ($ and # allowed), so judging by it
		// keeps every verdict this validator gave before it composed here.
		// Callers have no vendor in scope at config time.
		if err := identifier.Validate(dbtypes.Oracle, part); err != nil {
			if errors.Is(err, identifier.ErrIdentifierTooLong) {
				return fmt.Errorf("table name part %q exceeds %d bytes", part, identifier.MaxOracleBytes)
			}
			return fmt.Errorf("table name part %q contains invalid identifier characters", part)
		}
	}

	return nil
}

// IndexBaseName returns the unqualified last dot-separated segment of name,
// used to derive index (and similar) identifier names. An index name cannot be
// schema-qualified, so a schema-qualified table like "myschema.events" must base
// its index names on "events" while the index still targets the qualified table.
func IndexBaseName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// LeaderTableName returns the companion leader-table name for a table, preserving
// any schema prefix ("myschema.outbox" -> "myschema.outbox_leader"). The input has
// already passed ValidateTableName.
func LeaderTableName(name string) string {
	return name + "_leader"
}
