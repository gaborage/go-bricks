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
		{name: "inner_separator", prefix: "orders:v2", valid: true},
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
