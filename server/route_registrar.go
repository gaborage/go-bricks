package server

import (
	"strings"

	"github.com/labstack/echo/v4"
)

type routeGroup struct {
	group  *echo.Group
	prefix string
}

func newRouteGroup(group *echo.Group, prefix string) RouteRegistrar {
	return &routeGroup{
		group:  group,
		prefix: normalizePrefix(prefix),
	}
}

func (rg *routeGroup) Add(method, path string, handler echo.HandlerFunc, middleware ...echo.MiddlewareFunc) *echo.Route {
	normalized := rg.relativePath(path)
	return rg.group.Add(method, normalized, handler, middleware...)
}

func (rg *routeGroup) Group(prefix string, middleware ...echo.MiddlewareFunc) RouteRegistrar {
	normalized := normalizePrefix(prefix)
	newGroup := rg.group.Group(normalized, middleware...)

	return &routeGroup{
		group:  newGroup,
		prefix: rg.combinePrefix(normalized),
	}
}

func (rg *routeGroup) Use(middleware ...echo.MiddlewareFunc) {
	rg.group.Use(middleware...)
}

func (rg *routeGroup) FullPath(path string) string {
	relative := rg.relativePath(path)
	if relative == "" {
		if rg.prefix == "" {
			return "/"
		}
		return rg.prefix
	}

	if rg.prefix == "" {
		return relative
	}

	return rg.prefix + relative
}

func (rg *routeGroup) relativePath(path string) string {
	normalized := ensureLeadingSlash(path)

	if normalized == "/" {
		return ""
	}

	if trimmed, ok := stripPathPrefix(normalized, rg.prefix); ok {
		if trimmed == "" {
			return ""
		}
		return trimmed
	}

	return normalized
}

func (rg *routeGroup) combinePrefix(suffix string) string {
	if suffix == "" {
		return rg.prefix
	}

	if rg.prefix == "" {
		return suffix
	}

	return rg.prefix + suffix
}

func ensureLeadingSlash(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func normalizePrefix(prefix string) string {
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func stripPathPrefix(path, prefix string) (string, bool) {
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
