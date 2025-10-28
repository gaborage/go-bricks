package builder

import (
	"fmt"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, "=", right}}
}

// NotEqColumn creates an inequality join condition (leftColumn != rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) NotEqColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, "!=", right}}
}

// LtColumn creates a less-than join condition (leftColumn < rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) LtColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, "<", right}}
}

// LteColumn creates a less-than-or-equal join condition (leftColumn <= rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) LteColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, "<=", right}}
}

// GtColumn creates a greater-than join condition (leftColumn > rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) GtColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, ">", right}}
}

// GteColumn creates a greater-than-or-equal join condition (leftColumn >= rightColumn).
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) GteColumn(leftColumn, rightColumn string) dbtypes.JoinFilter {
	left := jff.qb.quoteColumnForQuery(leftColumn)
	right := jff.qb.quoteColumnForQuery(rightColumn)
	return JoinFilter{sqlizer: columnComparison{left, ">=", right}}
}

// ========== Column-to-Value Comparison Operators ==========

// Eq creates an equality condition (column = value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
//
// Examples:
//
//	jf.Eq("status", "active")                          // status = ? (with placeholder)
//	jf.Eq("amount", qb.Expr("TO_NUMBER(?)"), 100)      // amount = TO_NUMBER(?) (expression)
func (jff *JoinFilterFactory) Eq(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " = " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" = ?", value)}
}

// NotEq creates an inequality condition (column != value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) NotEq(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " != " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" != ?", value)}
}

// Lt creates a less-than condition (column < value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Lt(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " < " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" < ?", value)}
}

// Lte creates a less-than-or-equal condition (column <= value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Lte(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " <= " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" <= ?", value)}
}

// Gt creates a greater-than condition (column > value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Gt(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " > " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" > ?", value)}
}

// Gte creates a greater-than-or-equal condition (column >= value).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
func (jff *JoinFilterFactory) Gte(column string, value any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if value is a RawExpression
	if expr, ok := value.(dbtypes.RawExpression); ok {
		// Expression - no placeholder
		return JoinFilter{sqlizer: squirrel.Expr(quotedColumn + " >= " + expr.SQL)}
	}

	// Regular value - use placeholder
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" >= ?", value)}
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
	quotedColumn := jff.qb.quoteColumnForQuery(column)
	normalized := normalizeToSlice(values)
	// Empty slice special case: generate "1=0" to ensure no matches
	if s, ok := normalized.([]any); ok && len(s) == 0 {
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
	quotedColumn := jff.qb.quoteColumnForQuery(column)
	normalized := normalizeToSlice(values)
	if s, ok := normalized.([]any); ok && len(s) == 0 {
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
	quotedColumn := jff.qb.quoteColumnForQuery(column)
	return JoinFilter{sqlizer: squirrel.Expr(quotedColumn+" LIKE ?", pattern)}
}

// Null creates an IS NULL condition.
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) Null(column string) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)
	return JoinFilter{sqlizer: squirrel.Eq{quotedColumn: nil}}
}

// NotNull creates an IS NOT NULL condition.
// Column names are automatically quoted according to database vendor rules.
func (jff *JoinFilterFactory) NotNull(column string) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)
	return JoinFilter{sqlizer: squirrel.NotEq{quotedColumn: nil}}
}

// Between creates a BETWEEN condition (column BETWEEN lowerBound AND upperBound).
// Column names are automatically quoted according to database vendor rules.
// Accepts RawExpression for complex SQL expressions without placeholders.
//
// Examples:
//
//	jf.Between("price", 10.0, 20.0)                  // price BETWEEN ? AND ?
//	jf.Between("age", qb.Expr("18"), qb.Expr("65"))  // age BETWEEN 18 AND 65 (expressions)
func (jff *JoinFilterFactory) Between(column string, lowerBound, upperBound any) dbtypes.JoinFilter {
	quotedColumn := jff.qb.quoteColumnForQuery(column)

	// Check if either bound is a RawExpression
	lowerIsExpr := false
	upperIsExpr := false
	var lowerExpr, upperExpr dbtypes.RawExpression

	if expr, ok := lowerBound.(dbtypes.RawExpression); ok {
		lowerIsExpr = true
		lowerExpr = expr
	}
	if expr, ok := upperBound.(dbtypes.RawExpression); ok {
		upperIsExpr = true
		upperExpr = expr
	}

	// Build condition based on whether bounds are expressions or values
	if lowerIsExpr && upperIsExpr {
		// Both expressions - no placeholders
		condition := squirrel.And{
			squirrel.Expr(quotedColumn + " >= " + lowerExpr.SQL),
			squirrel.Expr(quotedColumn + " <= " + upperExpr.SQL),
		}
		return JoinFilter{sqlizer: condition}
	} else if lowerIsExpr {
		// Lower is expression, upper is value
		condition := squirrel.And{
			squirrel.Expr(quotedColumn + " >= " + lowerExpr.SQL),
			squirrel.Expr(quotedColumn+" <= ?", upperBound),
		}
		return JoinFilter{sqlizer: condition}
	} else if upperIsExpr {
		// Upper is expression, lower is value
		condition := squirrel.And{
			squirrel.Expr(quotedColumn+" >= ?", lowerBound),
			squirrel.Expr(quotedColumn + " <= " + upperExpr.SQL),
		}
		return JoinFilter{sqlizer: condition}
	}

	// Both values - use placeholders
	condition := squirrel.And{
		squirrel.GtOrEq{quotedColumn: lowerBound},
		squirrel.LtOrEq{quotedColumn: upperBound},
	}
	return JoinFilter{sqlizer: condition}
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
// Use this method ONLY when the type-safe methods cannot express your JOIN condition.
//
// Examples:
//
//	jf.Raw(`users.id = profiles.user_id AND profiles."type" = ?`, "primary")  // Mixed column comparison + value
//	jf.Raw(`ST_Distance(users.location, stores.location) < 1000`)            // Spatial functions
func (jff *JoinFilterFactory) Raw(condition string, args ...any) dbtypes.JoinFilter {
	return JoinFilter{sqlizer: squirrel.Expr(condition, args...)}
}
