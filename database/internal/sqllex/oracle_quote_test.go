package sqllex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const assertFormat = "input: %s"

func TestIsQuotedString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty", "", false},
		{"properly_quoted", `"test"`, true},
		{"unquoted", "test", false},
		{"only_start_quote", `"test`, false},
		{"only_end_quote", `test"`, false},
		{"single_quote", `"`, false},
		{"just_quotes", `""`, true},
		{"quotes_inside", `"test"more"`, true}, // Full string is quoted
		{"spaces_quoted", `"  spaces  "`, true},
		{"single_char", "a", false},
		{"two_chars", "ab", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsQuotedString(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestIsValidOracleIdentifierStart(t *testing.T) {
	tests := []struct {
		name     string
		input    byte
		expected bool
	}{
		// Letters should return true
		{"uppercase_A", 'A', true},
		{"uppercase_Z", 'Z', true},
		{"lowercase_a", 'a', true},
		{"lowercase_z", 'z', true},
		{"middle_letter", 'M', true},

		// Numbers should return false
		{"digit_0", '0', false},
		{"digit_9", '9', false},
		{"digit_5", '5', false},

		// Special characters should return false
		{"underscore", '_', false},
		{"dollar", '$', false},
		{"hash", '#', false},
		{"hyphen", '-', false},
		{"space", ' ', false},
		{"dot", '.', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidOracleIdentifierStart(tt.input)
			assert.Equal(t, tt.expected, result, "input: %c", tt.input)
		})
	}
}

func TestIsValidIdentifierSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid_simple", "TABLE", true},
		{"valid_lowercase", "table", true},
		{"with_underscore", "MY_TABLE", true},
		{"with_dollar", "SYS$TABLE", true},
		{"with_hash", "MY#TABLE", true},
		{"with_numbers", "TABLE123", true},
		{"mixed_case", "MyTable", true},

		// Invalid cases - should start with letter
		{"starts_with_digit", "1TABLE", false},
		{"starts_with_underscore", "_TABLE", false},
		{"starts_with_dollar", "$TABLE", false},
		{"starts_with_hash", "#TABLE", false},

		// Invalid characters
		{"with_dash", "MY-TABLE", false},
		{"with_space", "MY TABLE", false},
		{"with_dot", "MY.TABLE", false},
		{"with_special", "MY@TABLE", false},

		// Edge cases
		{"empty", "", false},
		{"single_letter", "A", true},
		{"single_digit", "1", false},
		{"all_digits_start_letter", "A123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidIdentifierSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestIsValidQuotedSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid_quoted", `"test"`, true},
		{"quoted_with_spaces", `"test with spaces"`, true},
		{"quoted_with_special", `"test@#$%"`, true},
		{"quoted_with_numbers", `"123test"`, true},
		{"quoted_empty_content", `""`, false}, // Empty content inside quotes

		// Invalid cases
		{"not_quoted", "test", false},
		{"only_start_quote", `"test`, false},
		{"only_end_quote", `test"`, false},
		{"single_quote", `"`, false},
		{"empty", "", false},
		{"short_string", "a", false},

		// Edge cases
		{"quotes_in_content", `"test"inside"`, true}, // Valid quoted string
		{"single_char_quoted", `"a"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidQuotedSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestParseQualifiedIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		valid    bool
	}{
		{"simple", "FUNC", []string{"FUNC"}, true},
		{"schema_qualified", "SCHEMA.FUNC", []string{"SCHEMA", "FUNC"}, true},
		{"fully_qualified", "SCHEMA.PKG.FUNC", []string{"SCHEMA", "PKG", "FUNC"}, true},
		{"with_spaces", " SCHEMA . PKG . FUNC ", []string{"SCHEMA", "PKG", "FUNC"}, true},

		// Invalid cases
		{"empty_segment", "SCHEMA..FUNC", nil, false},
		{"starts_with_dot", ".FUNC", nil, false},
		{"ends_with_dot", "FUNC.", nil, false},
		{"only_dots", "...", nil, false},

		// Mixed quoted and unquoted
		{"mixed_quoted", `"SCHEMA".PKG."FUNC"`, []string{`"SCHEMA"`, "PKG", `"FUNC"`}, true},
		{"all_quoted", `"SCHEMA"."PKG"."FUNC"`, []string{`"SCHEMA"`, `"PKG"`, `"FUNC"`}, true},

		// Doubled quotes inside a quoted segment. The walker is a production
		// renderer path since #1151, so its escape handling is load-bearing:
		// these two inputs are what distinguish reading the character AFTER the
		// quote from reading the one before it.
		{"quoted_empty_segment", `"".x`, []string{`""`, "x"}, true},
		{"segment_ends_with_doubled_quote", `"a""".b`, []string{`"a"""`, "b"}, true},

		// Edge cases
		{"single_dot", ".", nil, false},
		{"empty_string", "", []string{""}, false},   // Will have empty segment
		{"just_spaces", "   ", []string{""}, false}, // Trimmed to empty
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments, valid := parseQualifiedIdentifier(tt.input)
			assert.Equal(t, tt.valid, valid, assertFormat, tt.input)
			if tt.valid {
				assert.Equal(t, tt.expected, segments, assertFormat, tt.input)
			} else {
				assert.Nil(t, segments, "input: %s should return nil segments", tt.input)
			}
		})
	}
}

func TestValidateSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid unquoted segments
		{"valid_unquoted", "TABLE", true},
		{"valid_with_numbers", "TABLE123", true},
		{"valid_with_underscore", "MY_TABLE", true},

		// Invalid unquoted segments
		{"invalid_starts_digit", "1TABLE", false},
		{"invalid_special_char", "MY-TABLE", false},

		// Valid quoted segments
		{"valid_quoted", `"table"`, true},
		{"valid_quoted_special", `"my-table"`, true},
		{"valid_quoted_numbers", `"123table"`, true},

		// Invalid quoted segments
		{"invalid_quoted_empty", `""`, false},
		{"invalid_unclosed_quote", `"table`, false},

		// Edge cases
		{"single_letter", "A", true},
		{"single_quoted", `"A"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateSegment(tt.input)
			assert.Equal(t, tt.expected, result, assertFormat, tt.input)
		})
	}
}

func TestQuoteOracleIdentifierCaseSensitivity(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercase_reserved_word",
			input:    "number",
			expected: `"number"`,
		},
		{
			name:     "uppercase_reserved_word",
			input:    "NUMBER",
			expected: `"NUMBER"`,
		},
		{
			name:     "mixed_case_reserved_word",
			input:    "Number",
			expected: `"Number"`,
		},
		{
			name:     "lowercase_non_reserved",
			input:    "name",
			expected: "name",
		},
		{
			name:     "uppercase_non_reserved",
			input:    "NAME",
			expected: "NAME",
		},
		{
			name:     "mixed_case_non_reserved",
			input:    "Name",
			expected: "Name",
		},
		{
			name:     "already_quoted_lowercase",
			input:    `"number"`,
			expected: `"number"`,
		},
		{
			name:     "already_quoted_uppercase",
			input:    `"NUMBER"`,
			expected: `"NUMBER"`,
		},
		{
			name:     "multiple_reserved_words",
			input:    "size",
			expected: `"size"`,
		},
		{
			name:     "dotted_identifier",
			input:    "table.number",
			expected: `"table"."number"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := QuoteOracleIdentifier(tt.input)
			if result != tt.expected {
				t.Fatalf("input: %s, expected: %s, got: %s", tt.input, tt.expected, result)
			}
		})
	}
}

func TestOracleQuoteIdentifierDottedQuotedNames(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		want       string
	}{
		{name: "qualified_second_segment_stays_intact", identifier: `t."my.col"`, want: `t."my.col"`},
		{name: "qualified_plain_name_splits", identifier: `t.level`, want: `t."level"`},
		{name: "already_quoted_segments_pass_through", identifier: `"a"."b"`, want: `"a"."b"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, QuoteOracleIdentifier(tt.identifier))
		})
	}
}
