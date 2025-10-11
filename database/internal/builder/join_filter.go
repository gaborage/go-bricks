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

// ========== Logical Operators ==========

// And combines multiple join filters with AND logic.
// Returns a filter that matches when ALL provided filters match.
//
// Example:
//
//	jf := qb.JoinFilter()
//	filter := jf.And(
//	    jf.EqColumn("users.id", "profiles.user_id"),
//	    jf.GtColumn("profiles.created_at", "users.created_at"),
//	)
func (jff *JoinFilterFactory) And(filters ...dbtypes.JoinFilter) dbtypes.JoinFilter {
	sqlizers := make(squirrel.And, len(filters))
	for i, filter := range filters {
		// Extract the underlying squirrel.Sqlizer
		if concreteFilter, ok := filter.(JoinFilter); ok {
			sqlizers[i] = concreteFilter.sqlizer
		} else {
			// Fallback: use the filter as-is (it implements Sqlizer)
			sqlizers[i] = filter
		}
	}
	return JoinFilter{sqlizer: sqlizers}
}

// Or combines multiple join filters with OR logic.
// Returns a filter that matches when ANY provided filter matches.
//
// Example:
//
//	jf := qb.JoinFilter()
//	filter := jf.Or(
//	    jf.EqColumn("users.primary_email", "contacts.email"),
//	    jf.EqColumn("users.secondary_email", "contacts.email"),
//	)
func (jff *JoinFilterFactory) Or(filters ...dbtypes.JoinFilter) dbtypes.JoinFilter {
	sqlizers := make(squirrel.Or, len(filters))
	for i, filter := range filters {
		// Extract the underlying squirrel.Sqlizer
		if concreteFilter, ok := filter.(JoinFilter); ok {
			sqlizers[i] = concreteFilter.sqlizer
		} else {
			// Fallback: use the filter as-is
			sqlizers[i] = filter
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
