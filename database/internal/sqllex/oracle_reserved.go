package sqllex

import "strings"

// OracleReservedWords contains all Oracle SQL reserved keywords that require double-quote quoting
// when used as identifiers (column names, table names, etc.).
//
// This is the canonical source of truth for Oracle reserved word detection across the GoBricks framework.
// Both the query builder and column parser rely on this list for automatic identifier quoting.
//
// Source: Oracle Database SQL Language Reference
// https://docs.oracle.com/en/database/oracle/oracle-database/19/sqlrf/Oracle-SQL-Reserved-Words.html
//
// Note: This list uses struct{} for zero-memory overhead in the map value.
var OracleReservedWords = map[string]struct{}{
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

// IsOracleReservedWord checks if a word is an Oracle reserved keyword.
// The check is case-insensitive since Oracle identifiers are case-insensitive by default.
//
// Examples:
//
//	IsOracleReservedWord("LEVEL")  // true
//	IsOracleReservedWord("level")  // true
//	IsOracleReservedWord("Level")  // true
//	IsOracleReservedWord("user_id") // false
func IsOracleReservedWord(word string) bool {
	_, exists := OracleReservedWords[strings.ToUpper(word)]
	return exists
}
