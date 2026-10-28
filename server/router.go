package server

import (
	"slices"

	"github.com/labstack/echo/v5"
)

// maskingRouter is echo's DefaultRouter that also learns, for every route it accepts, the
// template's masked rendering, so a sink can print the matched template of any request
// (c.Path()) with each parameter value and the wildcard tail replaced by maskToken. Matching,
// Remove, Routes and Route are the embedded router's alone. Remove forgets nothing: a
// rendering is a pure function of its template and holds no request value, so a stale entry
// stays correct.
type maskingRouter struct {
	*echo.DefaultRouter
	table maskTable
}

var _ echo.Router = (*maskingRouter)(nil)

// newMaskingRouter wraps the router echo.New builds. AllowOverwritingRoute is what echo.New
// sets; without it a middleware-bearing group's catch-all re-registration fails.
func newMaskingRouter() *maskingRouter {
	return &maskingRouter{DefaultRouter: echo.NewRouter(echo.RouterConfig{AllowOverwritingRoute: true})}
}

// newEngine builds an echo engine whose router is a maskingRouter.
func newEngine() *echo.Echo {
	return echo.NewWithConfig(echo.Config{Router: newMaskingRouter()})
}

// Add registers route with the embedded router and learns its masked rendering from echo's own
// parameter count. ri.Path is the caller's spelling while c.Path() is echo's normalized path, so
// the key is normalized to match. The RouteInfo is returned unchanged: echo sizes each context's
// path-value slots from its Parameters.
//
//nolint:gocritic // hugeParam: echo.Router.Add takes the Route by value.
func (r *maskingRouter) Add(route echo.Route) (echo.RouteInfo, error) {
	ri, err := r.DefaultRouter.Add(route)
	if err != nil {
		return ri, err
	}
	key := newRouteTemplate(ri.Path)
	r.table.learn(string(key), maskedTemplate(key, len(ri.Parameters)))
	return ri, nil
}

// maskedTemplate renders path through echo's Reverse with a maskToken for each of its params
// parameters and wildcard. params is echo's count (len(RouteInfo.Parameters)); the
// walkRouteTemplate walk must agree with it and is used only to find where to cut after the
// first wildcard, since Reverse would print the text echo ignores past it. If they disagree the
// grammars have drifted and the template renders as the bare maskToken: coarse, but value-free.
func maskedTemplate(path routeTemplate, params int) string {
	cut, walked := 0, 0
	walkRouteTemplate(path, func(kind routePartKind, text string) {
		cut += len(text)
		if kind != routePartStatic {
			walked++
		}
	})
	if walked != params {
		return maskToken
	}
	values := slices.Repeat([]any{maskToken}, params)
	return echo.RouteInfo{Path: string(path)[:cut]}.Reverse(values...)
}

// maskedPath renders the request's matched template masked, at zero allocations. A context
// without an engine, or on an engine without a maskingRouter, renders as the bare maskToken.
func maskedPath(c *echo.Context) string {
	e := c.Echo()
	if e == nil {
		return maskToken
	}
	r, ok := e.Router().(*maskingRouter)
	if !ok {
		return maskToken
	}
	return r.table.render(c.Path())
}
