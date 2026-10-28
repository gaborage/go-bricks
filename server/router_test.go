package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/racedetect"
)

const parityPAN = "4111111111111111"

// maskedPathSink keeps the measured maskedPath results live.
var maskedPathSink string

// assertMaskedPath serves a PAN-bearing request to /cards/:pan on the engine newE builds and
// asserts maskedPath rendered want; measure also asserts the rendering allocates nothing.
func assertMaskedPath(t *testing.T, newE func() *echo.Echo, want string, measure bool) {
	t.Helper()
	e := newE()
	masked, allocs := "", -1.0
	e.GET("/cards/:pan", func(c *echo.Context) error {
		masked = maskedPath(c)
		if measure {
			allocs = testing.AllocsPerRun(100, func() { maskedPathSink = maskedPath(c) })
		}
		return c.NoContent(http.StatusOK)
	})
	rec := serveEngine(e, http.MethodGet, "/cards/"+parityPAN)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, want, masked)
	if measure {
		assert.Zero(t, allocs)
	}
}

func TestMaskedPathRendersTemplate(t *testing.T) {
	assertMaskedPath(t, newEngine, "/cards/REDACTED", false)
}

func TestMaskedPathAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced by make test-alloc")
	}
	assertMaskedPath(t, newEngine, "/cards/REDACTED", true)
}

func TestMaskedPathNoEngine(t *testing.T) {
	c := echo.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cards/"+parityPAN, http.NoBody), httptest.NewRecorder())
	assert.Equal(t, maskToken, maskedPath(c))
}

func TestMaskedPathForeignEngine(t *testing.T) {
	assertMaskedPath(t, echo.New, maskToken, false)
}

// echoParameters is the independent oracle: the Parameters a plain echo router reports for
// template, the wildcard spelled "*" (echo v5.4.2 router.go:545).
func echoParameters(t *testing.T, template string) []string {
	t.Helper()
	ri, err := echo.NewRouter(echo.RouterConfig{}).Add(echo.Route{Method: http.MethodGet, Path: template, Handler: okEchoHandler})
	require.NoError(t, err)
	return ri.Parameters
}

// Escaped colons print unescaped, and everything after a wildcard is cut.
func TestMaskedTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{name: "static", template: "/healthz", want: "/healthz"},
		{name: "one_param", template: "/cards/:pan", want: "/cards/REDACTED"},
		{name: "two_params", template: "/a/:x/b/:y", want: "/a/REDACTED/b/REDACTED"},
		{name: "mid_segment_param", template: "/files/f:name/raw", want: "/files/fREDACTED/raw"},
		{name: "param_then_wildcard", template: "/files/:id/*", want: "/files/REDACTED/REDACTED"},
		{name: "named_wildcard", template: "/files/*rest", want: "/files/REDACTED"},
		{name: "text_after_wildcard_cut", template: "/static/*rest/ignored/:x", want: "/static/REDACTED"},
		{name: "star_inside_param_name", template: "/odd/:a*b/tail", want: "/odd/REDACTED/tail"},
		{name: "inline_verb", template: `/orders/:id\:cancel`, want: "/orders/REDACTED:cancel"},
		{name: "escaped_colon_static", template: `/a\:b`, want: "/a:b"},
		{name: "trailing_slash", template: "/users/:id/", want: "/users/REDACTED/"},
		{name: "empty_is_root", template: "", want: "/"},
		{name: "bare_wildcard", template: "*", want: "/REDACTED"},
		{name: "missing_slash", template: "cards/:pan", want: "/cards/REDACTED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := newRouteTemplate(tc.template)
			assert.Equal(t, tc.want, maskedTemplate(key, len(echoParameters(t, tc.template))))

			r := newMaskingRouter()
			_, err := r.Add(echo.Route{Method: http.MethodGet, Path: tc.template, Handler: okEchoHandler})
			require.NoError(t, err)
			assert.Equal(t, tc.want, r.table.render(string(key)))
		})
	}
}

// Every corpus template against echo's own Parameters: our walk counts what echo counts (a
// disagreement renders the bare token), no parameter name survives, and a template echo gives
// a wildcard ends at it.
func TestMaskedTemplateSweep(t *testing.T) {
	for _, tpl := range routeTemplateCorpus() {
		params := echoParameters(t, tpl)
		got := maskedTemplate(newRouteTemplate(tpl), len(params))
		assert.NotEqual(t, maskToken, got,
			"template %q fell back to the bare token: walkRouteTemplate no longer counts what echo counts; re-check echo's walkRoutePath after an echo upgrade", tpl)
		assert.NotContains(t, got, "id", "template %q leaked a parameter name", tpl)
		if len(params) > 0 && params[len(params)-1] == "*" {
			assert.True(t, strings.HasSuffix(got, maskToken), "template %q rendered %q past its wildcard", tpl, got)
		}
	}
}

func TestMaskedTemplateCountDisagreementFailsClosed(t *testing.T) {
	assert.Equal(t, maskToken, maskedTemplate(newRouteTemplate("/cards/:pan"), 0))
	assert.Equal(t, maskToken, maskedTemplate(newRouteTemplate("/healthz"), 1))
}

// The baseline is the router echo.New builds, not a copy of newMaskingRouter's config.
func TestMaskingRouterDelegates(t *testing.T) {
	plain := echo.New().Router()
	r := newMaskingRouter()
	for _, path := range []string{"/cards/:pan", "/files/:id/*", "", `/orders/:id\:cancel`} {
		want, err := plain.Add(echo.Route{Method: http.MethodGet, Path: path, Handler: okEchoHandler})
		require.NoError(t, err)
		got, err := r.Add(echo.Route{Method: http.MethodGet, Path: path, Handler: okEchoHandler})
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	assert.Equal(t, plain.Routes(), r.Routes())

	// echo.New lets a route be registered again; so must the wrapped router.
	_, err := r.Add(echo.Route{Method: http.MethodGet, Path: "/cards/:pan", Handler: okEchoHandler})
	require.NoError(t, err, "re-registering a route must overwrite it, as under echo.New")

	_, err = r.Add(echo.Route{Method: http.MethodGet, Path: "/nohandler/:x"})
	require.Error(t, err)
	assert.Equal(t, maskToken, r.table.render("/nohandler/:x"))

	require.NoError(t, r.Remove(http.MethodGet, "/cards/:pan"))
	require.NoError(t, plain.Remove(http.MethodGet, "/cards/:pan"))
	assert.Equal(t, plain.Routes(), r.Routes())
	assert.Equal(t, "/cards/REDACTED", r.table.render("/cards/:pan"), "a removed template keeps its value-free rendering")
}

type routeObservation struct {
	status int
	path   string
	allow  string
	values echo.PathValues
	masked string
}

// registerParityRoutes declares the same routes on any engine. rootCatchAll adds a
// middleware-bearing group with no prefix, whose catch-alls (/ and /*) take every unmatched
// request and every wrong-method request.
func registerParityRoutes(e *echo.Echo, rootCatchAll bool) {
	mw := func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	e.GET("/cards/:pan", okEchoHandler)
	e.DELETE("/cards/:id", okEchoHandler) // the node's last-registered template
	e.GET("/files/:id/*", okEchoHandler)
	e.GET("/static/*rest/ignored", okEchoHandler)
	e.GET("/odd/:a*b", okEchoHandler)
	e.GET("/orders/:id", okEchoHandler)
	e.POST(`/orders/:id\:cancel`, okEchoHandler)
	e.GET("/users/:id/", okEchoHandler)
	e.GET("/healthz", okEchoHandler)
	secure := e.Group("/secure", mw)
	secure.GET("/cards/:pan", okEchoHandler)
	api := e.Group("/api")
	api.GET("/v1/items/:item", okEchoHandler)
	if rootCatchAll {
		e.Group("", mw)
	}
}

func observe(t *testing.T, build func() *echo.Echo, rootCatchAll bool, tc *parityCase) routeObservation {
	t.Helper()
	e := build()
	var obs routeObservation
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			obs.path = c.Path()
			obs.values = slices.Clone(c.PathValues())
			obs.masked = maskedPath(c)
			return next(c)
		}
	})
	registerParityRoutes(e, rootCatchAll)
	req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, http.NoBody)
	for name, vals := range tc.header {
		req.Header[name] = vals
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	obs.status = rec.Code
	obs.allow = rec.Header().Get(echo.HeaderAllow)
	return obs
}

type parityCase struct {
	name       string
	method     string
	target     string
	header     http.Header
	wantStatus int
	wantMasked string
}

// echo.New and newEngine must agree on status, c.Path(), Allow and path values, and every
// non-empty c.Path() must be a learned template.
func TestMaskingRouterParity(t *testing.T) {
	big := strings.Repeat("9", 1<<20)
	card := "/cards/" + parityPAN
	preflight := http.Header{
		"Origin":                        {"https://app.example"},
		"Access-Control-Request-Method": {http.MethodDelete},
	}
	variants := []struct {
		name         string
		rootCatchAll bool
		cases        []parityCase
	}{
		{name: "routes", cases: []parityCase{
			{name: "param_match", method: http.MethodGet, target: card, wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "param_match_other_method", method: http.MethodDelete, target: card, wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "head_on_get_route", method: http.MethodHead, target: card, wantStatus: http.StatusMethodNotAllowed, wantMasked: "/cards/REDACTED"},
			{name: "method_not_allowed", method: http.MethodPost, target: card, wantStatus: http.StatusMethodNotAllowed, wantMasked: "/cards/REDACTED"},
			{name: "options", method: http.MethodOptions, target: card, wantStatus: http.StatusNoContent, wantMasked: "/cards/REDACTED"},
			{name: "cors_preflight", method: http.MethodOptions, target: card, header: preflight, wantStatus: http.StatusNoContent, wantMasked: "/cards/REDACTED"},
			{name: "wildcard_tail", method: http.MethodGet, target: "/files/7/a/" + parityPAN, wantStatus: http.StatusOK, wantMasked: "/files/REDACTED/REDACTED"},
			{name: "wildcard_empty_tail", method: http.MethodGet, target: "/files/7/", wantStatus: http.StatusOK, wantMasked: "/files/REDACTED/REDACTED"},
			{name: "wildcard_method_not_allowed", method: http.MethodPost, target: "/files/7/" + parityPAN, wantStatus: http.StatusMethodNotAllowed, wantMasked: "/files/REDACTED/REDACTED"},
			{name: "wildcard_text_after_star", method: http.MethodGet, target: "/static/" + parityPAN + "/x", wantStatus: http.StatusOK, wantMasked: "/static/REDACTED"},
			{name: "star_inside_param_name", method: http.MethodGet, target: "/odd/" + parityPAN, wantStatus: http.StatusOK, wantMasked: "/odd/REDACTED"},
			{name: "inline_verb", method: http.MethodPost, target: "/orders/" + parityPAN + ":cancel", wantStatus: http.StatusOK, wantMasked: "/orders/REDACTED:cancel"},
			{name: "inline_verb_shape_on_param_route", method: http.MethodGet, target: "/orders/" + parityPAN + ":cancel", wantStatus: http.StatusOK, wantMasked: "/orders/REDACTED"},
			{name: "inline_verb_method_not_allowed", method: http.MethodDelete, target: "/orders/" + parityPAN + ":cancel", wantStatus: http.StatusMethodNotAllowed, wantMasked: "/orders/REDACTED:cancel"},
			{name: "inline_verb_unknown_verb", method: http.MethodPost, target: "/orders/" + parityPAN + ":refund", wantStatus: http.StatusMethodNotAllowed, wantMasked: "/orders/REDACTED"},
			{name: "plain_param_route", method: http.MethodGet, target: "/orders/" + parityPAN, wantStatus: http.StatusOK, wantMasked: "/orders/REDACTED"},
			{name: "trailing_slash_match", method: http.MethodGet, target: "/users/" + parityPAN + "/", wantStatus: http.StatusOK, wantMasked: "/users/REDACTED/"},
			{name: "trailing_slash_missing", method: http.MethodGet, target: "/users/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: maskToken},
			{name: "static_route", method: http.MethodGet, target: "/healthz", wantStatus: http.StatusOK, wantMasked: "/healthz"},
			{name: "static_method_not_allowed", method: http.MethodPost, target: "/healthz", wantStatus: http.StatusMethodNotAllowed, wantMasked: "/healthz"},
			{name: "group_route", method: http.MethodGet, target: "/secure/cards/" + parityPAN, wantStatus: http.StatusOK, wantMasked: "/secure/cards/REDACTED"},
			{name: "group_catch_all_wrong_method", method: http.MethodPost, target: "/secure/cards/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: "/secure/REDACTED"},
			{name: "group_catch_all_unknown_path", method: http.MethodGet, target: "/secure/nope/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: "/secure/REDACTED"},
			{name: "group_catch_all_bare_prefix", method: http.MethodGet, target: "/secure", wantStatus: http.StatusNotFound, wantMasked: "/secure"},
			{name: "base_path_match", method: http.MethodGet, target: "/api/v1/items/" + parityPAN, wantStatus: http.StatusOK, wantMasked: "/api/v1/items/REDACTED"},
			{name: "base_path_method_not_allowed", method: http.MethodPut, target: "/api/v1/items/" + parityPAN, wantStatus: http.StatusMethodNotAllowed, wantMasked: "/api/v1/items/REDACTED"},
			{name: "base_path_unmatched", method: http.MethodGet, target: "/api/v2/items/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: maskToken},
			{name: "root_not_found", method: http.MethodGet, target: "/nope/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: maskToken},
			{name: "root_slash_not_found", method: http.MethodGet, target: "/", wantStatus: http.StatusNotFound, wantMasked: maskToken},
			{name: "percent_encoded_pan", method: http.MethodGet, target: "/cards/%34111111111111111", wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "double_encoded_pan", method: http.MethodGet, target: "/cards/%2534111111111111111", wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "encoded_slash_in_value", method: http.MethodGet, target: "/cards/4111%2F1111", wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "one_mib_value", method: http.MethodGet, target: "/cards/" + big, wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "one_mib_value_method_not_allowed", method: http.MethodPost, target: "/cards/" + big, wantStatus: http.StatusMethodNotAllowed, wantMasked: "/cards/REDACTED"},
		}},
		{name: "root_catch_all", rootCatchAll: true, cases: []parityCase{
			{name: "match", method: http.MethodGet, target: card, wantStatus: http.StatusOK, wantMasked: "/cards/REDACTED"},
			{name: "wrong_method", method: http.MethodPost, target: card, wantStatus: http.StatusNotFound, wantMasked: "/REDACTED"},
			{name: "head", method: http.MethodHead, target: card, wantStatus: http.StatusNotFound, wantMasked: "/REDACTED"},
			{name: "options", method: http.MethodOptions, target: card, wantStatus: http.StatusNotFound, wantMasked: "/REDACTED"},
			{name: "preflight", method: http.MethodOptions, target: card, header: preflight, wantStatus: http.StatusNotFound, wantMasked: "/REDACTED"},
			{name: "unknown_path", method: http.MethodGet, target: "/nope/" + parityPAN, wantStatus: http.StatusNotFound, wantMasked: "/REDACTED"},
			{name: "root", method: http.MethodGet, target: "/", wantStatus: http.StatusNotFound, wantMasked: "/"},
		}},
	}
	for _, v := range variants {
		for _, tc := range v.cases {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				plain := observe(t, echo.New, v.rootCatchAll, &tc)
				masking := observe(t, newEngine, v.rootCatchAll, &tc)

				assert.Equal(t, tc.wantStatus, masking.status)
				assert.Equal(t, plain.status, masking.status, "status")
				assert.Equal(t, plain.path, masking.path, "c.Path()")
				assert.Equal(t, plain.allow, masking.allow, "Allow header")
				assert.Equal(t, plain.values, masking.values, "path values")

				assert.Equal(t, tc.wantMasked, masking.masked)
				// Invariants for every row, including future ones without a hand-checked wantMasked.
				assert.NotContains(t, masking.masked, parityPAN)
				if masking.path == "" {
					assert.Equal(t, maskToken, masking.masked, "an empty template renders as the bare Token")
				} else {
					assert.NotEqual(t, maskToken, masking.masked, "template %q was never learned", masking.path)
				}
			})
		}
	}
}
