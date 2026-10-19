package jose_test

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	bricksjose "github.com/gaborage/go-bricks/jose"
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
)

var errUnknownKey = errors.New("unknown key name")

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

// TenantKeys is the consumer's per-tenant key source. ForTenant runs on every request,
// so it returns keys already parsed and cached.
type TenantKeys interface {
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
	receiptID       = "receipt-1"
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

func (noTenantKeys) ForTenant(context.Context, string) (TenantKeySet, error) {
	return TenantKeySet{}, errors.New("unknown tenant")
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
