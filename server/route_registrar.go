package server

import (
	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/internal/pathutil"
)

type routeGroup struct {
	group  *echo.Group
	prefix string
}

func newRouteGroup(group *echo.Group, prefix string) RouteRegistrar {
	return &routeGroup{
		group:  group,
		prefix: pathutil.NormalizePrefix(prefix),
	}
}

func (rg *routeGroup) Add(method, path string, handler echo.HandlerFunc, middleware ...echo.MiddlewareFunc) echo.RouteInfo {
	normalized := rg.relativePath(path)
	return rg.group.Add(method, normalized, handler, middleware...)
}

func (rg *routeGroup) Group(prefix string, middleware ...echo.MiddlewareFunc) RouteRegistrar {
	normalized := pathutil.NormalizePrefix(prefix)
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
	normalized := pathutil.EnsureLeadingSlash(path)

	if normalized == "/" {
		return ""
	}

	if trimmed, ok := pathutil.StripPathPrefix(normalized, rg.prefix); ok {
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
