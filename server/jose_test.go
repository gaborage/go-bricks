package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// joseFixture is the test-scoped state needed to exercise a JOSE-protected route:
// two RSA pairs, a resolver mapping kids to keys, and the matching inbound/outbound policies.
type joseFixture struct {
	resolver jose.KeyResolver
	inbound  *jose.Policy
	outbound *jose.Policy
	ourPriv  *rsa.PrivateKey
	peerPriv *rsa.PrivateKey
}

func newJOSEFixture(t *testing.T) *joseFixture {
	t.Helper()
	ourPriv, _ := jositest.GenerateTestKeyPair(t)
	peerPriv, _ := jositest.GenerateTestKeyPair(t)

	resolver := jositest.NewTestResolver(map[string]any{
		"our-key":  ourPriv,
		"peer-key": peerPriv,
	})

	return &joseFixture{
		resolver: resolver,
		inbound: &jose.Policy{
			Direction:  jose.DirectionInbound,
			DecryptKid: "our-key",
			VerifyKid:  "peer-key",
			SigAlg:     jose.DefaultSigAlg,
			KeyAlg:     jose.DefaultKeyAlg,
			Enc:        jose.DefaultEnc,
			Cty:        jose.DefaultCty,
		},
		outbound: &jose.Policy{
			Direction:  jose.DirectionOutbound,
			SignKid:    "our-key",
			EncryptKid: "peer-key",
			SigAlg:     jose.DefaultSigAlg,
			KeyAlg:     jose.DefaultKeyAlg,
			Enc:        jose.DefaultEnc,
			Cty:        jose.DefaultCty,
		},
		ourPriv:  ourPriv,
		peerPriv: peerPriv,
	}
}

type joseTokenReq struct {
	Pan string `json:"pan" validate:"required"`
}
type joseTokenResp struct {
	Token string `json:"token"`
}

// peerOutbound is the policy a peer (Visa, in production) would use to encrypt-to-us
// and sign-with-peer-key. It's the inverse of our server's outbound policy.
func (f *joseFixture) peerOutbound() *jose.Policy {
	return &jose.Policy{
		Direction:  jose.DirectionOutbound,
		SignKid:    "peer-key",
		EncryptKid: "our-key",
		SigAlg:     jose.DefaultSigAlg,
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        jose.DefaultEnc,
		Cty:        jose.DefaultCty,
	}
}

// peerInbound is the policy a peer would use to decrypt-with-peer-key and verify-with-our-key
// when receiving our outbound response.
func (f *joseFixture) peerInbound() *jose.Policy {
	return &jose.Policy{
		Direction:  jose.DirectionInbound,
		DecryptKid: "peer-key",
		VerifyKid:  "our-key",
		SigAlg:     jose.DefaultSigAlg,
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        jose.DefaultEnc,
		Cty:        jose.DefaultCty,
	}
}

// newJOSETestServer assembles an Echo handler wrapped with JOSE inbound/outbound
// policies for tests. Bypasses RegisterHandler (which requires a RouteRegistrar);
// the registration path is exercised separately via the registration-panic tests.
func newJOSETestServer(t *testing.T, f *joseFixture, handler HandlerFunc[joseTokenReq, joseTokenResp]) (*echo.Echo, echo.HandlerFunc) {
	t.Helper()
	return newJOSETestServerWithConfig(t, f, &config.Config{App: config.AppConfig{Env: "development"}}, handler)
}

// newJOSETestServerWithConfig is newJOSETestServer with the app config supplied by
// the caller, so a test can vary the debug × environment quadrant that gates
// response details (ADR-084).
func newJOSETestServerWithConfig(t *testing.T, f *joseFixture, cfg *config.Config, handler HandlerFunc[joseTokenReq, joseTokenResp]) (*echo.Echo, echo.HandlerFunc) {
	t.Helper()
	return newJOSETestServerWithObs(t, f, cfg, newJOSEObservability(nil, nil, nil), handler)
}

// newJOSETestServerWithObs is newJOSETestServerWithConfig with the observability bundle
// supplied by the caller, so a test can read what a failure left on the log and counter.
func newJOSETestServerWithObs(t *testing.T, f *joseFixture, cfg *config.Config, obs *joseObservability, handler HandlerFunc[joseTokenReq, joseTokenResp]) (*echo.Echo, echo.HandlerFunc) {
	t.Helper()
	e := echo.New()
	e.Validator = NewValidator()

	joseCfg := &joseRouteConfig{Inbound: f.inbound, Outbound: f.outbound, Resolver: f.resolver, Obs: obs}
	wrapped := wrapHandlerWithJOSE(handler, NewRequestBinder(), cfg, nil, false, joseCfg)
	return e, wrapped
}

func TestJOSEHappyPathRoundtrip(t *testing.T) {
	f := newJOSEFixture(t)
	e, h := newJOSETestServer(t, f, func(req joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
		return joseTokenResp{Token: "tok-" + req.Pan}, nil
	})

	plainReq := []byte(`{"pan":"4111111111111111"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
	req.Header.Set(echo.HeaderContentType, "application/jose")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/jose", rec.Header().Get(echo.HeaderContentType))

	plainResp, _ := jositest.OpenForTest(t, rec.Body.String(), f.peerInbound(), f.resolver)
	var token joseTokenResp
	require.NoError(t, json.Unmarshal(plainResp, &token))
	assert.Equal(t, "tok-4111111111111111", token.Token)
}

func TestJOSETamperedCiphertextReturnsPlaintextError(t *testing.T) {
	// THE security-invariant test: a tampered request must produce a *plaintext*
	// minimal error envelope. If this test ever observes Content-Type: application/jose
	// on the failure path, it is a security regression.
	f := newJOSEFixture(t)

	plainReq := []byte(`{"pan":"4111111111111111"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)
	tampered := []byte(compactReq)
	tampered[len(tampered)/2] ^= 0x01

	rec, body := driveJOSEInbound(t, f, jose.ContentType, bytes.NewReader(tampered), 0, nil)

	// Security invariant assertion #1: response is plaintext JSON, not JOSE.
	assert.NotEqual(t, "application/jose", rec.Header().Get(echo.HeaderContentType),
		"SECURITY REGRESSION: tampered request produced JOSE response — encryption to unauthenticated peer")
	assert.True(t, strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "application/json"),
		"tampered request should produce application/json response, got %q", rec.Header().Get(echo.HeaderContentType))

	// Security invariant assertion #2: minimal envelope (no traceId, timestamp, or framework metadata).
	assert.Contains(t, body, "code")
	assert.Contains(t, body, "message")
	assert.NotContains(t, body, "data", "minimal envelope must not include data")
	assert.NotContains(t, body, "meta", "minimal envelope must not include meta (would leak traceId)")
	assert.NotContains(t, body, "error", "minimal envelope uses top-level code/message, not nested error object")
	// #1163: the pre-trust envelope is not a fourth details renderer — joseAPIError
	// carries no details by construction, so there is nothing here to gate.
	assert.NotContains(t, body, "details", "minimal envelope must not include details — this peer is unauthenticated")

	// Status: 4xx (decrypt-failed = 401, malformed = 400 are both acceptable depending on which segment was tampered).
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized}, rec.Code)
}

func TestJOSEPostTrustErrorIsEncrypted(t *testing.T) {
	// When inbound succeeds but the handler returns an IAPIError (e.g., business
	// validation rejected the request), the error envelope MUST be JOSE-encrypted —
	// the channel is authenticated and the error may carry detail useful to the peer.
	f := newJOSEFixture(t)
	e, h := newJOSETestServer(t, f, func(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
		return joseTokenResp{}, NewBusinessLogicError("CARD_BLOCKED", "Card is blocked")
	})

	plainReq := []byte(`{"pan":"4111111111111111"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
	req.Header.Set(echo.HeaderContentType, "application/jose")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "application/jose", rec.Header().Get(echo.HeaderContentType),
		"post-trust errors on JOSE routes must be encrypted")

	plainResp, _ := jositest.OpenForTest(t, rec.Body.String(), f.peerInbound(), f.resolver)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(plainResp, &envelope))

	// Standard APIResponse envelope (encrypted): nested error object, meta with timestamp/traceId.
	require.Contains(t, envelope, "error")
	errObj, _ := envelope["error"].(map[string]any)
	assert.Equal(t, "CARD_BLOCKED", errObj["code"])
	assert.Contains(t, envelope, "meta")
}

func TestJOSEResultWithMetaSealsEnvelope(t *testing.T) {
	// When the handler returns ResultWithMeta on a JOSE-protected route, the sealed body
	// is the standard APIResponse envelope ({data, meta}) — symmetric with how the JOSE
	// error path already produces an envelope. Vanilla Result[R] continues to seal bare
	// data (see TestJOSEHappyPathRoundtrip).
	f := newJOSEFixture(t)
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	obs := newJOSEObservability(nil, nil, nil)
	joseCfg := &joseRouteConfig{Inbound: f.inbound, Outbound: f.outbound, Resolver: f.resolver, Obs: obs}

	handler := func(req joseTokenReq, _ HandlerContext) (ResultWithMeta[joseTokenResp], IAPIError) {
		return ResultWithMeta[joseTokenResp]{
			Data:   joseTokenResp{Token: "tok-" + req.Pan},
			Status: http.StatusOK,
			Meta: map[string]any{
				"total":   1,
				"hasMore": false,
			},
		}, nil
	}
	h := wrapHandlerWithJOSE(handler, NewRequestBinder(), cfg, nil, false, joseCfg)

	// Non-PAN sentinel keeps card-data scanners (OpenGrep) from flagging this fixture.
	plainReq := []byte(`{"pan":"test-pan-001"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
	req.Header.Set(echo.HeaderContentType, "application/jose")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/jose", rec.Header().Get(echo.HeaderContentType))

	// OpenForTest internally t.Fatals on decrypt/verify failure (jose/testing/helpers.go).
	// The discarded second return is *jose.Claims (verified claims), not an error — this
	// test doesn't need to inspect claims, only the envelope shape.
	plainResp, _ := jositest.OpenForTest(t, rec.Body.String(), f.peerInbound(), f.resolver)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(plainResp, &envelope))

	// Sealed body is the APIResponse envelope, not bare data.
	require.Contains(t, envelope, "data", "ResultWithMeta on JOSE route must seal {data, meta} envelope")
	require.Contains(t, envelope, "meta")

	dataObj, _ := envelope["data"].(map[string]any)
	assert.Equal(t, "tok-test-pan-001", dataObj["token"])

	metaObj, _ := envelope["meta"].(map[string]any)
	assert.InDelta(t, float64(1), metaObj["total"], 0)
	assert.Equal(t, false, metaObj["hasMore"])
	// Framework keys present.
	assert.Contains(t, metaObj, "timestamp")
	assert.Contains(t, metaObj, "traceId")
}

func TestJOSEResultWithMetaReservedKeyDroppedInsideSeal(t *testing.T) {
	// The framework-key invariant must hold even inside the JOSE-sealed envelope.
	// A handler that supplies "traceId" or "timestamp" in Meta MUST have those keys
	// overridden by the framework before the body is sealed, so peers receive the
	// authoritative trace correlation rather than handler-controlled values.
	f := newJOSEFixture(t)
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	obs := newJOSEObservability(nil, nil, nil)
	joseCfg := &joseRouteConfig{Inbound: f.inbound, Outbound: f.outbound, Resolver: f.resolver, Obs: obs}

	handler := func(req joseTokenReq, _ HandlerContext) (ResultWithMeta[joseTokenResp], IAPIError) {
		return ResultWithMeta[joseTokenResp]{
			Data:   joseTokenResp{Token: "tok-" + req.Pan},
			Status: http.StatusOK,
			Meta: map[string]any{
				fieldTraceID:   "attacker-controlled-trace",
				fieldTimestamp: "attacker-controlled-timestamp",
				"page":         7,
			},
		}, nil
	}
	rec := &levelRecLogger{}
	h := wrapHandlerWithJOSE(handler, NewRequestBinder(), cfg, rec, false, joseCfg)

	plainReq := []byte(`{"pan":"test-pan-002"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
	req.Header.Set(echo.HeaderContentType, "application/jose")
	req.Header.Set(echo.HeaderXRequestID, "real-jose-trace")
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, w.Code)

	// Per OpenForTest comment above (line 271 sibling test): the `_` is *jose.Claims,
	// not an error; decrypt/verify already t.Fatals on failure.
	plainResp, _ := jositest.OpenForTest(t, w.Body.String(), f.peerInbound(), f.resolver)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(plainResp, &envelope))

	metaObj, _ := envelope["meta"].(map[string]any)
	// Framework key wins inside the sealed envelope.
	assert.Equal(t, "real-jose-trace", metaObj["traceId"])
	assert.NotEqual(t, "attacker-controlled-trace", metaObj["traceId"])
	assert.NotEqual(t, "attacker-controlled-timestamp", metaObj["timestamp"])
	assert.InDelta(t, float64(7), metaObj["page"], 0)

	// WARN logs fired for both reserved keys on the JOSE path too. Asserts on
	// presence + content rather than exact event count so a future refactor that
	// batches both collisions into a single structured WARN remains compatible.
	require.NotEmpty(t, rec.events, "expected at least one log event for JOSE reserved-key collision")
	joseWarnKeys := map[string]bool{}
	for _, ev := range rec.events {
		if ev.level != "warn" {
			continue
		}
		if k := ev.fields["key"]; k != "" {
			joseWarnKeys[k] = true
		}
	}
	assert.True(t, joseWarnKeys[fieldTimestamp], "JOSE WARN missing for timestamp collision")
	assert.True(t, joseWarnKeys[fieldTraceID], "JOSE WARN missing for traceId collision")
}

// --- Registration-time panic tests ---

type asymmetricRequest struct {
	_   struct{} `jose:"decrypt=our-key,verify=peer-key"`
	Pan string   `json:"pan"`
}
type plainResponse struct {
	Token string `json:"token"`
}

type plainRequest struct {
	Pan string `json:"pan"`
}
type joseTaggedResponse struct {
	_     struct{} `jose:"sign=our-key,encrypt=peer-key"`
	Token string   `json:"token"`
}

type taggedRequestUnknownKid struct {
	_   struct{} `jose:"decrypt=ghost-key,verify=peer-key"`
	Pan string   `json:"pan"`
}
type taggedResponseUnknownKid struct {
	_     struct{} `jose:"sign=our-key,encrypt=ghost-key"`
	Token string   `json:"token"`
}

type taggedReq struct {
	_   struct{} `jose:"decrypt=our-key,verify=peer-key"`
	Pan string   `json:"pan"`
}
type taggedResp struct {
	_     struct{} `jose:"sign=our-key,encrypt=peer-key"`
	Token string   `json:"token"`
}

// fakeRegistrar collects added routes without standing up a real Echo router.
// It satisfies the echo-free RouteRegistrar interface only (it is not a *routeGroup),
// so RegisterHandler exercises its fallback path through Add.
type fakeRegistrar struct{}

func (fakeRegistrar) Add(_, _ string, _ Handler, _ ...MiddlewareFunc) {}
func (fakeRegistrar) Group(string, ...MiddlewareFunc) RouteRegistrar  { return fakeRegistrar{} }
func (fakeRegistrar) Use(...MiddlewareFunc)                           {}
func (fakeRegistrar) FullPath(p string) string                        { return p }

func registerJOSE[T, R any](resolver jose.KeyResolver, opts ...RouteOption) func() {
	return func() {
		hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSEResolver(resolver))
		handler := func(_ T, _ HandlerContext) (R, IAPIError) {
			var zero R
			return zero, nil
		}
		RegisterHandler[T, R](hr, fakeRegistrar{}, http.MethodPost, "/tokens", handler, opts...)
	}
}

// assertRegistrationPanics runs fn and asserts it panics with a string message
// containing wantSubstring. Centralizes the deferred-recover boilerplate.
func assertRegistrationPanics(t *testing.T, wantSubstring string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected registration to panic, got nil recover")
		msg, ok := r.(string)
		require.True(t, ok, "expected panic value to be a string, got %T: %v", r, r)
		assert.Contains(t, msg, wantSubstring, "panic message did not contain expected substring")
	}()
	fn()
}

func TestJOSERegistrationPanicsOnAsymmetricTags(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()
	assertRegistrationPanics(t, "asymmetric jose policy",
		registerJOSE[asymmetricRequest, plainResponse](f.resolver))
}

func TestJOSERegistrationPanicsOnReverseAsymmetry(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()
	assertRegistrationPanics(t, "asymmetric jose policy",
		registerJOSE[plainRequest, joseTaggedResponse](f.resolver))
}

func TestJOSERegistrationPanicsOnUnknownInboundKid(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()
	assertRegistrationPanics(t, "inbound key resolution failed",
		registerJOSE[taggedRequestUnknownKid, taggedResp](f.resolver))
}

func TestJOSERegistrationPanicsOnUnknownOutboundKid(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()
	assertRegistrationPanics(t, "outbound key resolution failed",
		registerJOSE[taggedReq, taggedResponseUnknownKid](f.resolver))
}

func TestJOSERegistrationPanicsOnRawResponseConflict(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()
	assertRegistrationPanics(t, "WithRawResponse",
		registerJOSE[taggedReq, taggedResp](f.resolver, WithRawResponse()))
}

func TestJOSERegistrationPanicsWhenNoResolverWired(t *testing.T) {
	defer DefaultRouteRegistry.Clear()
	// No resolver — but route declares jose tags. Must panic at startup, not runtime.
	assertRegistrationPanics(t, "no KeyResolver wired",
		registerJOSE[taggedReq, taggedResp](nil))
}

func TestRoutesCloneDoesNotMutateLiveJOSEPolicy(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)

	f := newJOSEFixture(t)
	e := echo.New()
	e.Validator = NewValidator()
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSEResolver(f.resolver))
	registrar := newRouteGroup(e.Group(""), "", nil)
	POST(hr, registrar, "/tokens", func(req taggedReq, _ HandlerContext) (taggedResp, IAPIError) {
		return taggedResp{Token: "tok-" + req.Pan}, nil
	})

	drive := func() *httptest.ResponseRecorder {
		t.Helper()
		plainReq := []byte(`{"pan":"4111111111111111"}`)
		compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
		req.Header.Set(echo.HeaderContentType, "application/jose")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	first := drive()
	require.Equal(t, http.StatusOK, first.Code)

	routes := DefaultRouteRegistry.Routes()
	require.Len(t, routes, 1)
	require.NotNil(t, routes[0].InboundJOSE)
	routes[0].InboundJOSE.DecryptKid = "ghost-key"
	routes[0].InboundJOSE.Mode = jose.SealModeBareJWE

	second := drive()
	require.Equal(t, http.StatusOK, second.Code, "mutating a Routes() policy must not change the serving kids")
}

func TestNonJOSERouteUnaffectedByResolver(t *testing.T) {
	f := newJOSEFixture(t)
	defer DefaultRouteRegistry.Clear()

	// A regular route with no JOSE tags must register cleanly even when a resolver is wired.
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSEResolver(f.resolver))
	type plainReq struct {
		Name string `json:"name"`
	}
	type plainResp struct {
		Greeting string `json:"greeting"`
	}
	handler := func(req plainReq, _ HandlerContext) (plainResp, IAPIError) {
		return plainResp{Greeting: "hi " + req.Name}, nil
	}
	require.NotPanics(t, func() {
		RegisterHandler[plainReq, plainResp](hr, fakeRegistrar{}, http.MethodPost, "/greet", handler)
	})
}

// driveJOSEInbound runs the JOSE inbound path with an arbitrary body reader, optionally
// behind one middleware, and returns the recorder plus the decoded minimal error envelope.
// A non-zero contentLength overrides whatever httptest infers from the reader, which is
// how the declared-length rejection is reached without supplying a body that large.
// Every caller exercises a pre-trust failure, so the handler must never run.
func driveJOSEInbound(t *testing.T, f *joseFixture, contentType string, body io.Reader, contentLength int64, mw echo.MiddlewareFunc) (rec *httptest.ResponseRecorder, envelope map[string]any) {
	t.Helper()
	e, h := newJOSETestServer(t, f, func(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
		t.Error("handler must not be invoked on a pre-trust failure")
		return joseTokenResp{}, nil
	})
	if mw != nil {
		h = mw(h)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", body)
	req.Header.Set(echo.HeaderContentType, contentType)
	if contentLength != 0 {
		req.ContentLength = contentLength
	}
	rec = httptest.NewRecorder()
	require.NoError(t, h(e.NewContext(req, rec)))

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return rec, envelope
}

// unreadableBody fails the test if anything reads it, so the cases below observe "the body
// was not touched" directly instead of inferring it from a swallowed read error.
type unreadableBody struct{ t *testing.T }

func (b unreadableBody) Read([]byte) (int, error) {
	b.t.Error("request body must not be read on a header-only rejection path")
	return 0, io.EOF
}

// TestJOSEInboundPreTrustFailures is the decision table wiki/jose.md documents: every
// pre-trust rejection is a (Content-Type, body, middleware) -> (status, code) row. The
// bodies are built per-subtest so the two 10 MiB buffers are never live at once.
func TestJOSEInboundPreTrustFailures(t *testing.T) {
	f := newJOSEFixture(t)

	tests := []struct {
		name        string
		contentType string
		body        func(t *testing.T) io.Reader
		length      int64
		mw          echo.MiddlewareFunc
		wantStatus  int
		wantCode    string
	}{
		{
			name:        "plaintext_content_type",
			contentType: echo.MIMEApplicationJSON,
			body:        func(*testing.T) io.Reader { return bytes.NewReader([]byte(`{"pan":"4111111111111111"}`)) },
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "JOSE_PLAINTEXT_REJECTED",
		},
		{
			// Ordering pin: the reader fails the test on any Read, so the untouched body is
			// observed rather than inferred from the status.
			name:        "wrong_content_type_body_never_read",
			contentType: echo.MIMEApplicationJSON,
			body:        func(t *testing.T) io.Reader { return unreadableBody{t: t} },
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "JOSE_PLAINTEXT_REJECTED",
		},
		{
			// Both failure conditions at once: the Content-Type wins, so this is 415 rather
			// than the 400 an empty body alone produces. This is the documented change.
			name:        "wrong_content_type_with_empty_body",
			contentType: echo.MIMEApplicationJSON,
			body:        func(*testing.T) io.Reader { return bytes.NewReader(nil) },
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "JOSE_PLAINTEXT_REJECTED",
		},
		{
			name:        "body_read_error",
			contentType: jose.ContentType,
			body:        func(*testing.T) io.Reader { return io.NopCloser(iotest.ErrReader(errors.New("boom"))) },
			wantStatus:  http.StatusBadRequest,
			wantCode:    "JOSE_BODY_REQUIRED",
		},
		{
			name:        "empty_body",
			contentType: jose.ContentType,
			body:        func(*testing.T) io.Reader { return bytes.NewReader(nil) },
			wantStatus:  http.StatusBadRequest,
			wantCode:    "JOSE_BODY_REQUIRED",
		},
		{
			name:        "oversize_body",
			contentType: jose.ContentType,
			body: func(*testing.T) io.Reader {
				return bytes.NewReader(bytes.Repeat([]byte("a"), maxJOSERequestBytes+1))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "JOSE_BODY_TOO_LARGE",
		},
		{
			// Declared-length pin: over the cap by Content-Length alone, so it must be
			// rejected before the reader is ever touched.
			name:        "declared_length_over_cap_not_read",
			contentType: jose.ContentType,
			body:        func(t *testing.T) io.Reader { return unreadableBody{t: t} },
			length:      maxJOSERequestBytes + 1,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "JOSE_BODY_TOO_LARGE",
		},
		{
			// A body-limit middleware errors mid-stream on an unknown-length body over
			// server.bodylimit, reaching the JOSE path as a read error. Still 413, not the
			// generic read-failure 400.
			name:        "body_limit_middleware_overflow",
			contentType: jose.ContentType,
			body: func(*testing.T) io.Reader {
				return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("a"), 4096))) // NopCloser ⇒ ContentLength -1
			},
			mw:         middleware.BodyLimit(1024),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "JOSE_BODY_TOO_LARGE",
		},
		{
			// Off-by-one pin: exactly the limit must reach jose.Open and fail there, not at 413.
			name:        "body_at_size_limit_reaches_open",
			contentType: jose.ContentType,
			body: func(*testing.T) io.Reader {
				return bytes.NewReader(bytes.Repeat([]byte("a"), maxJOSERequestBytes))
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   "JOSE_MALFORMED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, envelope := driveJOSEInbound(t, f, tc.contentType, tc.body(t), tc.length, tc.mw)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantCode, envelope["code"])
		})
	}
}

const (
	joseFailureCounter = "jose.failures.total"
	joseRouteAttr      = "http.route"
)

// TestMatchedRouteTemplate pins the pure contract both JOSE failure surfaces read: which
// routing shapes yield a registered route template and which yield nothing at all. wantPath
// records what c.Path() reports for the same request, so the shapes where the two diverge —
// echo v5.3.0's group catch-alls, auto-registered at BOTH "/api" and "/api/*" — read as the
// reason the helper's RouteNotFound guard exists rather than as a bare expectation. A global
// middleware is the capture seam: it runs after routing, and it runs on the requests no
// route matched, which echo's own not-found handler serves.
func TestMatchedRouteTemplate(t *testing.T) {
	var (
		gotTemplate string
		gotPath     string
		captured    bool
	)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			gotTemplate, gotPath, captured = matchedRouteTemplate(ec), ec.Path(), true
			return next(ec)
		}
	})
	noContent := func(ec *echo.Context) error { return ec.NoContent(http.StatusOK) }
	e.POST("/widget/:id", noContent)
	// The pass-through middleware is the whole point of the group: it is what makes echo
	// auto-register the catch-alls (group.go Group.Use).
	g := e.Group("/api", func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	g.POST("/tokens/:id", noContent)

	tests := []struct {
		name         string
		method       string
		target       string
		wantStatus   int
		wantTemplate string
		wantPath     string
	}{
		{
			name:         "matched_route_reports_its_template",
			method:       http.MethodPost,
			target:       "/api/tokens/42",
			wantStatus:   http.StatusOK,
			wantTemplate: "/api/tokens/:id",
			wantPath:     "/api/tokens/:id",
		},
		{
			// The bare group prefix hits the catch-all registered at "" — a distinct shape
			// from the "/*" one below, and the one whose c.Path() looks most like a match.
			name:         "bare_group_prefix_reports_nothing",
			method:       http.MethodPost,
			target:       "/api",
			wantStatus:   http.StatusNotFound,
			wantTemplate: "",
			wantPath:     "/api",
		},
		{
			name:         "group_catch_all_sub_path_reports_nothing",
			method:       http.MethodPost,
			target:       "/api/nope/x",
			wantStatus:   http.StatusNotFound,
			wantTemplate: "",
			wantPath:     "/api/*",
		},
		{
			name:         "global_404_reports_nothing",
			method:       http.MethodPost,
			target:       "/nothing/here",
			wantStatus:   http.StatusNotFound,
			wantTemplate: "",
			wantPath:     "",
		},
		{
			// The guard stays NARROW on purpose: a top-level wrong-method request keeps the
			// engine's best-match template, a registered value and therefore bounded.
			name:         "global_405_reports_best_match_template",
			method:       http.MethodGet,
			target:       "/widget/5",
			wantStatus:   http.StatusMethodNotAllowed,
			wantTemplate: "/widget/:id",
			wantPath:     "/widget/:id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTemplate, gotPath, captured = "", "", false

			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.True(t, captured, "the global middleware must observe every request")
			require.Equal(t, tc.wantStatus, rec.Code)

			assert.Equal(t, tc.wantPath, gotPath, "c.Path() drifted; the case premise no longer holds")
			assert.Equal(t, tc.wantTemplate, gotTemplate)
		})
	}
}

// TestJOSEFailureRecordsMatchedRouteOnBothSurfaces drives the PRODUCTION seam —
// wrapHandlerWithJOSE → runJOSEInbound → recordFailure on a registered route — and pins the
// contract only that seam can show: the failure log and the failure counter agree on whether
// http.route is present and on its value. Which shapes yield a template is
// matchedRouteTemplate's contract, pinned by TestMatchedRouteTemplate above; a JOSE-wrapped
// typed handler only ever runs on a matched route, so this is the shape production reaches.
func TestJOSEFailureRecordsMatchedRouteOnBothSurfaces(t *testing.T) {
	const routeTmpl = "/tokens/:id"

	f := newJOSEFixture(t)
	mp := obtest.NewTestMeterProvider()
	recLog := &recLogger{}
	obs := newJOSEObservability(recLog, nil, mp)

	e, h := newJOSETestServerWithObs(t, f, &config.Config{App: config.AppConfig{Env: "development"}}, obs,
		func(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
			t.Error("handler must not be invoked on a pre-trust failure")
			return joseTokenResp{}, nil
		})
	e.POST(routeTmpl, h)

	// A plaintext Content-Type is the cheapest pre-trust rejection, so the failure is
	// recorded through runJOSEInbound before any crypto runs.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens/42",
		strings.NewReader(`{"pan":"4111111111111111"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)

	m := obtest.FindMetric(mp.Collect(t), joseFailureCounter)
	require.NotNil(t, m, "counter %s was never recorded", joseFailureCounter)
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s, got %T", joseFailureCounter, m.Data)
	require.Len(t, sum.DataPoints, 1, "expected exactly one failure series for %s", joseFailureCounter)
	onCounter, onCounterPresent := sum.DataPoints[0].Attributes.Value(joseRouteAttr)

	require.NotNil(t, recLog.last, "no failure log event was emitted")
	inLog, inLogPresent := recLog.last.fields[joseRouteAttr]

	require.Equal(t, onCounterPresent, inLogPresent,
		"log and counter disagree on whether %s is present", joseRouteAttr)
	require.True(t, onCounterPresent, "a matched route must report its template on both surfaces")
	assert.Equal(t, routeTmpl, onCounter.AsString(), "counter must carry the route template")
	assert.Equal(t, routeTmpl, inLog, "log must carry the same route template as the counter")
}

// TestJOSEFailureOmitsRouteWhenNoTemplate is the unit-level pin of the omit rule itself:
// given a context that never routed, recordFailure must leave http.route off BOTH surfaces
// rather than substituting the concrete URL path. It calls the unexported method directly
// because production cannot reach this state — a JOSE-wrapped typed handler only ever runs
// on a matched route, which is what TestJOSEFailureRecordsMatchedRouteOnBothSurfaces above
// pins — and an unreachable state is exactly what no seam test can cover: without this case
// nothing fails if the c.Request().URL.Path fallback comes back, or if the route != ""
// guard is inverted so both surfaces stamp an empty template. The http.method assertions
// are the positive control: they prove both surfaces were written and that http.route alone
// was withheld.
func TestJOSEFailureOmitsRouteWhenNoTemplate(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	recLog := &recLogger{}
	obs := newJOSEObservability(recLog, nil, mp)

	// Straight from NewContext, so the router never ran: no matched route, and a concrete
	// request path that must not be substituted for the missing template.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens/42", http.NoBody)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	require.Empty(t, c.Path(), "premise: an unrouted context carries no route template")

	obs.recordFailure(context.Background(), c, "inbound",
		&joseAPIError{code: errCodeJOSEPlaintextRejected, message: "unrouted", status: http.StatusUnsupportedMediaType})

	m := obtest.FindMetric(mp.Collect(t), joseFailureCounter)
	require.NotNil(t, m, "counter %s was never recorded", joseFailureCounter)
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s, got %T", joseFailureCounter, m.Data)
	require.Len(t, sum.DataPoints, 1, "expected exactly one failure series for %s", joseFailureCounter)
	require.NotNil(t, recLog.last, "no failure log event was emitted")

	method, methodOnCounter := sum.DataPoints[0].Attributes.Value("http.method")
	require.True(t, methodOnCounter, "positive control: the counter must still carry http.method")
	require.Equal(t, http.MethodPost, method.AsString())
	require.Equal(t, http.MethodPost, recLog.last.fields["http.method"],
		"positive control: the log must still carry http.method")

	_, onCounter := sum.DataPoints[0].Attributes.Value(joseRouteAttr)
	assert.False(t, onCounter, "%s must be absent from the counter when no template matched", joseRouteAttr)
	assert.NotContains(t, recLog.last.fields, joseRouteAttr,
		"%s must be absent from the log when no template matched", joseRouteAttr)
}
