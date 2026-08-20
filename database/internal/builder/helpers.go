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

// requireDistinctColumnIdentities rejects a column list that names one column
// twice, keyed by vendor identity: Oracle folds the unquoted identifiers it
// emits, so id and ID are one column there, while PostgreSQL quotes every
// identifier and sees two — a legitimate composite conflict target.
//
// The caller passes conflictColumns in its own order and the two column maps
// through sortedKeys. A map cannot hold an exact repeat, but two of its keys can
// still fold onto one Oracle column, which builds a MERGE declaring one alias
// twice in its USING clause — ORA-00957 at parse — and naming it twice in the
// INSERT list. On PostgreSQL a key is its own identity, so this cannot fire for
// either map.
func (qb *QueryBuilder) requireDistinctColumnIdentities(kind string, columns []string) error {
	seen := make(map[string]string, len(columns))
	for _, col := range columns {
		identity := qb.columnIdentity(col)
		if first, ok := seen[identity]; ok {
			return fmt.Errorf("%s columns must be distinct: %q and %q name the same column for upsert", kind, first, col)
		}
		seen[identity] = col
	}
	return nil
}

// requireSingleColumnNames rejects a key Oracle's MERGE has no way to name.
// Every upsert column becomes a column alias in the USING clause — :1 AS
// <column> — which admits one identifier and nothing else: no qualifier, no
// function call, no empty name. Such a key could only ever render SQL Oracle
// refuses to parse, so the failure moves from execution to build time. Rendering
// goes through the same helper buildOracleMerge uses, so what is judged here is
// what the statement would carry.
//
// Running before the identity checks is what keeps columnIdentity's quote test
// honest — see its doc for the shape that leaves it correct.
//
// PostgreSQL is untouched: a dotted key renders there as a qualified reference
// rather than a column name, but rejecting it would be a second breaking change,
// and one this seam has no evidence for.
func (qb *QueryBuilder) requireSingleColumnNames(kind string, columns []string) error {
	if qb.vendor != dbtypes.Oracle {
		return nil
	}

	for i, rendered := range qb.quoteOracleColumnsForDML(columns...) {
		if !isSingleColumnName(rendered) {
			return fmt.Errorf("%s column %q is not a single column name for upsert", kind, columns[i])
		}
	}
	return nil
}

// isSingleColumnName reports whether a rendered identifier is one column name
// and nothing else: not empty, not qualified, a valid segment, and — when
// quoted — carrying no quote that ends the identifier early.
//
// That last test is the one with teeth. oracleQuoteIdentifier wraps a key in
// quotes without doubling the ones inside it, so a key spelled
// `role" = 'admin', "name` renders as `"role" = 'admin', "name"`: not a column,
// but a second assignment the caller never asked for, in a position no bind
// parameter guards. A quote inside a quoted identifier is legal only doubled,
// which is what stripping the "" pairs and looking for a survivor tests. The
// renderer's own missing escape is the wider bug and is tracked separately;
// this refuses to name what it cannot render.
func isSingleColumnName(rendered string) bool {
	if rendered == "" || strings.Contains(rendered, ".") || !validateSegment(rendered) {
		return false
	}
	if rendered[0] != '"' {
		// validateSegment took the unquoted branch, whose charset excludes the
		// quote character outright.
		return true
	}
	inner := rendered[1 : len(rendered)-1]
	return !strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`)
}

// columnIdentity returns the form of an identifier that decides column identity
// for the active vendor, so the overlap check sees columns the way the database
// will. PostgreSQL quotes every identifier, leaving "id" and "ID" distinct.
// Oracle leaves non-reserved identifiers unquoted and folds them to upper case,
// so id and ID are one column there; reserved words it quotes stay case-sensitive.
//
// The quote test reads position 0, which answers correctly only for a rendering
// that either carries no quote at all or begins AND ends with one. A rendering
// quoted somewhere in between — t."level" — is upper-cased through its own
// quotes, folding two distinct Oracle columns onto one identity.
// requireSingleColumnNames rules that shape out for every BuildUpsert caller by
// rejecting, ahead of this, any key whose rendering is empty, holds a dot, or is
// not one valid segment.
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
