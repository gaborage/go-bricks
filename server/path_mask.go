package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/logger"
)

// sensitiveParamLookup returns the compiled WithSensitiveParams plan of the route registered at
// method + path template. The Server's is routeConflictTracker.lookupSensitivePlan; a nil lookup
// masks nothing.
type sensitiveParamLookup func(method, path string) (sensitivePlan, bool)

// maskSensitivePathParams returns rawPath with every value the matched route declared via
// WithSensitiveParams masked (see maskBySegment). A route without a plan gets rawPath back
// unchanged, allocation-free; a rawPath whose shape does not fit the plan yields the route
// template, so a mismatch never leaks a value.
func maskSensitivePathParams(c *echo.Context, rawPath string, lookup sensitiveParamLookup) string {
	if lookup == nil {
		return rawPath
	}
	route := c.Path()
	plan, ok := lookup(c.Request().Method, route)
	if !ok {
		return rawPath
	}
	if masked, ok := maskBySegment(plan, rawPath); ok {
		return masked
	}
	return route
}

// maskBySegment applies plan to rawPath by segment position, never by searching for a value: each
// plan.maskIdx segment becomes logger.DefaultMaskValue, and with plan.maskTail everything from the
// wildcard index on collapses into one mask segment, hiding the tail's values and depth. It reports
// false when the shapes do not line up: rawPath needs exactly plan.segCount segments on a
// wildcard-free route, and at least plan.wildcardIdx on a wildcard route.
func maskBySegment(plan sensitivePlan, rawPath string) (string, bool) {
	segs := strings.Split(rawPath, "/")
	if (plan.wildcardIdx == -1 && len(segs) != plan.segCount) || len(segs) < plan.wildcardIdx {
		return "", false
	}
	for _, i := range plan.maskIdx {
		segs[i] = logger.DefaultMaskValue
	}
	if plan.maskTail && len(segs) > plan.wildcardIdx {
		segs = append(segs[:plan.wildcardIdx], logger.DefaultMaskValue)
	}
	return strings.Join(segs, "/"), true
}
