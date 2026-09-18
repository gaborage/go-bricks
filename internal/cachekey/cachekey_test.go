package cachekey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSep(t *testing.T) {
	assert.Equal(t, ":", Sep)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		valid  bool
	}{
		{name: "plain_word", prefix: "orders", valid: true},
		{name: "punctuation_redis_does_not_glob", prefix: "a-b_c.d", valid: true},
		{name: "empty_is_the_opt_out", prefix: "", valid: true},
		{name: "inner_space", prefix: "or ders"},
		{name: "leading_space", prefix: " orders"},
		{name: "trailing_space", prefix: "orders "},
		{name: "tab", prefix: "orders\tv2"},
		{name: "glob_star", prefix: "orders*"},
		{name: "glob_question_mark", prefix: "a?b"},
		{name: "glob_class", prefix: "[x]"},
		{name: "hash_tag_braces", prefix: "{tag}"},
		{name: "open_brace_alone", prefix: "orders{"},
		{name: "close_brace_alone", prefix: "orders}"},
		{name: "trailing_separator", prefix: "orders:"},
		{name: "inner_separator", prefix: "orders:v2"},
		{name: "leading_separator", prefix: ":orders"},
		{name: "separator_alone", prefix: ":"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.prefix)
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			// Reason-only: each layer spells its own field key, so the message must
			// not carry one of its own.
			assert.NotContains(t, err.Error(), "keyprefix")
		})
	}
}

// TestValidateSeparatorReasonNamesTheGrammar pins WHY a separator is refused. The
// rule is not a typo guard: a prefix carrying ':' would occupy more than the first
// segment of the wire key, and the operator who wrote "orders:v2" needs to be told
// that the segment after the prefix belongs to the tenant, not to them.
func TestValidateSeparatorReasonNamesTheGrammar(t *testing.T) {
	for _, prefix := range []string{"orders:v2", "orders:", ":orders"} {
		err := Validate(prefix)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one segment", "the reason must state the grammar it enforces")
		assert.Contains(t, err.Error(), "tenant", "the reason must name what the next segment carries")
	}
}

// TestValidateNamespace pins the ASSEMBLED door, the one the decorator uses. It is
// segment-wise where Validate is whole-string: the framework's own tenant fold produces
// <prefix>:<tenantID>, which must pass, while every segment of it must still be a prefix
// Validate would accept and none may be empty — "orders::acme" is a base that ended in
// the separator, and accepting it would let the namespace the operator wrote differ from
// the one the keys land in.
func TestValidateNamespace(t *testing.T) {
	tests := []struct {
		name  string
		ns    string
		valid bool
	}{
		{name: "one_segment", ns: "orders", valid: true},
		{name: "prefix_and_tenant", ns: "orders:acme", valid: true},
		{name: "three_segments", ns: "orders:acme:v2", valid: true},
		{name: "empty_is_the_opt_out", ns: "", valid: true},
		{name: "empty_inner_segment", ns: "orders::acme"},
		{name: "trailing_separator", ns: "orders:"},
		{name: "leading_separator", ns: ":acme"},
		{name: "whitespace_in_a_later_segment", ns: "orders:bad name"},
		{name: "glob_in_a_later_segment", ns: "orders:acme*"},
		{name: "hash_tag_in_a_later_segment", ns: "orders:{acme}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNamespace(tt.ns)
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "keyprefix")
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name     string
		segments []string
		want     string
	}{
		{name: "two_segments", segments: []string{"app", "acme"}, want: "app:acme"},
		{name: "empty_head_is_dropped", segments: []string{"", "acme"}, want: "acme"},
		{name: "empty_tail_is_dropped", segments: []string{"app", ""}, want: "app"},
		{name: "all_empty", segments: []string{"", ""}, want: ""},
		{name: "no_segments", segments: nil, want: ""},
		{name: "three_segments", segments: []string{"app", "acme", "v2"}, want: "app:acme:v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Join(tt.segments...))
		})
	}
}
