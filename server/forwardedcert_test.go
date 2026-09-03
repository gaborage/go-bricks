package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// Test fixtures for the ALB verify-mode identity headers.
//
// testForwardedCertLeaf is a real self-signed certificate, URL-encoded the
// way AWS ALB encodes X-Amzn-Mtls-Clientcert-Leaf (percent-encoding with
// "+=/ as safe characters": spaces -> %20, newlines -> %0A, +/=/- left
// literal — see https://docs.aws.amazon.com/elasticloadbalancing/latest/application/mutual-authentication.html,
// retrieved 2026-07-27). It deliberately contains a literal '+' in its base64
// body (e.g. "...GQ764rRgGq7W...", "...zGQFAvjO7RNGQ764rRg..."): swapping the
// decoder to url.QueryUnescape corrupts that '+' into a space, and pem.Decode
// then finds no block. This is the vector TestParseForwardedClientCertVectors'
// mutation check pins.
const (
	testForwardedCertSubject = "CN=partner.example.com,OU=partner-test,O=mTLS,C=US"
	testForwardedCertSerial  = "3C:B4:97:12:38:6F:92:4A:6D:79:05:EE:A7:63:00:6C:B9:69:E6:7B"

	testForwardedCertLeaf = `-----BEGIN%20CERTIFICATE-----%0A` +
		`MIIDgzCCAmugAwIBAgIUPLSXEjhvkklteQXup2MAbLlp5nswDQYJKoZIhvcNAQEL%0A` +
		`BQAwUTELMAkGA1UEBhMCVVMxDTALBgNVBAoMBG1UTFMxFTATBgNVBAsMDHBhcnRu%0A` +
		`ZXItdGVzdDEcMBoGA1UEAwwTcGFydG5lci5leGFtcGxlLmNvbTAeFw0yNjA3Mjcx%0A` +
		`NjUxMDlaFw0yNzA3MjcxNjUxMDlaMFExCzAJBgNVBAYTAlVTMQ0wCwYDVQQKDARt%0A` +
		`VExTMRUwEwYDVQQLDAxwYXJ0bmVyLXRlc3QxHDAaBgNVBAMME3BhcnRuZXIuZXhh%0A` +
		`bXBsZS5jb20wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDheepMtfdQ%0A` +
		`8zGQFAvjO7RNGQ764rRgGq7WzfqwaMimGtqtaIk/oc+srU9N3rHSURCrjvyxQY9h%0A` +
		`SSswTFj1nusrbIaA/y/vQAYv07q+4ON9DbJ9NrBtkz4BC76SE5lRj8U1KMD98m7+%0A` +
		`mRgUrciNClQMaPYczDilfBVLIFBTmVSjEid81KZYe5BpLHZXJO3ZvGXtpWalJEDC%0A` +
		`3z+hHW5EWy63RUyqXIqUXBvBhKd9RpA5L6aypfOE+nlyXvUYGrI3Wz06+5cbdRF3%0A` +
		`g6yR86YjnBTbIUQTdzS0VoBEvjtqv7fxDo0a/g84R5T6Z/xWpnfPfi5dNDU2yxRe%0A` +
		`2ZIfBysLXRg9AgMBAAGjUzBRMB0GA1UdDgQWBBQJsNHVXyHQcET2Xc+bkLXs8fVS%0A` +
		`VDAfBgNVHSMEGDAWgBQJsNHVXyHQcET2Xc+bkLXs8fVSVDAPBgNVHRMBAf8EBTAD%0A` +
		`AQH/MA0GCSqGSIb3DQEBCwUAA4IBAQDHbljL4Ix3Csl8XhhydCpl+BWohQBoXDRc%0A` +
		`hZhi5R7I/n2lfqHF50peMb2utVoFgW2yaYi+SNmmAI6YQdUCfcwIL84aHenIgGgE%0A` +
		`v6LLRANf4BcQ2OtQwyKvUeTQlqaXscFeQPNn41aB2DBrWunWy2QMFnTSlPhKMPyT%0A` +
		`vGMs9VP9YSXNb2t49HtkC2a/W2wfGl2MrbxEG4lYK+gwCNHO3hvAuWsUtd39qVkm%0A` +
		`Bx6HVU6IVATMSM1lF3LOPMDGw5AtsmT3MDiw4rAtZSipcGPCHrEA15WI3R/anDtN%0A` +
		`R2KopQc5AzWHnDVvWQasywAdkM2P0Y/t2jz/CnGK9foe8aTNuCss%0A` +
		`-----END%20CERTIFICATE-----%0A`
)

// testForwardedCertLeafCorrupted derives from testForwardedCertLeaf by
// replacing a run of base64 body characters (spanning a %0A line-break token)
// with garbage of identical length. It still url.PathUnescape-decodes cleanly
// (the corruption never touches a %XX escape sequence's own bytes), but
// pem.Decode can no longer find a well-formed block.
var testForwardedCertLeafCorrupted = strings.Replace(testForwardedCertLeaf,
	"YJKoZIhvcNAQEL%0ABQA", strings.Repeat("Z", len("YJKoZIhvcNAQEL%0ABQA")), 1)

// forwardedCertVectorCase is a single TestParseForwardedClientCertVectors
// table row. Named (rather than anonymous) so assertForwardedCertVector can
// take it as a parameter.
type forwardedCertVectorCase struct {
	name            string
	headers         map[string][]string
	wantSubject     string // expected cert.Subject in every non-error case (may be empty)
	wantSerial      string // expected cert.SerialNumber in every non-error case (may be empty)
	wantNoIdent     bool   // errNoForwardedCert: neither Subject nor Serial-Number present
	wantLeafOK      bool   // Leaf decoded and parsed cleanly
	wantLeafFail    bool   // identity present, Leaf failed to decode (err != nil, not errNoForwardedCert)
	wantDuplicate   bool   // errDuplicateForwardedHeader: some header carried more than one value
	wantErrContains string // when set, asserted against err's message (distinguishes cap-rejection from ordinary parse failure)
}

// TestParseForwardedClientCertVectors pins parseForwardedClientCert's pure
// (non-HTTP) parsing rules against a header-getter fixture.
func TestParseForwardedClientCertVectors(t *testing.T) {
	tests := []forwardedCertVectorCase{
		{
			name: "full_verify_mode_set_with_plus_in_leaf",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertIssuer:       {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
				headerClientCertLeaf:         {testForwardedCertLeaf},
			},
			wantSubject: testForwardedCertSubject,
			wantSerial:  testForwardedCertSerial,
			wantLeafOK:  true,
		},
		{
			name: "subject_only_no_leaf",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
			},
			wantSubject: testForwardedCertSubject,
			wantSerial:  testForwardedCertSerial,
		},
		{
			name: "subject_only",
			headers: map[string][]string{
				headerClientCertSubject: {testForwardedCertSubject},
			},
			wantSubject: testForwardedCertSubject,
			wantSerial:  "",
		},
		{
			name: "serial_only",
			headers: map[string][]string{
				headerClientCertSerialNumber: {testForwardedCertSerial},
			},
			wantSubject: "",
			wantSerial:  testForwardedCertSerial,
		},
		{
			name: "corrupted_leaf",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
				headerClientCertLeaf:         {testForwardedCertLeafCorrupted},
			},
			wantSubject:  testForwardedCertSubject,
			wantSerial:   testForwardedCertSerial,
			wantLeafFail: true,
		},
		{
			name:        "nothing_present",
			headers:     map[string][]string{},
			wantNoIdent: true,
		},
		{
			name: "oversized_leaf_rejected_without_parse_attempt",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
				headerClientCertLeaf:         {strings.Repeat("A", maxForwardedLeafBytes+1)},
			},
			wantSubject:     testForwardedCertSubject,
			wantSerial:      testForwardedCertSerial,
			wantLeafFail:    true,
			wantErrContains: "byte cap",
		},
		{
			name: "duplicated_subject",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject, "CN=forged.example.com"},
				headerClientCertSerialNumber: {testForwardedCertSerial},
			},
			wantDuplicate:   true,
			wantErrContains: headerClientCertSubject,
		},
		{
			name: "duplicated_serial_number",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial, "FORGED"},
			},
			wantDuplicate:   true,
			wantErrContains: headerClientCertSerialNumber,
		},
		{
			name: "duplicated_issuer",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
				headerClientCertIssuer:       {testForwardedCertSubject, "CN=forged-issuer.example.com"},
			},
			wantDuplicate:   true,
			wantErrContains: headerClientCertIssuer,
		},
		{
			name: "duplicated_leaf",
			headers: map[string][]string{
				headerClientCertSubject:      {testForwardedCertSubject},
				headerClientCertSerialNumber: {testForwardedCertSerial},
				headerClientCertLeaf:         {testForwardedCertLeaf, testForwardedCertLeafCorrupted},
			},
			wantDuplicate:   true,
			wantErrContains: headerClientCertLeaf,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getter := func(name string) []string { return tt.headers[name] }
			cert, err := parseForwardedClientCert(getter)
			assertForwardedCertVector(t, &tt, cert, err)
		})
	}
}

// assertForwardedCertVector asserts a single forwardedCertVectorCase's
// expectations against parseForwardedClientCert's result. Extracted from
// TestParseForwardedClientCertVectors so the table-driven loop itself stays
// flat (SonarCloud go:S3776) — semantics are unchanged from the inline form.
func assertForwardedCertVector(t *testing.T, tt *forwardedCertVectorCase, cert ForwardedClientCert, err error) {
	t.Helper()

	if tt.wantDuplicate {
		require.ErrorIs(t, err, errDuplicateForwardedHeader)
		require.NotErrorIs(t, err, errNoForwardedCert, "a duplicate must never be confused with absence")
		assert.Equal(t, ForwardedClientCert{}, cert, "a duplicate must return the zero value — no Subject, Issuer, SerialNumber, or Leaf leaks")
		if tt.wantErrContains != "" {
			assert.ErrorContains(t, err, tt.wantErrContains)
		}
		return
	}

	if tt.wantNoIdent {
		require.ErrorIs(t, err, errNoForwardedCert)
		assert.Empty(t, cert.Subject)
		return
	}

	assert.Equal(t, tt.wantSubject, cert.Subject)
	assert.Equal(t, tt.wantSerial, cert.SerialNumber)

	switch {
	case tt.wantLeafOK:
		require.NoError(t, err)
		require.NotNil(t, cert.Leaf)
		assert.Equal(t, testForwardedCertSubject, cert.Leaf.Subject.String())
	case tt.wantLeafFail:
		require.Error(t, err)
		require.NotErrorIs(t, err, errNoForwardedCert, "a Leaf-only failure must never be confused with absence")
		assert.Nil(t, cert.Leaf)
		if tt.wantErrContains != "" {
			assert.ErrorContains(t, err, tt.wantErrContains)
		}
	default:
		require.NoError(t, err)
		assert.Nil(t, cert.Leaf)
	}
}

// TestForwardedClientCertMiddleware exercises forwardedClientCertMiddlewareEcho
// end-to-end through the echo request/response cycle.
func TestForwardedClientCertMiddleware(t *testing.T) {
	t.Run("disabled_is_inert", func(t *testing.T) {
		// No middleware registered at all — mirrors what SetupMiddlewares does
		// when cfg.Server.ForwardedClientCert.Enabled is false: it never wires
		// forwardedClientCertMiddlewareEcho, so the accessor must report absent
		// regardless of what headers a caller sends.
		e := newTenantTestEcho()
		e.GET("/check", func(c *echo.Context) error {
			_, ok := ForwardedClientCertFromContext(c.Request().Context())
			assert.False(t, ok, "accessor must report absent when the middleware never ran")
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		req.Header.Set(headerClientCertSubject, testForwardedCertSubject)
		req.Header.Set(headerClientCertSerialNumber, testForwardedCertSerial)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("enabled_exposes_identity", func(t *testing.T) {
		e := newTenantTestEcho()
		e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true}, nil, &testLogger{}))

		var gotSubject string
		var gotOK bool
		e.GET("/check", func(c *echo.Context) error {
			cert, ok := ForwardedClientCertFromContext(c.Request().Context())
			gotSubject, gotOK = cert.Subject, ok
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		req.Header.Set(headerClientCertSubject, testForwardedCertSubject)
		req.Header.Set(headerClientCertSerialNumber, testForwardedCertSerial)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, gotOK)
		assert.Equal(t, testForwardedCertSubject, gotSubject)
	})

	t.Run("require_rejects_missing", func(t *testing.T) {
		capturer := &capturingLogger{}
		e := newTenantTestEcho()
		e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: true}, nil, capturer))
		e.GET("/check", func(c *echo.Context) error {
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Len(t, capturer.warns, 1, "exactly one WARN per rejected request")
		assert.Contains(t, capturer.warns[0], `reason="forwarded_client_cert_missing"`)
		assert.Contains(t, capturer.warns[0], "status=401")
		assert.Contains(t, rec.Body.String(), "error", "standard {error,meta} envelope, not a hand-built body")
		assert.NotContains(t, rec.Body.String(), "X-Amzn-Mtls", "the response must never echo a header name or value")
	})

	t.Run("require_passes_on_corrupt_leaf", func(t *testing.T) {
		capturer := &capturingLogger{}
		e := newTenantTestEcho()
		e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: true}, nil, capturer))

		var gotOK, gotLeafNil bool
		e.GET("/check", func(c *echo.Context) error {
			cert, ok := ForwardedClientCertFromContext(c.Request().Context())
			gotOK = ok
			gotLeafNil = cert.Leaf == nil
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		req.Header.Set(headerClientCertSubject, testForwardedCertSubject)
		req.Header.Set(headerClientCertSerialNumber, testForwardedCertSerial)
		req.Header.Set(headerClientCertLeaf, testForwardedCertLeafCorrupted)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "a Leaf-only decode failure is never a rejection — the ALB-verified Subject stands")
		assert.True(t, gotOK)
		assert.True(t, gotLeafNil)
		require.Len(t, capturer.warns, 1, "the lost Leaf enrichment still leaves a WARN trail")
		assert.Contains(t, capturer.warns[0], "leaf certificate not decoded")
		assert.NotContains(t, capturer.warns[0], testForwardedCertLeafCorrupted, "header content must never be logged")
	})

	t.Run("require_skips_health", func(t *testing.T) {
		e := newTenantTestEcho()
		skipper := CreateProbeSkipper("/health", "/ready")
		e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: true}, skipper, &testLogger{}))
		e.GET("/health", func(c *echo.Context) error {
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "ALB health checks carry no client certificate — Require must never take the target group down")
	})

	// client_supplied_headers_note pins the documented trust model (see
	// wiki/forwarded_client_cert.md, "Trust model"): with Require=false,
	// whatever X-Amzn-Mtls-Clientcert-* headers arrive get parsed and exposed.
	// Provenance is never established here — it rests entirely on the
	// deployment posture (mTLS-verify ALB listener + closed security groups +
	// single ingress path to the target group), not on any AWS header-
	// stripping guarantee (public AWS docs are silent on that point) and not
	// on any in-app check.
	t.Run("client_supplied_headers_note", func(t *testing.T) {
		e := newTenantTestEcho()
		e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: false}, nil, &testLogger{}))

		var gotOK bool
		var gotSubject string
		e.GET("/check", func(c *echo.Context) error {
			cert, ok := ForwardedClientCertFromContext(c.Request().Context())
			gotOK, gotSubject = ok, cert.Subject
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		req.Header.Set(headerClientCertSubject, "CN=forged-identity.example.com")
		req.Header.Set(headerClientCertSerialNumber, "FORGED")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, gotOK, "the middleware parses whatever headers arrive; it cannot verify their provenance")
		assert.Equal(t, "CN=forged-identity.example.com", gotSubject)
	})
}

// TestForwardedClientCertMiddlewareDuplicateHeaders exercises the duplicate
// case end-to-end, one header at a time: a header sent twice is never
// trusted, regardless of Require — same shape as absent identity (fail
// closed under Require, fail open without it), never first-value-wins.
func TestForwardedClientCertMiddlewareDuplicateHeaders(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{name: "subject", header: headerClientCertSubject},
		{name: "serial_number", header: headerClientCertSerialNumber},
		{name: "issuer", header: headerClientCertIssuer},
		{name: "leaf", header: headerClientCertLeaf},
	}

	buildRequest := func(dupHeader string) *http.Request {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/check", http.NoBody)
		req.Header.Set(headerClientCertSubject, testForwardedCertSubject)
		req.Header.Set(headerClientCertSerialNumber, testForwardedCertSerial)
		req.Header.Add(dupHeader, "duplicate-value-1")
		req.Header.Add(dupHeader, "duplicate-value-2")
		return req
	}

	for _, tc := range cases {
		t.Run(tc.name+"_require_true_rejects", func(t *testing.T) {
			capturer := &capturingLogger{}
			e := newTenantTestEcho()
			e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: true}, nil, capturer))
			e.GET("/check", func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, buildRequest(tc.header))

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.Len(t, capturer.warns, 1, "exactly one WARN per rejected request")
			assert.Contains(t, capturer.warns[0], `reason="forwarded_client_cert_duplicate"`)
			assert.Contains(t, capturer.warns[0], tc.header, "the WARN must name which header was duplicated")
		})

		t.Run(tc.name+"_require_false_proceeds_without_identity", func(t *testing.T) {
			capturer := &capturingLogger{}
			e := newTenantTestEcho()
			e.Use(forwardedClientCertMiddlewareEcho(config.ForwardedClientCertConfig{Enabled: true, Require: false}, nil, capturer))

			var gotOK bool
			e.GET("/check", func(c *echo.Context) error {
				_, gotOK = ForwardedClientCertFromContext(c.Request().Context())
				return c.String(http.StatusOK, "ok")
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, buildRequest(tc.header))

			require.Equal(t, http.StatusOK, rec.Code, "a duplicated header is never rejected outright when Require is false")
			assert.False(t, gotOK, "a duplicated header must never attach an identity — no first-value-wins")
			require.Len(t, capturer.warns, 1, "the duplicate still leaves a WARN trail even when not required")
			assert.Contains(t, capturer.warns[0], `reason="forwarded_client_cert_duplicate"`)
			assert.Contains(t, capturer.warns[0], tc.header, "the WARN must name which header was duplicated")
		})
	}
}

// TestForwardedClientCertWarnsEscapeRequestValues varies the attacker-written
// dimension of the three forwarded-client-cert WARN sinks: method, path and
// client must reach the log escaped on both logger paths.
func TestForwardedClientCertWarnsEscapeRequestValues(t *testing.T) {
	tests := []struct {
		name string
		emit func(l logger.Logger, c *echo.Context)
	}{
		{name: "rejection", emit: logForwardedClientCertRejection},
		{name: "duplicate_warning", emit: func(l logger.Logger, c *echo.Context) {
			logForwardedClientCertDuplicateWarning(l, c, errors.New("dup"))
		}},
		{name: "leaf_warning", emit: func(l logger.Logger, c *echo.Context) {
			logForwardedClientCertLeafWarning(l, c, errors.New("parse"), "leaf")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRejectionLogEscapesRequestValues(t, tt.emit)
		})
	}
}
