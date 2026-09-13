package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

const (
	middlewareSubject = "middleware-secret-subject"
	testPath          = "/api/thing"
	openPath          = "/open"
	healthPath        = "/health"
	readyPath         = "/ready"
)

// middlewareRun is what one middleware invocation observed: the recorder it
// wrote headers to, the error it returned, and whether the next handler ran and
// what identity it saw.
type middlewareRun struct {
	rec          *httptest.ResponseRecorder
	err          error
	handlerRan   bool
	principal    Principal
	hasPrincipal bool
}

// newAuthRequest builds a request carrying the given Authorization header value.
// setHeader distinguishes an absent header from a present but empty one.
func newAuthRequest(ctx context.Context, authorization string, setHeader bool) *http.Request {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, testPath, http.NoBody)
	if setHeader {
		req.Header.Set(headerAuthorization, authorization)
	}
	return req
}

// runMiddleware drives one request through Middleware(v) over a synthetic
// HandlerContext and reports what happened.
func runMiddleware(t *testing.T, v *Verifier, req *http.Request) *middlewareRun {
	t.Helper()
	rec := httptest.NewRecorder()
	c := server.NewHandlerContextForTest(rec, req, &config.Config{})
	run := &middlewareRun{rec: rec}
	run.err = Middleware(v)(c, func() error {
		run.handlerRan = true
		run.principal, run.hasPrincipal = PrincipalFromContext(c.RequestContext())
		return nil
	})
	return run
}

// runWithCredential is the common case: a GET carrying "Bearer <credential>".
func runWithCredential(t *testing.T, v *Verifier, credential string) *middlewareRun {
	t.Helper()
	return runMiddleware(t, v, newAuthRequest(context.Background(), schemeBearer+" "+credential, true))
}

// assertAPIError pins the status and envelope code the framework will render
// from the returned error.
func assertAPIError(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	require.Error(t, err)
	var apiErr server.IAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, wantStatus, apiErr.HTTPStatus())
	assert.Equal(t, wantCode, apiErr.ErrorCode())
}

// assertRejected asserts the handler never ran and no identity was attached.
func assertRejected(t *testing.T, run *middlewareRun) {
	t.Helper()
	assert.False(t, run.handlerRan, "a rejected request must not reach the handler")
	assert.False(t, run.hasPrincipal, "a rejected request must not attach a principal")
}

func TestMiddlewarePanicsOnANilVerifier(t *testing.T) {
	assert.PanicsWithValue(t, "auth: Middleware requires a non-nil Verifier", func() {
		Middleware(nil)
	})
}

// TestMiddlewareChallengesARequestWithoutACredential covers every Authorization
// shape that presents no bearer credential. All of them are the
// missing-credential answer — a realm challenge — never a verification failure.
func TestMiddlewareChallengesARequestWithoutACredential(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	wantChallenge := schemeBearer + ` realm="` + iss.IssuerURL() + `"`

	tests := []struct {
		name          string
		authorization string
		setHeader     bool
	}{
		{name: "absent_header", setHeader: false},
		{name: "empty_header", authorization: "", setHeader: true},
		{name: "scheme_without_a_space", authorization: "Bearer", setHeader: true},
		{name: "empty_token", authorization: "Bearer ", setHeader: true},
		{name: "whitespace_only_token", authorization: "Bearer    ", setHeader: true},
		{name: "wrong_scheme", authorization: "Basic dXNlcjpwYXNz", setHeader: true},
		{name: "scheme_prefix_only", authorization: "BearerToken abc", setHeader: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := runMiddleware(t, v, newAuthRequest(context.Background(), tc.authorization, tc.setHeader))

			assertAPIError(t, run.err, http.StatusUnauthorized, "UNAUTHORIZED")
			assert.Equal(t, wantChallenge, run.rec.Header().Get(headerWWWAuthenticate))
			assert.Empty(t, run.rec.Header().Get(headerRetryAfter))
			assertRejected(t, run)
		})
	}
}

// TestMiddlewareMatchesTheBearerSchemeCaseInsensitively pins RFC 7235: the
// scheme token is case-insensitive, so a lowercase or shouted scheme still
// carries a credential.
func TestMiddlewareMatchesTheBearerSchemeCaseInsensitively(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{IssuedAt: verifierNow, ExpiresAt: verifierNow.Add(time.Hour)})

	tests := []struct {
		name   string
		scheme string
	}{
		{name: "canonical_case", scheme: "Bearer"},
		{name: "lower_case", scheme: "bearer"},
		{name: "upper_case", scheme: "BEARER"},
		{name: "mixed_case", scheme: "BeArEr"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := runMiddleware(t, v, newAuthRequest(context.Background(), tc.scheme+" "+credential, true))

			require.NoError(t, run.err)
			assert.True(t, run.handlerRan, "a valid credential must reach the handler")
		})
	}
}

// TestMiddlewareRejectsAnInvalidCredential covers the verification-failure arm:
// any rule, including an unknown kid, answers 401 with error="invalid_token".
func TestMiddlewareRejectsAnInvalidCredential(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	tests := []struct {
		name       string
		credential string
	}{
		{name: "expired", credential: iss.MintExpired()},
		{name: "wrong_audience", credential: iss.MintWrongAudience()},
		{name: "wrong_issuer", credential: iss.MintWrongIssuer()},
		{name: "unknown_kid", credential: iss.MintUnknownKeyID()},
		{name: "bad_signature", credential: iss.MintBadSignature()},
		{name: "alg_none", credential: iss.MintAlgNone()},
		{name: "not_a_jwt", credential: "not-a-credential"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := runWithCredential(t, v, tc.credential)

			assertAPIError(t, run.err, http.StatusUnauthorized, "UNAUTHORIZED")
			assert.Equal(t, schemeBearer+` error="invalid_token"`, run.rec.Header().Get(headerWWWAuthenticate))
			assert.Empty(t, run.rec.Header().Get(headerRetryAfter))
			assertRejected(t, run)
		})
	}
}

// TestMiddlewareAnswers503WhenTheKeySetIsUnavailable pins the server-fault arm:
// an unusable key set is not a 401, and it carries Retry-After rather than a
// WWW-Authenticate challenge.
func TestMiddlewareAnswers503WhenTheKeySetIsUnavailable(t *testing.T) {
	iss := newTestIssuer()
	v, err := NewVerifierWithResolver(verifierConfig(iss), nil, NewStaticKeyResolver(nil))
	require.NoError(t, err)
	v.now = fixedClock

	run := runWithCredential(t, v, iss.Mint(authtesting.Claims{IssuedAt: verifierNow, ExpiresAt: verifierNow.Add(time.Hour)}))

	assertAPIError(t, run.err, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	// This verifier owns no JWKS resolver, so the configured refresh floor is not
	// a retry hint: nothing is going to refetch. It advertises the minimum.
	assert.Equal(t, "1", run.rec.Header().Get(headerRetryAfter))
	assert.Empty(t, run.rec.Header().Get(headerWWWAuthenticate), "a server fault must not challenge the caller")
	assertRejected(t, run)
}

// TestMiddlewareRetryAfterFollowsTheRefreshFloor pins the Retry-After source —
// auth.jwt.jwks.minrefreshinterval — including the one-second floor at exactly
// the boundary and the round-up of a sub-second remainder.
func TestMiddlewareRetryAfterFollowsTheRefreshFloor(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		want     string
	}{
		{name: "zero_floors_to_one_second", interval: 0, want: "1"},
		{name: "exactly_one_second", interval: time.Second, want: "1"},
		{name: "below_the_floor", interval: 900 * time.Millisecond, want: "1"},
		{name: "just_above_the_floor", interval: 1100 * time.Millisecond, want: "2"},
		{name: "default_refresh_floor", interval: 30 * time.Second, want: "30"},
		{name: "fractional_rounds_up", interval: 90500 * time.Millisecond, want: "91"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, retryAfterSeconds(tc.interval))
		})
	}
}

// TestMiddlewareRetryAfterIgnoresTheFloorWithoutAnOwnedResolver pins that the
// configured refresh floor reaches the header only when a JWKS resolver is
// actually going to act on it. Over a pinned resolver nothing refetches, so
// advertising the floor would park the caller waiting for a fetch that cannot
// happen.
func TestMiddlewareRetryAfterIgnoresTheFloorWithoutAnOwnedResolver(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	cfg.JWKS.MinRefreshInterval = 5 * time.Second
	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(nil))
	require.NoError(t, err)
	v.now = fixedClock

	run := runWithCredential(t, v, iss.Mint(authtesting.Claims{IssuedAt: verifierNow, ExpiresAt: verifierNow.Add(time.Hour)}))

	assert.Equal(t, "1", run.rec.Header().Get(headerRetryAfter))
}

// TestMiddlewareRetryAfterUsesTheFloorOfAnOwnedResolver is the other half: a
// verifier that built its own JWKS resolver does advertise the configured floor,
// because that resolver really will not refetch before it elapses.
func TestMiddlewareRetryAfterUsesTheFloorOfAnOwnedResolver(t *testing.T) {
	srv := newJWKSFixture(t)
	cfg := jwksConfig(srv)
	cfg.JWKS.MinRefreshInterval = 5 * time.Second

	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))
	require.NoError(t, err)
	t.Cleanup(func() { _ = v.Close() })
	require.NotNil(t, v.owned, "NewVerifier must own the resolver it built")

	credential := srv.Issuer().Mint(authtesting.Claims{})
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	srv.SetMode(authtesting.JWKSServerError)
	clock.Advance(testStaleCeiling + time.Second)

	run := runWithCredential(t, v, credential)

	assertAPIError(t, run.err, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	assert.Equal(t, "5", run.rec.Header().Get(headerRetryAfter))
}

// TestMiddlewareAttachesTheVerifiedPrincipal pins the success path: the handler
// runs and reads back the subject and the claims the credential carried.
func TestMiddlewareAttachesTheVerifiedPrincipal(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{
		Subject:   middlewareSubject,
		IssuedAt:  verifierNow,
		ExpiresAt: verifierNow.Add(time.Hour),
		Extra:     map[string]any{"scope": "cards:read"},
	})

	run := runWithCredential(t, v, credential)

	require.NoError(t, run.err)
	require.True(t, run.handlerRan, "a valid credential must reach the handler")
	require.True(t, run.hasPrincipal, "a verified request must carry a principal")
	assert.Equal(t, middlewareSubject, run.principal.Subject)
	assert.Equal(t, iss.IssuerURL(), run.principal.Issuer)
	scope, ok := run.principal.Claim("scope")
	require.True(t, ok, "the raw claims must survive onto the context")
	assert.Equal(t, "cards:read", scope)
	assert.Empty(t, run.rec.Header().Get(headerWWWAuthenticate))
}

// TestSanitizeRealmEscapesTheQuotedStringBody pins the header-injection guard:
// a quote or backslash is escaped and a control character is dropped, while
// every legitimate byte — spaces and non-ASCII included — survives.
func TestSanitizeRealmEscapesTheQuotedStringBody(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		want   string
	}{
		{name: "plain_issuer_url", issuer: "https://issuer.example.com/realm", want: "https://issuer.example.com/realm"},
		{name: "space_is_kept", issuer: "acme corp issuer", want: "acme corp issuer"},
		{name: "non_ascii_is_kept", issuer: "https://issuer.exämple.com", want: "https://issuer.exämple.com"},
		{name: "quote_is_escaped", issuer: `a"b`, want: `a\"b`},
		{name: "backslash_is_escaped", issuer: `a\b`, want: `a\\b`},
		{name: "carriage_return_is_dropped", issuer: "a\rb", want: "ab"},
		{name: "line_feed_is_dropped", issuer: "a\nb", want: "ab"},
		{name: "tab_is_dropped", issuer: "a\tb", want: "ab"},
		{name: "null_is_dropped", issuer: "a\x00b", want: "ab"},
		{name: "delete_is_dropped", issuer: "a\x7fb", want: "ab"},
		{name: "header_injection_attempt", issuer: "x\r\nSet-Cookie: a=b", want: "xSet-Cookie: a=b"},
		{name: "challenge_breakout_attempt", issuer: `x", error="none`, want: `x\", error=\"none`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeRealm(tc.issuer))
		})
	}
}

// TestMiddlewareRealmCannotBreakOutOfTheHeaderValue drives the sanitized realm
// through a real response: a hostile issuer neither splits the header nor adds a
// challenge parameter of its own.
func TestMiddlewareRealmCannotBreakOutOfTheHeaderValue(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, func(cfg *Config) {
		cfg.Issuer = "https://issuer.example.com\r\nX-Injected: yes\", error=\"none"
	})

	run := runMiddleware(t, v, newAuthRequest(context.Background(), "", false))

	got := run.rec.Header().Get(headerWWWAuthenticate)
	assert.Equal(t, `Bearer realm="https://issuer.example.comX-Injected: yes\", error=\"none"`, got)
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\n")
	assert.Empty(t, run.rec.Header().Get("X-Injected"), "the issuer must not be able to inject a header")
}

// TestMiddlewareEmitsExactlyOneRejectionBreadcrumb pins the single-breadcrumb
// rule: the verifier's class-only DEBUG line is the whole log record of a
// rejection, and the middleware adds none of its own.
func TestMiddlewareEmitsExactlyOneRejectionBreadcrumb(t *testing.T) {
	iss := newTestIssuer()
	expired := iss.MintExpired()

	tests := []struct {
		name          string
		authorization string
		setHeader     bool
		wantLines     int
	}{
		{name: "rejected_credential", authorization: schemeBearer + " " + expired, setHeader: true, wantLines: 1},
		// Nothing was presented to classify, so Verify answers the sentinel
		// without going through reject: the outcome lives on the
		// auth.verification.total counter, not in the log.
		{name: "missing_credential", setHeader: false, wantLines: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
				require.NoError(t, err)
				v.now = fixedClock
				runMiddleware(t, v, newAuthRequest(context.Background(), tc.authorization, tc.setHeader))
			})

			assert.Equal(t, tc.wantLines, strings.Count(out, "auth: credential rejected"))
			assert.NotContains(t, out, "auth: request rejected", "the middleware must add no breadcrumb of its own")
		})
	}
}

// TestMiddlewareLogsTheRejectionClassOnly proves the DEBUG rejection line
// carries the class and nothing from the credential.
func TestMiddlewareLogsTheRejectionClassOnly(t *testing.T) {
	iss := newTestIssuer()
	credential := iss.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
		Subject:   middlewareSubject,
		IssuedAt:  verifierNow.Add(-2 * time.Hour),
		ExpiresAt: verifierNow.Add(-time.Hour),
	}})

	var run *middlewareRun
	out := captureStdout(t, func() {
		v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
		require.NoError(t, err)
		v.now = fixedClock
		run = runWithCredential(t, v, credential)
	})

	assertAPIError(t, run.err, http.StatusUnauthorized, "UNAUTHORIZED")
	assert.Contains(t, out, string(ClassExpired))
	assert.Contains(t, out, "auth: credential rejected")
	assert.NotContains(t, out, middlewareSubject)
	for i, segment := range strings.Split(credential, ".") {
		assert.NotContainsf(t, out, segment, "log output leaked credential segment %d", i)
	}
}

// TestMiddlewareRejectionSurvivesANilLogger guards the no-op logging arm: a
// verifier built without a logger still rejects rather than panicking.
func TestMiddlewareRejectionSurvivesANilLogger(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	run := runMiddleware(t, v, newAuthRequest(context.Background(), "", false))

	assertAPIError(t, run.err, http.StatusUnauthorized, "UNAUTHORIZED")
}

// newRecordedSpanContext starts a span on an in-memory exporter and returns the
// context carrying it plus the finished-span accessor.
func newRecordedSpanContext(t *testing.T) (ctx context.Context, finish func() []sdktrace.ReadOnlySpan) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	ctx, span := tp.Tracer("auth-middleware-test").Start(context.Background(), "request")
	// No tp.Shutdown: InMemoryExporter.Shutdown resets the recorded spans, and
	// WithSyncer exports on End, so there is nothing left to flush.
	return ctx, func() []sdktrace.ReadOnlySpan {
		span.End()
		return exporter.GetSpans().Snapshots()
	}
}

// spanAttribute returns the value of key on the single recorded span.
func spanAttribute(t *testing.T, spans []sdktrace.ReadOnlySpan, key string) (value string, found bool) {
	t.Helper()
	require.Len(t, spans, 1)
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

// TestMiddlewareRecordsEndUserIDWhenEnabled pins the opt-in telemetry arm.
func TestMiddlewareRecordsEndUserIDWhenEnabled(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, func(cfg *Config) { cfg.Telemetry.EndUserID = true })
	credential := iss.Mint(authtesting.Claims{
		Subject:   middlewareSubject,
		IssuedAt:  verifierNow,
		ExpiresAt: verifierNow.Add(time.Hour),
	})

	ctx, finish := newRecordedSpanContext(t)
	run := runMiddleware(t, v, newAuthRequest(ctx, schemeBearer+" "+credential, true))
	spans := finish()

	require.NoError(t, run.err)
	require.True(t, run.handlerRan)
	value, found := spanAttribute(t, spans, attrEndUserID)
	require.True(t, found, "enduser.id must be recorded when auth.jwt.telemetry.enduserid is true")
	assert.Equal(t, middlewareSubject, value)
}

// TestMiddlewareOmitsEndUserIDByDefault pins the default: the subject reaches
// neither the span nor any log line.
func TestMiddlewareOmitsEndUserIDByDefault(t *testing.T) {
	iss := newTestIssuer()
	credential := iss.Mint(authtesting.Claims{
		Subject:   middlewareSubject,
		IssuedAt:  verifierNow,
		ExpiresAt: verifierNow.Add(time.Hour),
	})

	ctx, finish := newRecordedSpanContext(t)
	var run *middlewareRun
	out := captureStdout(t, func() {
		v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
		require.NoError(t, err)
		v.now = fixedClock
		require.False(t, v.cfg.Telemetry.EndUserID, "enduser.id must be off unless configured on")
		run = runMiddleware(t, v, newAuthRequest(ctx, schemeBearer+" "+credential, true))
	})
	spans := finish()

	require.NoError(t, run.err)
	require.True(t, run.handlerRan)
	_, found := spanAttribute(t, spans, attrEndUserID)
	assert.False(t, found, "enduser.id must not be recorded by default")
	assert.NotContains(t, out, middlewareSubject, "the subject must never reach a log line")
	assert.NotContains(t, run.rec.Body.String(), middlewareSubject)
	for i, segment := range strings.Split(credential, ".") {
		assert.NotContainsf(t, out, segment, "log output leaked credential segment %d", i)
	}
}

// denyRow is one row of the failure matrix wiki/auth.md publishes: the rejection
// a Verify outcome carries, and the HTTP answer the middleware must produce for
// it. constName is the Go identifier of the Class constant, so the completeness
// check below can name a class nobody added a row for.
type denyRow struct {
	name       string
	constName  string
	err        error
	wantStatus int
	wantCode   string
	wantAuth   string
	wantRetry  string
}

// denyMatrix enumerates every rejection the middleware can answer, one row per
// exported Class constant plus the two sentinels outside the Class vocabulary.
//
// It is written out BY HAND on purpose: deriving it from the Class constants
// would make it agree with the code by construction and assert nothing. Adding a
// Class without adding a row here fails
// TestDenyCoversEveryDeclaredClassConstant, so the wiki's failure matrix is a
// transcription of this table rather than prose nobody enforces.
func denyMatrix() []denyRow {
	const (
		unauthorized = "UNAUTHORIZED"
		unavailable  = "SERVICE_UNAVAILABLE"
	)
	invalid := func(name, constName string, class Class) denyRow {
		return denyRow{
			name:       name,
			constName:  constName,
			err:        NewVerificationError(class, nil),
			wantStatus: http.StatusUnauthorized,
			wantCode:   unauthorized,
			wantAuth:   `Bearer error="invalid_token"`,
		}
	}
	return []denyRow{
		{
			name:       "missing_credential",
			err:        ErrMissingCredential,
			wantStatus: http.StatusUnauthorized,
			wantCode:   unauthorized,
			wantAuth:   `Bearer realm="` + testRealmIssuer + `"`,
		},
		{
			name:       "key_set_unavailable",
			constName:  "ClassKeySetUnavailable",
			err:        fmt.Errorf("auth: issuer key lookup failed: %w", ErrKeySetUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   unavailable,
			wantRetry:  testRetryAfter,
		},
		invalid("malformed", "ClassMalformed", ClassMalformed),
		invalid("algorithm", "ClassAlgorithm", ClassAlgorithm),
		invalid("kid_missing", "ClassKidMissing", ClassKidMissing),
		invalid("kid_unknown", "ClassKidUnknown", ClassKidUnknown),
		invalid("signature", "ClassSignature", ClassSignature),
		invalid("issuer", "ClassIssuer", ClassIssuer),
		invalid("audience", "ClassAudience", ClassAudience),
		invalid("expired", "ClassExpired", ClassExpired),
		invalid("not_yet_valid", "ClassNotYetValid", ClassNotYetValid),
		invalid("issued_in_future", "ClassIssuedInFuture", ClassIssuedInFuture),
		invalid("missing_expiry", "ClassMissingExpiry", ClassMissingExpiry),
		invalid("type", "ClassType", ClassType),
		// The fail-closed default arm: a class this table does not name still
		// answers 401 with the invalid_token challenge, never a fall-through.
		invalid("an_unlisted_class", "", Class("a_class_added_later")),
	}
}

// testRealmIssuer and testRetryAfter are the two configuration-derived values
// the matrix's challenge is built from.
const (
	testRealmIssuer = "https://issuer.example.com"
	testRetryAfter  = "30"
)

// TestDenyAnswersTheDocumentedFailureMatrix pins status, envelope code and
// response header for every rejection the middleware can produce.
func TestDenyAnswersTheDocumentedFailureMatrix(t *testing.T) {
	ch := &challenge{
		missing:    schemeBearer + ` realm="` + testRealmIssuer + `"`,
		retryAfter: testRetryAfter,
	}

	for _, tc := range denyMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testPath, http.NoBody)
			c := server.NewHandlerContextForTest(rec, req, &config.Config{})

			err := ch.deny(c, tc.err)

			assertAPIError(t, err, tc.wantStatus, tc.wantCode)
			assert.Equal(t, tc.wantAuth, rec.Header().Get(headerWWWAuthenticate))
			assert.Equal(t, tc.wantRetry, rec.Header().Get(headerRetryAfter))
		})
	}
}

// TestDenyCoversEveryDeclaredClassConstant makes the matrix above fail loudly
// when a Class constant is added without a row.
//
// The declared set is read from the package SOURCE rather than from any Go
// value, so the check cannot be satisfied by the same list the table already
// enumerates: a new `Class = "..."` constant anywhere in the package is seen the
// moment it is declared.
func TestDenyCoversEveryDeclaredClassConstant(t *testing.T) {
	declared := declaredClassConstants(t)
	require.NotEmpty(t, declared, "the source scan found no Class constants — the scan itself is broken")

	covered := make([]string, 0, len(declared))
	for _, row := range denyMatrix() {
		if row.constName != "" {
			covered = append(covered, row.constName)
		}
	}

	slices.Sort(declared)
	slices.Sort(covered)
	assert.Equal(t, declared, covered,
		"every Class constant needs a row in denyMatrix and a row in wiki/auth.md's failure matrix")
}

// declaredClassConstants reports the names of every `X Class = "..."` constant
// declared in the package's non-test sources.
func declaredClassConstants(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr, "parsing %s", name)
		for _, decl := range file.Decls {
			names = append(names, classConstantNames(decl)...)
		}
	}
	return names
}

// classConstantNames returns the Class-typed constant names one declaration
// introduces.
func classConstantNames(decl ast.Decl) []string {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.CONST {
		return nil
	}
	var names []string
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		ident, ok := value.Type.(*ast.Ident)
		if !ok || ident.Name != "Class" {
			continue
		}
		for _, name := range value.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

// okHandler is the trivial route body used by the end-to-end group test.
func okHandler(c server.HandlerContext) error {
	return c.String(http.StatusOK, "ok")
}

// serveTestServer starts srv on the loopback port cfg already carries and
// returns its base URL, shutting the server down when the test ends.
func serveTestServer(t *testing.T, srv *server.Server, cfg *config.Config) string {
	t.Helper()
	// Start reports why the bind failed. The loopback port was reserved and then
	// released, so another process can take it in the gap; without this the
	// readiness poll below just times out and hides the real cause.
	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	base := "http://" + net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	require.Eventually(t, func() bool {
		select {
		case err := <-startErr:
			require.NoError(t, err, "test server failed to start")
		default:
		}
		resp, err := http.Get(base + healthPath) //nolint:noctx // readiness probe against the loopback test server
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}, 5*time.Second, 5*time.Millisecond, "test server never accepted a connection")
	return base
}

// newLoopbackConfig reserves an ephemeral loopback port and returns a config
// bound to it.
func newLoopbackConfig(t *testing.T) *config.Config {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port
	return cfg
}

// httpAnswer is a fully-read response from the live test server.
type httpAnswer struct {
	status int
	header http.Header
	body   []byte
}

// getWithCredential issues a GET against the live test server, optionally
// carrying a bearer credential, and drains the response.
func getWithCredential(t *testing.T, base, path, credential string) httpAnswer {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, http.NoBody)
	require.NoError(t, err)
	if credential != "" {
		req.Header.Set(headerAuthorization, schemeBearer+" "+credential)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return httpAnswer{status: resp.StatusCode, header: resp.Header, body: body}
}

// TestMiddlewareGuardsOnlyItsOwnRouteGroup is the end-to-end proof of the
// per-route-group attachment decision: the guarded group answers 401 with the
// standard envelope while the probes and an ungrouped sibling route stay open.
func TestMiddlewareGuardsOnlyItsOwnRouteGroup(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{
		Subject:   middlewareSubject,
		IssuedAt:  verifierNow,
		ExpiresAt: verifierNow.Add(time.Hour),
	})

	cfg := newLoopbackConfig(t)
	srv := server.New(cfg, logger.New("error", false))
	root := srv.RootGroup()
	root.Add(http.MethodGet, openPath, okHandler)
	root.Group("/api", Middleware(v)).Add(http.MethodGet, "/thing", okHandler)
	base := serveTestServer(t, srv, cfg)

	for _, path := range []string{healthPath, readyPath, openPath} {
		t.Run("open"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			answer := getWithCredential(t, base, path, "")
			assert.Equal(t, http.StatusOK, answer.status, "%s must not be guarded", path)
			assert.Empty(t, answer.header.Get(headerWWWAuthenticate))
		})
	}

	t.Run("guarded_route_without_a_credential", func(t *testing.T) {
		answer := getWithCredential(t, base, testPath, "")

		require.Equal(t, http.StatusUnauthorized, answer.status)
		assert.Equal(t, schemeBearer+` realm="`+iss.IssuerURL()+`"`, answer.header.Get(headerWWWAuthenticate))
		var envelope server.APIResponse
		require.NoError(t, json.Unmarshal(answer.body, &envelope))
		require.NotNil(t, envelope.Error, "the framework envelope must carry the error")
		assert.Equal(t, "UNAUTHORIZED", envelope.Error.Code)
		assert.NotContains(t, envelope.Error.Message, middlewareSubject)
	})

	t.Run("guarded_route_with_a_credential", func(t *testing.T) {
		answer := getWithCredential(t, base, testPath, credential)

		assert.Equal(t, http.StatusOK, answer.status)
	})
}
