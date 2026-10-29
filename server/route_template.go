package server

import "github.com/gaborage/go-bricks/internal/pathutil"

// routeTemplate is a route path in the spelling echo's router stores: it starts with '/'.
// Construct one only via newRouteTemplate, so the grammar scanner never sees a template
// without the leading slash echo adds.
type routeTemplate string

// newRouteTemplate normalizes path as echo v5.4.2's normalizePathSlash does: an empty path
// becomes "/" and a missing leading slash is added.
func newRouteTemplate(path string) routeTemplate {
	return routeTemplate(pathutil.EnsureLeadingSlash(path))
}

// routePartKind classifies one part of a route template in echo's grammar.
type routePartKind uint8

const (
	routePartStatic routePartKind = iota
	routePartParam
	routePartWildcard
)

// walkRouteTemplate is this package's one scanner of echo's route grammar (walkRoutePath, echo
// v5.4.2): it calls emit with each part of path, in order. An unescaped ':' starts a parameter
// anywhere in the path, and its text is the ':' and a name running to the next '/', unless an
// inline verb ends it first (paramNameEnd); an escaped "\:" is a literal colon and stays in the
// static text verbatim; the first '*' outside a parameter name is the wildcard, its text is the
// '*' alone, and the walk stops there, so nothing after it is emitted.
func walkRouteTemplate(path routeTemplate, emit func(kind routePartKind, text string)) {
	p := string(path)
	for i := 0; i < len(p); {
		start := i
		switch {
		case isParamStart(p, i):
			i = paramNameEnd(p, i+1)
			emit(routePartParam, p[start:i])
		case p[i] == '*':
			emit(routePartWildcard, p[i:i+1])
			return
		default:
			i++
			for i < len(p) && p[i] != '*' && !isParamStart(p, i) {
				i++
			}
			emit(routePartStatic, p[start:i])
		}
	}
}

// isParamStart reports whether path[i] is a ':' opening a parameter, one no backslash escapes.
func isParamStart(path string, i int) bool {
	return path[i] == ':' && (i == 0 || path[i-1] != '\\')
}

// paramNameEnd returns where echo's walkRoutePath ends the parameter name at path[start]: the
// segment end, or a "\:" opening an inline verb (/:id\:cancel) after a name holding no ':' or '*'.
func paramNameEnd(path string, start int) int {
	plainName := true
	i := start
	for ; i < len(path) && path[i] != '/'; i++ {
		if path[i] == '\\' && plainName && i+1 < len(path) && path[i+1] == ':' && isInlineVerb(path[i+2:]) {
			return i
		}
		if path[i] == ':' || path[i] == '*' {
			plainName = false
		}
	}
	return i
}

// isInlineVerb reports whether rest, the route text after an escaped colon, stays static to
// the end of its segment: no '*' and no unescaped ':'.
func isInlineVerb(rest string) bool {
	for i := 0; i < len(rest) && rest[i] != '/'; i++ {
		if rest[i] == '*' || isParamStart(rest, i) {
			return false
		}
	}
	return true
}
