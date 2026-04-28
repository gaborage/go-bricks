package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
)

// joseEchoServer simulates a JOSE-aware partner: it decrypts the request, echoes the
// plaintext back inside an encrypted response.
func joseEchoServer(t *testing.T, f *jositest.BidirectionalFixture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !jose.IsContentType(r.Header.Get("Content-Type")) {
			http.Error(w, `{"code":"JOSE_PLAINTEXT_REJECTED","message":"need jose"}`, http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		plaintext, _, _, err := jose.Open(string(body), f.PeerInbound, f.Resolver)
		if err != nil {
			http.Error(w, `{"code":"JOSE_DECRYPT_FAILED","message":"could not decrypt"}`, http.StatusUnauthorized)
			return
		}
		respPayload := []byte(`{"echo":` + string(plaintext) + `}`)
		compact, err := jose.Seal(respPayload, f.PeerOutbound, f.Resolver)
		require.NoError(t, err)
		w.Header().Set("Content-Type", jose.ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(compact))
	}))
}

func TestJOSETransportRoundtripEncryptsAndDecrypts(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	server := joseEchoServer(t, f)
	defer server.Close()

	transport := &httpclient.JOSETransport{
		Outbound: f.ClientOutbound,
		Inbound:  f.ClientInbound,
		Resolver: f.Resolver,
	}

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

	transport := &httpclient.JOSETransport{
		Outbound: f.ClientOutbound,
		Inbound:  f.ClientInbound,
		Resolver: f.Resolver,
	}

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

	transport := &httpclient.JOSETransport{
		Outbound: f.ClientOutbound,
		Inbound:  f.ClientInbound,
		Resolver: f.Resolver,
	}

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
		body, _ := io.ReadAll(r.Body)
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

func TestJOSETransportOutboundRequiresResolver(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	transport := &httpclient.JOSETransport{Outbound: f.ClientOutbound, Resolver: nil}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.invalid", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)

	_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip returns the configuration error before any HTTP exchange; no body to close
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyResolver")
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
	client := httpclient.NewBuilder(log).
		WithJOSE(httpclient.JOSEConfig{Outbound: f.ClientOutbound, Inbound: f.ClientInbound, Resolver: f.Resolver}).
		Build()

	resp, err := client.Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"echo":{"hello":"world"}}`, string(resp.Body))
}
