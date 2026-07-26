package httpclient

import (
	"context"
	"crypto"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// mapKeys is a minimal PrivateKeyResolver backed by a name -> key map.
type mapKeys map[string]*rsa.PrivateKey

func (m mapKeys) PrivateKey(name string) (*rsa.PrivateKey, error) {
	key, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("mapKeys: no key named %q", name)
	}
	return key, nil
}

// spyBody wraps a *strings.Reader and records whether Close was called, so
// tests can assert http.RoundTripper's close-on-every-path contract without
// depending on a real file descriptor or pipe.
type spyBody struct {
	r      *strings.Reader
	closed bool
}

func newSpyBody(s string) *spyBody {
	return &spyBody{r: strings.NewReader(s)}
}

func (s *spyBody) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *spyBody) Close() error {
	s.closed = true
	return nil
}

// oauth1TestKeyName is the fixed key name used across this file's tests.
const oauth1TestKeyName = "signing-key"

// oauth1SharedTestKey generates one 2048-bit RSA key, shared by every test in
// the package. No test needs key isolation — each verifies a signature
// against its own key's public half — and re-generating one per test is real
// wall-clock cost on the Ubuntu×Windows CI matrix.
var oauth1SharedTestKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		panic(err) // test-only fixture: crypto/rand failing here means the environment is broken
	}
	return key
})

func generateOAuth1TestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	return oauth1SharedTestKey()
}

// newOAuth1TestConfig returns an OAuth1Config wired to the shared test key,
// plus the key itself (for verifying signatures against its public half).
// The negative-path configs (nil Keys / a resolver that always errors) keep
// their own literals — they are deliberately different.
func newOAuth1TestConfig(t *testing.T) (OAuth1Config, *rsa.PrivateKey) {
	t.Helper()
	key := generateOAuth1TestKey(t)
	return OAuth1Config{
		ConsumerKey: "consumer123",
		KeyName:     oauth1TestKeyName,
		Keys:        mapKeys{oauth1TestKeyName: key},
	}, key
}

// oauth1Capture is a mutex-guarded record of what an httptest handler
// observed, appended once per request it serves. The handler always runs on
// a goroutine distinct from the test goroutine, and only the actual
// request/response synchronizes them, so unguarded field writes are a race
// -race can flag under scheduling that isn't guaranteed to repeat.
type oauth1Capture struct {
	mu          sync.Mutex
	authHeaders []string
	bodies      [][]byte
}

// record appends one request's Authorization header and body under the
// mutex. Safe to call from an httptest handler goroutine.
func (c *oauth1Capture) record(authHeader string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authHeaders = append(c.authHeaders, authHeader)
	c.bodies = append(c.bodies, body)
}

// Hits reports how many requests have been recorded so far.
func (c *oauth1Capture) Hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.authHeaders)
}

// AuthHeaders returns a copy of every Authorization header recorded so far.
func (c *oauth1Capture) AuthHeaders() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.authHeaders)
}

// Bodies returns a copy of every request body recorded so far.
func (c *oauth1Capture) Bodies() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.bodies)
}

// newCapturingServer starts an httptest server whose handler records every
// request's Authorization header and body into the returned capture, then
// responds 200 with an empty body. Handler failures use t.Errorf (not
// require.*): the handler runs on a goroutine distinct from the test
// goroutine, where require.*'s FailNow (-> runtime.Goexit) is undefined
// behavior per the testing package docs.
func newCapturingServer(t *testing.T) (*httptest.Server, *oauth1Capture) {
	t.Helper()
	capture := &oauth1Capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server: read body failed: %v", err)
			return
		}
		capture.record(r.Header.Get(headerAuthorization), body)
		w.WriteHeader(http.StatusOK)
	}))
	return server, capture
}

// parseOAuth1AuthHeader splits an "OAuth k="v", k="v"" header value into a
// plain map. Every value except oauth_signature was emitted raw by the
// transport, so only oauth_signature is percent-decoded here.
func parseOAuth1AuthHeader(t *testing.T, header string) map[string]string {
	t.Helper()
	require.True(t, strings.HasPrefix(header, "OAuth "))
	rest := strings.TrimPrefix(header, "OAuth ")
	pairs := strings.Split(rest, ", ")
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		eq := strings.IndexByte(pair, '=')
		require.GreaterOrEqual(t, eq, 0, "malformed header pair %q", pair)
		key := pair[:eq]
		val, err := strconv.Unquote(pair[eq+1:])
		require.NoError(t, err)
		if key == oauthSignatureParam {
			decoded, err := url.QueryUnescape(val)
			require.NoError(t, err)
			val = decoded
		}
		out[key] = val
	}
	return out
}

// reconstructRequestURL rebuilds the absolute URL the client signed, from the
// server's view of the request (r.Host + r.URL's path/query). This mirrors
// what OAuth1Transport saw as clone.URL when it computed the signature.
func reconstructRequestURL(r *http.Request) *url.URL {
	return &url.URL{
		Scheme:   "http",
		Host:     r.Host,
		Path:     r.URL.Path,
		RawPath:  r.URL.RawPath,
		RawQuery: r.URL.RawQuery,
	}
}

func TestOAuth1TransportSetsAuthorizationHeader(t *testing.T) {
	// Method deliberately left unset: this test pins the zero-value
	// default (RSA-SHA256) end-to-end.
	cfg, _ := newOAuth1TestConfig(t)

	const sentBody = `{"hello":"world"}`
	server, capture := newCapturingServer(t)
	defer server.Close()

	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(sentBody))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 1, capture.Hits())
	authHeader := capture.AuthHeaders()[0]
	receivedBody := capture.Bodies()[0]

	require.True(t, strings.HasPrefix(authHeader, "OAuth "))
	assert.Contains(t, authHeader, `oauth_consumer_key="consumer123"`)
	assert.Contains(t, authHeader, `oauth_signature_method="RSA-SHA256"`,
		"zero-value Method must normalize to RSA-SHA256, not emit an empty string")
	assert.Contains(t, authHeader, `oauth_version="1.0"`)

	sum := sha256.Sum256(receivedBody)
	wantBodyHash := base64.StdEncoding.EncodeToString(sum[:])
	assert.Contains(t, authHeader, fmt.Sprintf("oauth_body_hash=%q", wantBodyHash))

	assert.Equal(t, sentBody, string(receivedBody), "the signed body must reach the server intact")
}

func TestOAuth1TransportSignatureVerifies(t *testing.T) {
	cfg, key := newOAuth1TestConfig(t)

	// This test needs method+URL alongside the header, which oauth1Capture doesn't
	// carry, so it stays a bespoke server — but still under mutex discipline: the
	// handler runs on a goroutine distinct from the test goroutine, and only the
	// actual request/response synchronizes them.
	var mu sync.Mutex
	var authHeader string
	var capturedMethod string
	var capturedURL *url.URL

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeader = r.Header.Get(headerAuthorization)
		capturedMethod = r.Method
		capturedURL = reconstructRequestURL(r)
		mu.Unlock()
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/verify?x=1",
		strings.NewReader(`{"a":1}`))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	gotAuthHeader, gotMethod, gotURL := authHeader, capturedMethod, capturedURL
	mu.Unlock()

	params := parseOAuth1AuthHeader(t, gotAuthHeader)
	signature := params[oauthSignatureParam]
	delete(params, oauthSignatureParam)

	queryParams, err := oauth1ExtractQueryParams(gotURL)
	require.NoError(t, err)
	paramString := oauth1ParamString(queryParams, params)
	baseURL := oauth1BaseURL(gotURL)
	sbs := oauth1SignatureBaseString(gotMethod, baseURL, paramString)

	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(sbs))
	err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sigBytes)
	require.NoError(t, err)
}

func TestOAuth1TransportFreshNoncePerAttempt(t *testing.T) {
	t.Run("two_direct_roundtrips", func(t *testing.T) {
		cfg, _ := newOAuth1TestConfig(t)

		server, capture := newCapturingServer(t)
		defer server.Close()

		transport := &OAuth1Transport{Config: cfg}

		for i := 0; i < 2; i++ {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
			require.NoError(t, err)
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			resp.Body.Close()
		}

		headers := capture.AuthHeaders()
		require.Len(t, headers, 2)
		n1 := parseOAuth1AuthHeader(t, headers[0])[oauthNonceParam]
		n2 := parseOAuth1AuthHeader(t, headers[1])[oauthNonceParam]
		assert.NotEqual(t, n1, n2, "each RoundTrip must generate a fresh nonce")
	})

	// Proves the now/nonce test seams are actually wired: RoundTrip must use
	// them when set, rather than always falling back to time.Now/oauth1Nonce.
	t.Run("now_and_nonce_seams_are_wired", func(t *testing.T) {
		cfg, _ := newOAuth1TestConfig(t)

		server, capture := newCapturingServer(t)
		defer server.Close()

		fixedTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		transport := &OAuth1Transport{
			Config: cfg,
			now:    func() time.Time { return fixedTime },
			nonce:  func() (string, error) { return "fixed-nonce-value", nil },
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
		require.NoError(t, err)
		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		resp.Body.Close()

		require.Equal(t, 1, capture.Hits())
		params := parseOAuth1AuthHeader(t, capture.AuthHeaders()[0])
		assert.Equal(t, "fixed-nonce-value", params[oauthNonceParam], "nonce seam must be wired")
		assert.Equal(t, strconv.FormatInt(fixedTime.Unix(), 10), params[oauthTimestampParam], "now seam must be wired")
	})

	// Stronger variant: drive the real retry loop (503 then 200) instead of
	// calling RoundTrip twice directly, exercising the actual
	// buildRequest-per-attempt path. The 503-then-200 status logic stays
	// bespoke — it drives its decision off the shared capture's hit count.
	t.Run("real_retry_loop_resigns_each_attempt", func(t *testing.T) {
		cfg, _ := newOAuth1TestConfig(t)

		capture := &oauth1Capture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("server: read body failed: %v", err)
				return
			}
			capture.record(r.Header.Get(headerAuthorization), body)

			if capture.Hits() == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		log := createTestLogger()
		c := NewBuilder(log).WithOAuth1(cfg).WithRetries(2, 10*time.Millisecond).Build()

		resp, err := c.Post(context.Background(), &Request{URL: server.URL, Body: []byte(`{"retry":true}`)})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		headers := capture.AuthHeaders()
		require.Len(t, headers, 2, "server must observe exactly two attempts")

		p1 := parseOAuth1AuthHeader(t, headers[0])
		p2 := parseOAuth1AuthHeader(t, headers[1])
		assert.NotEqual(t, p1[oauthNonceParam], p2[oauthNonceParam], "nonce must differ across retry attempts")
		assert.NotEqual(t, p1[oauthSignatureParam], p2[oauthSignatureParam], "signature must differ across retry attempts")

		bodies := capture.Bodies()
		require.Len(t, bodies, 2)
		assert.Equal(t, string(bodies[0]), string(bodies[1]), "the retried body must stay identical across attempts")
	})
}

func TestOAuth1TransportEmptyBodyGET(t *testing.T) {
	cfg, _ := newOAuth1TestConfig(t)

	server, capture := newCapturingServer(t)
	defer server.Close()

	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil) //nolint:gocritic // nil body is the path readAndCloseBody must tolerate
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 1, capture.Hits())
	params := parseOAuth1AuthHeader(t, capture.AuthHeaders()[0])
	assert.Equal(t, "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=", params[oauthBodyHashParam])
}

// TestOAuth1TransportEmptyBodyPOSTFraming pins the wire-framing invariant: an
// empty-body POST through OAuth1Transport must stay Content-Length: 0 with no
// Transfer-Encoding, exactly like a POST built without WithOAuth1 at all.
// GET doesn't exercise this — net/http's outgoingLength() only special-cases
// ContentLength==0 with a non-nil, non-NoBody Body for methods that can carry
// a body (POST/PUT/PATCH), which is why TestOAuth1TransportEmptyBodyGET alone
// cannot catch a regression here.
func TestOAuth1TransportEmptyBodyPOSTFraming(t *testing.T) {
	cfg, _ := newOAuth1TestConfig(t)

	// This test needs TransferEncoding+ContentLength, which oauth1Capture doesn't
	// carry, so it stays a bespoke server — but still under mutex discipline (see
	// TestOAuth1TransportSignatureVerifies).
	var mu sync.Mutex
	var transferEncoding []string
	var contentLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		transferEncoding = r.TransferEncoding
		contentLength = r.ContentLength
		mu.Unlock()
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	gotTransferEncoding, gotContentLength := transferEncoding, contentLength
	mu.Unlock()

	assert.Empty(t, gotTransferEncoding, "an empty-body POST must not be silently upgraded to chunked transfer encoding")
	assert.Equal(t, int64(0), gotContentLength, "an empty-body POST must report Content-Length: 0, not -1 (unknown)")
}

func TestOAuth1TransportResolverErrorFailsClosed(t *testing.T) {
	server, capture := newCapturingServer(t)
	defer server.Close()

	cfg := OAuth1Config{
		ConsumerKey: "consumer123",
		KeyName:     "missing-key",
		Keys:        mapKeys{}, // resolver returns an error for any name
	}
	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip fails before any HTTP exchange; no body to close
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-key", "the error must name the key that failed to resolve, not just fail generically")
	assert.Equal(t, 0, capture.Hits(), "no request should reach the server when key resolution fails")
}

// TestOAuth1TransportClosesBodyOnEarlyReturn pins the http.RoundTripper
// contract: RoundTrip must close req.Body on EVERY return path, including
// errors — http.Client.do will not do it for the transport. The nil-Keys
// guard and the resolver-error return both fire BEFORE readAndCloseBody, so
// without the fix a consumer using OAuth1Transport directly with an *os.File
// or io.Pipe body leaks the descriptor on every failure. The nil_keys case
// also covers the nil Config.Keys guard, which has no other coverage: every
// other test in this file supplies a non-nil (if sometimes empty) resolver,
// so deleting the guard would leave the suite green while a misconfigured
// client nil-panics inside RoundTrip instead of failing closed. The
// unknown_signature_method case covers the THIRD early return
// (oauth1HashForMethod failure), added when hash resolution moved earlier in
// RoundTrip: it also closes the body, but nothing else exercised that guard,
// so deleting its closeRequestBody call would leave the suite green too. It
// constructs OAuth1Transport directly (not through WithOAuth1) so it reaches
// RoundTrip's guard even after WithOAuth1 gained its own build-time
// validation — that validation never sees this cfg.
func TestOAuth1TransportClosesBodyOnEarlyReturn(t *testing.T) {
	tests := []struct {
		name string
		cfg  OAuth1Config
	}{
		{name: "nil_keys", cfg: OAuth1Config{ConsumerKey: "c", KeyName: "k", Keys: nil}},
		{name: "resolver_error", cfg: OAuth1Config{ConsumerKey: "c", KeyName: "missing-key", Keys: mapKeys{}}},
		{
			name: "unknown_signature_method",
			cfg: OAuth1Config{
				ConsumerKey: "c",
				KeyName:     oauth1TestKeyName,
				Keys:        mapKeys{oauth1TestKeyName: generateOAuth1TestKey(t)},
				Method:      OAuth1SignatureMethod("HMAC-SHA1"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := newSpyBody(`{"a":1}`)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.invalid", body)
			require.NoError(t, err)

			transport := &OAuth1Transport{Config: tc.cfg}
			_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip fails before any HTTP exchange; the spy body is what's under test
			require.Error(t, err)
			assert.True(t, body.closed, "req.Body must be closed even on this early-return path")
		})
	}
}

// TestOAuth1TransportMalformedQueryFailsClosed pins the RoundTrip-level
// invariant: url.ParseQuery skips a segment it cannot parse (e.g. a
// semicolon separator) while still returning the rest, and the wire still
// sends req.URL.RawQuery verbatim — so signing whatever ParseQuery salvaged
// would silently sign a strict subset of what is actually transmitted.
// RoundTrip must fail closed instead.
func TestOAuth1TransportMalformedQueryFailsClosed(t *testing.T) {
	cfg, _ := newOAuth1TestConfig(t)

	server, capture := newCapturingServer(t)
	defer server.Close()

	transport := &OAuth1Transport{Config: cfg}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/x?a=b;c=d", http.NoBody)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req) //nolint:bodyclose // RoundTrip fails before any HTTP exchange; no body to close
	require.Error(t, err, "a malformed (semicolon-separated) query must fail closed rather than sign a partial parameter set")
	assert.Equal(t, 0, capture.Hits(), "no request should reach the server when the query fails to parse")
}

// TestOAuth1TransportOverwritesStaleAuthorizationHeader pins that
// OAuth1Transport.Set()s the Authorization header rather than Add()ing to it.
// The collision is reachable from the public API: Request.Headers and
// WithDefaultHeader both flow through applyHeaders (which itself Sets, not
// Adds) before the transport chain runs, so a caller-supplied Authorization
// header — stale or otherwise — must not survive alongside the OAuth1 one.
func TestOAuth1TransportOverwritesStaleAuthorizationHeader(t *testing.T) {
	cfg, _ := newOAuth1TestConfig(t)

	// This test needs every Authorization VALUE (r.Header.Values, not .Get) to
	// prove Set-not-Add — a distinction oauth1Capture's single-value Get-based
	// AuthHeaders() would silently mask — so it stays a bespoke server, but still
	// under mutex discipline (see TestOAuth1TransportSignatureVerifies).
	var mu sync.Mutex
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = r.Header.Values(headerAuthorization)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	log := createTestLogger()
	c := NewBuilder(log).WithOAuth1(cfg).Build()

	resp, err := c.Post(context.Background(), &Request{
		URL:     server.URL,
		Headers: map[string]string{headerAuthorization: "Bearer stale-token"},
		Body:    []byte(`{"a":1}`),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	gotAuthHeaders := authHeaders
	mu.Unlock()

	require.Len(t, gotAuthHeaders, 1, "the server must see exactly one Authorization value, not the stale one plus the OAuth1 one")
	assert.True(t, strings.HasPrefix(gotAuthHeaders[0], "OAuth "), "the surviving Authorization value must be the OAuth1 one, not the caller-supplied stale token")
}

// TestBuilderWithOAuth1ValidatesConfig pins that WithOAuth1 fails fast at
// build time on an invalid OAuth1Config, rather than deferring the failure to
// the first signed request. WithOAuth1 is new API in this release, so adding
// this now costs nothing; adding it after release would be a breaking change.
func TestBuilderWithOAuth1ValidatesConfig(t *testing.T) {
	log := createTestLogger()
	validKey := generateOAuth1TestKey(t)

	tests := []struct {
		name string
		cfg  OAuth1Config
	}{
		{name: "nil_keys", cfg: OAuth1Config{ConsumerKey: "c", KeyName: "k", Keys: nil}},
		{name: "empty_key_name", cfg: OAuth1Config{ConsumerKey: "c", KeyName: "", Keys: mapKeys{"k": validKey}}},
		{name: "empty_consumer_key", cfg: OAuth1Config{ConsumerKey: "", KeyName: "k", Keys: mapKeys{"k": validKey}}},
		{
			name: "unknown_signature_method",
			cfg: OAuth1Config{
				ConsumerKey: "c",
				KeyName:     "k",
				Keys:        mapKeys{"k": validKey},
				Method:      OAuth1SignatureMethod("HMAC-SHA1"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Panics(t, func() {
				NewBuilder(log).WithOAuth1(tc.cfg)
			})
		})
	}

	t.Run("valid_config_does_not_panic", func(t *testing.T) {
		cfg := OAuth1Config{ConsumerKey: "c", KeyName: "k", Keys: mapKeys{"k": validKey}}
		assert.NotPanics(t, func() {
			NewBuilder(log).WithOAuth1(cfg)
		})
	})
}

func TestBuilderWithOAuth1RegistersSignerLayer(t *testing.T) {
	log := createTestLogger()

	assertNesting := func(t *testing.T, built Client, base *stubRoundTripper) {
		t.Helper()
		clientImpl, ok := built.(*client)
		require.True(t, ok)
		joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
		require.True(t, ok, "body-transform layer must be outermost")
		signer, ok := joseTransport.Inner.(*OAuth1Transport)
		require.True(t, ok, "signer layer must sit between JOSE and the base")
		assert.Same(t, base, signer.Inner)
	}

	t.Run("oauth1_registered_first", func(t *testing.T) {
		base := &stubRoundTripper{name: "base"}
		cfg := OAuth1Config{ConsumerKey: "c", KeyName: "k", Keys: mapKeys{}}
		built := NewBuilder(log).WithOAuth1(cfg).WithJOSE(JOSEConfig{}).WithTransport(base).Build()
		assertNesting(t, built, base)
	})

	t.Run("jose_registered_first", func(t *testing.T) {
		base := &stubRoundTripper{name: "base"}
		cfg := OAuth1Config{ConsumerKey: "c", KeyName: "k", Keys: mapKeys{}}
		built := NewBuilder(log).WithJOSE(JOSEConfig{}).WithOAuth1(cfg).WithTransport(base).Build()
		assertNesting(t, built, base)
	})
}

func TestOAuth1SignsSealedJOSEBody(t *testing.T) {
	f := jositest.NewBidirectionalFixture(t)
	oauthCfg, _ := newOAuth1TestConfig(t)

	server, capture := newCapturingServer(t)
	defer server.Close()

	log := createTestLogger()
	c := NewBuilder(log).
		WithJOSE(JOSEConfig{Outbound: f.ClientOutbound, Inbound: f.ClientInbound, Resolver: f.Resolver}).
		WithOAuth1(oauthCfg).
		Build()

	resp, err := c.Post(context.Background(), &Request{URL: server.URL, Body: []byte(`{"pan":"4111111111111111"}`)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Equal(t, 1, capture.Hits())
	rawBody := capture.Bodies()[0]
	require.NotEmpty(t, rawBody)
	// This is the actual sealing proof: asserting oauth_body_hash == sha256(rawBody)
	// alone would hold even if JOSE sealed nothing at all, since rawBody is simply
	// whatever arrived on the wire. Asserting the plaintext PAN is absent is what
	// discriminates "the hash covers the wire bytes" from "the wire bytes are
	// ciphertext".
	assert.NotContains(t, string(rawBody), "4111111111111111", "the wire body must be JOSE ciphertext, not the plaintext PAN")

	sum := sha256.Sum256(rawBody)
	wantBodyHash := base64.StdEncoding.EncodeToString(sum[:])

	params := parseOAuth1AuthHeader(t, capture.AuthHeaders()[0])
	assert.Equal(t, wantBodyHash, params[oauthBodyHashParam],
		"oauth_body_hash must cover the sealed (ciphertext) body, not the plaintext")
}
