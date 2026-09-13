package httpclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"

	"github.com/gaborage/go-bricks/jose"
)

// headerContentType is the canonical HTTP Content-Type header name. Extracted to a
// const so it isn't repeated as a string literal at every Get/Set call site (SonarCloud
// S1192). net/http does not provide a stdlib constant for header field names.
const headerContentType = "Content-Type"

// DefaultMaxJOSEBodyBytes caps the size of an inbound JOSE response body when no
// explicit MaxResponseBytes is set on JOSETransport. 10 MiB is comfortably larger than
// any expected VTS-style payload (token responses are typically <2 KiB) but small
// enough to bound peak memory if a counterparty (or attacker) sends a malicious
// response. Defense-in-depth against memory exhaustion.
const DefaultMaxJOSEBodyBytes int64 = 10 << 20 // 10 MiB

// ErrJOSEPlaintextResponse names a 2xx response the Inbound policy never opened, which the
// transport refuses rather than handing the caller a body nothing authenticated. Match it
// with errors.Is; it survives the client's own wrapping and the *url.Error net/http adds.
var ErrJOSEPlaintextResponse = errors.New("httpclient: successful response was not JOSE-protected")

// errEnvelopeUnbounded names the envelope-plus-unbounded-cap refusal. Build and RoundTrip
// raise the same code from the same constructor so a caller matches one thing either way.
func errEnvelopeUnbounded(message string) error {
	return &jose.Error{
		Sentinel: jose.ErrPolicyMismatch,
		Code:     "JOSE_POLICY_ENVELOPE_UNBOUNDED",
		Status:   500,
		Message:  message,
	}
}

// BodyEnvelope shapes the sealed body on the wire for counterparties that do not carry
// the compact JOSE serialization on its own -- Visa Message Level Encryption's
// {"encData":"<compact>"} JSON object, for example.
//
// A nil BodyEnvelope is the identity: the compact JWE is the request body with a
// Content-Type of application/jose, and only application/jose responses are unwrapped.
//
// One interface rather than two function fields, so JOSETransport and JOSEConfig stay
// comparable values -- a func field is not comparable, and both structs are part of the
// package's exported surface.
type BodyEnvelope interface {
	// Wrap builds the outbound request body from the compact jose.Seal produced and names
	// the Content-Type to advertise; an empty content type sends no Content-Type header at
	// all. An error aborts the round trip: no request is sent. Consulted only when Outbound
	// is set.
	Wrap(compact string) (body []byte, contentType string, err error)
	// Unwrap recognizes and extracts a compact from a buffered response body, given the
	// response Content-Type. Returning ok=false passes the body through untouched.
	// Consulted only when Inbound is set, and it replaces the application/jose
	// Content-Type rule, so EVERY eligible response body is buffered before it runs.
	Unwrap(contentType string, body []byte) (compact string, ok bool)
}

// JOSETransport is an http.RoundTripper that seals outbound request bodies (jose.Seal)
// and opens inbound response bodies (jose.Open) using a fixed pair of policies and a
// single KeyResolver. What sealing and opening MEAN is the policy's Mode: sign+encrypt
// and decrypt+verify on the nested JWE-of-JWS default, encrypt-only and decrypt-only on
// a SealModeBareJWE policy, which carries no signature to verify.
//
// Only bodies are protected: a request with no body is forwarded unsealed regardless of
// method, and a response net/http guarantees is empty (1xx, 204, 304, any reply to HEAD) is
// returned as-is even when it advertises application/jose. Every other response carrying that
// content type is decrypted and verified, including shapes that are bodyless by RFC but not
// by net/http — see unwrapResponse for why the guarantee, not the RFC, sets the boundary.
//
// Architectural placement: JOSETransport sits below the httpclient retry loop, so each
// retry attempt produces a freshly-sealed request — important for protocols that
// require unique iat/jti claims per attempt (Visa Token Services and similar).
//
// Response Content-Type discrimination: only application/jose responses are unwrapped;
// other Content-Types pass through untouched on a failure status and are refused on a
// successful one (ErrJOSEPlaintextResponse). This mirrors the GoBricks server's hybrid
// error envelope — pre-trust failures from the counterparty come back as plaintext
// minimal JSON because the peer was never authenticated, and the transport must not
// attempt to decrypt those.
//
// An Envelope moves that boundary for counterparties whose protected payload travels
// inside another format — Visa Message Level Encryption's {"encData":"<compact>"} JSON
// envelope, for example. Its Unwrap replaces the Content-Type rule, which costs a
// buffered read of every eligible response body.
type JOSETransport struct {
	// Inner is the underlying RoundTripper that performs the actual HTTP exchange.
	// Nil defaults to nethttp.DefaultTransport — relevant only when JOSETransport is
	// hand-constructed, since httpclient.Builder-produced clients always seed a non-nil Inner.
	Inner nethttp.RoundTripper

	// Outbound is optional: when set, it is the policy used to seal every outbound
	// request body (sign+encrypt, or encrypt-only under SealModeBareJWE).
	// A nil Outbound disables outbound wrapping entirely (the transport delegates to Inner).
	Outbound *jose.Policy

	// Inbound is optional: when set, application/jose responses are opened
	// (decrypt+verify, or decrypt-only under SealModeBareJWE).
	// Other response Content-Types pass through unmodified on a failure status, so plaintext
	// error envelopes from JOSE-aware counterparties (e.g., GoBricks pre-trust failures)
	// remain readable; on a 2xx they are refused as ErrJOSEPlaintextResponse.
	Inbound *jose.Policy

	// Resolver supplies keys for both Outbound (sign/encrypt) and Inbound (decrypt/verify).
	// Required when either policy is set.
	Resolver jose.KeyResolver

	// MaxResponseBytes bounds the response body read when Inbound is set. Zero means
	// use DefaultMaxJOSEBodyBytes. A negative value disables the cap entirely, which is
	// only defensible while the application/jose Content-Type gate has already vouched
	// for the body — that is, while Envelope is nil.
	//
	// Negative beside an Envelope and an Inbound policy is refused, not quietly
	// defaulted: Builder.WithJOSE rejects it at Build time and RoundTrip rejects it
	// before the request is sent, both as JOSE_POLICY_ENVELOPE_UNBOUNDED. A silent
	// fallback would hide the misconfiguration instead of naming it.
	MaxResponseBytes int64

	// Envelope optionally shapes the sealed body on the wire in both directions. Nil is
	// the identity: the compact itself outbound, the application/jose Content-Type rule
	// inbound. Wrap runs only when Outbound is set, Unwrap only when Inbound is set.
	//
	// With an Envelope set and Inbound non-nil, EVERY eligible response body is buffered
	// before Unwrap decides — bounded by MaxResponseBytes, with the same default and
	// over-cap error — because the Content-Type gate that would otherwise leave a
	// non-JOSE body unread no longer applies.
	Envelope BodyEnvelope

	// AllowPlaintextSuccess disables the fail-closed rule on successful responses: with it
	// set, a 2xx body the Inbound policy never opened reaches the caller as the peer sent
	// it instead of raising ErrJOSEPlaintextResponse.
	//
	// The Strangler-migration knob, and nothing else: set it only while a peer legitimately
	// answers some 2xx routes in plaintext, and clear it once every route is protected.
	// Leaving it set means a stripped ciphertext, or a route quietly switched to plaintext,
	// is indistinguishable from a genuine unprotected reply. Transport-wide by design; see
	// ADR-107's amendment for why there is no per-response predicate.
	AllowPlaintextSuccess bool
}

// RoundTrip wraps the request body with JOSE (when Outbound is set), forwards to the
// inner transport, and unwraps the response body (when Inbound is set AND the response
// is recognized as protected — by Content-Type, or by Envelope.Unwrap when one is set).
func (t *JOSETransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	if t.refusesUnboundedEnvelopeRead() {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errEnvelopeUnbounded("MaxResponseBytes cannot be negative when an Envelope is set beside an Inbound policy")
	}

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

	if err := t.unwrapResponse(wrapped, resp); err != nil {
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
// If Outbound is nil, or the request carries no body, returns req unchanged.
func (t *JOSETransport) wrapRequest(req *nethttp.Request) (*nethttp.Request, error) {
	if t.Outbound == nil {
		return req, nil
	}
	if t.Resolver == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errors.New("httpclient: JOSETransport requires a KeyResolver when Outbound is set")
	}

	// Sealing a request that has no body would stamp a JWE body onto it: gateways, CDNs and
	// ALBs drop or reject a GET carrying one, and a HEAD with Content-Length is a protocol
	// violation. Keyed on body presence rather than a method allowlist, which also covers
	// CONNECT and any future bodyless method for free.
	if req.Body == nil || req.Body == nethttp.NoBody {
		return req, nil
	}

	// Outbound body is from this application — trusted size — so no cap.
	// The OOM concern is for untrusted inbound responses (handled in unwrapResponse).
	plaintext, err := readAndCloseBody(req.Body, -1)
	if err != nil {
		return nil, fmt.Errorf("httpclient: read request body: %w", err)
	}

	compact, err := jose.Seal(plaintext, t.Outbound, t.Resolver)
	if err != nil {
		return nil, err
	}

	body, contentType := []byte(compact), jose.ContentType
	if t.Envelope != nil {
		body, contentType, err = t.Envelope.Wrap(compact)
		if err != nil {
			return nil, fmt.Errorf("httpclient: wrap JOSE request body: %w", err)
		}
	}

	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	// GetBody enables stdlib-driven request replay: it's invoked on redirect-following,
	// connection retry, and HTTP/2 retry-on-RST_STREAM. Without it those paths see an
	// already-drained body and silently send an empty payload.
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	// An empty contentType means the hook wants no media type at all. Setting it would put
	// a bare "Content-Type:" on the wire, which jose.IsContentType rejects and a JOSE-aware
	// peer can refuse; defaulting it back to application/jose would mislabel a body the hook
	// deliberately wrapped in some other format. Deleting is the only honest reading.
	if contentType == "" {
		clone.Header.Del(headerContentType)
	} else {
		clone.Header.Set(headerContentType, contentType)
	}
	return clone, nil
}

// unwrapResponse opens resp.Body — decrypt+verify, or decrypt-only under SealModeBareJWE —
// when Inbound is set AND the response is recognized as protected. A body that is not
// passes through unmodified on a failure status (e.g. a pre-trust error envelope from a
// JOSE-aware peer) and is refused on a successful one; responses that definitionally carry
// no body pass through untouched either way.
func (t *JOSETransport) unwrapResponse(req *nethttp.Request, resp *nethttp.Response) error {
	if t.skipsUnwrap(req, resp) {
		return nil
	}

	// Without a hook the Content-Type alone decides, and a non-JOSE body is never read: on a
	// failure status it reaches the caller as the peer sent it, unbuffered and uncapped. A
	// hook replaces that rule with one that needs the bytes, so from here every eligible body
	// is read and Unwrap's verdict stands in for the Content-Type's.
	if t.Envelope == nil && !jose.IsContentType(resp.Header.Get(headerContentType)) {
		if t.refusesPlaintext(resp.StatusCode) {
			return errPlaintextSuccess(resp.StatusCode)
		}
		return nil
	}

	if t.Resolver == nil {
		return errors.New("httpclient: JOSETransport requires a KeyResolver when Inbound is set")
	}
	maxBytes := t.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxJOSEBodyBytes
	}
	raw, err := readAndCloseBody(resp.Body, maxBytes)
	if err != nil {
		// From here readAndCloseBody has closed the peer's body, so every error return leaves
		// RoundTrip an inert one: its cleanup must not be a second Close on a hand-rolled
		// Inner's body, which net/http's own bodies tolerate but a caller's need not.
		replaceBody(resp, nil, "")
		return fmt.Errorf("httpclient: read response body: %w", err)
	}

	compact := string(raw)
	if t.Envelope != nil {
		extracted, ok := t.Envelope.Unwrap(resp.Header.Get(headerContentType), raw)
		if !ok {
			if t.refusesPlaintext(resp.StatusCode) {
				replaceBody(resp, nil, "")
				return errPlaintextSuccess(resp.StatusCode)
			}
			replaceBody(resp, raw, "")
			return nil
		}
		compact = extracted
	}

	plaintext, _, _, err := jose.Open(compact, t.Inbound, t.Resolver)
	if err != nil {
		replaceBody(resp, nil, "")
		return err
	}

	replaceBody(resp, plaintext, mimeApplicationJSON)
	return nil
}

// refusesUnboundedEnvelopeRead reports the one configuration RoundTrip must refuse
// outright. An Envelope replaces the application/jose Content-Type gate with Unwrap's
// verdict, and Unwrap needs the bytes, so every eligible response body is buffered; a
// negative MaxResponseBytes then means buffering a peer's body without any limit, one
// response at a time. Builder.WithJOSE rejects the trio at Build time, but JOSETransport
// is exported and a hand-built one reaches here unchecked.
//
// Refused rather than quietly capped: substituting DefaultMaxJOSEBodyBytes would honor
// neither of the two things the caller wrote down, and would hide the mistake.
// Outbound-only is untouched (nothing is read), and so is a negative cap without an
// Envelope, where the Content-Type gate has already vouched for the body.
func (t *JOSETransport) refusesUnboundedEnvelopeRead() bool {
	return t.Envelope != nil && t.Inbound != nil && t.MaxResponseBytes < 0
}

// replaceBody installs payload as resp's body and keeps ContentLength in step with it.
// An empty contentType leaves the response headers exactly as the peer sent them, which
// is what a pass-through restore needs; a non-empty one relabels the body.
func replaceBody(resp *nethttp.Response, payload []byte, contentType string) {
	resp.Body = io.NopCloser(bytes.NewReader(payload))
	resp.ContentLength = int64(len(payload))
	if contentType != "" {
		resp.Header.Set(headerContentType, contentType)
	}
}

// skipsUnwrap reports the responses that are not candidates at all: no inbound policy, no
// body, or a shape net/http guarantees is empty. Nothing is read and nothing is judged.
func (t *JOSETransport) skipsUnwrap(req *nethttp.Request, resp *nethttp.Response) bool {
	if t.Inbound == nil || resp == nil || resp.Body == nil {
		return true
	}
	// Exactly the shapes net/http GUARANTEES arrive empty: bodyAllowedForStatus rejects 1xx,
	// 204 and 304, and noResponseBodyExpected rejects every reply to HEAD, so fixLength pins
	// their length at zero. Such a response advertises application/jose anyway (HEAD headers
	// mirror the GET they describe) and jose.Open on the empty string fails as JOSE_MALFORMED.
	//
	// The guarantee is what makes skipping safe, so the set deliberately stops there. 205 and
	// a 2xx answer to CONNECT are bodyless per RFC 9110 but NOT per net/http, which reads a
	// body on both — skipping them would hand a peer's unverified bytes to the caller under a
	// status code it chose. For the same reason the key is the response shape and not "the
	// read came back empty": an empty read, and equally a nethttp.NoBody body, also describes
	// a 200 whose ciphertext was stripped in transit, which must keep failing closed.
	//
	// 101 is the one 1xx a RoundTripper returns, and its Body wraps the live hijacked
	// connection — reading that would hang, not error. The method comes from the request, not
	// resp.Request: *http.Transport back-fills that field but the RoundTripper contract does
	// not require an Inner to, so it can be nil.
	return resp.StatusCode < nethttp.StatusOK || resp.StatusCode == nethttp.StatusNoContent ||
		resp.StatusCode == nethttp.StatusNotModified || req.Method == nethttp.MethodHead
}

// refusesPlaintext reports whether an unopened body at this status must be refused; see
// ADR-107's amendment.
func (t *JOSETransport) refusesPlaintext(status int) bool {
	return !t.AllowPlaintextSuccess && IsSuccessStatus(status)
}

// errPlaintextSuccess names the refusal by status alone; the body must not be reported.
func errPlaintextSuccess(status int) error {
	return fmt.Errorf("%w (status: %d)", ErrJOSEPlaintextResponse, status)
}

// readAndCloseBody drains body up to maxBytes (negative = unbounded) and closes it.
// Uses http.MaxBytesReader rather than io.LimitReader+length-check so an oversize
// payload errors mid-stream — without that, up to maxBytes of memory would be
// materialized on the heap before the overflow is detected.
//
// The overflow is mapped to a typed httpclient.ClientError (ValidationError) so
// callers can distinguish it from network/IO errors via IsErrorType.
func readAndCloseBody(body io.ReadCloser, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	if maxBytes < 0 {
		return io.ReadAll(body)
	}
	b, err := io.ReadAll(nethttp.MaxBytesReader(nil, body, maxBytes))
	if err != nil {
		var maxErr *nethttp.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, NewValidationError(
				fmt.Sprintf("JOSE response body exceeds %d bytes", maxBytes),
				"response_body",
			)
		}
		return nil, err
	}
	return b, nil
}

// IsJOSEError reports whether err is a JOSE crypto failure — kept as a thin re-export
// of jose.IsError for discoverability from the httpclient package, since transport
// callers typically already import httpclient and may not realize the canonical helper
// lives in jose.
func IsJOSEError(err error) bool {
	return jose.IsError(err)
}
