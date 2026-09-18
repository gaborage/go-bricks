// Package cachekey holds the grammar shared by every layer that builds a cache
// key namespace: the separator, the prefix rules, and the join that assembles a
// prefix from its segments. It lives here rather than in cache because the
// config layer validates a prefix long before a cache exists, and cache already
// imports config.
package cachekey

import (
	"errors"
	"strings"
	"unicode"
)

// Sep separates the segments of a namespaced cache key: the wire key is
// <prefix>:<key>. Fixed, not configurable — it is the separator Redis
// convention already reads as a namespace boundary.
const Sep = ":"

// forbidden are the characters a prefix may not contain. The glob
// metacharacters *?[] would make a prefix match keys it does not own in any
// pattern-taking command an operator runs against the namespace; the braces {}
// would turn the prefix into a Redis Cluster hash tag, pinning every key of the
// service to one slot and defeating the sharding a cluster endpoint exists for.
const forbidden = "*?[]{}"

// Validate reports whether prefix is a usable cache key namespace. The empty
// prefix is valid: it is the documented opt-out from namespacing. Errors are
// reason-only, carrying no field key — each layer addresses them to its own
// (cache.redis.keyprefix, app.name).
func Validate(prefix string) error {
	if prefix == "" {
		return nil
	}

	for _, r := range prefix {
		if unicode.IsSpace(r) {
			return errors.New("must not contain whitespace")
		}
		if strings.ContainsRune(forbidden, r) {
			return errors.New("must not contain the glob or hash-tag characters " + forbidden)
		}
	}

	if strings.HasSuffix(prefix, Sep) {
		return errors.New("must not end with '" + Sep + "': the separator is added between the prefix and the key")
	}

	return nil
}

// Join assembles a prefix from its segments, dropping empty ones so an absent
// segment collapses instead of leaving a dangling separator: Join("", "acme")
// is "acme", not ":acme".
func Join(segments ...string) string {
	kept := make([]string, 0, len(segments))
	for _, s := range segments {
		if s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, Sep)
}
