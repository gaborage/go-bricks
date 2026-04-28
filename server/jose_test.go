package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
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
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	obs := newJOSEObservability(nil, nil, nil)
	joseCfg := &joseRouteConfig{Inbound: f.inbound, Outbound: f.outbound, Resolver: f.resolver, Obs: obs}
	wrapped := wrapHandlerWithJOSE(handler, NewRequestBinder(), cfg, false, joseCfg)
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
	e, h := newJOSETestServer(t, f, func(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
		t.Fatal("handler must not be invoked when inbound decryption fails")
		return joseTokenResp{}, nil
	})

	plainReq := []byte(`{"pan":"4111111111111111"}`)
	compactReq := jositest.SealForTest(t, plainReq, f.peerOutbound(), f.resolver)
	tampered := []byte(compactReq)
	tampered[len(tampered)/2] ^= 0x01

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader(tampered))
	req.Header.Set(echo.HeaderContentType, "application/jose")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))

	// Security invariant assertion #1: response is plaintext JSON, not JOSE.
	assert.NotEqual(t, "application/jose", rec.Header().Get(echo.HeaderContentType),
		"SECURITY REGRESSION: tampered request produced JOSE response — encryption to unauthenticated peer")
	assert.True(t, strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "application/json"),
		"tampered request should produce application/json response, got %q", rec.Header().Get(echo.HeaderContentType))

	// Security invariant assertion #2: minimal envelope (no traceId, timestamp, or framework metadata).
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body, "code")
	assert.Contains(t, body, "message")
	assert.NotContains(t, body, "data", "minimal envelope must not include data")
	assert.NotContains(t, body, "meta", "minimal envelope must not include meta (would leak traceId)")
	assert.NotContains(t, body, "error", "minimal envelope uses top-level code/message, not nested error object")

	// Status: 4xx (decrypt-failed = 401, malformed = 400 are both acceptable depending on which segment was tampered).
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized}, rec.Code)
}

func TestJOSEPlaintextRequestRejectedWith415(t *testing.T) {
	f := newJOSEFixture(t)
	e, h := newJOSETestServer(t, f, func(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
		t.Fatal("handler must not be invoked when content-type is wrong")
		return joseTokenResp{}, nil
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(`{"pan":"4111111111111111"}`)))
	req.Header.Set(echo.HeaderContentType, "application/json") // wrong on a JOSE route
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "JOSE_PLAINTEXT_REJECTED", body["code"])
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
type fakeRegistrar struct{}

func (fakeRegistrar) Add(string, string, echo.HandlerFunc, ...echo.MiddlewareFunc) echo.RouteInfo {
	return echo.RouteInfo{}
}
func (fakeRegistrar) Group(string, ...echo.MiddlewareFunc) RouteRegistrar { return fakeRegistrar{} }
func (fakeRegistrar) Use(...echo.MiddlewareFunc)                          {}
func (fakeRegistrar) FullPath(p string) string                            { return p }

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
