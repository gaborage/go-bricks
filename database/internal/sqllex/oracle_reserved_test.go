package sqllex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsOracleReservedWordCommonKeywords tests commonly used Oracle reserved words
func TestIsOracleReservedWordCommonKeywords(t *testing.T) {
	commonReserved := []string{
		"LEVEL", "NUMBER", "SIZE", // Common field name conflicts
		"SELECT", "FROM", "WHERE", "ORDER", "GROUP", // SQL keywords
		"TABLE", "INDEX", "VIEW", "TRIGGER", // DDL keywords
		"INSERT", "UPDATE", "DELETE", // DML keywords
		"IS", "NULL", "NOT", "AND", "OR", // Operators
	}

	for _, word := range commonReserved {
		t.Run(word, func(t *testing.T) {
			assert.True(t, IsOracleReservedWord(word), "Expected %q to be a reserved word", word)
		})
	}
}

// TestIsOracleReservedWordCaseInsensitive tests case-insensitive detection
func TestIsOracleReservedWordCaseInsensitive(t *testing.T) {
	testCases := []struct {
		word     string
		expected bool
	}{
		// Reserved words in different cases
		{"LEVEL", true},
		{"level", true},
		{"Level", true},
		{"LeVeL", true},
		{"NUMBER", true},
		{"number", true},
		{"Number", true},

		// Non-reserved words
		{"user_id", false},
		{"account_name", false},
		{"id", false},
		{"name", false},
		{"email", false},
	}

	for _, tc := range testCases {
		t.Run(tc.word, func(t *testing.T) {
			result := IsOracleReservedWord(tc.word)
			assert.Equal(t, tc.expected, result, "IsOracleReservedWord(%q) = %v, expected %v", tc.word, result, tc.expected)
		})
	}
}

// TestIsOracleReservedWordNonReserved tests that non-reserved words return false
func TestIsOracleReservedWordNonReserved(t *testing.T) {
	nonReserved := []string{
		"id", "name", "email", "status", "created_at", "updated_at",
		"user_id", "account_id", "product_id",
		"first_name", "last_name", "phone", "address",
		"price", "quantity", "total", "discount",
	}

	for _, word := range nonReserved {
		t.Run(word, func(t *testing.T) {
			assert.False(t, IsOracleReservedWord(word), "Expected %q to NOT be a reserved word", word)
		})
	}
}

// TestOracleReservedWordsMapNotEmpty verifies the map is populated
func TestOracleReservedWordsMapNotEmpty(t *testing.T) {
	assert.NotEmpty(t, OracleReservedWords, "OracleReservedWords map should not be empty")
	assert.Greater(t, len(OracleReservedWords), 50, "Expected at least 50 Oracle reserved words")
}

// TestOracleReservedWordsUpperCase verifies all keys are uppercase
func TestOracleReservedWordsUpperCase(t *testing.T) {
	for word := range OracleReservedWords {
		assert.Equal(t, strings.ToUpper(word), word, "All keys in OracleReservedWords should be uppercase: found %q", word)
	}
}

// TestOracleReservedWordsEmptyString tests edge case of empty string
func TestOracleReservedWordsEmptyString(t *testing.T) {
	assert.False(t, IsOracleReservedWord(""), "Empty string should not be a reserved word")
}

// TestOracleReservedWordsSpecificKeywords verifies specific critical reserved words
func TestOracleReservedWordsSpecificKeywords(t *testing.T) {
	// These are particularly problematic in real-world schemas
	criticalWords := map[string]bool{
		"ACCESS":   true,
		"LEVEL":    true,
		"NUMBER":   true,
		"SIZE":     true,
		"MODE":     true,
		"COMMENT":  true,
		"SHARE":    true,
		"ROW":      true,
		"ROWNUM":   true,
		"OPTION":   true,
		"COMPRESS": true, // Official Oracle 19c reserved word (M10 fix)
	}

	for word, shouldExist := range criticalWords {
		t.Run(word, func(t *testing.T) {
			_, exists := OracleReservedWords[word]
			assert.Equal(t, shouldExist, exists, "OracleReservedWords[%q] existence = %v, expected %v", word, exists, shouldExist)
		})
	}
}

// TestIsOracleReservedWordAllEntries verifies IsOracleReservedWord returns true for all map entries
func TestIsOracleReservedWordAllEntries(t *testing.T) {
	count := 0
	for word := range OracleReservedWords {
		assert.True(t, IsOracleReservedWord(word), "IsOracleReservedWord should return true for map entry %q", word)
		count++
	}
	assert.Greater(t, count, 0, "Should have tested at least one reserved word")
}

// TestOracleQuotingFailureWithMissingReservedWords reproduces M10: the map was missing
// 41 official Oracle 19c reserved keywords, so identifiers like DATE/VARCHAR2/USER were
// emitted unquoted, triggering ORA-00936/ORA-00904 against Oracle 19c. Every word below
// is a V$RESERVED_WORDS (reserved='Y') keyword in Oracle 19c and MUST be quoted.
func TestOracleQuotingFailureWithMissingReservedWords(t *testing.T) {
	previouslyMissing := []string{
		"AUDIT", "CHAR", "CLUSTER", "COMPRESS", "DATE", "DECIMAL", "DEFAULT",
		"EXCLUSIVE", "FILE", "FLOAT", "IDENTIFIED", "IMMEDIATE", "INCREMENT",
		"INITIAL", "INTEGER", "LONG", "MODIFY", "NOAUDIT", "NOWAIT", "OFFLINE",
		"ONLINE", "PCTFREE", "PRIOR", "PUBLIC", "RAW", "RENAME", "RESOURCE",
		"REVOKE", "ROWID", "ROWS", "SESSION", "SMALLINT", "SUCCESSFUL", "SYNONYM",
		"SYSDATE", "UID", "USER", "VALIDATE", "VARCHAR", "VARCHAR2", "WHENEVER",
	}

	for _, word := range previouslyMissing {
		t.Run(word, func(t *testing.T) {
			assert.True(t, IsOracleReservedWord(word),
				"Oracle 19c reserved word %q must be detected so identifiers are quoted (ORA-00936/ORA-00904 otherwise)", word)
		})
	}
}

// TestOracleReservedWordsMatchOfficial19cList asserts parity between the map and a vendored
// copy of the official Oracle 19c reserved-word list (V$RESERVED_WORDS where reserved='Y').
// HARDEN: the map must cover the full official list; any intentional superset entry must be
// explicitly documented here so it cannot silently mask a parity regression.
func TestOracleReservedWordsMatchOfficial19cList(t *testing.T) {
	// official19cReserved is the canonical Oracle 19c reserved-word set.
	// Source: Oracle Database SQL Language Reference 19c, "Oracle SQL Reserved Words".
	official19cReserved := map[string]struct{}{
		"ACCESS": {}, "ADD": {}, "ALL": {}, "ALTER": {}, "AND": {}, "ANY": {},
		"AS": {}, "ASC": {}, "AUDIT": {}, "BETWEEN": {}, "BY": {}, "CHAR": {},
		"CHECK": {}, "CLUSTER": {}, "COLUMN": {}, "COMMENT": {}, "COMPRESS": {},
		"CONNECT": {}, "CREATE": {}, "CURRENT": {}, "DATE": {}, "DECIMAL": {},
		"DEFAULT": {}, "DELETE": {}, "DESC": {}, "DISTINCT": {}, "DROP": {},
		"ELSE": {}, "EXCLUSIVE": {}, "EXISTS": {}, "FILE": {}, "FLOAT": {},
		"FOR": {}, "FROM": {}, "GRANT": {}, "GROUP": {}, "HAVING": {},
		"IDENTIFIED": {}, "IMMEDIATE": {}, "IN": {}, "INCREMENT": {}, "INDEX": {},
		"INITIAL": {}, "INSERT": {}, "INTEGER": {}, "INTERSECT": {}, "INTO": {},
		"IS": {}, "LEVEL": {}, "LIKE": {}, "LOCK": {}, "LONG": {}, "MAXEXTENTS": {},
		"MINUS": {}, "MLSLABEL": {}, "MODE": {}, "MODIFY": {}, "NOAUDIT": {},
		"NOCOMPRESS": {}, "NOT": {}, "NOWAIT": {}, "NULL": {}, "NUMBER": {},
		"OF": {}, "OFFLINE": {}, "ON": {}, "ONLINE": {}, "OPTION": {}, "OR": {},
		"ORDER": {}, "PCTFREE": {}, "PRIOR": {}, "PUBLIC": {}, "RAW": {},
		"RENAME": {}, "RESOURCE": {}, "REVOKE": {}, "ROW": {}, "ROWID": {},
		"ROWNUM": {}, "ROWS": {}, "SELECT": {}, "SESSION": {}, "SET": {},
		"SHARE": {}, "SIZE": {}, "SMALLINT": {}, "START": {}, "SUCCESSFUL": {},
		"SYNONYM": {}, "SYSDATE": {}, "TABLE": {}, "THEN": {}, "TO": {},
		"TRIGGER": {}, "UID": {}, "UNION": {}, "UNIQUE": {}, "UPDATE": {},
		"USER": {}, "VALIDATE": {}, "VALUES": {}, "VARCHAR": {}, "VARCHAR2": {},
		"VIEW": {}, "WHENEVER": {}, "WHERE": {}, "WITH": {},
	}

	// Intentional supersets: keywords NOT on the official V$RESERVED_WORDS list that the
	// framework still quotes defensively (reserved in newer versions, PL/SQL contexts, or
	// common DDL/DML hazards). Documenting them here keeps the parity assertion meaningful.
	intentionalSupersets := map[string]struct{}{
		"BEGIN":   {}, // PL/SQL block keyword; hazardous as an unquoted identifier.
		"CASE":    {}, // SQL CASE expression keyword.
		"WHEN":    {}, // SQL CASE/WHEN keyword.
		"EXCLUDE": {}, // Reserved in analytic windowing (WINDOW frame EXCLUDE clause).
	}

	// Every official reserved word must be present in the map.
	for word := range official19cReserved {
		_, exists := OracleReservedWords[word]
		assert.True(t, exists, "official Oracle 19c reserved word %q is missing from OracleReservedWords", word)
	}

	// Every map entry must be either an official reserved word or a documented superset.
	for word := range OracleReservedWords {
		_, official := official19cReserved[word]
		_, superset := intentionalSupersets[word]
		assert.True(t, official || superset,
			"OracleReservedWords contains %q which is neither an official 19c reserved word nor a documented intentional superset", word)
	}
}
