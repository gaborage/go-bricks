package sqllex

import (
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
		assert.Equal(t, word, word, "All keys in OracleReservedWords should be uppercase: found %q", word)
		// Verify no lowercase characters
		for _, char := range word {
			assert.False(t, char >= 'a' && char <= 'z', "Key %q contains lowercase character %c", word, char)
		}
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
		"COMPRESS": false, // Not in the list
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
