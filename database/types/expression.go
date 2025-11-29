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
// Validation (fail-fast with panic):
//   - SQL cannot be empty
//   - Maximum 1 alias parameter allowed
//   - Alias cannot contain dangerous characters: ; ' " -- (SQL injection patterns)
//
// Examples:
//
//	// Aggregation with alias
//	qb.Expr("COUNT(*)", "total")
//
//	// Function without alias
//	qb.Expr("UPPER(name)")
//
//	// Calculation with alias
//	qb.Expr("price * quantity", "line_total")
//
//	// Window function with alias
//	qb.Expr("ROW_NUMBER() OVER (PARTITION BY category ORDER BY date)", "row_num")
//
// SECURITY WARNING: Never interpolate user input directly into the sql parameter.
// This function does NOT sanitize SQL - you are responsible for ensuring safety.
func Expr(sql string, alias ...string) RawExpression {
	// Validate SQL is not empty
	if strings.TrimSpace(sql) == "" {
		panic("expression SQL cannot be empty") //nolint:S8148 // NOSONAR: Fail-fast on invalid SQL expression construction
	}

	// Validate max 1 alias
	if len(alias) > 1 {
		panic(fmt.Sprintf("Expr accepts maximum 1 alias, got %d", len(alias))) //nolint:S8148 // NOSONAR: Fail-fast on invalid SQL expression construction
	}

	// Extract alias if provided
	var aliasStr string
	if len(alias) == 1 {
		aliasStr = alias[0]

		// Validate alias doesn't contain dangerous characters (SQL injection patterns)
		dangerousChars := []string{";", "'", "\"", "--", "/*", "*/"}
		for _, char := range dangerousChars {
			if strings.Contains(aliasStr, char) {
				panic(fmt.Sprintf("alias contains dangerous character '%s': %s", char, aliasStr)) //nolint:S8148 // NOSONAR: Fail-fast on invalid SQL expression construction
			}
		}
	}

	return RawExpression{
		SQL:   sql,
		Alias: aliasStr,
	}
}
