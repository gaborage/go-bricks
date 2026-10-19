package jose_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	bricksjose "github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
)

// ---- Recipe copy: wiki/jose.md#serving-a-detached-signature-envelope-peer-imperative-recipe.
// Keep it identical to the wiki's Go block except for the import aliases: go-bricks jose is
// bricksjose here and go-jose is jose (josev4 in the wiki).

// Resolver key names for one tenant's three keys. They name keys inside the
// per-request resolver, not wire headers: the peer sends no kid. If it ever
// sends one, it must equal the constant, or Open refuses it (JOSE_KID_UNKNOWN).
const (
	peerVerifyKid = "peer-verify" // peer public key: verifies the detached signature
	ourDecryptKid = "our-decrypt" // our private key: opens the request JWE
	peerSealKid   = "peer-seal"   // peer public key: seals the reply data

	// maxSignatureHeaderBytes bounds the header value before any parse (DoS bound).
	maxSignatureHeaderBytes = 8 << 10
)

// The consumer's own reply codes.
const (
	codeSignatureHeaderInvalid = "SIGNATURE_HEADER_INVALID"
	codeDataMissing            = "DATA_MISSING"
	codePayloadInvalid         = "PAYLOAD_INVALID"
	codeInternal               = "INTERNAL"
	codeRequestRejected        = "REQUEST_REJECTED"
	codeTenantForbidden        = "TENANT_FORBIDDEN"
)

var (
	errUnknownKey      = errors.New("unknown key name")
	errTenantForbidden = errors.New("principal not authorized for tenant") // Authorize's refusal; fail answers 403
)

// consumerCodes maps jose codes into the consumer's own vocabulary; any other
// code answers codeRequestRejected.
var consumerCodes = map[string]string{
	"JOSE_SIGNATURE_INVALID": "SIGNATURE_INVALID",
	"JOSE_KID_UNKNOWN":       "KEY_UNKNOWN",
	"JOSE_MALFORMED":         "MALFORMED",
	"JOSE_OUTER_NOT_JWS":     "MALFORMED",
}

type envelopeConfig struct {
	SignatureHeader string `config:"example.envelope.signatureheader" required:"true"` // e.g. X-Example-Signature
}

type envelopeRequest struct {
	Data string `json:"data"`
}

// statusRequest is the bodyless twin's request: a struct (ADR-121) with no data field.
type statusRequest struct{}

// envelopeReply is the consumer's envelope. Only Data is ever sealed.
type envelopeReply struct {
	Code string `json:"code,omitempty"`
	Data string `json:"data,omitempty"`
}

// TenantKeySet is one tenant's key material, parsed by the consumer (PEM included).
type TenantKeySet struct {
	PeerVerify *rsa.PublicKey
	OurDecrypt *rsa.PrivateKey
	PeerSeal   *rsa.PublicKey
}

// TenantKeys is the consumer's per-tenant source. Authorize reads the request's principal
// from ctx (for example auth.PrincipalFromContext) and returns errTenantForbidden unless
// that principal may act for tenantID, including when there is none. ForTenant runs on
// every request, so it returns keys already parsed and cached.
type TenantKeys interface {
	Authorize(ctx context.Context, tenantID string) error
	ForTenant(ctx context.Context, tenantID string) (TenantKeySet, error)
}

// Operations is the consumer's business logic; Order and Receipt are its own types.
type Operations interface {
	Submit(ctx context.Context, tenantID string, in Order) (Receipt, error)
	Status(ctx context.Context, tenantID string) (Receipt, error)
}

// tenantKeyStore adapts one tenant's keys to bricksjose.KeyStoreLike. A nil key answers
// errUnknownKey: bricksjose.KeyStoreResolver passes a nil key through unchanged.
type tenantKeyStore TenantKeySet

func (s tenantKeyStore) PrivateKey(name string) (*rsa.PrivateKey, error) {
	if name == ourDecryptKid && s.OurDecrypt != nil {
		return s.OurDecrypt, nil
	}
	return nil, errUnknownKey
}

func (s tenantKeyStore) PublicKey(name string) (*rsa.PublicKey, error) {
	if name == peerVerifyKid && s.PeerVerify != nil {
		return s.PeerVerify, nil
	}
	if name == peerSealKid && s.PeerSeal != nil {
		return s.PeerSeal, nil
	}
	return nil, errUnknownKey
}

func inboundPolicy() *bricksjose.Policy {
	return &bricksjose.Policy{
		Direction:        bricksjose.DirectionInbound,
		Mode:             bricksjose.SealModeJWSofJWEMinimal,
		DecryptKid:       ourDecryptKid, // resolver key name, not a wire header
		VerifyKid:        peerVerifyKid,
		SigAlg:           jose.RS512,
		KeyAlg:           bricksjose.DefaultKeyAlg,
		Enc:              bricksjose.DefaultEnc, // A256GCM; pins Open to exactly this
		AcceptMissingKid: true,
		// Cty left empty: the inner cty is not pinned.
	}
}

func outboundPolicy() *bricksjose.Policy {
	return &bricksjose.Policy{
		Direction:  bricksjose.DirectionOutbound,
		Mode:       bricksjose.SealModeBareJWE,
		EncryptKid: peerSealKid,
		KeyAlg:     bricksjose.DefaultKeyAlg, // required: Validate refuses an empty KeyAlg
		Enc:        bricksjose.DefaultEnc,    // required: Validate refuses an empty Enc
		OmitKid:    true,
		// Cty and Typ empty: Seal writes no cty or typ header.
	}
}

type envelopeHandler struct {
	cfg    envelopeConfig
	keys   TenantKeys
	ops    Operations
	log    logger.Logger
	bearer []server.MiddlewareFunc // e.g. auth.Middleware(verifier); see note a
}

// newEnvelopeHandler is called from the module's Init with deps.Config and deps.Logger.
func newEnvelopeHandler(
	cfg *config.Config, keys TenantKeys, ops Operations, log logger.Logger, bearer ...server.MiddlewareFunc,
) (*envelopeHandler, error) {
	h := &envelopeHandler{keys: keys, ops: ops, log: log, bearer: bearer}
	if err := cfg.InjectInto(&h.cfg); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *envelopeHandler) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	g := r.Group("/example", h.bearer...)
	server.POST(hr, g, "/orders", h.submit, server.WithRawResponse())
	server.GET(hr, g, "/orders/status", h.status, server.WithRawResponse())
}

func (h *envelopeHandler) submit(req envelopeRequest, ctx server.HandlerContext) (server.Result[envelopeReply], server.IAPIError) {
	// Header.Values, not RequestHeader: Header.Get cannot see a duplicated header.
	values := ctx.Request().Header.Values(h.cfg.SignatureHeader)
	if len(values) != 1 { // missing or duplicated
		return refuse(http.StatusUnauthorized, codeSignatureHeaderInvalid), nil
	}
	if len(values[0]) > maxSignatureHeaderBytes {
		return refuse(http.StatusBadRequest, codeSignatureHeaderInvalid), nil
	}
	detached, ok := strings.CutPrefix(values[0], "JWS ")
	if !ok {
		return refuse(http.StatusUnauthorized, codeSignatureHeaderInvalid), nil
	}
	// Refused here, not by a validate tag, so the consumer's envelope renders. This also
	// catches a body the binder skipped because Content-Type was missing or not JSON.
	if req.Data == "" {
		return refuse(http.StatusBadRequest, codeDataMissing), nil
	}

	rctx := ctx.RequestContext()
	tenantID, resolver, err := h.tenantResolver(rctx)
	if err != nil {
		return h.fail(err), nil
	}
	compact, err := bricksjose.AttachDetachedJWS(detached, req.Data)
	if err != nil {
		return h.fail(err), nil
	}
	plaintext, _, _, err := bricksjose.Open(compact, inboundPolicy(), resolver)
	if err != nil {
		return h.fail(err), nil
	}
	// Replay protection goes here, now that Open returned nil (note c).

	// The opened plaintext is unvalidated input: the pipeline's validate tags ran
	// on envelopeRequest only.
	var in Order
	if json.Unmarshal(plaintext, &in) != nil || in.Validate() != nil {
		return refuse(http.StatusBadRequest, codePayloadInvalid), nil
	}

	out, err := h.ops.Submit(rctx, tenantID, in)
	if err != nil {
		return h.fail(err), nil
	}
	return h.sealReply(out, resolver), nil
}

// status is the bodyless twin: nothing was signed, so it calls neither
// AttachDetachedJWS nor Open, but it still seals the reply data (note b).
func (h *envelopeHandler) status(_ statusRequest, ctx server.HandlerContext) (server.Result[envelopeReply], server.IAPIError) {
	rctx := ctx.RequestContext()
	tenantID, resolver, err := h.tenantResolver(rctx)
	if err != nil {
		return h.fail(err), nil
	}
	out, err := h.ops.Status(rctx, tenantID)
	if err != nil {
		return h.fail(err), nil
	}
	return h.sealReply(out, resolver), nil
}

func (h *envelopeHandler) tenantResolver(ctx context.Context) (string, bricksjose.KeyResolver, error) {
	tenantID, err := multitenant.TenantID(ctx)
	if err != nil {
		return "", nil, err // ErrNoTenant: a wiring fault (multi-tenancy off, or a skipped route), so a 500
	}
	// The tenant header is caller-written: authorize before any tenant-scoped lookup (note a).
	err = h.keys.Authorize(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}
	keys, err := h.keys.ForTenant(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}
	return tenantID, bricksjose.NewKeyStoreResolver(tenantKeyStore(keys)), nil
}

// sealReply runs on success paths only, so every error reply stays plaintext.
func (h *envelopeHandler) sealReply(out Receipt, resolver bricksjose.KeyResolver) server.Result[envelopeReply] {
	body, err := json.Marshal(out)
	if err != nil {
		return h.fail(err)
	}
	sealed, err := bricksjose.Seal(body, outboundPolicy(), resolver)
	if err != nil {
		return h.fail(err)
	}
	return server.Result[envelopeReply]{Data: envelopeReply{Data: sealed}}
}

// fail maps any error to the consumer's envelope. Only jerr.Code is logged for a jose
// refusal; Cause, Error() and plaintext never reach the wire.
func (h *envelopeHandler) fail(err error) server.Result[envelopeReply] {
	if errors.Is(err, errTenantForbidden) {
		h.log.Warn().Msg("detached-signature envelope forbidden for the tenant")
		return refuse(http.StatusForbidden, codeTenantForbidden)
	}
	var jerr *bricksjose.Error
	if !errors.As(err, &jerr) {
		h.log.Error().Err(err).Msg("detached-signature envelope failed")
		return refuse(http.StatusInternalServerError, codeInternal)
	}
	h.log.Warn().Str("jose_code", jerr.Code).Msg("detached-signature envelope refused")
	status := jerr.Status
	if status == 0 { // Validate refusals and KeyStoreResolver lookup errors carry no status
		status = http.StatusInternalServerError
	}
	code, ok := consumerCodes[jerr.Code]
	if !ok {
		code = codeRequestRejected
	}
	return refuse(status, code)
}

func refuse(status int, code string) server.Result[envelopeReply] {
	return server.Result[envelopeReply]{Data: envelopeReply{Code: code}, Status: status}
}

// ---- End of the recipe copy.

const (
	signatureHeader = "X-Example-Signature"
	tenantA         = "tenant-a"
	tenantNoVerify  = "tenant-no-verify"
	receiptID       = "receipt-1"
	receiptJSON     = `{"id":"` + receiptID + `"}`
)

type Order struct {
	Amount int `json:"amount"`
}

func (o Order) Validate() error {
	if o.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	return nil
}

type Receipt struct {
	ID string `json:"id"`
}

type noTenantKeys struct{}

func (noTenantKeys) Authorize(context.Context, string) error {
	return nil
}

func (noTenantKeys) ForTenant(context.Context, string) (TenantKeySet, error) {
	return TenantKeySet{}, errors.New("unknown tenant")
}

type fakeTenantKeys map[string]TenantKeySet

func (f fakeTenantKeys) ForTenant(ctx context.Context, tenantID string) (TenantKeySet, error) {
	keys, ok := f[tenantID]
	if !ok {
		return noTenantKeys{}.ForTenant(ctx, tenantID)
	}
	return keys, nil
}

type fakeOperations struct {
	submitted   []Order
	statusCalls int
}

func (f *fakeOperations) Submit(_ context.Context, _ string, in Order) (Receipt, error) {
	f.submitted = append(f.submitted, in)
	return Receipt{ID: receiptID}, nil
}

func (f *fakeOperations) Status(_ context.Context, _ string) (Receipt, error) {
	f.statusCalls++
	return Receipt{ID: receiptID}, nil
}

// routeRecorder is a minimal server.RouteRegistrar that keeps each route it is handed.
type routeRecorder struct {
	prefix string
	routes map[string]server.Handler
}

func (r routeRecorder) Add(method, path string, h server.Handler, _ ...server.MiddlewareFunc) {
	r.routes[method+" "+r.prefix+path] = h
}

func (r routeRecorder) Group(prefix string, _ ...server.MiddlewareFunc) server.RouteRegistrar {
	return routeRecorder{prefix: r.prefix + prefix, routes: r.routes}
}

func (routeRecorder) Use(...server.MiddlewareFunc) {}

func (r routeRecorder) FullPath(path string) string { return r.prefix + path }

// newTestEnvelopeHandler builds the recipe's handler over keys, with fresh fake operations.
func newTestEnvelopeHandler(t *testing.T, keys TenantKeys) (*envelopeHandler, *fakeOperations) {
	t.Helper()
	cfg, err := config.LoadFromMap(map[string]any{"example.envelope.signatureheader": signatureHeader})
	require.NoError(t, err)
	ops := &fakeOperations{}
	h, err := newEnvelopeHandler(cfg, keys, ops, logger.New("disabled", false))
	require.NoError(t, err)
	return h, ops
}

// serveRoute drives h's registered route ("METHOD /path") as tenantA and decodes its JSON reply.
func serveRoute(t *testing.T, h *envelopeHandler, route, contentType, body string, signatures ...string) (status int, reply map[string]string) {
	t.Helper()
	recorder := routeRecorder{routes: map[string]server.Handler{}}
	h.RegisterRoutes(server.NewHandlerRegistry(nil), recorder)
	handler, ok := recorder.routes[route]
	require.True(t, ok, "route %q not registered", route)
	method, path, _ := strings.Cut(route, " ")
	r := httptest.NewRequestWithContext(multitenant.SetTenant(t.Context(), tenantA), method, path, strings.NewReader(body))
	for _, s := range signatures {
		r.Header.Add(signatureHeader, s)
	}
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	require.NoError(t, handler(server.NewHandlerContextForTest(w, r, nil)))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	return w.Code, reply
}

// TestDetachedSignatureEnvelopeRecipeServesRegisteredRoutes runs binding and the raw response as on a
// live request; with no tenant keys, a request past the header and data checks stops at a 500.
func TestDetachedSignatureEnvelopeRecipeServesRegisteredRoutes(t *testing.T) {
	const ordersRoute, statusRoute = "POST /example/orders", "GET /example/orders/status"
	signed := []string{"JWS header..signature"}
	tests := []struct {
		name        string
		route       string
		contentType string
		body        string
		signatures  []string
		wantStatus  int
		wantCode    string
	}{
		{"missing_signature_header", ordersRoute, "application/json", `{"data":"x"}`, nil, http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"duplicated_signature_header", ordersRoute, "application/json", `{"data":"x"}`, append(signed, signed...), http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"signature_header_without_scheme", ordersRoute, "application/json", `{"data":"x"}`, []string{"header..signature"}, http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"body_without_json_content_type", ordersRoute, "text/plain", `{"data":"x"}`, signed, http.StatusBadRequest, codeDataMissing},
		{"json_body_reaches_key_lookup", ordersRoute, "application/json", `{"data":"x"}`, signed, http.StatusInternalServerError, codeInternal},
		{"bodyless_route_reaches_key_lookup", statusRoute, "", "", nil, http.StatusInternalServerError, codeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ops := newTestEnvelopeHandler(t, noTenantKeys{})
			status, reply := serveRoute(t, h, tt.route, tt.contentType, tt.body, tt.signatures...)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, map[string]string{"code": tt.wantCode}, reply)
			assert.Empty(t, ops.submitted)
			assert.Zero(t, ops.statusCalls)
		})
	}
}

// envelopeVector is one request as the peer sends it: the JWE for the body's data member,
// the detached signature over that exact string, and the attached form of the same JWS.
type envelopeVector struct {
	jwe      string
	detached string
	attached string
}

type envelopeFixture struct {
	keys          fakeTenantKeys
	peerSignPriv  *rsa.PrivateKey
	ourDecryptPub *rsa.PublicKey
	peerSealPriv  *rsa.PrivateKey
}

// envelopeKeys are the peer's signing, our decryption and the peer's sealing pairs, shared by
// every test here. Keys are read-only, so generating them per test would only cost time.
var envelopeKeys = sync.OnceValue(func() [3]*rsa.PrivateKey {
	var keys [3]*rsa.PrivateKey
	for i := range keys {
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		keys[i] = priv
	}
	return keys
})

func newEnvelopeFixture(t *testing.T) *envelopeFixture {
	t.Helper()
	keys := envelopeKeys()
	peerSign, ourDecrypt, peerSeal := keys[0], keys[1], keys[2]
	return &envelopeFixture{
		keys: fakeTenantKeys{
			tenantA:        {PeerVerify: &peerSign.PublicKey, OurDecrypt: ourDecrypt, PeerSeal: &peerSeal.PublicKey},
			tenantNoVerify: {OurDecrypt: ourDecrypt, PeerSeal: &peerSeal.PublicKey},
		},
		peerSignPriv:  peerSign,
		ourDecryptPub: &ourDecrypt.PublicKey,
		peerSealPriv:  peerSeal,
	}
}

func (f *envelopeFixture) newHandler(t *testing.T) (*envelopeHandler, *fakeOperations) {
	t.Helper()
	return newTestEnvelopeHandler(t, f.keys)
}

// mint plays the peer with go-jose directly: it encrypts order to our decrypt key and
// signs the JWE compact string with RS512, writing no kid on either token.
func (f *envelopeFixture) mint(t *testing.T, order string) envelopeVector {
	t.Helper()
	encrypter, err := jose.NewEncrypter(jose.A256GCM, jose.Recipient{Algorithm: jose.RSA_OAEP_256, Key: f.ourDecryptPub}, nil)
	require.NoError(t, err)
	encrypted, err := encrypter.Encrypt([]byte(order))
	require.NoError(t, err)
	jwe, err := encrypted.CompactSerialize()
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS512, Key: f.peerSignPriv}, nil)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(jwe))
	require.NoError(t, err)
	detached, err := signed.DetachedCompactSerialize()
	require.NoError(t, err)
	attached, err := signed.CompactSerialize()
	require.NoError(t, err)
	return envelopeVector{jwe: jwe, detached: detached, attached: attached}
}

// openReply plays the peer opening the reply's data: a bare JWE sealed to its seal key that
// must carry no kid, cty or typ header.
func (f *envelopeFixture) openReply(t *testing.T, sealed string) []byte {
	t.Helper()
	header, err := base64.RawURLEncoding.DecodeString(strings.SplitN(sealed, ".", 2)[0])
	require.NoError(t, err)
	var members map[string]any
	require.NoError(t, json.Unmarshal(header, &members))
	assert.NotContains(t, members, "kid")
	assert.NotContains(t, members, "cty")
	assert.NotContains(t, members, "typ")

	p := &bricksjose.Policy{
		Direction:        bricksjose.DirectionInbound,
		Mode:             bricksjose.SealModeBareJWE,
		DecryptKid:       peerSealKid,
		KeyAlg:           bricksjose.DefaultKeyAlg,
		Enc:              bricksjose.DefaultEnc,
		AcceptMissingKid: true,
	}
	plaintext, _ := jositest.OpenForTest(t, sealed, p, jositest.NewTestResolver(map[string]any{peerSealKid: f.peerSealPriv}))
	return plaintext
}

func envelopeContext(t *testing.T, method, tenantID string, signatures ...string) server.HandlerContext {
	t.Helper()
	r := httptest.NewRequestWithContext(multitenant.SetTenant(t.Context(), tenantID), method, "/example/orders", http.NoBody)
	for _, sig := range signatures {
		r.Header.Add(signatureHeader, sig)
	}
	return server.NewHandlerContextForTest(httptest.NewRecorder(), r, nil)
}

func TestDetachedSignatureEnvelopeRecipe(t *testing.T) {
	f := newEnvelopeFixture(t)
	v := f.mint(t, `{"amount":42}`)
	header := "JWS " + v.detached

	t.Run("round_trip", func(t *testing.T) {
		h, ops := f.newHandler(t)
		res, apiErr := h.submit(envelopeRequest{Data: v.jwe}, envelopeContext(t, http.MethodPost, tenantA, header))
		require.Nil(t, apiErr)
		assert.Zero(t, res.Status)
		assert.Empty(t, res.Data.Code)
		assert.JSONEq(t, receiptJSON, string(f.openReply(t, res.Data.Data)))
		assert.Equal(t, []Order{{Amount: 42}}, ops.submitted)
	})

	t.Run("bodyless_twin_seals_reply", func(t *testing.T) {
		h, ops := f.newHandler(t)
		res, apiErr := h.status(statusRequest{}, envelopeContext(t, http.MethodGet, tenantA, "JWS not-a-token"))
		require.Nil(t, apiErr)
		assert.Zero(t, res.Status)
		assert.JSONEq(t, receiptJSON, string(f.openReply(t, res.Data.Data)))
		assert.Equal(t, 1, ops.statusCalls)
	})

	t.Run("round_trip_through_registered_route", func(t *testing.T) {
		h, ops := f.newHandler(t)
		status, reply := serveRoute(t, h, "POST /example/orders", "application/json", `{"data":"`+v.jwe+`"}`, header)
		assert.Equal(t, http.StatusOK, status)
		require.Len(t, reply, 1, "the reply carries only its sealed data member")
		assert.JSONEq(t, receiptJSON, string(f.openReply(t, reply["data"])))
		assert.Equal(t, []Order{{Amount: 42}}, ops.submitted)
	})

	t.Run("bodyless_route_seals_reply", func(t *testing.T) {
		h, ops := f.newHandler(t)
		status, reply := serveRoute(t, h, "GET /example/orders/status", "", "")
		assert.Equal(t, http.StatusOK, status)
		assert.JSONEq(t, receiptJSON, string(f.openReply(t, reply["data"])))
		assert.Equal(t, 1, ops.statusCalls)
	})

	// Any change to the signed string breaks the signature, which Open checks before it decrypts.
	tampered := v.jwe + "A"
	invalid := f.mint(t, `{"amount":0}`)
	refusals := []struct {
		name       string
		data       string
		signatures []string
		tenantID   string
		wantStatus int
		wantCode   string
	}{
		{"missing_signature_header", v.jwe, nil, tenantA, http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"duplicated_signature_header", v.jwe, []string{header, header}, tenantA, http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"oversize_signature_header", v.jwe, []string{"JWS " + strings.Repeat("a", maxSignatureHeaderBytes)}, tenantA, http.StatusBadRequest, codeSignatureHeaderInvalid},
		{"signature_header_without_scheme", v.jwe, []string{v.detached}, tenantA, http.StatusUnauthorized, codeSignatureHeaderInvalid},
		{"empty_data", "", []string{header}, tenantA, http.StatusBadRequest, codeDataMissing},
		{"tampered_jwe", tampered, []string{header}, tenantA, http.StatusUnauthorized, "SIGNATURE_INVALID"},
		{"embedded_payload", v.jwe, []string{"JWS " + v.attached}, tenantA, http.StatusBadRequest, "MALFORMED"},
		{"invalid_order", invalid.jwe, []string{"JWS " + invalid.detached}, tenantA, http.StatusBadRequest, codePayloadInvalid},
		{"no_tenant", v.jwe, []string{header}, "", http.StatusInternalServerError, codeInternal},
		{"tenant_without_verify_key", v.jwe, []string{header}, tenantNoVerify, http.StatusInternalServerError, "KEY_UNKNOWN"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			h, ops := f.newHandler(t)
			res, apiErr := h.submit(envelopeRequest{Data: tt.data}, envelopeContext(t, http.MethodPost, tt.tenantID, tt.signatures...))
			require.Nil(t, apiErr)
			assert.Equal(t, tt.wantStatus, res.Status)
			assert.Equal(t, tt.wantCode, res.Data.Code)
			assert.Empty(t, res.Data.Data, "an error reply stays plaintext")
			assert.Empty(t, ops.submitted)
		})
	}
}
