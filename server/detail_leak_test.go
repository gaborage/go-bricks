package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// panShapedKey is the marker every leak assertion below hunts for. It is
// PAN-shaped on purpose: the redaction rule has no digits-only exemption
// precisely because a card number is all digits.
const panShapedKey = "4111111111111111"

type limitsRequest struct {
	Limits map[string]int `json:"limits" validate:"required,dive,gte=100"`
}

type mapFreeRequest struct {
	Amount int64  `json:"amount"`
	Name   string `json:"name" validate:"required"`
}

func devDebugConfig() *config.Config {
	return &config.Config{App: config.AppConfig{Env: config.EnvDevelopment, Debug: true}}
}

// postJSON drives a typed handler end to end and returns the decoded envelope
// alongside the raw body, because "the raw key appears nowhere in the body" is a
// claim about the bytes, not about the fields a struct happened to decode.
func postJSON[T any, R any](t *testing.T, cfg *config.Config, handler func(T, HandlerContext) (R, IAPIError), body string) (resp APIResponse, raw string, status int) {
	t.Helper()

	e := echo.New()
	e.Validator = NewValidator()
	h := WrapHandler(handler, NewRequestBinder(), cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	require.NoError(t, h(e.NewContext(req, rec)))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	return resp, rec.Body.String(), rec.Code
}

func echoLimits(req limitsRequest, _ HandlerContext) (limitsRequest, IAPIError) { return req, nil }

// TestValidationDetailsRedactMapKeyAndDropValue is the empirical half of #1175:
// a real request, a real validator, a real response body.
func TestValidationDetailsRedactMapKeyAndDropValue(t *testing.T) {
	resp, raw, status := postJSON(t, devDebugConfig(), echoLimits,
		`{"limits":{"`+panShapedKey+`-SECRET":1}}`)

	require.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, resp.Error)
	require.NotNil(t, resp.Error.Details, "precondition: debug+dev renders details")
	require.Contains(t, resp.Error.Details, "validationErrors",
		"precondition: the validation failure actually happened")

	assert.Contains(t, raw, "Limits[*]", "the redacted namespace is what reaches the body")
	assert.NotContains(t, raw, panShapedKey, "input map key must not reach the response body")
	assert.NotContains(t, raw, `"value"`, "FieldError no longer carries the rejected value")

	// The rejected value itself (1) is unquotable as a substring, so assert the
	// field shape instead: exactly field + message, nothing else.
	entries, ok := resp.Error.Details["validationErrors"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	entry, ok := entries[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"field", "message"}, sortedKeys(entry))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}

// TestBindDetailsWithholdFieldPathForMapBearingType pins the type-gated half:
// the request type reaches an input path, so no field path is rendered.
func TestBindDetailsWithholdFieldPathForMapBearingType(t *testing.T) {
	resp, raw, status := postJSON(t, devDebugConfig(), echoLimits,
		`{"limits":{"`+panShapedKey+`":"not-a-number"}}`)

	require.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, resp.Error)
	assert.Equal(t, "Invalid request data", resp.Error.Message,
		"precondition: this is the bind path, not the validation path")

	detail := requireDetailString(t, resp)
	// The byte offset moves with the toolchain's decoder, so pin the shape: the
	// wanted type renders, the field path does not.
	assert.True(t, strings.HasPrefix(detail, "json: type mismatch (want int, offset "), "got %q", detail)
	assert.NotContains(t, detail, "field", "a map-bearing type renders no field path")
	assert.NotContains(t, raw, panShapedKey, "input map key must not reach the response body")
	assert.NotContains(t, raw, "not-a-number", "payload bytes must not reach the response body")
}

// TestBindDetailsKeepFieldPathForMapFreeType is the other side of the gate: a
// map-free request type keeps the destination field name, which is schema.
func TestBindDetailsKeepFieldPathForMapFreeType(t *testing.T) {
	handler := func(req mapFreeRequest, _ HandlerContext) (mapFreeRequest, IAPIError) { return req, nil }

	resp, raw, status := postJSON(t, devDebugConfig(), handler, `{"amount":"`+panShapedKey+`"}`)

	require.Equal(t, http.StatusBadRequest, status)
	detail := requireDetailString(t, resp)
	assert.Contains(t, detail, `field "amount"`, "a schema-safe type keeps its field path")
	assert.NotContains(t, raw, panShapedKey, "the rejected literal must not reach the response body")
	assert.Equal(t, http.StatusBadRequest, status)
}

func requireDetailString(t *testing.T, resp APIResponse) string {
	t.Helper()

	require.NotNil(t, resp.Error)
	require.NotNil(t, resp.Error.Details)
	detail, ok := resp.Error.Details["error"].(string)
	require.True(t, ok, "details should carry a string \"error\" entry")

	return detail
}

// detailsGateQuadrants is shared by both renderers' gate tests, so the matrix
// cannot drift between the enveloped and the raw-mode assertion.
var detailsGateQuadrants = []struct {
	name        string
	env         string
	debug       bool
	wantDetails bool
}{
	{name: "debug_on_development", env: config.EnvDevelopment, debug: true, wantDetails: true},
	{name: "debug_off_development", env: config.EnvDevelopment},
	{name: "debug_on_production", env: config.EnvProduction, debug: true},
	{name: "debug_off_production", env: config.EnvProduction},
}

// TestResponseDetailsGateQuadrants walks all four debug × environment
// combinations: details render for exactly one of them.
func TestResponseDetailsGateQuadrants(t *testing.T) {
	for _, tc := range detailsGateQuadrants {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{App: config.AppConfig{Env: tc.env, Debug: tc.debug}}
			resp, _, status := postJSON(t, cfg, echoLimits, `{"limits":{"a":1}}`)

			require.Equal(t, http.StatusBadRequest, status,
				"precondition: the request fails validation in every quadrant")
			require.NotNil(t, resp.Error)
			if tc.wantDetails {
				assert.NotNil(t, resp.Error.Details)
				return
			}
			assert.Nil(t, resp.Error.Details)
		})
	}
}

// TestRawResponseDetailsGateQuadrants pins the same gate on the raw-mode
// renderer, which shares devDetails.
func TestRawResponseDetailsGateQuadrants(t *testing.T) {
	for _, tc := range detailsGateQuadrants {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{App: config.AppConfig{Env: tc.env, Debug: tc.debug}}
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody), rec)

			apiErr := NewBadRequestError("nope")
			apiErr.WithDetails("error", "some detail")
			require.NoError(t, formatRawErrorResponse(c, apiErr, cfg))

			var payload rawErrorPayload
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			if tc.wantDetails {
				assert.NotNil(t, payload.Details)
				return
			}
			assert.Nil(t, payload.Details)
		})
	}
}

// TestBindSummaryFailsClosed pins the substitution: an error that is not a
// *bindError renders the fixed phrase, never its own text.
func TestBindSummaryFailsClosed(t *testing.T) {
	assert.Equal(t, unauditedBindSummary, bindSummary(assertAnError{}))
	assert.Equal(t, unauditedBindSummary, bindSummary(&bindError{err: assertAnError{}}))
	assert.Equal(t, "audited", bindSummary(&bindError{summary: "audited", err: assertAnError{}}))
}

type assertAnError struct{}

func (assertAnError) Error() string { return "raw cause with " + panShapedKey }

// TestBindErrorNilSafety pins the guards: a nil receiver and a nil cause are
// both reachable from a hand-built value and must not panic.
func TestBindErrorNilSafety(t *testing.T) {
	var nilErr *bindError

	assert.Equal(t, unauditedBindSummary, nilErr.Error())
	assert.NoError(t, nilErr.Unwrap())
	assert.Equal(t, unauditedBindSummary, (&bindError{}).Error())
	assert.NoError(t, (&bindError{}).Unwrap())
	assert.Equal(t, assertAnError{}, (&bindError{err: assertAnError{}}).Unwrap())
}

// TestBindErrorKeepsRawCauseForLogs pins the split: the response reads the
// summary, the log path still reads the cause (#1168 owns that seam).
func TestBindErrorKeepsRawCauseForLogs(t *testing.T) {
	err := newFieldBindError(bindSourceQuery, "ratio", assertAnError{})

	assert.Contains(t, err.Error(), panShapedKey, "Error() keeps the cause for logging")
	assert.Equal(t, `failed to bind query param "ratio"`, bindSummary(err))
}

// TestValidationFieldSurvivesNamespaceTruncation is the regression guard for the
// bypass the redaction's first shape had: validator stores FieldError.Field's
// length in a uint8 and slices the namespace by it, so a key long enough to push
// the namespace past 255 bytes made Field() return a bracket-free suffix of the
// key itself — which any bracket-based rule copies through verbatim. The key
// length is the caller's, so the cut lands wherever the caller wants it.
func TestValidationFieldSurvivesNamespaceTruncation(t *testing.T) {
	// 250 filler bytes plus the PAN puts the cut inside the key for the shape
	// that read Field(); the assertion below is on the emitted bytes.
	key := strings.Repeat("A", 250) + panShapedKey
	resp, raw, status := postJSON(t, devDebugConfig(), echoLimits,
		`{"limits":{"`+key+`":1}}`)

	require.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, resp.Error)
	require.Contains(t, resp.Error.Details, "validationErrors",
		"precondition: the validation failure actually happened")

	assert.Contains(t, raw, "Limits[*]", "the field must still read as a redacted span")
	assert.NotContains(t, raw, panShapedKey, "the input map key must not reach the response body")
	assert.NotContains(t, raw, "AAAA", "no run of the key may reach the response body")
}

// --- JOSE post-trust axis (#1163) -------------------------------------------
//
// The JOSE envelope used to copy IAPIError.Details() to the wire ungated, on the
// reasoning that ciphertext to an authenticated peer is not disclosure. It is:
// the peer decrypts and frequently logs the body, so a production envelope must
// carry the same fields as the standard channel. These tests pin the funnel.

// postJOSE drives a JOSE-protected route end to end under cfg and returns the
// DECRYPTED envelope. Decrypting is the point: an assertion against the
// ciphertext would pass no matter what the envelope contains.
// The fixture is a parameter, not built here: its two RSA keypairs cost ~40ms
// each and are invariant across every quadrant, so the 16-subtest matrix shares
// one fixture instead of generating 32 keys it cannot tell apart.
func postJOSE(t *testing.T, f *joseFixture, cfg *config.Config, handler HandlerFunc[joseTokenReq, joseTokenResp], body string) (envelope map[string]any, status int) {
	t.Helper()

	e, h := newJOSETestServerWithConfig(t, f, cfg, handler)

	compactReq := jositest.SealForTest(t, []byte(body), f.peerOutbound(), f.resolver)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tokens", bytes.NewReader([]byte(compactReq)))
	req.Header.Set(echo.HeaderContentType, jose.ContentType)
	rec := httptest.NewRecorder()

	require.NoError(t, h(e.NewContext(req, rec)))
	require.Equal(t, jose.ContentType, rec.Header().Get(echo.HeaderContentType),
		"precondition: a post-trust error must still be encrypted, else this test proves nothing about the JOSE renderer")

	plainResp, _ := jositest.OpenForTest(t, rec.Body.String(), f.peerInbound(), f.resolver)
	require.NoError(t, json.Unmarshal(plainResp, &envelope))
	return envelope, rec.Code
}

// joseErrorObject returns the decrypted envelope's error object.
func joseErrorObject(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	errObj, ok := envelope["error"].(map[string]any)
	require.True(t, ok, "envelope must carry a nested error object, got %v", envelope)
	return errObj
}

// detailsBearingBadRequest is the single fixture the two channels are compared
// on. Both TestJOSEDetailsMatchStandardChannel handlers build their error here,
// so the comparison cannot drift into comparing two different errors.
func detailsBearingBadRequest() IAPIError {
	apiErr := NewBadRequestError("nope")
	apiErr.WithDetails("error", "some detail")
	return apiErr
}

func joseDetailsHandler(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
	return joseTokenResp{}, detailsBearingBadRequest()
}

func joseInternalHandler(_ joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
	return joseTokenResp{}, NewInternalServerError("boom")
}

func joseOKHandler(req joseTokenReq, _ HandlerContext) (joseTokenResp, IAPIError) {
	return joseTokenResp{Token: "tok-" + req.Pan}, nil
}

// joseDetailSources are the ways an IAPIError with details reaches the JOSE
// renderer. Each must obey the gate, so a leak reintroduced on any single one of
// them fails here rather than hiding behind the others.
var joseDetailSources = []struct {
	name    string
	body    string
	handler HandlerFunc[joseTokenReq, joseTokenResp]
}{
	{name: "bind_failure", body: `{"pan":`, handler: joseOKHandler},
	{name: "validation_failure", body: `{}`, handler: joseOKHandler},
	{name: "handler_with_details", body: `{"pan":"` + panShapedKey + `"}`, handler: joseDetailsHandler},
	{name: "unhandled_5xx", body: `{"pan":"` + panShapedKey + `"}`, handler: joseInternalHandler},
}

// TestJOSEDetailsGateQuadrants is the #1163 regression pin: the encrypted
// post-trust envelope renders details in exactly the quadrant the enveloped and
// raw renderers do. It reuses detailsGateQuadrants so the JOSE matrix cannot
// drift from theirs.
func TestJOSEDetailsGateQuadrants(t *testing.T) {
	// Stack capture on: it gives the unhandled-5xx source a details map it would
	// not otherwise have, and it makes the production-quadrant assertions bite on
	// the worst thing this envelope could disclose — server file paths.
	withStackCapture(t, true)
	f := newJOSEFixture(t)

	for _, src := range joseDetailSources {
		for _, q := range detailsGateQuadrants {
			t.Run(src.name+"/"+q.name, func(t *testing.T) {
				cfg := &config.Config{App: config.AppConfig{Env: q.env, Debug: q.debug}}
				envelope, status := postJOSE(t, f, cfg, src.handler, src.body)

				require.GreaterOrEqual(t, status, http.StatusBadRequest,
					"precondition: this source must fail in every quadrant")
				errObj := joseErrorObject(t, envelope)
				assert.Contains(t, errObj, "code", "code survives the gate in every quadrant")

				if q.wantDetails {
					assert.Contains(t, errObj, "details",
						"debug+development is the one quadrant that discloses details")
					return
				}
				assert.NotContains(t, errObj, "details",
					"SECURITY REGRESSION (#1163): production JOSE envelope disclosed error.details")
			})
		}
	}
}

// TestJOSEDetailsMatchStandardChannel is the other half of the funnel claim: in
// the disclosing quadrant the JOSE envelope must render the SAME details the
// enveloped renderer does for the same error, stackTrace included. Asserting
// only absence would leave a renderer free to disclose something different.
func TestJOSEDetailsMatchStandardChannel(t *testing.T) {
	withStackCapture(t, true)
	cfg := devDebugConfig()

	envelope, _ := postJOSE(t, newJOSEFixture(t), cfg, joseDetailsHandler, `{"pan":"`+panShapedKey+`"}`)
	joseDetails, ok := joseErrorObject(t, envelope)["details"].(map[string]any)
	require.True(t, ok, "debug+development JOSE envelope must carry a details object")

	// Same IAPIError shape through the standard enveloped renderer.
	standard, _, _ := postJSON(t, cfg, func(_ mapFreeRequest, _ HandlerContext) (mapFreeRequest, IAPIError) {
		return mapFreeRequest{}, detailsBearingBadRequest()
	}, `{"amount":1,"name":"x"}`)
	require.NotNil(t, standard.Error)
	require.NotNil(t, standard.Error.Details)

	assert.Equal(t, standard.Error.Details["error"], joseDetails["error"],
		"the two renderers must disclose the same handler-set detail")
	assert.Contains(t, joseDetails, stackTraceDetailKey,
		"stackTrace is injected by devDetails, so funneling must carry it onto the JOSE envelope too")
	assert.Contains(t, standard.Error.Details, stackTraceDetailKey,
		"precondition: the standard channel injects stackTrace for this fixture")
}
