package sqllex

import "strings"

const (
	oracleReservedLevel  = "LEVEL"
	oracleReservedNumber = "NUMBER"
	oracleReservedSize   = "SIZE"
)

// OracleReservedWords contains all Oracle SQL reserved keywords that require double-quote quoting
// when used as identifiers (column names, table names, etc.).
//
// This is the canonical source of truth for Oracle reserved word detection across the GoBricks framework.
// Both the query builder and column parser rely on this list for automatic identifier quoting.
//
// The map is kept in parity with the full official Oracle 19c reserved-word list
// (V$RESERVED_WORDS where reserved='Y'); parity is enforced by
// TestOracleReservedWordsMatchOfficial19cList. A small, intentional superset is also quoted
// defensively (BEGIN, CASE, WHEN, EXCLUDE) — see that test for the rationale of each entry.
//
// Source: Oracle Database SQL Language Reference 19c, "Oracle SQL Reserved Words".
// https://docs.oracle.com/en/database/oracle/oracle-database/19/sqlrf/Oracle-SQL-Reserved-Words.html
//
// Note: This list uses struct{} for zero-memory overhead in the map value.
//
//nolint:goconst // Literal reserved-word data table; extracting each keyword to a named constant would harm readability and defeat the purpose of a flat lookup set.
var OracleReservedWords = map[string]struct{}{
	// Official Oracle 19c reserved words.
	"ACCESS": {}, "ADD": {}, "ALL": {}, "ALTER": {}, "AND": {}, "ANY": {}, "AS": {}, "ASC": {},
	"AUDIT": {}, "BETWEEN": {}, "BY": {}, "CHAR": {}, "CHECK": {}, "CLUSTER": {}, "COLUMN": {},
	"COMMENT": {}, "COMPRESS": {}, "CONNECT": {}, "CREATE": {}, "CURRENT": {}, "DATE": {},
	"DECIMAL": {}, "DEFAULT": {}, "DELETE": {}, "DESC": {}, "DISTINCT": {}, "DROP": {}, "ELSE": {},
	"EXCLUSIVE": {}, "EXISTS": {}, "FILE": {}, "FLOAT": {}, "FOR": {}, "FROM": {}, "GRANT": {},
	"GROUP": {}, "HAVING": {}, "IDENTIFIED": {}, "IMMEDIATE": {}, "IN": {}, "INCREMENT": {},
	"INDEX": {}, "INITIAL": {}, "INSERT": {}, "INTEGER": {}, "INTERSECT": {}, "INTO": {}, "IS": {},
	oracleReservedLevel: {}, "LIKE": {}, "LOCK": {}, "LONG": {}, "MAXEXTENTS": {}, "MINUS": {},
	"MLSLABEL": {}, "MODE": {}, "MODIFY": {}, "NOAUDIT": {}, "NOCOMPRESS": {}, "NOT": {},
	"NOWAIT": {}, "NULL": {}, oracleReservedNumber: {}, "OF": {}, "OFFLINE": {}, "ON": {},
	"ONLINE": {}, "OPTION": {}, "OR": {}, "ORDER": {}, "PCTFREE": {}, "PRIOR": {}, "PUBLIC": {},
	"RAW": {}, "RENAME": {}, "RESOURCE": {}, "REVOKE": {}, "ROW": {}, "ROWID": {}, "ROWNUM": {},
	"ROWS": {}, "SELECT": {}, "SESSION": {}, "SET": {}, "SHARE": {}, oracleReservedSize: {},
	"SMALLINT": {}, "START": {}, "SUCCESSFUL": {}, "SYNONYM": {}, "SYSDATE": {}, "TABLE": {},
	"THEN": {}, "TO": {}, "TRIGGER": {}, "UID": {}, "UNION": {}, "UNIQUE": {}, "UPDATE": {},
	"USER": {}, "VALIDATE": {}, "VALUES": {}, "VARCHAR": {}, "VARCHAR2": {}, "VIEW": {},
	"WHENEVER": {}, "WHERE": {}, "WITH": {},

	// Intentional supersets (not on the official V$RESERVED_WORDS list, quoted defensively).
	"BEGIN":   {}, // PL/SQL block keyword; hazardous as an unquoted identifier.
	"CASE":    {}, // SQL CASE expression keyword.
	"WHEN":    {}, // SQL CASE/WHEN keyword.
	"EXCLUDE": {}, // Reserved in analytic windowing (WINDOW frame EXCLUDE clause).
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
