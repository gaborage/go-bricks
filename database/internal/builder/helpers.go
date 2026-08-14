package builder

import (
	"fmt"
	"sort"
)

// sortedKeys returns a deterministically ordered slice of keys from the provided map.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// valuesByKeyOrder returns a slice of values from m following the order specified in keys.
func valuesByKeyOrder(m map[string]any, keys []string) []any {
	vals := make([]any, 0, len(keys))
	for _, k := range keys {
		vals = append(vals, m[k])
	}
	return vals
}

// rejectConflictColumnUpdates enforces the vendor-portable upsert contract:
// a conflict column cannot also be updated. Oracle's MERGE rejects it at
// execution (ORA-38104); PostgreSQL would accept it, so both builders reject
// at build time to keep one call meaning one thing on both vendors.
func rejectConflictColumnUpdates(conflictColumns []string, updateColumns map[string]any) error {
	for _, col := range conflictColumns {
		if _, ok := updateColumns[col]; ok {
			return fmt.Errorf("conflict column %q cannot appear in update columns (Oracle MERGE forbids updating ON-clause columns, ORA-38104; rejected on all vendors for parity)", col)
		}
	}
	return nil
}

// escapeIdentifiers returns a new slice containing the escaped form of each identifier using qb.EscapeIdentifier.
func (qb *QueryBuilder) escapeIdentifiers(columns []string) []string {
	escaped := make([]string, len(columns))
	for i, col := range columns {
		escaped[i] = qb.EscapeIdentifier(col)
	}
	return escaped
}
