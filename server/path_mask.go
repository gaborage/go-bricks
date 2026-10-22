package server

import (
	"slices"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/logger"
)

// sensitiveParamLookup returns the WithSensitiveParams names of the route registered at
// method + path template, or nil. The Server's is routeConflictTracker.sensitiveParams; a nil
// lookup masks nothing.
type sensitiveParamLookup func(method, path string) []string

// maskSensitivePathParams returns rawPath with the value of every path parameter the matched
// route marked via WithSensitiveParams replaced by the logger's mask value. A route without
// sensitive params gets rawPath back unchanged, allocation-free.
//
// The value is masked by segment position against the route pattern, so a value that also occurs
// in a literal segment is never masked in the wrong place, and a percent-encoded value is masked
// whole. A wildcard route falls back to replacing the first literal occurrence of c.Param's value;
// a value spelled differently in rawPath stays unmasked.
func maskSensitivePathParams(c *echo.Context, rawPath string, lookup sensitiveParamLookup) string {
	if lookup == nil {
		return rawPath
	}
	route := c.Path()
	names := lookup(c.Request().Method, route)
	if len(names) == 0 {
		return rawPath
	}
	if masked, ok := maskBySegment(route, rawPath, names); ok {
		return masked
	}
	masked := rawPath
	for _, name := range names {
		if v := c.Param(name); v != "" {
			masked = strings.Replace(masked, v, logger.DefaultMaskValue, 1)
		}
	}
	return masked
}

// maskBySegment masks every rawPath segment whose route-pattern counterpart is a sensitive
// ":name". It reports false when the two do not have the same segment count or the route holds a
// wildcard, since segment positions then say nothing about where a value sits.
func maskBySegment(route, rawPath string, names []string) (string, bool) {
	if strings.Contains(route, "*") {
		return "", false
	}
	routeSegs := strings.Split(route, "/")
	pathSegs := strings.Split(rawPath, "/")
	if len(routeSegs) != len(pathSegs) {
		return "", false
	}
	for i, seg := range routeSegs {
		if name, ok := strings.CutPrefix(seg, ":"); ok && slices.Contains(names, name) {
			pathSegs[i] = logger.DefaultMaskValue
		}
	}
	return strings.Join(pathSegs, "/"), true
}
