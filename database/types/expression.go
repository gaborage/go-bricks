//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"fmt"
	"strings"
)

// RawExpression represents a raw SQL expression that can be used in SELECT, GROUP BY, and ORDER BY clauses.
// It allows using SQL functions, aggregations, calculations, and other expressions that go beyond simple column names.
//
// SECURITY WARNING: Raw SQL expressions are NOT escaped or sanitized by the framework.
// Never interpolate user input directly into expressions - this creates SQL injection vulnerabilities.
// Only use static SQL or carefully validated values in expressions.
//
// Safe usage:
//
//	expr, err := qb.Expr("COUNT(*)", "total") // Aggregation with alias
//	if err != nil { return err }
//	qb.Select(expr)
//
//	expr, err = qb.Expr("UPPER(name)") // Function without alias
//	if err != nil { return err }
//	qb.Select(expr)
//
//	expr, err = qb.Expr("price * quantity", "total") // Calculation with alias
//	if err != nil { return err }
//	qb.Select(expr)
//
// Unsafe usage (NEVER do this):
//
//	userInput := req.Query("column")
//	expr, err := qb.Expr(fmt.Sprintf("UPPER(%s)", userInput)) // SQL INJECTION RISK!
//	if err != nil { return err }
//	qb.Select(expr)
type RawExpression struct {
	SQL   string // The raw SQL expression
	Alias string // Optional alias (AS clause)
}

// Expr creates a raw SQL expression with optional alias for use in SELECT, GROUP BY, and ORDER BY clauses.
//
// Parameters:
//   - sql: The raw SQL expression (e.g., "COUNT(*)", "UPPER(name)", "price * quantity")
//   - alias: Optional alias for the expression (e.g., "total", "upper_name"). Max 1 alias allowed.
//
// Returns:
//   - RawExpression: The constructed expression
//   - error: ErrEmptyExpressionSQL, ErrTooManyAliases, or ErrDangerousAlias on validation failure
//
// Examples:
//
//	// Aggregation with alias
//	expr, err := qb.Expr("COUNT(*)", "total")
//	if err != nil { return err }
//
//	// Function without alias
//	expr, err := qb.Expr("UPPER(name)")
//
//	// Calculation with alias
//	expr, err := qb.Expr("price * quantity", "line_total")
//
// SECURITY WARNING: Never interpolate user input directly into the sql parameter.
// This function does NOT sanitize SQL - you are responsible for ensuring safety.
func Expr(sql string, alias ...string) (RawExpression, error) {
	if len(alias) > 1 {
		return RawExpression{}, fmt.Errorf("%w: got %d", ErrTooManyAliases, len(alias))
	}

	expr := RawExpression{SQL: sql}
	if len(alias) == 1 {
		expr.Alias = alias[0]
	}

	if err := expr.Validate(); err != nil {
		return RawExpression{}, err
	}
	return expr, nil
}

// dangerousAliasChars are the SQL metacharacters an alias may never contain: an
// alias is interpolated verbatim after AS, so any of them turns the rest of the
// statement into caller-controlled syntax.
var dangerousAliasChars = []string{";", "'", "\"", "--", "/*", "*/"}

// Validate reports why this expression may not be interpolated, or nil.
//
// RawExpression is a plain struct, so a caller can build one directly and never
// reach Expr(). This is the single funnel both paths share: Expr() calls it at
// construction, and every builder door that interpolates an expression calls it
// again at consumption, where a struct literal is indistinguishable from a
// constructed one (#1153, ADR-082).
//
// The SQL itself is NOT validated — it is the sanctioned raw-SQL escape hatch,
// and the caller owns its safety. Only its emptiness and the alias are checked.
func (e RawExpression) Validate() error {
	if strings.TrimSpace(e.SQL) == "" {
		return ErrEmptyExpressionSQL
	}

	for _, char := range dangerousAliasChars {
		if strings.Contains(e.Alias, char) {
			return fmt.Errorf("%w '%s': %s", ErrDangerousAlias, char, e.Alias)
		}
	}
	return nil
}

// MustExpr is like Expr but panics on error.
// Use this only in static initialization or tests where errors indicate programming bugs.
func MustExpr(sql string, alias ...string) RawExpression {
	expr, err := Expr(sql, alias...)
	if err != nil {
		panic(fmt.Sprintf("MustExpr: %v", err))
	}
	return expr
}
