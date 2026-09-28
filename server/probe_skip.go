package server

import (
	"cmp"
	"net/http"

	"github.com/labstack/echo/v5"
)

// SkipperFunc decides whether middleware processing is skipped for a request. It receives
// the stdlib *http.Request — all a skip decision needs — so application code never names an
// echo type and the decision never depends on per-request state that may be unpopulated.
//
// A skipper that matches on the path should match the route the ROUTER matched, not the URL:
// r.Pattern carries the matched template (echo's router stamps it), r.URL.Path is the decoded
// path, and an earlier middleware in the chain can rewrite that field outright. See
// CreateProbeSkipper, which is the framework's own answer to this shape.
type SkipperFunc func(r *http.Request) bool

// probeSkipper reports whether a routed request is one of the server's own health/ready
// probes and may therefore bypass a middleware. Framework-internal and echo-native: every
// framework seat that asks the question (OTel, tenant resolution, the ALB
// forwarded-client-cert identity, module global middleware, the access logger) holds an
// echo context, so all of them read the same answer from the same place.
type probeSkipper func(c *echo.Context) bool

// newProbeSkipper binds isProbeRequest to the probe paths this engine serves.
func newProbeSkipper(healthPath, readyPath string) probeSkipper {
	return func(c *echo.Context) bool {
		return isProbeRequest(c, healthPath, readyPath)
	}
}

// isProbeRequest is the single probe-exemption decision in the framework. It asks the router
// what it matched — c.Path() is the matched route template — and requires one of the methods
// the probes answer.
//
// SECURITY: the decision must never be taken from the request URL. r.URL.Path is the DECODED
// path while echo's router matches the escaped form (DefaultRouter.Route), so
// "<base>/%72eady" decodes onto the ready path while routing to a module param or wildcard
// route; and a middleware upstream in the chain may rewrite r.URL.Path outright. Either way a
// URL-keyed answer exempts a request the probe routes never served, and everything keyed on
// this decision — tenant resolution, the forwarded-client-cert identity, OTel, and every
// module global middleware (ADR-036, the documented seat for cross-cutting auth) — would be
// skipped while consumer code ran.
//
// The template identifies the probe HANDLER only because a module cannot end up owning a probe
// path: echo's Add would silently overwrite the handler while keeping the template, and the
// server itself refuses that duplicate — the tracker keeps the first registration (the probes
// register in New, before any module can) and Start refuses to serve with a conflict recorded.
// The method check covers the one case where the template outlives the match, a top-level 405
// (echo keeps the best-match template there).
//
// Reads two fields and compares strings, so the decision allocates nothing — which is what
// keeps the default chain inside its ADR-026 ceiling. c.Path() is a plain field read;
// RouteInfo() would clone the param slice.
func isProbeRequest(c *echo.Context, healthPath, readyPath string) bool {
	return isProbeMethod(c.Request().Method) && matchesProbePath(c.Path(), healthPath, readyPath)
}

// isProbeMethod reports whether method is one the probe routes answer. The set is
// probeMethods (server.go), spelled as a switch because this runs on every request;
// TestProbeSkipperMatchesRegisteredProbeMethods fails in either direction if the two drift.
func isProbeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		return false
	}
}

// matchesProbePath compares a matched route (a template, or a raw path) against the probe
// paths. An empty candidate never matches: no route matched, and an unmatched request is
// never a probe. That also keeps a caller who passes an empty probe path — SetupMiddlewares
// and CreateProbeSkipper both accept one, though New cannot produce one (normalizeRoutePath)
// — from exempting every unmatched request.
func matchesProbePath(path, healthPath, readyPath string) bool {
	return path != "" && (path == healthPath || path == readyPath)
}

// CreateProbeSkipper creates a skipper function for health probe endpoints, for callers that
// hold only an *http.Request — consumer middleware built on the exported SkipperFunc. The
// framework's own seats take the echo-native isProbeRequest instead.
//
// It answers from the same key wherever it can: echo's router stamps the matched template on
// the request (DefaultRouter.Route sets r.Pattern), and no URL rewrite can move that. It
// falls back to the RAW path only when there is no template, which means either that nothing
// matched — never a probe, since the server registers the probe paths — or that the caller is
// not running behind echo's router, where failing the probes would be worse than comparing
// paths.
//
// SECURITY: the fallback must stay on the raw path, never on the decoded r.URL.Path. Echo's
// router matches r.URL.RawPath whenever it is set (default RouterConfig), so
// "<base>/%72eady" routes to a module param/wildcard route while URL.Path decodes to
// "<base>/ready"; a decoded comparison would call that request a probe and bypass the
// middleware while consumer code ran. The method check closes the same mismatch on the method
// axis: registration is per method+path, so a module may legally own a non-probe method on the
// probe path. (Echo's RouterConfig.UseEscapedPathForMatching is inverted relative to its own
// doc comment — setting it true matches the DECODED path. Flipping it makes this skipper
// exempt less than it should, never more.)
//
// Allocates nothing per request.
func CreateProbeSkipper(healthPath, readyPath string) SkipperFunc {
	return func(r *http.Request) bool {
		if !isProbeMethod(r.Method) {
			return false
		}

		if r.Pattern != "" {
			return matchesProbePath(r.Pattern, healthPath, readyPath)
		}

		return matchesProbePath(routerPath(r), healthPath, readyPath)
	}
}

// routerPath returns the path echo's router keyed on: RawPath when it is set, Path otherwise.
// cmp.Or is exactly the router's own selection (DefaultRouter.Route under the default
// RouterConfig), which is what makes it the right key for anything that has to agree with the
// matched route. It is not "the escaped path": net/url leaves RawPath empty whenever the
// canonical escaping equals the spelling the client sent, so the answer differs from the
// decoded path only for a request that actually arrived percent-encoded.
//
// Which is the whole point for GET <base>/%68ealth against a server serving both <base>/health
// and <base>/:id — the router keys on the encoded spelling and matches the param route, while
// URL.Path decodes to <base>/health, a route this request never reached. Reported beside the
// matched route, the decoded form states two things an operator cannot reconcile.
//
// url.URL.EscapedPath() is NOT equivalent and must not be substituted: it re-encodes Path
// when RawPath is unset, and it silently IGNORES a RawPath that fails its own validity check
// even though the router would still have matched on it. Either divergence reports a path the
// router never routed.
//
// One coupling to know about: this mirrors echo's DEFAULT router selection. A consumer that
// installs its own router with RouterConfig.UseEscapedPathForMatching — reachable through
// echo.NewWithConfig, and note the flag is inverted relative to its name, so setting it makes
// the router match the DECODED path — would make this function disagree with what that router
// matched. The framework cannot detect it: the field is unexported and absent from the Router
// interface, so there is nothing to read and nothing to reject. Such a deployment gets a
// url.path that disagrees with its http.route, the defect this helper exists to fix, in the
// opposite direction; the probe exemption stays fail-safe there, per the SECURITY note below.
//
// SECURITY: CreateProbeSkipper above keys its no-template fallback on this function, so the
// selection is load-bearing beyond log readability — decoding, normalizing, lowercasing the
// hex digits or switching to EscapedPath() here reopens GHSA-h4jw-4c64-48mh, where a
// percent-encoded spelling of a probe path was exempted from tenant resolution, the
// forwarded-client-cert identity, OTel and every module global middleware while the router
// served it from a module route. The SECURITY block on CreateProbeSkipper has the full
// argument.
func routerPath(r *http.Request) string {
	return cmp.Or(r.URL.RawPath, r.URL.Path)
}

// skipperFromRequest adapts a consumer-supplied SkipperFunc to the echo-native form the
// framework middlewares take. Public constructors (TenantMiddleware) accept the
// *http.Request form, so the adapter is where the two worlds meet; nil stays nil, meaning
// "never skip".
func skipperFromRequest(skipper SkipperFunc) probeSkipper {
	if skipper == nil {
		return nil
	}
	return func(c *echo.Context) bool { return skipper(c.Request()) }
}
