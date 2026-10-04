package messaging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTopicMatches pins RabbitMQ's topic semantics: '.' separates words, the empty string is zero
// words, '*' matches exactly one word and '#' zero or more.
func TestTopicMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		key     string
		want    bool
	}{
		{name: "literal_equal", pattern: "dead.orders", key: "dead.orders", want: true},
		{name: "literal_differs", pattern: "dead.orders", key: "dead.payments", want: false},
		{name: "literal_prefix_only", pattern: "dead", key: "dead.orders", want: false},
		{name: "literal_longer_than_key", pattern: "dead.orders.eu", key: "dead.orders", want: false},
		{name: "empty_pattern_empty_key", pattern: "", key: "", want: true},
		{name: "empty_pattern_non_empty_key", pattern: "", key: "orders", want: false},
		{name: "hash_alone_empty_key", pattern: "#", key: "", want: true},
		{name: "hash_alone_one_word", pattern: "#", key: "orders", want: true},
		{name: "hash_alone_three_words", pattern: "#", key: "a.b.c", want: true},
		{name: "hash_hash_empty_key", pattern: "#.#", key: "", want: true},
		{name: "hash_hash_two_words", pattern: "#.#", key: "a.b", want: true},
		{name: "star_zero_words", pattern: "*", key: "", want: false},
		{name: "star_one_word", pattern: "*", key: "orders", want: true},
		{name: "star_two_words", pattern: "*", key: "dead.orders", want: false},
		{name: "prefix_star_matches", pattern: "dead.*", key: "dead.orders", want: true},
		{name: "prefix_star_needs_a_word", pattern: "dead.*", key: "dead", want: false},
		{name: "prefix_star_one_word_only", pattern: "dead.*", key: "dead.orders.eu", want: false},
		{name: "prefix_hash_zero_more", pattern: "dead.#", key: "dead", want: true},
		{name: "prefix_hash_many", pattern: "dead.#", key: "dead.orders.eu", want: true},
		{name: "hash_then_literal", pattern: "#.orders", key: "eu.dead.orders", want: true},
		{name: "hash_then_literal_misses", pattern: "#.orders", key: "orders.eu", want: false},
		{name: "star_middle", pattern: "dead.*.eu", key: "dead.orders.eu", want: true},
		{name: "star_matches_empty_word", pattern: "a.*.b", key: "a..b", want: true},
		{name: "empty_words_literal", pattern: "a..b", key: "a..b", want: true},
		{name: "empty_word_is_not_absent", pattern: "a.b", key: "a..b", want: false},
		{name: "trailing_dot_key_is_two_words", pattern: "*", key: "orders.", want: false},
		{name: "trailing_dot_key_star_star", pattern: "*.*", key: "orders.", want: true},
		{name: "leading_dot_key", pattern: "*.orders", key: ".orders", want: true},
		{name: "leading_dot_pattern", pattern: ".orders", key: "orders", want: false},
		{name: "trailing_dot_pattern", pattern: "orders.", key: "orders.", want: true},
		{name: "lone_dot_is_two_empty_words", pattern: "*.*", key: ".", want: true},
		{name: "hash_is_literal_in_key", pattern: "orders", key: "#", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, topicMatches(tc.pattern, tc.key))
		})
	}
}

// TestTopicMatchesIsPolynomialInTheWildcards runs patterns whose '#' words made a backtracking
// matcher exponential; under the default test timeout they finish only if matching is O(p·k).
func TestTopicMatchesIsPolynomialInTheWildcards(t *testing.T) {
	key := strings.TrimSuffix(strings.Repeat("a.", 30), ".")
	miss := strings.TrimSuffix(strings.Repeat("b.", 30), ".")
	hashes := strings.TrimSuffix(strings.Repeat("#.", 12), ".")
	alternating := strings.TrimSuffix(strings.Repeat("#.a.", 12), ".")

	assert.True(t, topicMatches(hashes, key))
	assert.True(t, topicMatches(hashes+".z", key+".z"))
	assert.False(t, topicMatches(hashes+".z", key))
	assert.False(t, topicMatches(alternating, miss))
	assert.True(t, topicMatches(alternating, key))
}
