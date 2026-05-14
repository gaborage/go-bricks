// Package pathutil hosts the URL-path helpers shared by route registration
// and tenant resolution. The functions preserve the exact semantics that
// server/route_registrar.go relied on before extraction, so existing callers
// behave identically.
package pathutil

import "strings"

func EnsureLeadingSlash(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// NormalizePrefix produces the canonical form expected by StripPathPrefix:
// inputs "" and "/" both collapse to "" so an empty prefix never matches a
// real path.
func NormalizePrefix(prefix string) string {
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

// StripPathPrefix returns the part of path that follows prefix when path is
// under prefix (equals it exactly or has "prefix/" as its head), guarding
// against partial-word matches.
//
// When ok is false, the returned string is the original path unchanged, not
// "" — callers can pass through StripPathPrefix without losing input.
func StripPathPrefix(path, prefix string) (string, bool) {
	if prefix == "" {
		return path, false
	}
	if !strings.HasPrefix(path, prefix) {
		return path, false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if remainder == "" {
		return "", true
	}
	if strings.HasPrefix(remainder, "/") {
		return remainder, true
	}
	return path, false
}
