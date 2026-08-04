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

// closeTrackingBody is a bytes.Reader-backed io.ReadCloser that records whether
// Close was called, so tests can observe a RoundTripper's close-on-error obligation.
type closeTrackingBody struct {
	r      *bytes.Reader
	closed bool
}

func newCloseTrackingBody(s string) *closeTrackingBody {
	return &closeTrackingBody{r: bytes.NewReader([]byte(s))}
}

func (b *closeTrackingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *closeTrackingBody) Close() error               { b.closed = true; return nil }

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

// plainJSONServer answers with a plaintext {"ok":true} after handing the request the
// transport actually put on the wire to assertReq.
//
// assertReq runs on httptest's handler goroutine (see joseEchoServer): t.Errorf only.
func plainJSONServer(t *testing.T, assertReq func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertReq(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

func TestJOSETransportRoundtripEncryptsAndDecrypts(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := joseEchoServer(t, f)
	defer server.Close()

	transport := newJOSETransport(f)

	plaintextReq := `{"pan":"4111111111111111"}`
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
	assert.JSONEq(t, `{"echo":{"pan":"4111111111111111"}}`, string(body))
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
	// Defense-in-depth: a JOSE-aware peer (or attacker) returning a Content-Type:
	// application/jose body larger than MaxResponseBytes must produce an error before
	// the body is buffered into memory in full.
	f := jositest.NewBidirectionalFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jose")
		w.WriteHeader(http.StatusOK)
		// Write a body larger than the cap. The transport should reject it without
		// attempting to decrypt — io.LimitReader caps the read at maxBytes+1.
		_, _ = w.Write(bytes.Repeat([]byte("A"), 1024))
	}))
	defer server.Close()

	transport := newJOSETransport(f)
	transport.MaxResponseBytes = 256 // well under the 1024-byte payload

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
	assert.True(t, body.closed, "RoundTrip must close req.Body even when it fails before reading it")
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
	const pan = "4111111111111111"
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
			server := plainJSONServer(t, func(r *http.Request) {
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

			resp, err := newJOSETransport(f).RoundTrip(req)
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
	f := jositest.NewBidirectionalFixture(t)

	tests := []struct {
		name   string
		method string
		status int
	}{
		{name: "switching_protocols_101", method: http.MethodGet, status: http.StatusSwitchingProtocols},
		{name: "no_content_204", method: http.MethodDelete, status: http.StatusNoContent},
		{name: "not_modified_304", method: http.MethodDelete, status: http.StatusNotModified},
		// The HEAD skip must read the method from the request the transport was handed, not
		// from resp.Request: *http.Transport back-fills that field, so keying on it would
		// pass this suite while silently resuming unwrap of empty HEAD bodies behind a
		// custom Inner. bodylessResponder leaves resp.Request nil, so this row pins that.
		{name: "head_reply_200", method: http.MethodHead, status: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := newJOSETransport(f)
			transport.Inner = bodylessResponder{status: tc.status}

			//nolint:gocritic // literal nil, not http.NoBody, reproduces a real GET/DELETE/HEAD's nil req.Body
			req, err := http.NewRequestWithContext(context.Background(), tc.method, "http://example.invalid", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, jose.ContentType, resp.Header.Get("Content-Type"), "pass-through must leave the header alone")
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

// bodylessResponder replies with a JOSE-typed, empty-bodied response at a fixed status,
// mimicking what net/http hands back for a 204/304/HEAD reply. It deliberately leaves
// Response.Request nil: *http.Transport back-fills that field but the RoundTripper contract
// does not require an Inner to, so this is the shape a custom Inner can produce.
type bodylessResponder struct{ status int }

func (b bodylessResponder) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: b.status,
		Header:     http.Header{"Content-Type": []string{jose.ContentType}},
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
