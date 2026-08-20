package builder

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// errConflictColumnsRequired is defined once so both upsert builders report the
// same precondition the same way; the vendor is already implied by the builder
// the caller reached.
var errConflictColumnsRequired = errors.New("conflict columns required for upsert")

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

// requireUniqueConflictColumns rejects a conflict column list that names the same
// column twice. Duplicates are keyed by vendor identity, so Oracle — which folds
// the unquoted identifiers it emits — sees id and ID as one column, while
// PostgreSQL quotes every identifier and sees two, making that pairing a
// legitimate composite conflict target there.
func (qb *QueryBuilder) requireUniqueConflictColumns(conflictColumns []string) error {
	seen := make(map[string]string, len(conflictColumns))
	for _, col := range conflictColumns {
		identity := qb.columnIdentity(col)
		if first, ok := seen[identity]; ok {
			return fmt.Errorf("conflict columns must be distinct: %q and %q name the same column for upsert", first, col)
		}
		seen[identity] = col
	}
	return nil
}

// requireConflictColumnsInInsertSet keeps one BuildUpsert call meaning one thing
// on both vendors. Oracle's MERGE names every conflict column in its ON clause as
// source.<column>, which the USING SELECT supplies only for inserted columns, so
// an absent one builds invalid SQL. PostgreSQL builds legal SQL — its conflict
// target only picks the arbiter index — but the proposed row then takes the
// column's default, so the conflict fires only when that default collides.
// Rejecting on both makes the caller state the value it is matching on.
//
// Membership is keyed by vendor identity, like the two checks around it, so a
// conflict column that names an inserted column in a different spelling is
// accepted on Oracle, where both render unquoted and fold to one column.
func (qb *QueryBuilder) requireConflictColumnsInInsertSet(conflictColumns []string, insertColumns map[string]any) error {
	insertIdentities := make(map[string]struct{}, len(insertColumns))
	for col := range insertColumns {
		insertIdentities[qb.columnIdentity(col)] = struct{}{}
	}

	for _, col := range conflictColumns {
		if _, ok := insertIdentities[qb.columnIdentity(col)]; !ok {
			return fmt.Errorf("conflict column %q must be present in insert columns for upsert", col)
		}
	}
	return nil
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

// requireDistinctColumnIdentities rejects a column map holding two keys that name
// one column for the active vendor. A map cannot hold an exact repeat, but Oracle
// folds the unquoted identifiers it emits, so {"id": 1, "ID": 2} is one column
// written twice: the MERGE declares one alias twice in its USING clause, which
// Oracle refuses at parse time (ORA-00957), and names it twice in the INSERT
// list. On PostgreSQL every identifier is quoted, so a key is its own identity
// and this can never fire — the same pairing is two columns there.
func (qb *QueryBuilder) requireDistinctColumnIdentities(kind string, columns map[string]any) error {
	seen := make(map[string]string, len(columns))
	// sortedKeys keeps the reported pair deterministic when three or more keys
	// fold onto one identity.
	for _, col := range sortedKeys(columns) {
		identity := qb.columnIdentity(col)
		if first, ok := seen[identity]; ok {
			return fmt.Errorf("%s columns must be distinct: %q and %q name the same column for upsert", kind, first, col)
		}
		seen[identity] = col
	}
	return nil
}

// requireSingleColumnNames rejects a key Oracle's MERGE has no way to name. Every
// upsert column becomes a column alias in the USING clause — :1 AS <column> —
// which admits one identifier and nothing else: no qualifier, no function call,
// no empty name. Such a key could only ever render SQL Oracle refuses to parse,
// so the failure moves from execution to build time.
//
// It also keeps columnIdentity honest. That guard asks whether a rendering is
// quoted by testing position 0, which a partially-quoted rendering such as
// t."level" defeats: it is upper-cased through its own quotes, folding two
// distinct Oracle columns onto one identity. Every key surviving this check
// renders as one whole token, quoted or not — the shape the guard reads
// correctly — so no BuildUpsert call reaches that flaw.
//
// PostgreSQL quotes the whole key, naming a column that is unusual but legal, so
// this is Oracle's MERGE grammar rather than a portable rule.
func (qb *QueryBuilder) requireSingleColumnNames(conflictColumns []string, insertColumns, updateColumns map[string]any) error {
	if qb.vendor != dbtypes.Oracle {
		return nil
	}

	groups := []struct {
		kind    string
		columns []string
	}{
		{kind: "conflict", columns: conflictColumns},
		{kind: "insert", columns: sortedKeys(insertColumns)},
		{kind: "update", columns: sortedKeys(updateColumns)},
	}

	for _, group := range groups {
		for _, col := range group.columns {
			rendered := oracleQuoteIdentifier(col)
			if rendered == "" || strings.Contains(rendered, ".") || !validateSegment(rendered) {
				return fmt.Errorf("%s column %q is not a single column name for upsert", group.kind, col)
			}
		}
	}
	return nil
}

// columnIdentity returns the form of an identifier that decides column identity
// for the active vendor, so the overlap check sees columns the way the database
// will. PostgreSQL quotes every identifier, leaving "id" and "ID" distinct.
// Oracle leaves non-reserved identifiers unquoted and folds them to upper case,
// so id and ID are one column there; reserved words it quotes stay case-sensitive.
//
// The quote test reads position 0, so it holds only for a rendering that is one
// whole token. requireSingleColumnNames is what guarantees that for every
// BuildUpsert caller: a qualified or function-shaped key renders quoted somewhere
// other than the front, and is rejected before reaching here.
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
