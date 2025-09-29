package builder

import (
	"fmt"
	"sort"
	"strings"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// quoteOracleColumn handles Oracle-specific column name quoting.
// It identifies Oracle reserved words and applies appropriate quoting.
var oracleReservedWords = map[string]struct{}{
	"ACCESS": {}, "ADD": {}, "ALL": {}, "ALTER": {}, "AND": {}, "ANY": {}, "AS": {}, "ASC": {},
	"BEGIN": {}, "BETWEEN": {}, "BY": {}, "CASE": {}, "CHECK": {}, "COLUMN": {}, "COMMENT": {},
	"CONNECT": {}, "CREATE": {}, "CURRENT": {}, "DELETE": {}, "DESC": {}, "DISTINCT": {},
	"DROP": {}, "ELSE": {}, "EXCLUDE": {}, "EXISTS": {}, "FOR": {}, "FROM": {}, "GRANT": {},
	"GROUP": {}, "HAVING": {}, "IN": {}, "INDEX": {}, "INSERT": {}, "INTERSECT": {}, "INTO": {},
	"IS": {}, "LEVEL": {}, "LIKE": {}, "LOCK": {}, "MINUS": {}, "MODE": {}, "NOCOMPRESS": {},
	"NOT": {}, "NULL": {}, "NUMBER": {}, "OF": {}, "ON": {}, "OPTION": {}, "OR": {}, "ORDER": {},
	"ROW": {}, "ROWNUM": {}, "SELECT": {}, "SET": {}, "SHARE": {}, "SIZE": {}, "START": {},
	"TABLE": {}, "THEN": {}, "TO": {}, "TRIGGER": {}, "UNION": {}, "UNIQUE": {}, "UPDATE": {},
	"VALUES": {}, "VIEW": {}, "WHEN": {}, "WHERE": {}, "WITH": {},
}

func isOracleReservedWord(identifier string) bool {
	if identifier == "" {
		return false
	}
	_, ok := oracleReservedWords[strings.ToUpper(identifier)]
	return ok
}

func oracleNeedsQuoting(identifier string) bool {
	if identifier == "" {
		return false
	}

	first := identifier[0]
	if first >= '0' && first <= '9' {
		return true
	}

	for _, r := range identifier {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '$' || r == '#' {
			continue
		}
		return true
	}

	return false
}

// isLetter checks if a character is a letter
func isLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// isValidIdentifierChar checks if a character is valid in an identifier
func isValidIdentifierChar(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '$' || c == '#'
}

// =========================== HELPER FUNCTIONS FOR SQL FUNCTION DETECTION ===========================

// isQuotedString checks if a string is fully enclosed in double quotes
func isQuotedString(s string) bool {
	return len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"'
}

// extractFunctionNameAndArgs splits a potential function call into name and validates parentheses structure
func extractFunctionNameAndArgs(s string) (name string, hasValidStructure bool) {
	// Find first opening parenthesis
	parenIndex := strings.IndexByte(s, '(')
	if parenIndex <= 0 {
		return "", false
	}

	// Must have matching closing parenthesis
	if !strings.Contains(s[parenIndex:], ")") {
		return "", false
	}

	// Extract and validate function name part
	functionName := strings.TrimSpace(s[:parenIndex])
	if functionName == "" {
		return "", false
	}

	return functionName, true
}

// isValidOracleIdentifierStart checks if a character can start an Oracle identifier
// Oracle identifiers must start with a letter (A-Z, a-z)
func isValidOracleIdentifierStart(c byte) bool {
	return isLetter(c)
}

// isValidIdentifierSegment validates a single unquoted identifier segment
// Must start with letter and contain only valid identifier characters
func isValidIdentifierSegment(segment string) bool {
	if segment == "" {
		return false
	}

	// Must start with a letter (fixes the bug with "1COUNT" etc.)
	if !isValidOracleIdentifierStart(segment[0]) {
		return false
	}

	// All characters must be valid identifier characters
	for i := 0; i < len(segment); i++ {
		if !isValidIdentifierChar(segment[i]) {
			return false
		}
	}

	return true
}

// isValidQuotedSegment validates a quoted identifier segment
// Must be properly quoted and non-empty inside quotes
func isValidQuotedSegment(segment string) bool {
	if len(segment) < 2 {
		return false
	}

	// Must start and end with quotes
	if segment[0] != '"' || segment[len(segment)-1] != '"' {
		return false
	}

	// Content inside quotes must not be empty
	return segment[1:len(segment)-1] != ""
}

// parseQualifiedIdentifier splits and validates a potentially qualified identifier
// Returns segments and validity status
func parseQualifiedIdentifier(name string) ([]string, bool) {
	// Split on dots to handle qualified names like SCHEMA.PKG.FUNC
	segments := strings.Split(name, ".")

	// Validate each segment is not empty after trimming
	for i, segment := range segments {
		segments[i] = strings.TrimSpace(segment)
		if segments[i] == "" {
			return nil, false
		}
	}

	return segments, true
}

// validateSegment checks if a segment is valid (quoted or unquoted)
func validateSegment(segment string) bool {
	if segment[0] == '"' {
		return isValidQuotedSegment(segment)
	}
	return isValidIdentifierSegment(segment)
}

// isSQLFunction checks if the given string is a SQL function call
func isSQLFunction(s string) bool {
	if s == "" {
		return false
	}

	// Quoted strings are identifiers, not functions
	if isQuotedString(s) {
		return false
	}

	// Extract function name and validate structure
	functionName, hasValidStructure := extractFunctionNameAndArgs(s)
	if !hasValidStructure {
		return false
	}

	// Parse and validate the function name (may be qualified)
	segments, valid := parseQualifiedIdentifier(functionName)
	if !valid {
		return false
	}

	// Validate each segment
	for _, segment := range segments {
		if !validateSegment(segment) {
			return false
		}
	}

	return true
}

func oracleQuoteIdentifier(column string) string {
	trimmed := strings.TrimSpace(column)
	if trimmed == "" {
		return trimmed
	}

	// Don't quote SQL functions like COUNT(*), SUM(column), etc.
	if isSQLFunction(trimmed) {
		return trimmed
	}

	if strings.Contains(trimmed, ".") {
		parts := strings.Split(trimmed, ".")
		for i, part := range parts {
			parts[i] = oracleQuoteIdentifier(part)
		}
		return strings.Join(parts, ".")
	}

	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		return trimmed
	}

	if isOracleReservedWord(trimmed) {
		return `"` + trimmed + `"`
	}

	if oracleNeedsQuoting(trimmed) {
		return `"` + trimmed + `"`
	}

	return trimmed
}

func (qb *QueryBuilder) quoteOracleColumn(column string) string {
	if qb.vendor != dbtypes.Oracle {
		return column
	}
	return oracleQuoteIdentifier(column)
}

// quoteOracleColumnsForDML applies Oracle-specific quoting for column lists used in DML statements
// like INSERT or UPDATE where reserved words must be safely referenced. In these contexts, we
// prefer upper-cased quoted identifiers for reserved words to match Oracle's default identifier case.
func (qb *QueryBuilder) quoteOracleColumnsForDML(columns ...string) []string {
	if qb.vendor != dbtypes.Oracle {
		return columns
	}

	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = oracleQuoteIdentifier(col)
	}
	return quoted
}

// buildOraclePaginationClause builds an Oracle-compatible pagination suffix.
// Oracle 12c+ supports OFFSET ... ROWS FETCH NEXT ... ROWS ONLY syntax.
// buildOraclePaginationClause constructs an Oracle-compatible pagination clause using OFFSET and FETCH NEXT syntax.
// The returned string contains "OFFSET {offset} ROWS" and/or "FETCH NEXT {limit} ROWS ONLY" as applicable; it is empty if both limit and offset are less than or equal to zero.
func buildOraclePaginationClause(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}

	parts := make([]string, 0, 2)
	if offset > 0 {
		parts = append(parts, fmt.Sprintf("OFFSET %d ROWS", offset))
	}
	if limit > 0 {
		parts = append(parts, fmt.Sprintf("FETCH NEXT %d ROWS ONLY", limit))
	}

	return strings.Join(parts, " ")
}

// BuildUpsert creates an UPSERT/MERGE query using Oracle's MERGE statement.
// Oracle uses MERGE INTO ... USING ... ON ... WHEN MATCHED ... WHEN NOT MATCHED syntax.
func (qb *QueryBuilder) BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	if qb.vendor != dbtypes.Oracle {
		return qb.buildNonOracleUpsert(table, conflictColumns, insertColumns, updateColumns)
	}

	// Oracle uses MERGE statement
	return qb.buildOracleMerge(table, conflictColumns, insertColumns, updateColumns)
}

// buildOracleMerge constructs an Oracle MERGE statement for upsert operations
func (qb *QueryBuilder) buildOracleMerge(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	if len(conflictColumns) == 0 {
		return "", nil, fmt.Errorf("conflict columns required for Oracle MERGE")
	}

	// Build the USING clause with values
	insertKeys := sortedKeys(insertColumns)
	escapedInsertCols := qb.escapeIdentifiers(insertKeys)
	usingValues := make([]string, len(insertKeys))
	for i, col := range escapedInsertCols {
		usingValues[i] = fmt.Sprintf(":%d AS %s", i+1, col)
	}
	usingArgs := valuesByKeyOrder(insertColumns, insertKeys)

	// Build ON clause for conflict detection
	orderedConflicts := append([]string(nil), conflictColumns...)
	sort.Strings(orderedConflicts)
	escapedConflicts := qb.escapeIdentifiers(orderedConflicts)
	onConditions := make([]string, len(escapedConflicts))
	for i, col := range escapedConflicts {
		onConditions[i] = fmt.Sprintf("target.%s = source.%s", col, col)
	}

	// Build UPDATE SET clause
	updateKeys := sortedKeys(updateColumns)
	escapedUpdateCols := qb.escapeIdentifiers(updateKeys)
	updateSets := make([]string, len(updateKeys))
	baseIndex := len(insertKeys) + 1
	for i, col := range escapedUpdateCols {
		updateSets[i] = fmt.Sprintf("%s = :%d", col, baseIndex+i)
	}
	updateArgs := valuesByKeyOrder(updateColumns, updateKeys)

	// Build INSERT clause
	insertCols := make([]string, len(escapedInsertCols))
	insertVals := make([]string, len(escapedInsertCols))
	for i, col := range escapedInsertCols {
		insertCols[i] = col
		insertVals[i] = "source." + col
	}

	query = fmt.Sprintf(`MERGE INTO %s target USING (SELECT %s FROM dual) source ON (%s)`,
		table,
		strings.Join(usingValues, ", "),
		strings.Join(onConditions, " AND "))

	if len(updateSets) > 0 {
		query += fmt.Sprintf(" WHEN MATCHED THEN UPDATE SET %s", strings.Join(updateSets, ", "))
	}

	query += fmt.Sprintf(" WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		strings.Join(insertCols, ", "),
		strings.Join(insertVals, ", "))

	// Combine arguments: using args first, then update args
	args = make([]any, 0, len(usingArgs)+len(updateArgs))
	args = append(args, usingArgs...)
	args = append(args, updateArgs...)

	return query, args, nil
}

// buildNonOracleUpsert handles upsert for non-Oracle databases (primarily PostgreSQL)
func (qb *QueryBuilder) buildNonOracleUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	if qb.vendor == dbtypes.PostgreSQL {
		return qb.buildPostgreSQLUpsert(table, conflictColumns, insertColumns, updateColumns)
	}

	return "", nil, fmt.Errorf("upsert not supported for database vendor: %s", qb.vendor)
}
