package messaging

import "strings"

// topicWords splits a topic routing key or binding pattern into words as RabbitMQ does: the empty
// string is zero words, and every '.' separates two words, so "a." and ".a" each carry an empty word.
func topicWords(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

// topicMatches reports whether a topic exchange delivers a message routed with key to a binding with
// pattern: '*' matches exactly one word and '#' zero or more.
func topicMatches(pattern, key string) bool {
	return topicWordsMatch(topicWords(pattern), topicWords(key))
}

// topicWordsMatch walks the pattern one word at a time, keeping which key prefixes the pattern so
// far matches: O(len(pattern)·len(key)), where backtracking over '#' is exponential.
func topicWordsMatch(pattern, key []string) bool {
	reach := make([]bool, len(key)+1)
	reach[0] = true
	for _, word := range pattern {
		next := make([]bool, len(key)+1)
		if word == "#" {
			matched := false
			for j := range next {
				matched = matched || reach[j]
				next[j] = matched
			}
		} else {
			for j := 1; j <= len(key); j++ {
				next[j] = reach[j-1] && (word == "*" || word == key[j-1])
			}
		}
		reach = next
	}
	return reach[len(key)]
}
