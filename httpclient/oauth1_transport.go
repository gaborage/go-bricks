package httpclient

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	nethttp "net/http"
	"strconv"
	"time"
)

// headerAuthorization mirrors headerContentType's rationale in jose_transport.go: avoid repeating the literal at every Get/Set call site.
const headerAuthorization = "Authorization"

// OAuth1Transport signs every outbound request with an OAuth 1.0a
// Authorization header (RSA + oauth_body_hash). It sits at the transport
// chain's signer layer: body transforms such as JOSE run outside it, so the
// hash covers the bytes actually sent. Each RoundTrip generates a fresh
// nonce/timestamp, so httpclient retries re-sign automatically.
type OAuth1Transport struct {
	// Inner is the underlying RoundTripper that performs the actual HTTP
	// exchange. Nil defaults to nethttp.DefaultTransport.
	Inner nethttp.RoundTripper

	// Config supplies the consumer key, signing key resolver, and signature
	// method.
	Config OAuth1Config

	// now is a test seam for the timestamp source; nil means time.Now.
	now func() time.Time
	// nonce is a test seam for nonce generation; nil means oauth1Nonce.
	nonce func() (string, error)
}

// closeRequestBody closes body when non-nil. http.RoundTripper requires
// RoundTrip to close req.Body on EVERY return path, including errors —
// http.Client.do does not do this for the transport, so a pre-read error
// return that skips this leaks the caller's body (e.g. an *os.File or
// io.Pipe) on every failure.
func closeRequestBody(body io.ReadCloser) {
	if body != nil {
		_ = body.Close()
	}
}

// setBufferedBody installs body as req's Body/ContentLength/GetBody. An empty
// body gets nethttp.NoBody rather than io.NopCloser(bytes.NewReader(nil)):
// http.Request.outgoingLength() maps ContentLength 0 with a non-nil,
// non-NoBody Body to -1 (unknown), which pushes POST/PUT/PATCH onto the
// chunked-encoding path. Using nethttp.NoBody keeps an empty body's wire
// framing identical to a request built without WithOAuth1 at all
// (Content-Length: 0, no Transfer-Encoding) instead of silently upgrading it
// to chunked — some partner edges/WAFs reject chunked with 411/400/501.
func setBufferedBody(req *nethttp.Request, body []byte) {
	newBody := func() io.ReadCloser {
		if len(body) == 0 {
			return nethttp.NoBody
		}
		return io.NopCloser(bytes.NewReader(body))
	}
	req.Body = newBody()
	req.ContentLength = int64(len(body))
	// GetBody enables stdlib-driven request replay: it's invoked on redirect-following,
	// connection retry, and HTTP/2 retry-on-RST_STREAM. Without it those paths see an
	// already-drained body and silently send an empty payload under a signature computed
	// over the real one. nethttp.NoBody is a stateless singleton, so re-returning it here
	// is safe.
	req.GetBody = func() (io.ReadCloser, error) { return newBody(), nil }
}

// RoundTrip signs req with an OAuth 1.0a Authorization header and delegates
// to Inner. The Authorization header is Set (not Add): OAuth1Transport owns
// this header on the client it's installed on.
func (t *OAuth1Transport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	if t.Config.Keys == nil {
		closeRequestBody(req.Body)
		return nil, fmt.Errorf("httpclient: oauth1: Config.Keys (PrivateKeyResolver) is required")
	}
	key, err := t.Config.Keys.PrivateKey(t.Config.KeyName)
	if err != nil {
		closeRequestBody(req.Body)
		return nil, fmt.Errorf("httpclient: oauth1: resolve private key %q: %w", t.Config.KeyName, err)
	}
	// Resolved early, next to the other fail-closed guards: an unknown method now
	// fails before the caller's body is drained instead of after building the full
	// param string under a wrong (silently-defaulted) digest.
	h, err := oauth1HashForMethod(t.Config.Method)
	if err != nil {
		closeRequestBody(req.Body)
		return nil, err
	}

	body, err := readAndCloseBody(req.Body, -1)
	if err != nil {
		return nil, fmt.Errorf("httpclient: oauth1: read request body: %w", err)
	}

	clone := req.Clone(req.Context())
	setBufferedBody(clone, body)

	nonce, err := t.resolveNonce()
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(t.resolveNow().Unix(), 10)
	bodyHash := oauth1BodyHash(body, h)
	oauthParams := oauth1Params(&t.Config, bodyHash, nonce, timestamp)

	queryParams, err := oauth1ExtractQueryParams(clone.URL)
	if err != nil {
		return nil, err
	}
	paramString := oauth1ParamString(queryParams, oauthParams)
	baseURL := oauth1BaseURL(clone.URL)
	sbs := oauth1SignatureBaseString(clone.Method, baseURL, paramString)

	signature, err := oauth1Sign(sbs, key, t.Config.Method)
	if err != nil {
		return nil, err
	}

	// The OAuth params entered the param string raw; oauth_signature is the ONLY
	// value percent-encoded before insertion into the header.
	headerParams := maps.Clone(oauthParams)
	headerParams[oauthSignatureParam] = oauth1PercentEncode(signature)
	clone.Header.Set(headerAuthorization, oauth1AuthorizationHeader(headerParams))

	inner := t.Inner
	if inner == nil {
		inner = nethttp.DefaultTransport
	}
	return inner.RoundTrip(clone)
}

func (t *OAuth1Transport) resolveNow() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *OAuth1Transport) resolveNonce() (string, error) {
	if t.nonce != nil {
		return t.nonce()
	}
	return oauth1Nonce()
}

// WithOAuth1 signs every request with OAuth 1.0a at the signer layer of the
// transport chain — beneath body transforms such as WithJOSE, so signatures
// cover the wire payload, and independent of builder call order.
//
// Panics if cfg is invalid (nil Keys, empty ConsumerKey, or an unknown
// Method): WithOAuth1 is new API, so failing fast here costs nothing, while
// leaving it to fail on the first signed request would be a silent trap.
func (b *Builder) WithOAuth1(cfg OAuth1Config) *Builder {
	if err := cfg.validate(); err != nil {
		panic(err.Error()) // NOSONAR: Fail-fast on invalid initialization (manifesto: configuration errors crash at startup)
	}
	b.addTransportWrapper(layerSigner, func(inner nethttp.RoundTripper) nethttp.RoundTripper {
		return &OAuth1Transport{Inner: inner, Config: cfg}
	})
	return b
}
