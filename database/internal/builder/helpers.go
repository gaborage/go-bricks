package builder

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// errConflictColumnsRequired is defined once so both upsert builders report the
// same precondition the same way; the vendor is already implied by the builder
// the caller reached.
var errConflictColumnsRequired = errors.New("conflict columns required for upsert")

// errCyclicOperand marks an operand whose pointer chain returns to a pointer the
// walk already followed, which no amount of dereferencing will resolve.
var errCyclicOperand = errors.New("operand pointer chain is cyclic")

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

// upsertColumn pairs a caller's key with the normalized spelling the statement
// renders. Every identity check reads the normalized half — that is what makes
// the acceptance system judge what the renderer emits (#1196) — while a
// rejection names the half the caller wrote, so the message points at their own
// argument.
type upsertColumn struct {
	key        string
	normalized string
}

// normalizeUpsertColumns trims each key, rejects the ones no upsert can name,
// and returns the normalized spellings for the caller to render and compare.
// Returning the normalized identifier is the contract itself, the one
// normalizeAgainst gives every other identifier door: validating a trimmed value
// while rendering the untrimmed one is what left padded keys in the SQL here
// after every other door stopped emitting them (#1158).
//
// It is vendor-neutral on purpose: one rule at both doors is the point of #1187,
// and the Oracle rendering it judges against is the canonical single-token form
// of a key rather than the SQL either vendor emits.
func normalizeUpsertColumns(kind string, columns []string) ([]upsertColumn, error) {
	normalized := make([]upsertColumn, len(columns))
	for i, col := range columns {
		trimmed := strings.TrimSpace(col)
		if !isAcceptableUpsertColumnKey(trimmed) {
			return nil, fmt.Errorf("%s column %q is not a single column name for upsert", kind, col)
		}
		normalized[i] = upsertColumn{key: col, normalized: trimmed}
	}
	return normalized, nil
}

// normalizedColumnMap re-keys a caller's column map by the normalized spelling,
// so the vendor builders below name every column the way it was judged.
// Duplicate normalized keys cannot reach here: requireDistinctColumnIdentities
// refuses them on both vendors first.
func normalizedColumnMap(columns map[string]any, keys []upsertColumn) map[string]any {
	normalized := make(map[string]any, len(keys))
	for _, col := range keys {
		normalized[col.normalized] = columns[col.key]
	}
	return normalized
}

// normalizedSpellings returns just the normalized half of each column.
func normalizedSpellings(columns []upsertColumn) []string {
	spellings := make([]string, len(columns))
	for i, col := range columns {
		spellings[i] = col.normalized
	}
	return spellings
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
func (qb *QueryBuilder) requireConflictColumnsInInsertSet(conflictColumns, insertColumns []upsertColumn) error {
	insertIdentities := make(map[string]struct{}, len(insertColumns))
	for _, col := range insertColumns {
		insertIdentities[qb.upsertColumnName(col.normalized)] = struct{}{}
	}

	for _, col := range conflictColumns {
		if _, ok := insertIdentities[qb.upsertColumnName(col.normalized)]; !ok {
			return fmt.Errorf("conflict column %q must be present in insert columns for upsert", col.key)
		}
	}
	return nil
}

// rejectConflictColumnUpdates keeps one BuildUpsert call meaning one thing on
// both vendors: Oracle's MERGE rejects an updated ON-clause column at execution
// (ORA-38104) while PostgreSQL would accept it, so both builders reject early.
func (qb *QueryBuilder) rejectConflictColumnUpdates(conflictColumns, updateColumns []upsertColumn) error {
	if len(updateColumns) == 0 {
		return nil
	}

	// Keyed by the column each key names, so a spelling that names the same
	// column cannot slip past. The caller passes the update keys in sorted
	// order, which keeps the reported name deterministic when two of them
	// collapse to one column.
	byIdentity := make(map[string]string, len(updateColumns))
	for _, col := range updateColumns {
		if identity := qb.upsertColumnName(col.normalized); byIdentity[identity] == "" {
			byIdentity[identity] = col.key
		}
	}

	for _, col := range conflictColumns {
		if updateCol, ok := byIdentity[qb.upsertColumnName(col.normalized)]; ok {
			return fmt.Errorf("update column %q collides with conflict column %q (Oracle MERGE forbids updating ON-clause columns, ORA-38104; rejected on all vendors for parity)", updateCol, col.key)
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
// normalizeUpsertColumns rules every other shape out ahead of it, and hands
// this the TRIMMED spelling, so padding never reaches the fold.
func (qb *QueryBuilder) upsertColumnName(column string) string {
	if qb.vendor != dbtypes.Oracle {
		// Canonicalized through the RENDERER, the same way the Oracle branch below
		// is. PostgreSQL quotes every identifier, so EscapeIdentifier renders the
		// bare `ID` and the caller-quoted `"ID"` alike as "ID" — one column. Raw
		// spelling was an adequate identity only while a quoted key was refused
		// here; once the wrapper became acceptable, comparing raw spellings made
		// these doors disagree with the SQL: a quoted conflict key missed its
		// unquoted insert key, and two keys naming one column passed distinctness
		// and rendered ("ID","ID"). Case is still preserved, so id and ID remain
		// two columns — which is what PostgreSQL itself does with quoted names.
		return qb.EscapeIdentifier(column)
	}

	rendered := sqllex.QuoteOracleIdentifier(column)
	if len(rendered) >= 2 && rendered[0] == '"' && rendered[len(rendered)-1] == '"' {
		// The unescape is kept though no key reaching here can carry a doubled
		// quote any more: reading an escaped rendering as the name it denotes is
		// this function's own rule, not a consequence of the door's, and a
		// renderer that starts escaping something else must not silently rename
		// the column.
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
// two keys collide there when they RENDER alike: one differing only by padding,
// or a bare key against its caller-quoted twin (`ID` and `"ID"`), both of which
// used to render one column twice — `INSERT INTO users ("id","id")` — and fail at
// execution (#1196).
func (qb *QueryBuilder) requireDistinctColumnIdentities(kind string, columns []upsertColumn) error {
	seen := make(map[string]string, len(columns))
	for _, col := range columns {
		identity := qb.upsertColumnName(col.normalized)
		if first, ok := seen[identity]; ok {
			return fmt.Errorf("%s columns must be distinct: %q and %q name the same column for upsert", kind, first, col.key)
		}
		seen[identity] = col.key
	}
	return nil
}

// isAcceptableUpsertColumnKey reports whether a key names one column an upsert
// can carry on either vendor.
func isAcceptableUpsertColumnKey(key string) bool {
	// The two key-only scans come first: they answer with one pass and no
	// allocation, while oracleQuoteIdentifier parses the key into segments and
	// may allocate a quoted rendering. A key either of them refuses never pays
	// for that parse.
	return !keyCarriesInteriorQuote(key) &&
		!keyIsFunctionShaped(key) &&
		isSingleColumnName(sqllex.QuoteOracleIdentifier(key))
}

// keyCarriesInteriorQuote reports whether a caller's key carries a quote
// anywhere other than as the wrapper of a quoted identifier.
//
// It reads the KEY because the rendering no longer betrays it. The renderers now
// double an interior quote, so `role" = 'admin', "name` renders as one (absurd)
// column rather than as a second assignment, and the rendering-side test that
// used to catch it passes. Refusing the key keeps ADR-071's rule that the builder
// names only what the caller can have meant.
//
// A DOUBLED quote is refused too, on both vendors — Oracle used to exempt it as
// the legal spelling of a quote inside a name while PostgreSQL refused it. An
// identifier argument carries no quoting of its own; the door quotes. A column
// whose name genuinely contains a quote has NO spelling at this door — the
// signature takes strings, so qb.Expr() cannot reach a key here the way it
// reaches a Select or a predicate. Such a schema needs a hand-written statement
// (database.Raw, with its // SECURITY: annotation). Refusing the spelling here
// is what leaves ONE acceptance rule at this door (#1187).
func keyCarriesInteriorQuote(key string) bool {
	if sqllex.IsQuotedString(key) {
		return strings.Contains(key[1:len(key)-1], `"`)
	}
	return strings.Contains(key, `"`)
}

// keyIsFunctionShaped reports whether an unquoted key carries a parenthesis.
//
// It reads the KEY for the same reason keyCarriesInteriorQuote does: the rendering
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
	return !sqllex.IsQuotedIdentifier(key) && strings.ContainsAny(key, "()")
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
// rule that refuses such a key has moved to keyCarriesInteriorQuote, which reads
// the key itself. The quote test is kept rather than deleted: it is one comparison
// standing between a future renderer change and an injected assignment, and this
// is not the seam to economize on. Its other clauses — empty, qualified, not a
// valid segment — remain load-bearing on their own.
func isSingleColumnName(rendered string) bool {
	if rendered == "" || strings.Contains(rendered, ".") || !sqllex.ValidateSegment(rendered) {
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
	return !sqllex.HasUnescapedQuote(rendered[1 : len(rendered)-1])
}

// escapeIdentifiers returns a new slice containing the escaped form of each identifier using qb.EscapeIdentifier.
func (qb *QueryBuilder) escapeIdentifiers(columns []string) []string {
	escaped := make([]string, len(columns))
	for i, col := range columns {
		escaped[i] = qb.EscapeIdentifier(col)
	}
	return escaped
}

// ========== Operand Resolution (shared by Filter and JoinFilter) ==========

// operandWalk carries the two pieces of state the pointer walk accumulates: the
// pointers it has already followed, and whether it has spent its one Valuer
// question. Holding them together keeps the loop body reading as the three
// steps of the contract — settle nil, ask once, dereference — with the
// bookkeeping each step needs behind a call rather than inline.
type operandWalk struct {
	followed    map[uintptr]struct{}
	valuerAsked bool
}

// follow records a non-nil pointer level and reports a chain that returns to a
// pointer already visited, which no amount of dereferencing will resolve.
//
// The map is allocated lazily: most operands are not pointers at all, and one
// that is usually points at a value rather than at another pointer.
func (w *operandWalk) follow(r reflect.Value) error {
	address := r.Pointer()
	if _, seen := w.followed[address]; seen {
		return errCyclicOperand
	}
	if w.followed == nil {
		w.followed = make(map[uintptr]struct{}, 2)
	}
	w.followed[address] = struct{}{}
	return nil
}

// askValuer spends the walk's single Valuer question, replacing *value with the
// answer and reporting whether it asked. Asking at most once is what stops a
// Valuer that answers with another Valuer from spinning the loop.
func (w *operandWalk) askValuer(value *any) (asked bool, err error) {
	v, isValuer := (*value).(driver.Valuer)
	if !isValuer || w.valuerAsked {
		return false, nil
	}
	got, valuerErr := v.Value()
	if valuerErr != nil {
		return false, valuerErr
	}
	w.valuerAsked = true
	*value = got
	return true, nil
}

// resolveOperand mirrors the prologue of squirrel's Eq.toSQL — resolve a
// driver.Valuer, then dereference a pointer (a nil one becoming an untyped nil)
// — and classifies the RESULT the way squirrel's own isListType does.
//
// Classification and rendering share ONE resolution, and every caller renders
// and binds the value this returns rather than the original. Resolving twice is
// a real defect: a stateful Valuer asked a second time by database/sql can
// answer differently, so a door that classified the first answer and bound the
// original could bind a NULL under `col = ?` — the very `col = NULL` #1167
// exists to remove — or expand an IN list that no longer matches the operand.
//
// Skipping the prologue is a defect too, not a nicety: `sql.NullString{}` and a
// typed nil `*int` are neither `== nil` nor slices in their surface form, so a
// test that looks only at the surface calls them scalars and renders `col = ?`.
// f.Eq resolves them and renders IS NULL, so the doors would disagree on exactly
// the operands most likely to be nil in practice: an optional column read from a
// nullable database field.
//
// A Valuer whose Value() fails returns that error for the caller to WRAP, so
// errors.Is finds the cause. The ordering sentinel is deliberately not attached
// to it: a Valuer failure says nothing about comparability, and reporting it as
// ErrOrderingOperandNotComparable is what discarded the cause before.
func resolveOperand(value any) (resolved any, nullOrList bool, err error) {
	// One level at a time, and the ORDER within a level is the whole contract:
	// test for nil, THEN ask whether this level is a driver.Valuer, and only then
	// dereference. Each step is load-bearing.
	//
	// Nil first, because the two overlap: a `*sql.NullString` satisfies
	// driver.Valuer through NullString's VALUE receiver, so asking a nil one for
	// its value dereferences nil inside ToSQL. squirrel asserts before it tests
	// and panics for exactly that reason (expr.go:168).
	//
	// Assert BEFORE dereferencing, because a Value() declared on a POINTER
	// receiver belongs to `*T` and not to `T` — unwrap first and the operand
	// stops being a Valuer, so it is never asked and binds as a bare struct.
	//
	// Loop, because squirrel unwraps a single level and so did this door: a
	// `**int` with a nil inner pointer left a typed nil `*int`, which is neither
	// `== nil` nor a list, so it was classified a SCALAR and handed back —
	// `col < ?` bound to a nil pointer at an ordering door, the silent-no-rows
	// shape this change removes, one level deeper.
	//
	// The loop tracks the pointers it has already followed, because a chain does
	// NOT have to terminate on its own. An earlier version of this comment
	// argued it did — Go types being finite, each dereference shortens the type
	// — which holds for `**int` and fails the moment an INTERFACE is in the
	// chain: `var v any; v = &v` has type `*any`, dereferences to an `any`
	// holding `*any`, and shortens nothing. That spun ToSQL forever on the
	// request goroutine. A pointer already followed is reported, not followed.
	//
	// Value() is asked at most ONCE across the whole walk, so a Valuer answering
	// with another Valuer cannot spin the door.
	walk := operandWalk{}
	r := reflect.ValueOf(value)
	for {
		if r.Kind() == reflect.Pointer {
			if r.IsNil() {
				return nil, true, nil
			}
			if cycleErr := walk.follow(r); cycleErr != nil {
				return nil, false, cycleErr
			}
		}

		asked, valuerErr := walk.askValuer(&value)
		if valuerErr != nil {
			return nil, false, valuerErr
		}
		if asked {
			r = reflect.ValueOf(value)
			continue
		}

		if r.Kind() != reflect.Pointer {
			break
		}
		value = r.Elem().Interface()
		r = reflect.ValueOf(value)
	}

	if value == nil {
		return nil, true, nil
	}
	// squirrel's own isListType: a driver.Value — []byte included — is a scalar.
	if driver.IsValue(value) {
		return value, false, nil
	}
	return value, r.Kind() == reflect.Slice || r.Kind() == reflect.Array, nil
}

// orderingOperand resolves an ordering operand and fails closed on the shapes an
// ordering has no rendering for. Both families call it, so nil, a set and a
// Valuer reporting NULL take the SAME sentinel at `f` and at `jf` and errors.Is
// works on either (#1167, #1205). Squirrel, which FilterFactory delegated to,
// returned its own text for those, rendered `col < ?` bound to a typed nil
// pointer — the silent-no-rows shape — and panicked on a nil pointer to a
// Valuer (#1209).
func orderingOperand(op string, value any) (resolved any, err error) {
	resolved, nullOrList, err := resolveOperand(value)
	if err != nil {
		return nil, wrapOperandErr(op, err)
	}
	if nullOrList {
		return nil, orderingOperandErr(op)
	}
	return resolved, nil
}

// wrapOperandErr and orderingOperandErr are the two spellings every door shares.
// A Valuer failure names the operator it was resolving for; a nil or set operand
// at an ordering door names the same sentinel, written ONCE so the six doors that
// report it cannot drift apart in wording while still matching errors.Is.
func wrapOperandErr(op string, err error) error {
	return fmt.Errorf("resolving the %s operand: %w", op, err)
}

func orderingOperandErr(op string) error {
	return fmt.Errorf("%w: %s with a nil or slice operand",
		dbtypes.ErrOrderingOperandNotComparable, op)
}

// valuerType is the interface every list element is tested against ONCE, by
// TYPE, so a list whose elements cannot need resolution is not walked at all.
var valuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// resolveListOperands turns an IN / NOT IN operand into the list squirrel
// renders, resolving EVERY element that could need it the way the compare doors
// resolve a single one: a nil pointer — typed, or pointing at a driver.Valuer —
// becomes an untyped nil, and a Valuer holding a value is asked once, here, so
// its answer is what gets bound.
//
// Squirrel appends list elements to the argument list untouched, so without
// this a nil pointer to a Valuer survived the whole build and crashed at EXEC,
// where database/sql asks it for its value (#1209) — a build-time door
// reporting a run-time panic. Resolving also normalizes what a nil element
// renders as: it was already `IN (NULL)` at the driver, and it stays that,
// spelled by the door instead of by the driver.
//
// A scalar is wrapped in a one-element list rather than left alone, which is
// what keeps `IN (?)` from collapsing into squirrel's `= ?`. That includes a
// []byte, which is a driver.Value and therefore ONE operand, not N.
//
// The returned `empty` reports the empty-set case for the caller's constant,
// which the pass-through path could not answer from a []any length.
func resolveListOperands(op string, values any) (normalized any, empty bool, err error) {
	if values == nil {
		return []any{}, true, nil
	}

	v := reflect.ValueOf(values)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		element, _, resolveErr := resolveOperand(values)
		if resolveErr != nil {
			return nil, false, wrapOperandErr(op, resolveErr)
		}
		// Classify what resolution PRODUCED, not the operand's surface form. A
		// POINTER to a slice resolves to the slice, and wrapping that as a
		// single element left squirrel expanding only the outer list — `IN (?)`
		// bound to a whole []int, which the driver rejects. A resolved nil stays
		// one NULL element, and a []byte stays one operand, both handled by the
		// list path below.
		if element == nil {
			return []any{nil}, false, nil
		}
		return resolveList(op, reflect.ValueOf(element), element)
	}

	return resolveList(op, v, values)
}

// resolveList resolves an already-classified list operand. It takes both the
// reflect.Value and the original interface because the two answer different
// questions: driver.IsValue judges the interface, while the element type and
// length come from the reflected value.
func resolveList(op string, v reflect.Value, values any) (normalized any, empty bool, err error) {
	// A []byte is a driver.Value, not a list — squirrel's own rule, which the
	// compare doors follow through driver.IsValue. Splitting it into a list of
	// bytes would turn one operand into N.
	if driver.IsValue(values) {
		return []any{values}, false, nil
	}

	// The ELEMENT TYPE decides whether the list has to be walked. Resolution can
	// only change an element that is a pointer, an interface (which may hold
	// one), or a driver.Valuer; for any other element type resolveOperand is the
	// identity, so walking a []int of a thousand ids would allocate a second
	// thousand-entry list and box every element to return exactly what came in.
	// squirrel then makes its own O(N) pass regardless, so the skipped pass is
	// pure duplication, not a shortcut.
	elem := v.Type().Elem()
	if elem.Kind() != reflect.Pointer && elem.Kind() != reflect.Interface && !elem.Implements(valuerType) {
		return values, v.Len() == 0, nil
	}

	resolved := make([]any, v.Len())
	for i := range resolved {
		element, _, resolveErr := resolveOperand(v.Index(i).Interface())
		if resolveErr != nil {
			return nil, false, fmt.Errorf("resolving element %d of the %s operand: %w", i, op, resolveErr)
		}
		resolved[i] = element
	}
	return resolved, len(resolved) == 0, nil
}
