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
// Membership is keyed by the column each key names, like the checks around it,
// so a conflict column that names an inserted column in a different spelling is
// accepted on Oracle, where `"ID"` and id are one column.
func (qb *QueryBuilder) requireConflictColumnsInInsertSet(conflictColumns []string, insertColumns map[string]any) error {
	insertIdentities := make(map[string]struct{}, len(insertColumns))
	for col := range insertColumns {
		insertIdentities[qb.upsertColumnName(col)] = struct{}{}
	}

	for _, col := range conflictColumns {
		if _, ok := insertIdentities[qb.upsertColumnName(col)]; !ok {
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

	// Keyed by the column each key names, so a spelling that names the same
	// column cannot slip past. sortedKeys keeps the reported name deterministic
	// when two update keys collapse to one column.
	byIdentity := make(map[string]string, len(updateColumns))
	for _, col := range sortedKeys(updateColumns) {
		if identity := qb.upsertColumnName(col); byIdentity[identity] == "" {
			byIdentity[identity] = col
		}
	}

	for _, col := range conflictColumns {
		if updateCol, ok := byIdentity[qb.upsertColumnName(col)]; ok {
			return fmt.Errorf("update column %q collides with conflict column %q (Oracle MERGE forbids updating ON-clause columns, ORA-38104; rejected on all vendors for parity)", updateCol, col)
		}
	}
	return nil
}

// upsertColumnName returns the column a key actually names for the active
// vendor, which is what decides whether two keys are one column. Every upsert
// precondition keys on it, so all four judge one column the same way.
//
// It deliberately does not compare RENDERINGS, because two different renderings
// can name one Oracle column: `id` renders unquoted and Oracle folds it to ID,
// while `"ID"` renders quoted and IS ID. Comparing the renderings keeps them
// apart; Oracle does not, and the MERGE then declares one column twice in its
// USING clause (ORA-00957 at parse) or updates an ON-clause column it named in
// the other spelling (ORA-38104 at execution). So the quoted form is unwrapped
// to the text it names and the unquoted form is folded the way Oracle folds it.
// `id` and `"id"` stay two columns, `level` and `LEVEL` stay two (both render
// quoted, cases preserved), and `id` and `"ID"` become one.
//
// The quote test reads the first and last byte, which answers correctly only for
// a rendering that either carries no quote at all or begins AND ends with one.
// requireSingleColumnNames rules every other shape out ahead of it.
func (qb *QueryBuilder) upsertColumnName(column string) string {
	if qb.vendor != dbtypes.Oracle {
		return column
	}

	rendered := oracleQuoteIdentifier(column)
	if len(rendered) >= 2 && rendered[0] == '"' && rendered[len(rendered)-1] == '"' {
		return strings.ReplaceAll(rendered[1:len(rendered)-1], `""`, `"`)
	}
	return strings.ToUpper(rendered)
}

// requireDistinctColumnIdentities rejects a column list that names one column
// twice, keyed by the column each key actually names for the vendor: Oracle
// folds the unquoted identifiers it emits and reads a quoted one verbatim, so
// id, ID and "ID" are one column there, while PostgreSQL quotes every identifier
// and sees three — a legitimate composite conflict target.
//
// The caller passes conflictColumns in its own order and the two column maps
// through sortedKeys. A map cannot hold an exact repeat, but two of its keys can
// still name one Oracle column, which builds a MERGE declaring one alias twice
// in its USING clause and naming it twice in the INSERT list. On PostgreSQL a
// key is its own name, so this cannot fire for either map.
func (qb *QueryBuilder) requireDistinctColumnIdentities(kind string, columns []string) error {
	seen := make(map[string]string, len(columns))
	for _, col := range columns {
		identity := qb.upsertColumnName(col)
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
// Running before the identity checks is what keeps upsertColumnName's quote test
// honest — see its doc for the shape that leaves it correct.
//
// PostgreSQL is untouched: a dotted key renders there as a qualified reference
// rather than a column name, but rejecting it would be a second breaking change,
// and one this seam has no evidence for.
func (qb *QueryBuilder) requireSingleColumnNames(kind string, columns []string) error {
	if qb.vendor != dbtypes.Oracle {
		// The quote rule is the one half that is not Oracle grammar. It was
		// written when both escapers wrapped without doubling, so the same key
		// left the identifier on PostgreSQL too; ADR-082 fixed the renderers, and
		// the rule is kept because nothing legitimate passes a bare interior
		// quote, so refusing it still costs no working call. Note this branch is
		// STRICTER than the Oracle one: it refuses a well-formed quoted key
		// (`"a""b"`) that keyEscapesIdentifier exempts. That divergence pre-dates
		// ADR-082 and is left standing rather than widened inside a security fix;
		// it is tracked with the other upsert-acceptance question. The rest of
		// this check — qualifiers, function calls — stays Oracle's, where a
		// dotted key is a grammar violation rather than merely unusual.
		for _, col := range columns {
			if hasUnescapedQuote(col) {
				return fmt.Errorf("%s column %q is not a single column name for upsert", kind, col)
			}
		}
		return nil
	}

	for i, rendered := range qb.quoteOracleColumnsForDML(columns...) {
		if !isSingleColumnName(rendered) || keyEscapesIdentifier(columns[i]) || keyIsFunctionShaped(columns[i]) {
			return fmt.Errorf("%s column %q is not a single column name for upsert", kind, columns[i])
		}
	}
	return nil
}

// keyEscapesIdentifier reports whether a caller's key carries a quote that is
// neither half of a doubled escape nor the wrapper of a well-formed quoted
// identifier.
//
// It reads the KEY because the rendering no longer betrays it. The renderers now
// double an interior quote, so `role" = 'admin', "name` renders as one (absurd)
// column rather than as a second assignment, and the rendering-side test that
// used to catch it passes. Refusing the key keeps ADR-071's rule that the builder
// names only what the caller can have meant, and keeps this change from widening
// what an upsert accepts while it is closing an injection. Whether such a key
// should be accepted now that it renders correctly is tracked separately.
func keyEscapesIdentifier(key string) bool {
	return !isQuotedIdentifier(key) && hasUnescapedQuote(key)
}

// keyIsFunctionShaped reports whether an unquoted key carries a parenthesis.
//
// It reads the KEY for the same reason keyEscapesIdentifier does: the rendering
// stopped betraying it. `count(*)` used to reach isSingleColumnName verbatim,
// because the Oracle renderer returned anything function-shaped unquoted, and was
// refused there as not a valid segment. That pass-through is gone (#1149), so the
// key now renders as the quoted column `"count(*)"` and reads as a single name.
// Refusing it here keeps the intended acceptance rule rather than widening it as
// a side effect of deleting the branch. A key the caller QUOTED is left alone, as
// before: `"count(*)"` names a column that may genuinely exist.
//
// It is not byte-for-byte the old rule: a paren-bearing string that was NOT a
// well-formed call — `COUNT(` — used to fall through to normal quoting and be
// accepted as the literal column `"COUNT("`, and is refused now. That is a
// narrowing, deliberate and recorded rather than discovered later: an unquoted
// key carrying a parenthesis is not a bare identifier, and a caller who means
// such a column can still say so by quoting it.
func keyIsFunctionShaped(key string) bool {
	return !isQuotedIdentifier(key) && strings.ContainsAny(key, "()")
}

// isSingleColumnName reports whether a rendered identifier is one column name
// and nothing else: not empty, not qualified, a valid segment, and — when
// quoted — carrying no quote that ends the identifier early.
//
// That last test used to be the one with teeth, back when oracleQuoteIdentifier
// wrapped a key without doubling the quotes inside it and `role" = 'admin', "name`
// rendered as `"role" = 'admin', "name"` — not a column, but a second assignment
// in a position no bind parameter guards. The renderer escapes now, so no
// rendering it produces should reach this with an early-ending quote, and the
// rule that refuses such a key has moved to keyEscapesIdentifier, which reads the
// key itself. The quote test is kept rather than deleted: it is one comparison
// standing between a future renderer change and an injected assignment, and this
// is not the seam to economize on. Its other clauses — empty, qualified, not a
// valid segment — remain load-bearing on their own.
func isSingleColumnName(rendered string) bool {
	if rendered == "" || strings.Contains(rendered, ".") || !validateSegment(rendered) {
		return false
	}
	if rendered[0] != '"' {
		// validateSegment took the unquoted branch, whose charset excludes the
		// quote character outright.
		return true
	}
	// validateSegment's quoted branch has already established both wrapping
	// quotes and a non-empty interior, so these bounds are exact and need no
	// guard of their own. A quote surviving between them ends the identifier
	// early and turns the remainder into SQL.
	return !hasUnescapedQuote(rendered[1 : len(rendered)-1])
}

// hasUnescapedQuote reports whether text carries a quote that is not part of a
// doubled "" escape. On the PostgreSQL side this is applied to the KEY rather
// than its rendering: the escaper splits a dotted key and quotes each part, so
// a legitimate qualified reference renders with quotes in the middle and the
// rendering cannot tell that apart from an escape defect. The key can: nothing
// legitimate carries a bare quote, and one that does leaves the identifier the
// escaper wraps it in.
func hasUnescapedQuote(text string) bool {
	return strings.Contains(strings.ReplaceAll(text, `""`, ""), `"`)
}

// isQuotedIdentifier reports whether text is ALREADY a well-formed quoted
// identifier: wrapped in quotes with every interior quote doubled. Re-quoting
// one of these would change the name it denotes, which is why the renderers pass
// it through. Wrapping alone does not qualify — `role" = 'admin', "name` is
// wrapped and is two SQL tokens.
func isQuotedIdentifier(text string) bool {
	return isQuotedString(text) && !hasUnescapedQuote(text[1:len(text)-1])
}

// quoteIdentifierLiteral wraps text in quotes with every interior quote doubled,
// which is how Oracle and PostgreSQL both spell a quote inside a name. A quote
// left undoubled ends the identifier early, so the remainder is parsed as SQL.
//
// Collapsing precedes doubling because a key arrives in escaped form: `a""b`
// already denotes the one-quote name `a"b`, the reading upsertColumnName applies
// to it. Doubling blind would rename that column. Collapsing first makes the
// pass idempotent — an already-escaped key survives unchanged, and a lone quote
// is the only thing that gains a partner.
func quoteIdentifierLiteral(text string) string {
	if !strings.ContainsRune(text, '"') {
		// The overwhelming case, and the hot one: one scan, one allocation, the
		// same cost this had before it learned to escape.
		return `"` + text + `"`
	}
	collapsed := strings.ReplaceAll(text, `""`, `"`)
	return `"` + strings.ReplaceAll(collapsed, `"`, `""`) + `"`
}

// escapeIdentifiers returns a new slice containing the escaped form of each identifier using qb.EscapeIdentifier.
func (qb *QueryBuilder) escapeIdentifiers(columns []string) []string {
	escaped := make([]string, len(columns))
	for i, col := range columns {
		escaped[i] = qb.EscapeIdentifier(col)
	}
	return escaped
}
