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
//	qb.Select(qb.Expr("COUNT(*)", "total"))           // Aggregation with alias
//	qb.Select(qb.Expr("UPPER(name)"))                  // Function without alias
//	qb.Select(qb.Expr("price * quantity", "total"))    // Calculation with alias
//
// Unsafe usage (NEVER do this):
//
//	userInput := req.Query("column")
//	qb.Select(qb.Expr(fmt.Sprintf("UPPER(%s)", userInput))) // SQL INJECTION RISK!
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
	// Validate SQL is not empty
	if strings.TrimSpace(sql) == "" {
		return RawExpression{}, ErrEmptyExpressionSQL
	}

	// Validate max 1 alias
	if len(alias) > 1 {
		return RawExpression{}, fmt.Errorf("%w: got %d", ErrTooManyAliases, len(alias))
	}

	// Extract alias if provided
	var aliasStr string
	if len(alias) == 1 {
		aliasStr = alias[0]

		// Validate alias doesn't contain dangerous characters (SQL injection patterns)
		dangerousChars := []string{";", "'", "\"", "--", "/*", "*/"}
		for _, char := range dangerousChars {
			if strings.Contains(aliasStr, char) {
				return RawExpression{}, fmt.Errorf("%w '%s': %s", ErrDangerousAlias, char, aliasStr)
			}
		}
	}

	return RawExpression{
		SQL:   sql,
		Alias: aliasStr,
	}, nil
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
