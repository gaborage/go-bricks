package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
)

// closeTrackingBody is a bytes.Reader-backed io.ReadCloser that counts Close calls, so
// tests can observe a RoundTripper's close-on-error obligation — and that it closes once.
type closeTrackingBody struct {
	r      *bytes.Reader
	closes int
}

// stubEnvelope adapts two functions to httpclient.BodyEnvelope so a test can vary one
// direction and leave the other at the identity. A nil wrap sends the compact as
// application/jose; a nil unwrap recognizes nothing.
type stubEnvelope struct {
	wrap   func(compact string) (body []byte, contentType string, err error)
	unwrap func(contentType string, body []byte) (compact string, ok bool)
}

func (e stubEnvelope) Wrap(compact string) (body []byte, contentType string, err error) {
	if e.wrap == nil {
		return []byte(compact), "application/jose", nil
	}
	return e.wrap(compact)
}

func (e stubEnvelope) Unwrap(contentType string, body []byte) (compact string, ok bool) {
	if e.unwrap == nil {
		return "", false
	}
	return e.unwrap(contentType, body)
}

func newCloseTrackingBody(s string) *closeTrackingBody {
	return &closeTrackingBody{r: bytes.NewReader([]byte(s))}
}

func (b *closeTrackingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *closeTrackingBody) Close() error               { b.closes++; return nil }

// joseEchoServer simulates a JOSE-aware partner: it decrypts the request, echoes the
// plaintext back inside an encrypted response.
//
// Handler runs in a goroutine spawned by httptest.NewServer, distinct from the test
// goroutine. Per the testing package docs, require.* (which calls FailNow → Goexit)
// must NOT be called from a non-test goroutine — it produces undefined behavior.
// The handler uses t.Errorf + early return for diagnostics that survive that boundary.
func joseEchoServer(t *testing.T, f *jositest.BidirectionalFixture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !jose.IsContentType(r.Header.Get("Content-Type")) {
			http.Error(w, `{"code":"JOSE_PLAINTEXT_REJECTED","message":"need jose"}`, http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("echo handler: read request body failed: %v", err)
			http.Error(w, `{"code":"JOSE_READ_FAILED","message":"could not read body"}`, http.StatusBadRequest)
			return
		}
		plaintext, _, _, err := jose.Open(string(body), f.PeerInbound, f.Resolver)
		if err != nil {
			http.Error(w, `{"code":"JOSE_DECRYPT_FAILED","message":"could not decrypt"}`, http.StatusUnauthorized)
			return
		}
		respPayload := []byte(`{"echo":` + string(plaintext) + `}`)
		compact, err := jose.Seal(respPayload, f.PeerOutbound, f.Resolver)
		if err != nil {
			t.Errorf("echo handler: seal response failed: %v", err)
			http.Error(w, `{"code":"JOSE_SEAL_FAILED","message":"could not seal response"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", jose.ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(compact))
	}))
}

// newJOSETransport builds the fully-wired bidirectional transport that most round-trip
// tests share, so a fixture-wiring change lands in one place.
func newJOSETransport(f *jositest.BidirectionalFixture) *httpclient.JOSETransport {
	return &httpclient.JOSETransport{
		Outbound: f.ClientOutbound,
		Inbound:  f.ClientInbound,
		Resolver: f.Resolver,
	}
}

// plainJSONServer answers status with a plaintext {"ok":true} after handing the request the
// transport actually put on the wire to assertReq.
//
// assertReq runs on httptest's handler goroutine (see joseEchoServer): t.Errorf only.
func plainJSONServer(t *testing.T, status int, assertReq func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertReq(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

func TestJOSETransportRoundtripEncryptsAndDecrypts(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := joseEchoServer(t, f)
	defer server.Close()

	transport := newJOSETransport(f)

	plaintextReq := `{"pan":"card-fixture-0000"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(plaintextReq)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"echo":{"pan":"card-fixture-0000"}}`, string(body))
}

func TestJOSETransportPreTrustErrorPassesThrough(t *testing.T) {
	// When the partner returns a plaintext error envelope (Content-Type: application/json),
	// the transport must NOT try to decrypt it — that's the GoBricks pre-trust failure
	// shape. Caller should see the plaintext body and the original status code.
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"JOSE_DECRYPT_FAILED","message":"bad key"}`))
	}))
	defer server.Close()

	transport := newJOSETransport(f)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"pan":"x"}`)))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"JOSE_DECRYPT_FAILED"`)
}

// TestJOSETransportPlaintextSuccessFailsClosed pins the rule ADR-107's amendment added: a
// 2xx body an Inbound policy never opened is refused, because nothing about it was
// authenticated. Both gates are covered — the nested-mode Content-Type rule, and an
// Envelope whose Unwrap declines the body.
func TestJOSETransportPlaintextSuccessFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		envelope bool
	}{
		{name: "nested_mode_200", status: http.StatusOK},
		{name: "nested_mode_201", status: http.StatusCreated},
		{name: "nested_mode_299", status: 299},
		{name: "envelope_mode_200", status: http.StatusOK, envelope: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := jositest.NewBidirectionalFixture(t)
			server := plainJSONServer(t, tt.status, func(*http.Request) {})
			defer server.Close()

			transport := newJOSETransport(f)
			if tt.envelope {
				// Recognizes nothing, so the refusal comes from the unwrap verdict rather
				// than a decrypt failure.
				transport.Envelope = stubEnvelope{}
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is nil on this error path; RoundTrip closed the peer's body
			require.Error(t, err)
			assert.Nil(t, resp, "a plaintext success must not reach the caller")
			require.ErrorIs(t, err, httpclient.ErrJOSEPlaintextResponse)
		})
	}
}

// TestJOSETransportPlaintextPassesThrough is the other half of that rule. Only a successful
// status must have been unwrapped, so a counterparty's pre-trust error envelope — plaintext
// by design — keeps reaching the caller with its headers untouched; and AllowPlaintextSuccess
// puts a 2xx back on that same path, which is the whole of the Strangler-migration knob.
func TestJOSETransportPlaintextPassesThrough(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		envelope       bool
		allowPlaintext bool
	}{
		{name: "unauthorized_401", status: http.StatusUnauthorized},
		{name: "forbidden_403", status: http.StatusForbidden},
		{name: "server_error_500", status: http.StatusInternalServerError},
		{name: "envelope_mode_500", status: http.StatusInternalServerError, envelope: true},
		{name: "allowed_200", status: http.StatusOK, allowPlaintext: true},
		{name: "allowed_envelope_mode_200", status: http.StatusOK, envelope: true, allowPlaintext: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := jositest.NewBidirectionalFixture(t)
			server := plainJSONServer(t, tt.status, func(*http.Request) {})
			defer server.Close()

			transport := newJOSETransport(f)
			transport.AllowPlaintextSuccess = tt.allowPlaintext
			if tt.envelope {
				transport.Envelope = stubEnvelope{}
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.status, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"), "pass-through must leave the header alone")
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.JSONEq(t, `{"ok":true}`, string(body))
		})
	}
}

// TestJOSETransportEnvelopeRefusalClosesTheBodyOnce pins the cleanup on the one refusal path
// that has already drained the peer's body: readAndCloseBody closed it, so RoundTrip must
// not close it a second time. net/http's own bodies tolerate that; a hand-rolled Inner's
// need not.
func TestJOSETransportEnvelopeRefusalClosesTheBodyOnce(t *testing.T) {
	tests := []struct {
		name     string
		envelope bool
	}{
		{name: "nested_mode_never_reads_the_body"},
		{name: "envelope_mode_reads_then_refuses", envelope: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := jositest.NewBidirectionalFixture(t)
			body := newCloseTrackingBody(`{"ok":true}`)

			transport := newJOSETransport(f)
			transport.Inner = fixedResponder{status: http.StatusOK, contentType: "application/json", body: body}
			if tt.envelope {
				transport.Envelope = stubEnvelope{}
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", http.NoBody)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is nil on this error path; the assertion below is that the transport closed the peer's body exactly once
			require.ErrorIs(t, err, httpclient.ErrJOSEPlaintextResponse)
			require.Nil(t, resp)
			assert.Equal(t, 1, body.closes, "the peer's body must be closed exactly once")
		})
	}
}

// fixedResponder hands back one prepared response, so a test can watch what the transport
// does to a body it controls.
type fixedResponder struct {
	status      int
	contentType string
	body        io.ReadCloser
}

func (r fixedResponder) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: r.status,
		Header:     http.Header{"Content-Type": []string{r.contentType}},
		Body:       r.body,
	}, nil
}

func TestJOSETransportTamperedResponseFailsClosed(t *testing.T) {
	// If the partner returns Content-Type: application/jose but the body is corrupted,
	// the transport MUST return an error (not stale-but-readable ciphertext) and close
	// the body. This is the security invariant on the client side.
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jose")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not.a.real.jose.payload"))
	}))
	defer server.Close()

	transport := newJOSETransport(f)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is intentionally nil on this error path; transport closes the underlying body before returning
	require.Error(t, err)
	assert.Nil(t, resp, "tampered response must not be returned to the caller")
	assert.True(t, httpclient.IsJOSEError(err), "error must be identifiable as a JOSE crypto failure")
}

func TestJOSETransportOutboundOnlyMode(t *testing.T) {
	// Some integrations send JOSE outbound but receive plaintext responses (one-way trust).
	// With Inbound: nil, the transport encrypts requests and passes responses through.
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/jose", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	transport := &httpclient.JOSETransport{
		Outbound: f.ClientOutbound,
		Inbound:  nil,
		Resolver: f.Resolver,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
}

func TestJOSETransportPassthroughWhenOutboundNil(t *testing.T) {
	// Defensive: a transport with no policies set should be transparent. This makes
	// JOSETransport safely composable inside a builder chain that may conditionally
	// enable JOSE.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("passthrough handler: read request body failed: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	transport := &httpclient.JOSETransport{}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"untouched":true}`)))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"untouched":true}`, string(body))
}

func TestJOSETransportRespectsMaxResponseBytes(t *testing.T) {
	// Defense-in-depth: an oversize body must produce an error before it is buffered
	// into memory in full. Both gates are covered — the default Content-Type rule, and
	// an Envelope, where the Content-Type gate no longer keeps a non-JOSE body unread
	// and the cap is the only thing standing between caller and unbounded buffer.
	tests := []struct {
		name        string
		contentType string
		envelope    bool
	}{
		{name: "jose_content_type_without_envelope", contentType: "application/jose"},
		{name: "json_content_type_with_envelope", contentType: "application/json", envelope: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := jositest.NewBidirectionalFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusOK)
				// Write a body larger than the cap. The transport should reject it without
				// attempting to decrypt — MaxBytesReader errors mid-stream.
				_, _ = w.Write(bytes.Repeat([]byte("A"), 1024))
			}))
			defer server.Close()

			transport := newJOSETransport(f)
			transport.MaxResponseBytes = 256 // well under the 1024-byte payload
			if tt.envelope {
				transport.Envelope = stubEnvelope{unwrap: func(_ string, body []byte) (compact string, ok bool) {
					t.Errorf("Envelope.Unwrap must not run on a body that exceeded the cap (%d bytes)", len(body))
					return "", false
				}}
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is intentionally nil on this error path; transport closes the underlying body before returning
			require.Error(t, err)
			assert.Nil(t, resp, "response exceeding MaxResponseBytes must not be returned to the caller")
			assert.Contains(t, err.Error(), "exceeds")
			// The overflow surfaces as a typed ClientError of category ValidationError so
			// callers can distinguish policy failures from I/O errors via IsErrorType.
			assert.True(t, httpclient.IsErrorType(err, httpclient.ValidationError),
				"oversize response must be a typed ValidationError, got %T: %v", err, err)
		})
	}
}

func TestJOSETransportOutboundRequiresResolver(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	transport := &httpclient.JOSETransport{Outbound: f.ClientOutbound, Resolver: nil}
	body := newCloseTrackingBody(`{}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.invalid", body)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip returns the configuration error before any HTTP exchange; no body to close
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyResolver")
	assert.Equal(t, 1, body.closes, "RoundTrip must close req.Body exactly once when it fails before reading it")
}

func TestJOSETransportOutboundRequiresResolverNilBody(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	transport := &httpclient.JOSETransport{Outbound: f.ClientOutbound, Resolver: nil}
	//nolint:gocritic // literal nil, not http.NoBody, keeps req.Body nil to hit the guarded path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", nil)
	require.NoError(t, err)

	// A nil req.Body (e.g. a GET) must reach the error return without a close attempt.
	_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip returns the configuration error before any HTTP exchange; no body to close
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyResolver")
}

// assertRequestNotSealed pins the pass-through shape: nothing was stamped onto a request
// that carried no body. Runs on httptest's handler goroutine, so t.Errorf only.
func assertRequestNotSealed(t *testing.T, method string, r *http.Request, body []byte, ct string) {
	t.Helper()
	if ct != "" {
		t.Errorf("bodyless %s must not gain a Content-Type, got %q", method, ct)
	}
	if len(body) != 0 {
		t.Errorf("bodyless %s must not gain a body, got %d bytes", method, len(body))
	}
	if r.ContentLength != 0 {
		t.Errorf("bodyless %s must keep ContentLength 0, got %d", method, r.ContentLength)
	}
}

// assertRequestSealed is the negative control's other half: a request that does carry a
// body is still sealed, with the plaintext off the wire and the ciphertext openable by the
// peer. Same goroutine caveat as above.
func assertRequestSealed(t *testing.T, f *jositest.BidirectionalFixture, pan string, r *http.Request, body []byte, ct string) {
	t.Helper()
	if ct != jose.ContentType {
		t.Errorf("sealed request Content-Type = %q, want %q", ct, jose.ContentType)
	}
	if r.ContentLength == 0 {
		t.Errorf("sealed request must carry a non-zero ContentLength, got %d", r.ContentLength)
	}
	if bytes.Contains(body, []byte(pan)) {
		t.Error("sealed request body must not contain the plaintext")
	}
	if _, _, _, err := jose.Open(string(body), f.PeerInbound, f.Resolver); err != nil {
		t.Errorf("seal handler: request body did not open as JOSE: %v", err)
	}
}

func TestJOSETransportSealsOnlyWhenTheRequestCarriesABody(t *testing.T) {
	// The skip is keyed on body presence, not on method: a payload-free POST passes through
	// exactly like a bodyless GET, and http.NoBody counts as bodyless despite being non-nil.
	// An unset body field stays a nil io.Reader, so http.NewRequestWithContext leaves
	// req.Body nil — the shape a real GET carries.
	const pan = "card-fixture-0000"
	f := jositest.NewBidirectionalFixture(t)

	tests := []struct {
		name   string
		method string
		body   io.Reader
		sealed bool
	}{
		{name: "get_nil_body", method: http.MethodGet},
		{name: "head_nil_body", method: http.MethodHead},
		{name: "delete_nil_body", method: http.MethodDelete},
		{name: "post_nil_body", method: http.MethodPost},
		{name: "get_nobody_sentinel", method: http.MethodGet, body: http.NoBody},
		// Negative control: proves the guard is not over-broad — this row must start
		// failing if the skip ever widens to cover requests that do have a body.
		{name: "post_with_payload", method: http.MethodPost, body: bytes.NewReader([]byte(`{"pan":"` + pan + `"}`)), sealed: true},
		// Streaming body: ContentLength is unknown, so a guard keyed on length instead of
		// presence would stop sealing chunked uploads while every other row stayed green.
		{name: "post_streaming_body", method: http.MethodPost, body: io.NopCloser(strings.NewReader(`{"pan":"` + pan + `"}`)), sealed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := plainJSONServer(t, http.StatusOK, func(r *http.Request) {
				ct := r.Header.Get("Content-Type")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("seal handler: read request body failed: %v", err)
					return
				}
				if tc.sealed {
					assertRequestSealed(t, f, pan, r, body, ct)
					return
				}
				assertRequestNotSealed(t, tc.method, r, body, ct)
			})
			defer server.Close()

			req, err := http.NewRequestWithContext(context.Background(), tc.method, server.URL, tc.body)
			require.NoError(t, err)

			transport := newJOSETransport(f)
			// The request half is what this table pins; the plaintext 2xx the stub answers
			// with would otherwise be refused before any of it is asserted.
			transport.AllowPlaintextSuccess = true

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestJOSETransportBodylessRequestStillUnwrapsJOSEResponse(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		compact, err := jose.Seal([]byte(`{"status":"active"}`), f.PeerOutbound, f.Resolver)
		if err != nil {
			t.Errorf("unwrap handler: seal response failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", jose.ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(compact))
	}))
	defer server.Close()

	//nolint:gocritic // literal nil, not http.NoBody, reproduces a real GET's nil req.Body
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := newJOSETransport(f).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"active"}`, string(body))
}

func TestJOSETransportJOSETypedHeadResponseIsNotUnwrapped(t *testing.T) {
	// A real origin answers HEAD with the same headers it would put on the GET, so a JOSE
	// endpoint advertises application/jose on a response that carries no body at all.
	// Attempting jose.Open on that empty body would surface as a bogus JOSE_MALFORMED.
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jose.ContentType)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	//nolint:gocritic // literal nil, not http.NoBody, reproduces a real HEAD's nil req.Body
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, server.URL, nil)
	require.NoError(t, err)

	resp, err := newJOSETransport(f).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, jose.ContentType, resp.Header.Get("Content-Type"), "pass-through must leave the header alone")
}

func TestJOSETransportBodylessResponsesAreNotUnwrapped(t *testing.T) {
	// 1xx, 204, 304 and every reply to HEAD carry no body by definition. Driven through a
	// stub Inner rather than an httptest server because net/http's server strips
	// Content-Type from a 304 (RFC 7232 §4.1), so a real Go peer cannot produce the
	// JOSE-typed 304 shape under test.
	//
	// The plaintext rows are also the boundary of the fail-closed rule: a 204 is successful
	// and unprotected, but net/http GUARANTEES it carries no body, so there is no plaintext
	// to mistake for a decrypted payload. Refusing them would break every DELETE and every
	// conditional GET against a JOSE peer.
	f := jositest.NewBidirectionalFixture(t)

	tests := []struct {
		name        string
		method      string
		status      int
		contentType string
	}{
		{name: "switching_protocols_101", method: http.MethodGet, status: http.StatusSwitchingProtocols},
		{name: "no_content_204", method: http.MethodDelete, status: http.StatusNoContent},
		{name: "not_modified_304", method: http.MethodDelete, status: http.StatusNotModified},
		// The HEAD skip must read the method from the request the transport was handed, not
		// from resp.Request: *http.Transport back-fills that field, so keying on it would
		// pass this suite while silently resuming unwrap of empty HEAD bodies behind a
		// custom Inner. bodylessResponder leaves resp.Request nil, so this row pins that.
		{name: "head_reply_200", method: http.MethodHead, status: http.StatusOK},
		{name: "plaintext_no_content_204", method: http.MethodDelete, status: http.StatusNoContent, contentType: "application/json"},
		{name: "plaintext_not_modified_304", method: http.MethodGet, status: http.StatusNotModified, contentType: "application/json"},
		{name: "plaintext_head_reply_200", method: http.MethodHead, status: http.StatusOK, contentType: "application/json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := newJOSETransport(f)
			transport.Inner = bodylessResponder{status: tc.status, contentType: tc.contentType}

			//nolint:gocritic // literal nil, not http.NoBody, reproduces a real GET/DELETE/HEAD's nil req.Body
			req, err := http.NewRequestWithContext(context.Background(), tc.method, "http://example.invalid", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			wantType := tc.contentType
			if wantType == "" {
				wantType = jose.ContentType
			}
			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, wantType, resp.Header.Get("Content-Type"), "pass-through must leave the header alone")
		})
	}
}

func TestJOSETransportRFCBodylessButNotNetHTTPBodylessStillUnwraps(t *testing.T) {
	// 205 and a 2xx answer to CONNECT carry no body per RFC 9110, but net/http's
	// bodyAllowedForStatus and noResponseBodyExpected cover neither, so it reads a body on
	// both. Adding them to the skip set would therefore hand a peer's unverified bytes to
	// the caller under a status code the peer chose — the same bypass the empty-body case
	// below guards. They must keep reaching jose.Open and failing closed.
	f := jositest.NewBidirectionalFixture(t)

	tests := []struct {
		name   string
		method string
		status int
	}{
		{name: "reset_content_205", method: http.MethodGet, status: http.StatusResetContent},
		{name: "connect_tunnel_200", method: http.MethodConnect, status: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := newJOSETransport(f)
			transport.Inner = bodylessResponder{status: tc.status}

			req, err := http.NewRequestWithContext(context.Background(), tc.method, "http://example.invalid", http.NoBody)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is intentionally nil on this error path; transport closes the underlying body before returning
			require.Error(t, err, "%s must not be skipped: net/http does not guarantee it is empty", tc.name)
			assert.True(t, httpclient.IsJOSEError(err), "expected a JOSE error, got %v", err)
			assert.Nil(t, resp)
		})
	}
}

func TestJOSETransportEmptyJOSEBodyOnBodyBearingStatusFailsClosed(t *testing.T) {
	// Negative control for the bodyless guard. A 200 advertising application/jose with no
	// body is a ciphertext stripped in transit, not a legitimate empty response, and must
	// keep erroring. Without this row, widening the guard to "ContentLength == 0" passes
	// every other test in this file while turning a truncation into a silent empty success.
	f := jositest.NewBidirectionalFixture(t)
	transport := newJOSETransport(f)
	transport.Inner = bodylessResponder{status: http.StatusOK}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is intentionally nil on this error path; transport closes the underlying body before returning
	require.Error(t, err, "a JOSE-typed 200 carrying no body must not pass through unverified")
	assert.True(t, httpclient.IsJOSEError(err), "expected a JOSE error, got %v", err)
	assert.Nil(t, resp)
}

// bodylessResponder replies with an empty-bodied response at a fixed status, mimicking what
// net/http hands back for a 204/304/HEAD reply. An empty contentType advertises
// jose.ContentType. It deliberately leaves Response.Request nil: *http.Transport back-fills
// that field but the RoundTripper contract does not require an Inner to, so this is the
// shape a custom Inner can produce.
type bodylessResponder struct {
	status      int
	contentType string
}

func (b bodylessResponder) RoundTrip(_ *http.Request) (*http.Response, error) {
	contentType := b.contentType
	if contentType == "" {
		contentType = jose.ContentType
	}
	return &http.Response{
		StatusCode: b.status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       http.NoBody,
	}, nil
}

func TestIsJOSEErrorDistinguishesTransportFromCrypto(t *testing.T) {
	// IsJOSEError lets callers skip retries on signature failures while still retrying
	// on TCP resets. Plain net errors must not classify as JOSE errors.
	assert.False(t, httpclient.IsJOSEError(errors.New("tcp reset by peer")))
	assert.True(t, httpclient.IsJOSEError(&jose.Error{Sentinel: jose.ErrDecryptFailed, Code: "JOSE_DECRYPT_FAILED"}))
}

func TestBuilderWithJOSEWiresTransport(t *testing.T) {
	// End-to-end through the Builder: WithJOSE should produce a working client.
	f := jositest.NewBidirectionalFixture(t)
	server := joseEchoServer(t, f)
	defer server.Close()

	log := logger.New("info", false)
	client, err := httpclient.NewBuilder(log).
		WithJOSE(httpclient.JOSEConfig{Outbound: f.ClientOutbound, Inbound: f.ClientInbound, Resolver: f.Resolver}).
		Build()
	require.NoError(t, err)

	resp, err := client.Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"echo":{"hello":"world"}}`, string(resp.Body))
}

// TestBuilderWithJOSEForwardsAllowPlaintextSuccess pins that the config knob reaches the
// transport: without the copy in WithJOSE, a client built with it set would still refuse
// the plaintext 2xx.
func TestBuilderWithJOSEForwardsAllowPlaintextSuccess(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := plainJSONServer(t, http.StatusOK, func(*http.Request) {})
	defer server.Close()

	client, err := httpclient.NewBuilder(logger.New("info", false)).
		WithJOSE(httpclient.JOSEConfig{
			Outbound:              f.ClientOutbound,
			Inbound:               f.ClientInbound,
			Resolver:              f.Resolver,
			AllowPlaintextSuccess: true,
		}).
		Build()
	require.NoError(t, err)

	resp, err := client.Post(context.Background(), &httpclient.Request{URL: server.URL, Body: []byte(`{"x":1}`)})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"ok":true}`, string(resp.Body))
}

// TestBuilderWithJOSEFailsClosedOnAnEnvelopeWithoutAPolicy pins the pairing rule. Wrap is
// consulted only for an Outbound policy and Unwrap only for an Inbound one, so an Envelope
// with neither would be a silent no-op at request time. Either policy alone is legitimate:
// one-directional protection is a supported shape, and only the empty pair is refused.
func TestBuilderWithJOSEFailsClosedOnAnEnvelopeWithoutAPolicy(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	log := logger.New("info", false)

	client, err := httpclient.NewBuilder(log).WithJOSE(httpclient.JOSEConfig{
		Resolver: f.Resolver,
		Envelope: httpclient.VisaMLEEnvelope(),
	}).Build()

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "Envelope requires an Outbound or Inbound policy")
	// Same contract as every other Build JOSE failure: a *jose.Error carrying a
	// matchable sentinel, not a bare errors.New.
	assert.True(t, jose.IsError(err), "envelope wiring failure must be a *jose.Error, got %T", err)
	require.ErrorIs(t, err, jose.ErrPolicyMismatch)

	var joseErr *jose.Error
	require.ErrorAs(t, err, &joseErr)
	assert.Equal(t, "JOSE_POLICY_ENVELOPE_UNPAIRED", joseErr.Code)
}

// TestBuilderWithJOSEAcceptsAnEnvelopeWithOneDirection is the other side of that rule:
// an Envelope beside a single policy is a supported configuration, not a half-wiring.
func TestBuilderWithJOSEAcceptsAnEnvelopeWithOneDirection(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	log := logger.New("info", false)

	tests := []struct {
		name string
		cfg  httpclient.JOSEConfig
	}{
		{
			name: "outbound_only",
			cfg:  httpclient.JOSEConfig{Outbound: f.ClientOutbound, Resolver: f.Resolver, Envelope: httpclient.VisaMLEEnvelope()},
		},
		{
			name: "inbound_only",
			cfg:  httpclient.JOSEConfig{Inbound: f.ClientInbound, Resolver: f.Resolver, Envelope: httpclient.VisaMLEEnvelope()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := httpclient.NewBuilder(log).WithJOSE(tt.cfg).Build()

			require.NoError(t, err)
			assert.NotNil(t, client)
		})
	}
}

func TestBuilderWithJOSERefusesAnUnboundedCapWithAnEnvelope(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	log := logger.New("info", false)

	client, err := httpclient.NewBuilder(log).WithJOSE(httpclient.JOSEConfig{
		Inbound:          f.ClientInbound,
		Resolver:         f.Resolver,
		Envelope:         httpclient.VisaMLEEnvelope(),
		MaxResponseBytes: -1, // "unbounded" — every response body buffered without limit
	}).Build()

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "Envelope cannot be combined with an unbounded MaxResponseBytes")
	assert.True(t, jose.IsError(err), "unbounded-cap failure must be a *jose.Error, got %T", err)
	require.ErrorIs(t, err, jose.ErrPolicyMismatch)

	var joseErr *jose.Error
	require.ErrorAs(t, err, &joseErr)
	assert.Equal(t, "JOSE_POLICY_ENVELOPE_UNBOUNDED", joseErr.Code)
}

func TestJOSETransportEnvelopeWrapErrorAbortsBeforeSending(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request must not reach the server when Envelope.Wrap fails")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sentinel := errors.New("envelope unavailable")
	transport := newJOSETransport(f)
	transport.Envelope = stubEnvelope{wrap: func(string) (body []byte, contentType string, err error) {
		return nil, "", sentinel
	}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req) //nolint:bodyclose // resp is nil: RoundTrip failed before any HTTP exchange
	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, sentinel)
}

// TestJOSETransportEnvelopeWrapContentTypeHeader pins what an empty contentType from
// Envelope.Wrap means on the wire. Setting it would put a bare "Content-Type:", which
// jose.IsContentType rejects and a JOSE-aware peer can refuse; defaulting it back to
// application/jose would mislabel a body the hook deliberately wrapped in another format.
// Deleting the header is the only reading that matches "the hook wants no media type".
func TestJOSETransportEnvelopeWrapContentTypeHeader(t *testing.T) {
	tests := []struct {
		name                string
		envelopeContentType string
		wantPresent         bool
		wantValue           string
	}{
		{name: "named_media_type_is_advertised", envelopeContentType: "application/json", wantPresent: true, wantValue: "application/json"},
		{name: "empty_media_type_deletes_the_header", envelopeContentType: "", wantPresent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPresent bool
			var gotValue string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, gotPresent = r.Header[http.CanonicalHeaderKey("Content-Type")]
				gotValue = r.Header.Get("Content-Type")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			f := jositest.NewBidirectionalFixture(t)
			transport := newJOSETransport(f)
			transport.Inbound = nil // responses are out of scope here
			transport.Envelope = stubEnvelope{wrap: func(compact string) (body []byte, contentType string, err error) {
				return []byte(compact), tt.envelopeContentType, nil
			}}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"x":1}`)))
			require.NoError(t, err)
			// A caller-set Content-Type must not survive an empty hook verdict either.
			req.Header.Set("Content-Type", "application/json")

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			assert.Equal(t, tt.wantPresent, gotPresent, "Content-Type header presence on the wire")
			assert.Equal(t, tt.wantValue, gotValue)
		})
	}
}

// TestJOSETransportUnboundedCapRule pins the fail-closed rule on the one path Build
// cannot guard: JOSETransport is exported, so a hand-built one can carry any combination.
// An Envelope replaces the application/jose Content-Type gate with Unwrap's verdict, and
// Unwrap needs the bytes, so Envelope + Inbound + a negative cap is an unbounded read of
// a peer's body. RoundTrip refuses exactly that trio before any network call, rather than
// quietly substituting a default, which would hide the misconfiguration. Every other
// combination keeps working, including the documented negative-cap escape hatch.
func TestJOSETransportUnboundedCapRule(t *testing.T) {
	tests := []struct {
		name         string
		withEnvelope bool
		withInbound  bool
		maxBytes     int64
		wantRefused  bool
	}{
		{name: "envelope_and_inbound_and_negative_cap_is_refused", withEnvelope: true, withInbound: true, maxBytes: -1, wantRefused: true},
		// Without an envelope the Content-Type gate still decides which bodies are read,
		// so a negative cap stays the caller's documented choice.
		{name: "negative_cap_without_an_envelope_is_allowed", withInbound: true, maxBytes: -1},
		// Outbound-only reads nothing at all, so the cap cannot matter.
		{name: "envelope_and_negative_cap_without_inbound_is_allowed", withEnvelope: true, maxBytes: -1},
		// Zero is "unset", not "unbounded" — it takes DefaultMaxJOSEBodyBytes.
		{name: "envelope_and_inbound_with_a_default_cap_is_allowed", withEnvelope: true, withInbound: true, maxBytes: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := jositest.NewBidirectionalFixture(t)
			var reached bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"plaintext":true}`))
			}))
			defer server.Close()

			transport := newJOSETransport(f)
			transport.MaxResponseBytes = tt.maxBytes
			// The cap rule is the verdict under test; the stub's plaintext 2xx must not
			// stand in for it.
			transport.AllowPlaintextSuccess = true
			if !tt.withInbound {
				transport.Inbound = nil
			}
			if tt.withEnvelope {
				// Recognizes nothing, so an allowed case passes the body through and the
				// verdict under test stays the cap rule rather than a decrypt failure.
				transport.Envelope = stubEnvelope{}
			}

			body := newCloseTrackingBody(`{"x":1}`)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, body)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			if !tt.wantRefused {
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				assert.True(t, reached, "an allowed configuration must still send the request")
				return
			}

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.False(t, reached, "the request must not be sent when the cap is unbounded")
			assert.Equal(t, 1, body.closes, "the request body must be closed exactly once when the round trip is refused")

			var joseErr *jose.Error
			require.ErrorAs(t, err, &joseErr)
			assert.Equal(t, "JOSE_POLICY_ENVELOPE_UNBOUNDED", joseErr.Code)
		})
	}
}
