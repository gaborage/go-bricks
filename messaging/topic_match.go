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

func topicWordsMatch(pattern, key []string) bool {
	if len(pattern) == 0 {
		return len(key) == 0
	}
	if pattern[0] == "#" {
		for skip := 0; skip <= len(key); skip++ {
			if topicWordsMatch(pattern[1:], key[skip:]) {
				return true
			}
		}
		return false
	}
	if len(key) == 0 {
		return false
	}
	if pattern[0] != "*" && pattern[0] != key[0] {
		return false
	}
	return topicWordsMatch(pattern[1:], key[1:])
}
