package builder

import (
	"fmt"

	"github.com/Masterminds/squirrel"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Comparison operators whose operand contract differs from the ordering ones':
// nil and slices are meaningful for equality and refused for ordering.
const (
	opEqual    = "="
	opNotEqual = "!="
)

// JoinFilter represents a composable JOIN ON condition that compares columns to other columns.
// JoinFilters are created through JoinFilterFactory methods and maintain vendor-specific quoting rules.
//
// Unlike Filter (which compares columns to values with placeholders), JoinFilter compares columns
// to other columns directly in the SQL (e.g., "users.id = profiles.user_id").
type JoinFilter struct {
	sqlizer squirrel.Sqlizer
}

// ToSql generates the SQL fragment for this join condition.
// This method implements the squirrel.Sqlizer interface (inherited by dbtypes.JoinFilter).
//
//nolint:revive // ToSql is required by squirrel.Sqlizer interface (lowercase 's')
func (jf JoinFilter) ToSql() (sql string, args []any, err error) {
	return jf.sqlizer.ToSql()
}

// ToSQL is a convenience method with idiomatic Go naming (uppercase SQL).
// It delegates to ToSql() for actual implementation.
func (jf JoinFilter) ToSQL() (sql string, args []any, err error) {
	return jf.ToSql()
}

// Verify JoinFilter implements dbtypes.JoinFilter interface (which embeds squirrel.Sqlizer)
var _ dbtypes.JoinFilter = JoinFilter{}

// JoinFilterFactory provides methods for creating type-safe JOIN ON filters with automatic vendor-specific quoting.
// Obtain a JoinFilterFactory through QueryBuilder.JoinFilter().
type JoinFilterFactory struct {
	qb *QueryBuilder
}

// Verify JoinFilterFactory implements dbtypes.JoinFilterFactory interface
var _ dbtypes.JoinFilterFactory = (*JoinFilterFactory)(nil)

// ========== Factory Method ==========

// newJoinFilterFactory creates a new JoinFilterFactory bound to the provided QueryBuilder.
// This is an internal method - users should call qb.JoinFilter() instead.
func newJoinFilterFactory(qb *QueryBuilder) *JoinFilterFactory {
	return &JoinFilterFactory{qb: qb}
}

// ========== Column Comparison Operators ==========

// columnComparison implements squirrel.Sqlizer for column-to-column comparisons.
// It generates SQL like "users.id = profiles.user_id" with no placeholders.
type columnComparison struct {
	leftColumn  string
	operator    string
	rightColumn string
}

//nolint:revive // ToSql required by squirrel.Sqlizer
func (cc columnComparison) ToSql() (sql string, args []any, err error) {
	return fmt.Sprintf("%s %s %s", cc.leftColumn, cc.operator, cc.rightColumn), []any{}, nil
}

// EqColumn creates an equality join condition (leftColumn = rightColumn).
// Column names are automatically quoted according to database vendor rules.
//
// Example:
//
//	jf.EqColumn("users.id", "profiles.user_id")  // users.id = profiles.user_id
func (jff *JoinFilterFactory) EqColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, "=", right}}
}

// NotEqColumn creates an inequality join condition (leftColumn != rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) NotEqColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, "!=", right}}
}

// LtColumn creates a less-than join condition (leftColumn < rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) LtColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, "<", right}}
}

// LteColumn creates a less-than-or-equal join condition (leftColumn <= rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) LteColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, "<=", right}}
}

// GtColumn creates a greater-than join condition (leftColumn > rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) GtColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, ">", right}}
}

// GteColumn creates a greater-than-or-equal join condition (leftColumn >= rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) GteColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left, right, err := jff.columnPair(leftColumn, rightColumn)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: columnComparison{left, ">=", right}}
}

// compare renders `<column> <op> <value>`: a RawExpression is interpolated
// verbatim with no placeholder, any other operand is resolved ONCE by
// resolveOperand and it is the RESOLVED value that is classified, rendered and
// bound. The expression is validated HERE, not only in Expr() — RawExpression
// is a plain struct, so a caller can hand this door a literal that never passed
// through the constructor (#1153).
func (jff *JoinFilterFactory) compare(column, op string, value any) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}

	if expr, ok := value.(dbtypes.RawExpression); ok {
		if err := expr.Validate(); err != nil {
			return joinFilterErr(err)
		}
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " " + op + " " + expr.SQL)}
	}

	resolved, nullOrList, resolveErr := resolveOperand(value)
	if resolveErr != nil {
		return joinFilterErr(wrapOperandErr(op, resolveErr))
	}

	// A nil or slice operand delegates to the SAME construct f.Eq/f.NotEq use, so
	// one operand means one thing at both doors: nil renders IS NULL / IS NOT
	// NULL, a slice expands to IN / NOT IN, an empty slice takes squirrel's own
	// constant. `col = ?` bound nil to a placeholder (never true) and a slice to
	// one argument the driver rejects (#1167).
	//
	// A SCALAR keeps the `col op ?` form deliberately, rather than delegating
	// unconditionally: squirrel.NotEq spells inequality `<>` where this door has
	// always emitted `!=`, so delegating every operand would rewrite working SQL
	// for every caller to fix two broken shapes. The two doors still agree on
	// meaning; they differ in one token for scalar inequality, which is the
	// smaller debt and the one the issue scoped.
	if nullOrList {
		switch op {
		case opEqual:
			return JoinFilter{sqlizer: squirrel.Eq{quotedColumn: resolved}}
		case opNotEqual:
			return JoinFilter{sqlizer: squirrel.NotEq{quotedColumn: resolved}}
		default:
			// Ordering has no rendering for these, so the door fails closed rather
			// than emitting SQL that silently matches nothing.
			return joinFilterErr(orderingOperandErr(op))
		}
	}

	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" "+op+" ?", resolved)}
}

// ========== Column-to-Value Comparison Operators ==========

// Eq creates an equality condition (column = value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
//
// Examples:
//
//	jf.Eq("status", "active")                          // status = ? (with placeholder)
//	expr, _ := qb.Expr("TO_NUMBER(amount_str)")
//	jf.Eq("amount", expr)                              // amount = TO_NUMBER(amount_str) (expression, no bound placeholder)
func (jff *JoinFilterFactory) Eq(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, opEqual, value)
}

// NotEq creates an inequality condition (column != value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) NotEq(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, opNotEqual, value)
}

// Lt creates a less-than condition (column < value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Lt(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, "<", value)
}

// Lte creates a less-than-or-equal condition (column <= value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Lte(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, "<=", value)
}

// Gt creates a greater-than condition (column > value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Gt(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, ">", value)
}

// Gte creates a greater-than-or-equal condition (column >= value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Gte(column string, value any) dbtypes.JoinFilter {
	return jff.compare(column, ">=", value)
}

// In creates an IN condition (column IN (values...)).
// Accepts both slices and scalar values. Scalars are automatically wrapped in a slice.
// Column names are automatically quoted according to database vendor rules.
//
// Examples:
//
//	jf.In("status", []string{"active", "pending"})  // IN with multiple values
//	jf.In("status", "active")                       // IN with single value (wrapped automatically)
func (jff *JoinFilterFactory) In(column string, values any) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}
	normalized, empty, err := resolveListOperands("IN", values)
	if err != nil {
		return joinFilterErr(err)
	}
	// Empty slice special case: generate "1=0" to ensure no matches
	if empty {
		return JoinFilter{sqlizer: squirrel.Expr("(1=0)")} // Empty IN list - always false
	}
	return JoinFilter{sqlizer: squirrel.Eq{quotedColumn: normalized}}
}

// NotIn creates a NOT IN condition (column NOT IN (values...)).
// Accepts both slices and scalar values. Scalars are automatically wrapped in a slice.
// Column names are automatically quoted according to database vendor rules.
//
// Examples:
//
//	jf.NotIn("status", []string{"deleted", "banned"})  // NOT IN with multiple values
//	jf.NotIn("status", "deleted")                      // NOT IN with single value (wrapped automatically)
func (jff *JoinFilterFactory) NotIn(column string, values any) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}
	normalized, empty, err := resolveListOperands("NOT IN", values)
	if err != nil {
		return joinFilterErr(err)
	}
	if empty {
		return JoinFilter{sqlizer: squirrel.Expr("(1=1)")} // Empty NOT IN list - always true
	}
	return JoinFilter{sqlizer: squirrel.NotEq{quotedColumn: normalized}}
}

// Like creates a LIKE condition.
// Column names are automatically quoted according to database vendor rules.
// Pattern must be a string value (RawExpression not supported for LIKE).
//
// Note: This uses standard LIKE (case-sensitive). For case-insensitive matching,
// use Raw() with vendor-specific functions (ILIKE for PostgreSQL, UPPER() for Oracle).
//
// Examples:
//
//	jf.Like("name", "%Smith%")  // name LIKE ?
func (jff *JoinFilterFactory) Like(column, pattern string) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" LIKE ?", pattern)}
}

// Null creates an IS NULL condition.
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) Null(column string) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: squirrel.Eq{quotedColumn: nil}}
}

// NotNull creates an IS NOT NULL condition.
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) NotNull(column string) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}
	return JoinFilter{sqlizer: squirrel.NotEq{quotedColumn: nil}}
}

// Between creates a BETWEEN condition (column BETWEEN lowerBound AND upperBound).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
//
// Examples:
//
//	jf.Between("price", 10.0, 20.0)                  // price BETWEEN ? AND ?
//	lo, _ := qb.Expr("18")
//	hi, _ := qb.Expr("65")
//	jf.Between("age", lo, hi)                        // age BETWEEN 18 AND 65 (expressions)
func (jff *JoinFilterFactory) Between(column string, lowerBound, upperBound any) dbtypes.JoinFilter {
	quotedColumn, err := jff.qb.quoteColumnForQuery(column)
	if err != nil {
		return joinFilterErr(err)
	}

	// Each bound is resolved on its own and renders itself, so the door has two
	// steps rather than a branch per COMBINATION of bound kinds — the four-way
	// tree the pair-wise form needed (expr/expr, expr/value, value/expr,
	// value/value) said nothing the bounds do not each say for themselves.
	lower, err := resolveBetweenBound(">=", lowerBound)
	if err != nil {
		return joinFilterErr(err)
	}
	upper, err := resolveBetweenBound("<=", upperBound)
	if err != nil {
		return joinFilterErr(err)
	}

	return JoinFilter{sqlizer: squirrel.And{
		lower.sqlizer(quotedColumn, ">="),
		upper.sqlizer(quotedColumn, "<="),
	}}
}

// betweenBound is one resolved BETWEEN bound: either a RawExpression to splice
// verbatim, or an ordering operand already resolved to the value that will be
// bound.
type betweenBound struct {
	exprSQL string
	value   any
	isExpr  bool
}

// resolveBetweenBound validates an expression bound or resolves a value one.
//
// An expression is interpolated verbatim, so it is validated at this door for
// the same reason compare() validates its value: RawExpression is a plain
// struct and a caller can hand one over that never passed through Expr()
// (#1153). A value bound is an ordering operand wherever it ends up rendered —
// squirrel.Expr, the construct a mixed pair uses, resolves nothing at build
// time, so a bound spliced into it unresolved reached the driver as written: a
// nil rendered `col <= ?` bound to NULL, matching nothing, and a nil pointer to
// a Valuer was dereferenced at EXEC.
func resolveBetweenBound(op string, bound any) (resolved betweenBound, err error) {
	if expr, isExpr := bound.(dbtypes.RawExpression); isExpr {
		if validateErr := expr.Validate(); validateErr != nil {
			return betweenBound{}, validateErr
		}
		return betweenBound{exprSQL: expr.SQL, isExpr: true}, nil
	}

	value, operandErr := orderingOperand(op, bound)
	if operandErr != nil {
		return betweenBound{}, operandErr
	}
	return betweenBound{value: value}, nil
}

// sqlizer renders this bound as `<column> <op> …`: an expression inline with no
// placeholder and no argument, a value as a placeholder bound to the value the
// door already resolved.
func (b betweenBound) sqlizer(quotedColumn, op string) squirrel.Sqlizer {
	if b.isExpr {
		return squirrel.Expr(quotedColumn + " " + op + " " + b.exprSQL)
	}
	return squirrel.Expr(quotedColumn+" "+op+" ?", b.value)
}

// ========== Logical Operators ==========

// And combines multiple join filters with AND logic.
// Returns a filter that matches when ALL provided filters match.
// Nil filters are treated as no-ops and skipped.
//
// Example:
//
//	jf := qb.JoinFilter()
//	filter := jf.And(
//	    jf.EqColumn("users.id", "profiles.user_id"),
//	    jf.GtColumn("profiles.created_at", "users.created_at"),
//	)
func (jff *JoinFilterFactory) And(filters ...dbtypes.JoinFilter) dbtypes.JoinFilter {
	sqlizers := make(squirrel.And, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue // Skip nil filters - treat as no-op
		}
		// Extract the underlying squirrel.Sqlizer
		if concreteFilter, ok := filter.(JoinFilter); ok {
			sqlizers = append(sqlizers, concreteFilter.sqlizer)
		} else {
			// Fallback: use the filter as-is (it implements Sqlizer)
			sqlizers = append(sqlizers, filter)
		}
	}
	return JoinFilter{sqlizer: sqlizers}
}

// Or combines multiple join filters with OR logic.
// Returns a filter that matches when ANY provided filter matches.
// Nil filters are treated as no-ops and skipped.
//
// Example:
//
//	jf := qb.JoinFilter()
//	filter := jf.Or(
//	    jf.EqColumn("users.primary_email", "contacts.email"),
//	    jf.EqColumn("users.secondary_email", "contacts.email"),
//	)
func (jff *JoinFilterFactory) Or(filters ...dbtypes.JoinFilter) dbtypes.JoinFilter {
	sqlizers := make(squirrel.Or, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue // Skip nil filters - treat as no-op
		}
		// Extract the underlying squirrel.Sqlizer
		if concreteFilter, ok := filter.(JoinFilter); ok {
			sqlizers = append(sqlizers, concreteFilter.sqlizer)
		} else {
			// Fallback: use the filter as-is
			sqlizers = append(sqlizers, filter)
		}
	}
	return JoinFilter{sqlizer: sqlizers}
}

// ========== Raw Escape Hatch ==========

// Raw creates a join filter from raw SQL with manual placeholder handling.
//
// WARNING: This method bypasses all identifier quoting and SQL injection protection.
// It is the caller's responsibility to:
//   - Properly quote any identifiers (especially Oracle reserved words)
//   - Ensure the SQL fragment is valid for the target database
//   - Never concatenate user input directly into the condition string
//
// REQUIRED: Every call site MUST carry an inline annotation of the form
// `// SECURITY: Manual SQL review completed - <rationale>` documenting the
// specific safety property checked. See FilterFactory.Raw for details and
// CLAUDE.md "Security Guidelines".
//
// Use this method ONLY when the type-safe methods cannot express your JOIN condition.
//
// Examples:
//
//	// SECURITY: Manual SQL review completed - literal qualified identifiers; value side parameterized
//	jf.Raw(`users.id = profiles.user_id AND profiles."type" = ?`, "primary")
//	// SECURITY: Manual SQL review completed - column-to-column comparison only, no user input
//	jf.Raw(`ST_Distance(users.location, stores.location) < 1000`)
func (jff *JoinFilterFactory) Raw(condition string, args ...any) dbtypes.JoinFilter {
	return JoinFilter{sqlizer: squirrel.Expr(condition, args...)}
}

// columnPair renders both sides of a column-to-column join condition, reporting the
// FIRST failure so the error names the left column when both are bad. Both sides are
// identifier arguments — a JOIN condition compares two names and binds no value, so
// neither side has a placeholder to hide behind.
func (jff *JoinFilterFactory) columnPair(leftColumn, rightColumn string) (left, right string, err error) {
	if left, err = jff.qb.quoteColumnForQuery(leftColumn); err != nil {
		return "", "", err
	}
	if right, err = jff.qb.quoteColumnForQuery(rightColumn); err != nil {
		return "", "", err
	}
	return left, right, nil
}

// joinFilterErr wraps a column-validation failure as the deferred-error JoinFilter
// that ToSQL() surfaces. One place to change the shape, rather than eighteen.
func joinFilterErr(err error) dbtypes.JoinFilter {
	return JoinFilter{sqlizer: errorSqlizer{err: err}}
}
