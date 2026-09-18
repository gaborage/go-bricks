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
//
// A prefix is ONE segment: no Sep anywhere in it, not merely none at the end. A
// prefix that spanned two segments would collide with a caller key that opened
// with the second — "orders" writing "v2:user:1" and "orders:v2" writing
// "user:1" both land on "orders:v2:user:1" — so two services with distinct
// prefixes could still read and overwrite each other's entries. Single-segment,
// the prefix owns the first segment of every key it writes, and a tenant id
// (which cannot carry Sep either) owns the second.
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

	if strings.Contains(prefix, Sep) {
		return errors.New("must not contain '" + Sep +
			"': the prefix is one segment; '" + Sep + "' separates prefix, tenant and key")
	}

	return nil
}

// ValidateNamespace reports whether ns is a usable ASSEMBLED namespace: the value Join
// produced, which is one or more prefix segments. Every segment must be a prefix
// Validate accepts, and none may be empty — "orders::acme" is a base that ended in the
// separator, and letting it through would put the keys in a namespace the operator did
// not write.
//
// This is the door a namespace the FRAMEWORK assembled passes (<prefix>:<tenantID>),
// where Validate is the stricter door a CONFIGURED prefix passes: an operator's prefix
// is one segment, so it can never reach across into another service's namespace, while
// the segment the framework folds in is a tenant id it validated itself.
func ValidateNamespace(ns string) error {
	if ns == "" {
		return nil
	}

	for _, segment := range strings.Split(ns, Sep) {
		if segment == "" {
			return errors.New("must not contain an empty segment between two '" + Sep + "' separators")
		}
		if err := Validate(segment); err != nil {
			return err
		}
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
