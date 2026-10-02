package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	probeSkipBase        = "/api"
	probeSkipHealth      = probeSkipBase + "/health"
	probeSkipReady       = probeSkipBase + "/ready"
	probeSkipEncodedHlth = probeSkipBase + "/%68ealth"
	probeSkipEncodedRdy  = probeSkipBase + "/%72eady"
	probeSkipModuleRoute = probeSkipBase + "/:id"
)

// routedContext builds an echo context carrying the route template the router would have
// stamped for url, which is what the probe decision reads. template is set independently of
// url on purpose: the divergence between the two is the whole subject of these tests.
func routedContext(t *testing.T, method, url, template string) *echo.Context {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, url, http.NoBody)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	c.SetPath(template)
	return c
}

// assertServeCode drives one request through an engine and asserts the status.
func assertServeCode(t *testing.T, e *echo.Echo, method, path string, wantCode int, msgAndArgs ...any) {
	t.Helper()
	assert.Equal(t, wantCode, serveEngine(e, method, path).Code, msgAndArgs...)
}

// TestProbeSkipperMatchesRegisteredProbeMethods is the drift guard for isProbeMethod's
// hardcoded switch: every method in probeMethods (what registerProbeRoutes actually serves)
// must be exempt at BOTH doors, and every other standard method must not. Adding a method to
// probeMethods without extending the switch fails the first loop; removing one fails the
// second.
func TestProbeSkipperMatchesRegisteredProbeMethods(t *testing.T) {
	internal := newProbeSkipper(probeSkipHealth, probeSkipReady)
	exported := CreateProbeSkipper(probeSkipHealth, probeSkipReady)
	allMethods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
	}

	for _, method := range probeMethods {
		c := routedContext(t, method, probeSkipReady, probeSkipReady)
		assert.True(t, internal(c), "%s is a method the probes answer", method)
		assert.True(t, exported(c.Request()), "%s is a method the probes answer (exported door)", method)
	}
	for _, method := range allMethods {
		if slices.Contains(probeMethods, method) {
			continue
		}
		c := routedContext(t, method, probeSkipReady, probeSkipReady)
		assert.False(t, internal(c), "%s is not a method the probes answer, so it keeps its middleware", method)
		assert.False(t, exported(c.Request()), "%s keeps its middleware at the exported door too", method)
	}
}

// TestIsProbeRequestKeyedOnMatchedTemplate pins the decision to the route the router matched.
// The url column shows what a URL-keyed decision would have seen: a percent-encoded spelling
// that decodes onto a probe path, and a URL rewritten by an upstream middleware, both route
// elsewhere and must keep their middleware.
func TestIsProbeRequestKeyedOnMatchedTemplate(t *testing.T) {
	cases := []struct {
		name                  string
		method, url, template string
		healthPath, readyPath string
		want                  bool
	}{
		{
			name: "probe_route", method: http.MethodGet, url: probeSkipHealth, template: probeSkipHealth,
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: true,
		},
		{
			name: "ready_route", method: http.MethodHead, url: probeSkipReady, template: probeSkipReady,
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: true,
		},
		{
			name: "encoded_spelling_routes_to_module", method: http.MethodGet, url: probeSkipEncodedRdy, template: probeSkipModuleRoute,
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: false,
		},
		{
			name: "url_rewritten_to_probe_path", method: http.MethodGet, url: probeSkipHealth, template: probeSkipModuleRoute,
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: false,
		},
		{
			name: "unmatched_route_is_never_a_probe", method: http.MethodGet, url: probeSkipHealth, template: "",
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: false,
		},
		{
			name: "non_probe_method_on_probe_route", method: http.MethodPost, url: probeSkipReady, template: probeSkipReady,
			healthPath: probeSkipHealth, readyPath: probeSkipReady, want: false,
		},
		// SetupMiddlewares and CreateProbeSkipper both accept an empty probe path; it must never
		// turn an unmatched request (empty template) into a probe.
		{
			name: "empty_configured_paths_exempt_nothing", method: http.MethodGet, url: probeSkipHealth, template: "",
			healthPath: "", readyPath: "", want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := routedContext(t, tc.method, tc.url, tc.template)
			assert.Equal(t, tc.want, isProbeRequest(c, tc.healthPath, tc.readyPath))
		})
	}
}

// TestCreateProbeSkipperKeyedOnPatternThenRawPath covers the exported, request-only form used
// by consumer middleware. Echo stamps the matched template on the request (r.Pattern), so that
// door answers from the same key as the framework's; only when there is no template does it
// compare the RAW path, which keeps a percent-encoded spelling from passing as a probe.
func TestCreateProbeSkipperKeyedOnPatternThenRawPath(t *testing.T) {
	skipper := CreateProbeSkipper(probeSkipHealth, probeSkipReady)

	cases := []struct {
		name, method, target, pattern string
		want                          bool
	}{
		{name: "probe_template", method: http.MethodGet, target: probeSkipHealth, pattern: probeSkipHealth, want: true},
		{name: "probe_template_head", method: http.MethodHead, target: probeSkipReady, pattern: probeSkipReady, want: true},
		{name: "module_template_with_probe_url", method: http.MethodGet, target: probeSkipHealth, pattern: probeSkipModuleRoute, want: false},
		{name: "module_template_with_encoded_url", method: http.MethodGet, target: probeSkipEncodedRdy, pattern: probeSkipModuleRoute, want: false},
		{name: "non_probe_method_on_probe_template", method: http.MethodPost, target: probeSkipReady, pattern: probeSkipReady, want: false},
		// No template: the caller is off echo's router, or nothing matched. The raw path decides.
		{name: "no_template_probe_path", method: http.MethodGet, target: probeSkipReady, want: true},
		{name: "no_template_encoded_path", method: http.MethodGet, target: probeSkipEncodedRdy, want: false},
		{name: "no_template_module_path", method: http.MethodGet, target: probeSkipBase + "/users", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, http.NoBody)
			req.Pattern = tc.pattern
			if tc.target == probeSkipEncodedRdy {
				require.Equal(t, tc.target, req.URL.RawPath, "precondition: the raw path keeps the encoding the router matches")
				require.NotEqual(t, tc.target, req.URL.Path, "precondition: the decoded path differs, which is the trap")
			} else {
				require.Empty(t, req.URL.RawPath, "precondition: an unencoded target leaves RawPath empty, so the fallback reads Path")
			}
			assert.Equal(t, tc.want, skipper(req))
		})
	}
}

// TestWithRouteTemplateStampsRequestPattern pins that the test constructor seeds BOTH places
// the router writes on a live request. Without the r.Pattern stamp a test-built context would
// present a template to RouteTemplate() and none to CreateProbeSkipper, so test plumbing would
// disagree with production.
func TestWithRouteTemplateStampsRequestPattern(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	skipper := CreateProbeSkipper(probeSkipHealth, probeSkipReady)

	probe := NewHandlerContextForTest(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, probeSkipHealth, http.NoBody),
		cfg, WithRouteTemplate(probeSkipHealth))
	assert.Equal(t, probeSkipHealth, probe.RouteTemplate())
	assert.True(t, skipper(probe.Request()), "the stamped template must reach the request-only door")

	module := NewHandlerContextForTest(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, probeSkipHealth, http.NoBody),
		cfg, WithRouteTemplate(probeSkipModuleRoute))
	assert.False(t, skipper(module.Request()),
		"a module template with a probe-looking URL must not be exempt at the request-only door")
}

// TestCreateProbeSkipperReadsPatternStampedByTheRouter pins the premise the door rests on:
// echo's router puts the matched template on the request, for every shape, so a URL rewritten
// downstream of routing cannot make a module route look like a probe.
func TestCreateProbeSkipperReadsPatternStampedByTheRouter(t *testing.T) {
	e := echo.New()
	seen := map[string]bool{}
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			r := c.Request()
			require.Equal(t, c.Path(), r.Pattern, "the router stamps the same template on context and request")
			r.URL.Path = probeSkipHealth // an upstream middleware rewriting the URL
			seen[r.URL.RawPath] = CreateProbeSkipper(probeSkipHealth, probeSkipReady)(r)
			return next(c)
		}
	})
	e.GET(probeSkipModuleRoute, okEchoHandler)
	e.GET(probeSkipHealth, okEchoHandler)

	serveEngine(e, http.MethodGet, probeSkipEncodedRdy)
	serveEngine(e, http.MethodGet, probeSkipHealth)

	assert.False(t, seen[probeSkipEncodedRdy], "a module route whose URL was rewritten to the probe path is not a probe")
	assert.True(t, seen[""], "the genuine probe route is still a probe")
}

// TestRouterPath pins the helper to the router's own selection and, with the last two cases,
// to the reason url.EscapedPath() cannot stand in for it: EscapedPath re-encodes Path when
// RawPath is unset, and ignores a RawPath that fails its validity check even though echo's
// router would still have matched on it.
func TestRouterPath(t *testing.T) {
	tests := []struct {
		name          string
		path, rawPath string
		want          string
		// escapedPathDiverges marks the cases that pin the non-equivalence: EscapedPath()
		// answers something other than the path the router matched on.
		escapedPathDiverges bool
	}{
		{name: "raw_path_set_wins", path: probeSkipHealth, rawPath: probeSkipEncodedHlth, want: probeSkipEncodedHlth},
		{name: "no_raw_path_falls_back_to_path", path: probeSkipHealth, want: probeSkipHealth},
		// EscapedPath() would return "/api/a%20b" here; the router matched the literal bytes.
		{
			name: "path_needing_encoding_is_not_re_encoded", path: probeSkipBase + "/a b",
			want: probeSkipBase + "/a b", escapedPathDiverges: true,
		},
		// EscapedPath() discards this RawPath (it does not decode to Path); the router does not.
		{
			name: "invalid_raw_path_is_still_the_matched_path", path: probeSkipHealth,
			rawPath: probeSkipReady, want: probeSkipReady, escapedPathDiverges: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Placeholder target, then assign: NewRequest panics on a target carrying a space,
			// and no target can express a RawPath independently of Path.
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, probeSkipBase, http.NoBody)
			r.URL.Path = tc.path
			r.URL.RawPath = tc.rawPath

			assert.Equal(t, tc.want, routerPath(r))
			if tc.escapedPathDiverges {
				assert.NotEqual(t, r.URL.EscapedPath(), routerPath(r),
					"precondition: EscapedPath() diverges here, which is why it must not be substituted")
			}
		})
	}
}
