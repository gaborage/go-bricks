package httpclient

import (
	"bytes"
	"fmt"
	"io"
	nethttp "net/http"
	"strings"

	"github.com/gaborage/go-bricks/jose"
)

// JOSETransport is an http.RoundTripper that signs+encrypts outbound request bodies
// (jose.Seal) and decrypts+verifies inbound response bodies (jose.Open) using a fixed
// pair of policies and a single KeyResolver.
//
// Architectural placement: JOSETransport sits below the httpclient retry loop, so each
// retry attempt produces a freshly-sealed request — important for protocols that
// require unique iat/jti claims per attempt (Visa Token Services and similar).
//
// Response Content-Type discrimination: only application/jose responses are unwrapped;
// other Content-Types pass through untouched. This mirrors the GoBricks server's hybrid
// error envelope — pre-trust failures from the counterparty come back as plaintext
// minimal JSON because the peer was never authenticated, and the transport must not
// attempt to decrypt those.
type JOSETransport struct {
	// Inner is the underlying RoundTripper that performs the actual HTTP exchange.
	// Nil defaults to nethttp.DefaultTransport.
	Inner nethttp.RoundTripper

	// Outbound is required: the policy used to sign+encrypt every outbound request body.
	// A nil Outbound disables outbound wrapping entirely (the transport delegates to Inner).
	Outbound *jose.Policy

	// Inbound is optional: when set, application/jose responses are decrypted+verified.
	// Other response Content-Types pass through unmodified so plaintext error envelopes
	// from JOSE-aware counterparties (e.g., GoBricks pre-trust failures) remain readable.
	Inbound *jose.Policy

	// Resolver supplies keys for both Outbound (sign/encrypt) and Inbound (decrypt/verify).
	// Required when either policy is set.
	Resolver jose.KeyResolver
}

// RoundTrip wraps the request body with JOSE (when Outbound is set), forwards to the
// inner transport, and unwraps the response body (when Inbound is set AND the response
// Content-Type matches application/jose).
func (t *JOSETransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = nethttp.DefaultTransport
	}

	wrapped, err := t.wrapRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := inner.RoundTrip(wrapped)
	if err != nil {
		return resp, err
	}

	if err := t.unwrapResponse(resp); err != nil {
		// Close body to prevent leak, then return the error so the caller sees the
		// crypto failure instead of stale-but-readable ciphertext.
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return resp, nil
}

// wrapRequest reads req.Body, seals it with the Outbound policy, and returns a clone
// of req with the sealed body and updated Content-Type / Content-Length headers.
// If Outbound is nil, returns req unchanged.
func (t *JOSETransport) wrapRequest(req *nethttp.Request) (*nethttp.Request, error) {
	if t.Outbound == nil {
		return req, nil
	}
	if t.Resolver == nil {
		return nil, fmt.Errorf("httpclient: JOSETransport requires a KeyResolver when Outbound is set")
	}

	plaintext, err := readAndCloseBody(req.Body)
	if err != nil {
		return nil, fmt.Errorf("httpclient: read request body: %w", err)
	}

	compact, err := jose.Seal(plaintext, t.Outbound, t.Resolver)
	if err != nil {
		return nil, err
	}

	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(strings.NewReader(compact))
	clone.ContentLength = int64(len(compact))
	// GetBody enables stdlib-driven request replay: it's invoked on redirect-following,
	// connection retry, and HTTP/2 retry-on-RST_STREAM. Without it those paths see an
	// already-drained body and silently send an empty payload.
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(compact)), nil
	}
	clone.Header.Set("Content-Type", jose.ContentType)
	return clone, nil
}

// unwrapResponse decrypts+verifies resp.Body when Inbound is set AND the response's
// Content-Type indicates JOSE. Plaintext responses (e.g., pre-trust error envelopes
// from a JOSE-aware peer) pass through unmodified.
func (t *JOSETransport) unwrapResponse(resp *nethttp.Response) error {
	if t.Inbound == nil || resp == nil || resp.Body == nil {
		return nil
	}
	if !jose.IsContentType(resp.Header.Get("Content-Type")) {
		return nil
	}
	if t.Resolver == nil {
		return fmt.Errorf("httpclient: JOSETransport requires a KeyResolver when Inbound is set")
	}

	compact, err := readAndCloseBody(resp.Body)
	if err != nil {
		return fmt.Errorf("httpclient: read response body: %w", err)
	}

	plaintext, _, _, err := jose.Open(string(compact), t.Inbound, t.Resolver)
	if err != nil {
		return err
	}

	resp.Body = io.NopCloser(bytes.NewReader(plaintext))
	resp.ContentLength = int64(len(plaintext))
	resp.Header.Set("Content-Type", "application/json")
	return nil
}

func readAndCloseBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	return io.ReadAll(body)
}

// IsJOSEError reports whether err is a JOSE crypto failure — kept as a thin re-export
// of jose.IsError for discoverability from the httpclient package, since transport
// callers typically already import httpclient and may not realize the canonical helper
// lives in jose.
func IsJOSEError(err error) bool {
	return jose.IsError(err)
}
