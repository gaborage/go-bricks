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

// TestIsUnquotedIdentifierAcceptsOneUnquotedSegment pins the narrower language:
// the same unquoted segment IsBareIdentifier takes, and nothing quoted.
//
// Subtest names are the raw INPUT rather than the repo's usual snake_case labels,
// matching the IsBareIdentifier tables above: the input IS the case here, so a
// label would restate it less precisely — `u$1#a` says exactly which alphabet
// member is under test, `dollar_and_hash` does not, and a failure names the
// offending string directly.
func TestIsUnquotedIdentifierAcceptsOneUnquotedSegment(t *testing.T) {
	accepted := []string{"u", "_", "_u1", "users", "u$1#a", "U", "total_count"}

	for _, s := range accepted {
		t.Run(s, func(t *testing.T) {
			assert.True(t, IsUnquotedIdentifier(s), "IsUnquotedIdentifier(%q) must accept", s)
		})
	}
}

// TestIsUnquotedIdentifierRejectsQuotedForm is the whole reason the predicate
// exists: IsBareIdentifier accepts the framework's quoted reserved-word form, and
// the doors that take caller-supplied text (an expression alias) must not.
// Asserting BOTH predicates keeps the divergence pinned — a change that collapsed
// one into the other would pass a test that only checked the new one.
//
// Subtest names are the raw input here too, for the same reason.
func TestIsUnquotedIdentifierRejectsQuotedForm(t *testing.T) {
	for _, s := range []string{`"level"`, `"_u1"`, `"users"`} {
		t.Run(s, func(t *testing.T) {
			assert.True(t, IsBareIdentifier(s), "IsBareIdentifier(%q) still accepts the quoted form", s)
			assert.False(t, IsUnquotedIdentifier(s), "IsUnquotedIdentifier(%q) must refuse it", s)
		})
	}
}

func TestIsUnquotedIdentifierRejectsEverythingElse(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "leading_digit", input: "1u"},
		{name: "space", input: "my alias"},
		{name: "call", input: "f(x)"},
		{name: "semicolon", input: "a;b"},
		{name: "newline", input: "a\nb"},
		{name: "backtick", input: "`bt`"},
		{name: "dotted", input: "u.name"},
		{name: "clause_smuggle", input: "x FROM users"},
		{name: "unbalanced_quote", input: `"level`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, IsUnquotedIdentifier(tt.input), "IsUnquotedIdentifier(%q) must refuse", tt.input)
		})
	}
}
