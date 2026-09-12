package testing

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// JWKSMode selects what the fake key set endpoint serves. It exists so a test
// can drive a verifier through the failure modes a real issuer exhibits —
// outage, garbage, and a body too large to read — without a proxy.
type JWKSMode int

const (
	// JWKSHealthy serves the issuer's current public keys as a JWKS document.
	JWKSHealthy JWKSMode = iota
	// JWKSServerError answers every request with 503 and an empty body.
	JWKSServerError
	// JWKSMalformed answers 200 with a body that is not a JWKS document.
	JWKSMalformed
	// JWKSOversized answers 200 with a well-formed document padded past
	// DefaultOversizedBytes, to exercise the consumer's body cap.
	JWKSOversized
	// JWKSNonRSAOnly answers 200 with a well-formed document carrying only the
	// entries added through AddECKey/AddRawKey — no RSA key at all. It is the
	// shape an RSA-only consumer must treat as a failed fetch rather than as an
	// empty key set.
	JWKSNonRSAOnly
)

// DefaultOversizedBytes is the filler JWKSOversized adds. A test caps the
// consumer below it — the framework's auth.jwt.jwks.maxbodybytes — rather than
// raising this.
const DefaultOversizedBytes = 64 * 1024

// JWKSPath is the path the fake endpoint serves the key set on. Any other path
// is answered identically, so a consumer's URL only has to point at the server.
const JWKSPath = "/.well-known/jwks.json"

// JWKSRequest is one recorded request to the key set endpoint.
type JWKSRequest struct {
	// At is when the handler received the request, on the real clock. It orders
	// the log; a test asserting refresh behavior should assert on COUNT and
	// order, never on elapsed time.
	At time.Time
	// Method and Path are taken verbatim from the request line.
	Method string
	Path   string
}

// JWKSServer is the network half of the fake issuer: a TLS httptest server
// publishing Issuer's public keys as a JWKS document, with a settable failure
// mode and a request log.
//
// The request log is what a refresh test asserts on. Refresh behavior — one
// coalesced fetch per unknown kid, no second fetch inside the minimum interval —
// is observable as a request COUNT, so a test never has to sleep for it.
//
// Every method is safe for concurrent use, and rotation performed through Rotate
// is serialized against the handler, so a key added mid-test is published
// without racing a concurrent fetch.
type JWKSServer struct {
	issuer *Issuer
	server *httptest.Server

	mu             sync.RWMutex
	mode           JWKSMode
	extra          []json.RawMessage
	requests       []JWKSRequest
	oversizedBytes int
}

// NewJWKSServer starts a TLS server publishing issuer's public keys. The caller
// must Close it.
//
// The certificate is httptest's own, so a consumer must dial through HTTPClient
// — an httpclient built over any other transport will fail the handshake, which
// is the point: the key set is the verifier's trust anchor and the framework
// refuses a plaintext endpoint.
func NewJWKSServer(issuer *Issuer) *JWKSServer {
	s := &JWKSServer{issuer: issuer, oversizedBytes: DefaultOversizedBytes}
	s.server = httptest.NewTLSServer(nethttp.HandlerFunc(s.handle))
	return s
}

// Close shuts the server down. It is safe to call more than once only through
// the usual t.Cleanup discipline; httptest panics on a double close.
func (s *JWKSServer) Close() { s.server.Close() }

// URL is the key set endpoint, an https URL suitable for auth.jwt.jwksuri.
func (s *JWKSServer) URL() string { return s.server.URL + JWKSPath }

// HTTPClient returns an http.Client that trusts this server's certificate. Pass
// it to httpclient's WithHTTPClient to reach the endpoint.
func (s *JWKSServer) HTTPClient() *nethttp.Client { return s.server.Client() }

// Issuer returns the issuer whose keys this server publishes.
func (s *JWKSServer) Issuer() *Issuer { return s.issuer }

// SetMode changes what the endpoint serves, from the next request on.
func (s *JWKSServer) SetMode(mode JWKSMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}

// SetOversizedBytes sets the filler JWKSOversized adds, in bytes; the served
// document is that much larger than the real one. n must not be negative.
func (s *JWKSServer) SetOversizedBytes(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oversizedBytes = n
}

// Rotate adds a fresh key under kid to the issuer and publishes it, serialized
// against in-flight requests. It is Issuer.Rotate's race-free form and panics on
// the same inputs.
func (s *JWKSServer) Rotate(kid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issuer.Rotate(kid)
}

// AddECKey publishes the issuer's ECDSA P-256 public key under kid, so the
// document carries a key the RSA-only consumer must drop. The kid is the one a
// dropped-keys warning names.
func (s *JWKSServer) AddECKey(kid string) {
	// Bytes returns the uncompressed SEC 1 point (0x04 || X || Y), each
	// coordinate already left-padded to the curve's byte width — the encoding
	// RFC 7518 section 6.2.1.2 asks for, minus the leading tag.
	point, err := s.issuer.ecdsaKey().PublicKey.Bytes()
	if err != nil {
		panic(fmt.Sprintf("auth/testing: encode ECDSA public key: %v", err))
	}
	half := (len(point) - 1) / 2
	entry := map[string]string{
		"kty": "EC",
		"kid": kid,
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1 : 1+half]),
		"y":   base64.RawURLEncoding.EncodeToString(point[1+half:]),
	}
	s.AddRawKey(entry)
}

// AddRawKey publishes an arbitrary JWK entry alongside the issuer's RSA keys,
// for a shape the typed helpers do not cover.
func (s *JWKSServer) AddRawKey(entry any) {
	raw, err := json.Marshal(entry)
	if err != nil {
		panic(fmt.Sprintf("auth/testing: marshal jwk entry: %v", err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extra = append(s.extra, raw)
}

// Requests returns a copy of the request log, oldest first.
func (s *JWKSServer) Requests() []JWKSRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]JWKSRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// RequestCount is the number of requests served so far.
func (s *JWKSServer) RequestCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.requests)
}

// ResetRequests clears the request log, so a test can count the requests one
// phase makes without subtracting the previous phase's.
func (s *JWKSServer) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// handle records the request and serves the current mode. It holds the lock
// across the whole response so a concurrent Rotate cannot publish half a
// document.
func (s *JWKSServer) handle(w nethttp.ResponseWriter, r *nethttp.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, JWKSRequest{At: time.Now(), Method: r.Method, Path: r.URL.Path})
	mode, oversized := s.mode, s.oversizedBytes
	body := s.documentLocked(mode)
	s.mu.Unlock()

	if mode == JWKSServerError {
		w.WriteHeader(nethttp.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch mode {
	case JWKSMalformed:
		writeAll(w, []byte("this is not a jwks document"))
	case JWKSOversized:
		writeAll(w, padDocument(body, oversized))
	default:
		writeAll(w, body)
	}
}

// documentLocked renders the JWKS document. The caller holds the lock.
func (s *JWKSServer) documentLocked(mode JWKSMode) []byte {
	entries := make([]json.RawMessage, 0, len(s.issuer.keys))
	if mode != JWKSNonRSAOnly {
		for kid, key := range s.issuer.keys {
			entry := map[string]string{
				"kty": "RSA",
				"kid": kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   encodeExponent(key.E),
			}
			entries = append(entries, mustMarshal(entry))
		}
	}
	entries = append(entries, s.extra...)
	return mustMarshal(map[string]any{"keys": entries})
}

// padDocument grows a valid document by size bytes of filler, so an oversized
// body is otherwise well-formed: the consumer must reject it on SIZE, not on a
// parse failure it would have hit anyway. The filler is unconditional and
// exact, so the result always exceeds size whatever the document weighed.
func padDocument(document []byte, size int) []byte {
	padding := strings.Repeat("p", size)
	// The document always ends in "}", so the filler member is spliced in ahead
	// of it rather than re-marshaled — which would allocate the padding twice.
	return append(document[:len(document)-1], []byte(`,"padding":"`+padding+`"}`)...)
}

// encodeExponent renders an RSA public exponent as the minimal big-endian
// base64url integer RFC 7518 requires.
func encodeExponent(e int) string {
	return base64.RawURLEncoding.EncodeToString(big.NewInt(int64(e)).Bytes())
}

// writeAll writes the body, panicking on a short write: a fake that silently
// truncates would make a consumer's parse failure unexplainable.
func writeAll(w nethttp.ResponseWriter, body []byte) {
	if _, err := w.Write(body); err != nil {
		panic(fmt.Sprintf("auth/testing: write jwks response: %v", err))
	}
}
