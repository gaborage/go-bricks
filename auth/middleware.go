package auth

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/server"
)

// Request and response header names this middleware reads and writes.
const (
	headerAuthorization   = "Authorization"
	headerWWWAuthenticate = "WWW-Authenticate"
	headerRetryAfter      = "Retry-After"
)

// schemeBearer is the only credential scheme read. Cookies and query parameters
// are deliberately not consulted: a credential the browser attaches on its own
// is a CSRF surface, and a credential in a URL lands in access logs.
const schemeBearer = "Bearer"

// attrEndUserID is the OTel semantic-convention attribute for the authenticated
// end user. It is spelled out rather than taken from a semconv package because
// nothing else in this repository imports one, and the attribute is stable.
const attrEndUserID = "enduser.id"

// minRetryAfter floors the Retry-After value. A "0" would invite an immediate
// retry that cannot possibly find a fresher key set.
const minRetryAfter = time.Second

// Response messages. They name the failure, never the credential, the subject or
// the rule that rejected it — the class goes to the DEBUG log, not to the caller.
// The WWW-Authenticate challenge already distinguishes "none presented" from
// "presented and rejected", so the bodies stay deliberately terse.
const (
	msgBearerRequired    = "Authentication required"
	msgBearerRejected    = "Authentication failed"
	msgKeySetUnavailable = "Authentication temporarily unavailable"
)

// challenge holds the response header values the middleware can emit. They
// depend only on configuration, so they are built once per Middleware call
// rather than per request.
type challenge struct {
	// missing is the WWW-Authenticate value for a request that presented no
	// bearer credential: the realm alone, naming the issuer to authenticate with.
	missing string

	// invalid is the WWW-Authenticate value for a credential that was presented
	// and rejected (RFC 6750 error="invalid_token"). It carries no realm and no
	// error_description: a description would be the rejection class, which is
	// framework DEBUG detail, not something to hand an unauthenticated caller.
	invalid string

	// retryAfter is the Retry-After value for a 503, in whole seconds.
	retryAfter string
}

// Middleware returns the HTTP middleware that verifies the request's bearer
// credential and attaches the resulting Principal to the request context, where
// PrincipalFromContext reads it back.
//
// It is attached PER ROUTE GROUP — RouteRegistrar.Group(prefix, auth.Middleware(v))
// or Use — never globally. There is deliberately no path allowlist and no
// GlobalMiddlewareRegisterer path: a route that must stay open (a probe, a
// webhook with its own signature check) is exempted by not attaching the
// middleware to its group, which keeps the exemption visible at the registration
// site instead of buried in a skip list.
//
// It performs identification, not authorization. A request that reaches the
// handler carries a Principal whose credential verified against the configured
// issuer; whether that identity may perform the operation stays the handler's
// decision. Nothing here inspects claims or cross-checks the tenant.
//
// Outcomes:
//
//   - No Authorization header, a non-Bearer scheme, or an empty token — 401 with
//     WWW-Authenticate: Bearer realm="<issuer>".
//   - A credential that failed any verification rule — 401 with
//     WWW-Authenticate: Bearer error="invalid_token".
//   - An unusable issuer key set — 503 with Retry-After. It is a server-side
//     fault, deliberately distinct from the 401s: the caller's credential was
//     never judged.
//
// Every rejection returns a server.IAPIError, so the framework's error handler
// renders the standard envelope, and no rejection calls next: a verification
// failure cannot fall through to the handler.
//
// SECURITY: nothing here logs, renders or records the credential or the "sub"
// claim. A rejection is logged at DEBUG by class only, and the response body
// carries a fixed message. The single exception is the enduser.id span
// attribute, which records Principal.Subject and is off unless
// auth.jwt.telemetry.enduserid is true.
//
// It panics on a nil Verifier: the middleware is built during module Init, so a
// missing verifier is a wiring error that must abort startup rather than fail
// every request at runtime.
func Middleware(v *Verifier) server.MiddlewareFunc {
	if v == nil {
		panic("auth: Middleware requires a non-nil Verifier")
	}

	ch := &challenge{
		missing:    schemeBearer + ` realm="` + sanitizeRealm(v.cfg.Issuer) + `"`,
		invalid:    schemeBearer + ` error="invalid_token"`,
		retryAfter: retryAfterSeconds(v.cfg.JWKS.MinRefreshInterval),
	}

	return func(c server.HandlerContext, next func() error) error {
		ctx := c.RequestContext()
		// The credential is empty for every malformed Authorization shape, which
		// Verify answers with ErrMissingCredential. Routing that case through
		// Verify rather than short-circuiting keeps auth.verification.total's
		// missing_credential observation on the HTTP path too, and still records
		// exactly one observation per request.
		principal, err := v.Verify(ctx, bearerCredential(c.RequestHeader(headerAuthorization)))
		if err != nil {
			return v.deny(c, err, ch)
		}

		c.SetRequestContext(ContextWithPrincipal(ctx, principal))
		if v.cfg.Telemetry.EndUserID {
			trace.SpanFromContext(ctx).SetAttributes(attribute.String(attrEndUserID, principal.Subject))
		}
		return next()
	}
}

// deny sets the response headers for a rejection and returns the server error
// the framework's HTTPErrorHandler renders into the standard envelope. Headers
// are written before returning because server.IAPIError carries no header hook.
func (v *Verifier) deny(c server.HandlerContext, err error, ch *challenge) error {
	v.debugRequestRejected(verificationResult(err))
	header := c.ResponseWriter().Header()

	switch {
	case errors.Is(err, ErrMissingCredential):
		header.Set(headerWWWAuthenticate, ch.missing)
		return server.NewUnauthorizedError(msgBearerRequired)
	case errors.Is(err, ErrKeySetUnavailable):
		header.Set(headerRetryAfter, ch.retryAfter)
		return server.NewServiceUnavailableError(msgKeySetUnavailable)
	default:
		header.Set(headerWWWAuthenticate, ch.invalid)
		return server.NewUnauthorizedError(msgBearerRejected)
	}
}

// debugRequestRejected logs one rejected request at DEBUG, by class only.
//
// SECURITY: class is the verificationResult label — the same closed vocabulary
// the auth.result metric attribute carries — so no part of the credential, and
// no claim value, can reach the log through it.
func (v *Verifier) debugRequestRejected(class string) {
	if v.log == nil {
		return
	}
	v.log.Debug().Str("class", class).Msg("auth: request rejected")
}

// bearerCredential extracts the credential from an Authorization header value,
// returning "" for every shape that presents no bearer credential: an absent
// header, a scheme that is not Bearer, a value with no space after the scheme,
// and an empty or whitespace-only token.
//
// All of those are the MISSING-credential answer rather than a verification
// failure, because nothing was presented to verify; the caller passes the empty
// string to Verify, which reports ErrMissingCredential. The scheme is matched
// case-insensitively per RFC 7235.
func bearerCredential(header string) string {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, schemeBearer) {
		return ""
	}
	return strings.TrimSpace(token)
}

// retryAfterSeconds renders the Retry-After value for a key-set 503.
//
// The source is the resolver's refresh floor (auth.jwt.jwks.minrefreshinterval,
// 30s by default): the JWKS resolver will not issue another fetch to the issuer
// before it elapses, so a retry sooner than that cannot reach a fresher key set
// and only costs the caller a round trip. No new configuration key is
// introduced. The value is floored at one second — a verifier built over a
// pinned PublicKeyResolver leaves the JWKS group zero — and rounded up, because
// Retry-After is expressed in whole seconds.
func retryAfterSeconds(minRefreshInterval time.Duration) string {
	seconds := math.Ceil(max(minRefreshInterval, minRetryAfter).Seconds())
	return strconv.FormatInt(int64(seconds), 10)
}

// sanitizeRealm renders issuer as the body of an HTTP quoted-string.
//
// SECURITY: this is a header-injection seam. The issuer is operator-supplied and
// is only checked for non-emptiness at startup, so a value carrying a quote
// would close the realm parameter and let the rest of the string be read as
// further challenge parameters, while a CR or LF would split the response
// header. Control characters (CR and LF among them) are dropped, and a quote or
// backslash is backslash-escaped per the quoted-pair rule; every other byte,
// space and non-ASCII included, is kept verbatim so a legitimate issuer URL
// renders unchanged.
func sanitizeRealm(issuer string) string {
	var b strings.Builder
	for _, ch := range []byte(issuer) {
		switch {
		case ch < 0x20, ch == 0x7f:
			// Control character: dropped, never emitted in any form.
		case ch == '"', ch == '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}
