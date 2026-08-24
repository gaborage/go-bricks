package sqllex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsBareIdentifierAcceptsOneSegment pins the accepted language: a single
// unquoted identifier, or the framework's own quoted reserved-word form.
func TestIsBareIdentifierAcceptsOneSegment(t *testing.T) {
	accepted := []string{
		"u",
		"_",
		"_u1",
		"users",
		"u$1#a",
		"U",
		`"level"`,
		`"_u1"`,
	}

	for _, s := range accepted {
		t.Run(s, func(t *testing.T) {
			assert.True(t, IsBareIdentifier(s), "IsBareIdentifier(%q) must accept", s)
		})
	}
}

// TestIsBareIdentifierRejectsEverythingElse pins the refused language. The
// control characters matter most: the pattern is anchored with ^ and $, and Go
// anchors those to the whole text rather than to a line, so an embedded newline
// cannot smuggle a second token past the check.
func TestIsBareIdentifierRejectsEverythingElse(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "leading_digit", input: "1u"},
		{name: "qualified", input: "u.x"},
		{name: "two_tokens", input: "u x"},
		{name: "statement_terminator", input: "u;"},
		{name: "comment_marker", input: "u--"},
		{name: "table_swap", input: "id FROM secrets--"},
		{name: "interior_quote", input: `u"x`},
		{name: "unbalanced_quote", input: `"u`},
		{name: "quoted_qualified", input: `"u"."x"`},
		{name: "leading_space", input: " u"},
		{name: "trailing_space", input: "u "},
		{name: "newline_suffix", input: "u\n"},
		{name: "newline_smuggled_token", input: "u\nDROP TABLE x"},
		{name: "leading_newline", input: "\nu"},
		{name: "carriage_return", input: "u\rDROP"},
		{name: "null_byte", input: "u\x00"},
		{name: "tab", input: "u\tx"},
		{name: "non_ascii", input: "ü"},
		{name: "backslash", input: `u\`},
		{name: "wildcard", input: "*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, IsBareIdentifier(tt.input), "IsBareIdentifier(%q) must refuse", tt.input)
		})
	}
}
