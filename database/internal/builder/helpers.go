package builder

import (
	"fmt"
	"sort"
	"strings"

	dbtypes "github.com/gaborage/go-bricks/database/types"
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

// rejectConflictColumnUpdates keeps one BuildUpsert call meaning one thing on
// both vendors: Oracle's MERGE rejects an updated ON-clause column at execution
// (ORA-38104) while PostgreSQL would accept it, so both builders reject early.
func (qb *QueryBuilder) rejectConflictColumnUpdates(conflictColumns []string, updateColumns map[string]any) error {
	if len(updateColumns) == 0 {
		return nil
	}

	// Keyed by vendor identity, so a case variant that names the same column
	// cannot slip past. sortedKeys keeps the reported name deterministic when
	// two update keys collapse to one identity.
	byIdentity := make(map[string]string, len(updateColumns))
	for _, col := range sortedKeys(updateColumns) {
		if identity := qb.columnIdentity(col); byIdentity[identity] == "" {
			byIdentity[identity] = col
		}
	}

	for _, col := range conflictColumns {
		if updateCol, ok := byIdentity[qb.columnIdentity(col)]; ok {
			return fmt.Errorf("update column %q collides with conflict column %q (Oracle MERGE forbids updating ON-clause columns, ORA-38104; rejected on all vendors for parity)", updateCol, col)
		}
	}
	return nil
}

// columnIdentity returns the form of an identifier that decides column identity
// for the active vendor, so the overlap check sees columns the way the database
// will. PostgreSQL quotes every identifier, leaving "id" and "ID" distinct.
// Oracle leaves non-reserved identifiers unquoted and folds them to upper case,
// so id and ID are one column there; reserved words it quotes stay case-sensitive.
func (qb *QueryBuilder) columnIdentity(column string) string {
	if qb.vendor != dbtypes.Oracle {
		return column
	}

	rendered := oracleQuoteIdentifier(column)
	if strings.HasPrefix(rendered, `"`) {
		return rendered
	}
	return strings.ToUpper(rendered)
}

// escapeIdentifiers returns a new slice containing the escaped form of each identifier using qb.EscapeIdentifier.
func (qb *QueryBuilder) escapeIdentifiers(columns []string) []string {
	escaped := make([]string, len(columns))
	for i, col := range columns {
		escaped[i] = qb.EscapeIdentifier(col)
	}
	return escaped
}
