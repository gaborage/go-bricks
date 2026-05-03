package jose

import (
	"encoding/json"
	"errors"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// Open performs the inbound transformation: decrypt the compact JWE with our private key,
// verify the inner JWS with the peer's public key, and parse standard JWT claims out of
// the verified payload.
//
// Returns the verified plaintext payload, the extracted Claims, and the JWE+JWS Headers
// for diagnostic logging by the caller. On any failure, returns an *Error with the
// appropriate Code/Status (mostly 401 for crypto failures, 400 for malformed input).
//
// The middleware MUST set inbound-verified state on the context only when this returns
// nil error — that's the gate for the encrypt-on-response security invariant.
func Open(compact string, p *Policy, r KeyResolver) (plaintext []byte, claims *Claims, hdr OpenHeader, err error) {
	if p == nil || p.Direction != DirectionInbound {
		return nil, nil, OpenHeader{}, &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Status:   500,
			Message:  "Open requires an inbound policy",
		}
	}
	if r == nil {
		return nil, nil, OpenHeader{}, &Error{
			Sentinel: ErrKeyResolution,
			Code:     codeKeystoreUnavailable,
			Status:   500,
			Message:  "Open called without a KeyResolver",
		}
	}

	decKey, err := r.PrivateKey(p.DecryptKid)
	if err != nil {
		return nil, nil, OpenHeader{}, err
	}
	verKey, err := r.PublicKey(p.VerifyKid)
	if err != nil {
		return nil, nil, OpenHeader{}, err
	}

	jwsCompact, jweHdr, err := cryptoadapter.Decrypt(compact, decKey, &cryptoadapter.DecryptOptions{
		ExpectedKid:       p.DecryptKid,
		AllowedKeyAlgs:    AllowedKeyAlgs(),
		AllowedContentEnc: AllowedContentEncs(),
	})
	hdr.JWE = cryptoHeaderToOpen(&jweHdr)
	if err != nil {
		return nil, nil, hdr, mapDecryptError(err, p, &jweHdr)
	}

	innerPayload, jwsHdr, err := cryptoadapter.Verify(string(jwsCompact), verKey, &cryptoadapter.VerifyOptions{
		ExpectedKid:    p.VerifyKid,
		AllowedSigAlgs: AllowedSigAlgs(),
	})
	hdr.JWS = cryptoHeaderToOpen(&jwsHdr)
	if err != nil {
		return nil, nil, hdr, mapVerifyError(err, p, &jwsHdr)
	}

	// Inner JWS cty enforcement (defense-in-depth, post-verify).
	// Permissive: only reject when peer explicitly declares a cty AND it disagrees
	// with the policy. A peer that omits cty entirely is accepted, since cty is an
	// optional header per RFC 7515 §4.1.10. This catches content-type confusion
	// (peer signs cty=text/csv while we parse the bytes as JSON) without breaking
	// peers that don't bother to set cty.
	if p.Cty != "" && jwsHdr.Cty != "" && jwsHdr.Cty != p.Cty {
		return nil, nil, hdr, &Error{
			Sentinel: ErrCtyRejected,
			Code:     "JOSE_CTY_REJECTED",
			Status:   400,
			Message:  "Disallowed cty header",
			Kid:      jwsHdr.Kid,
			Alg:      jwsHdr.Alg,
		}
	}

	claims = parseClaims(innerPayload)
	return innerPayload, claims, hdr, nil
}

// OpenHeader holds the diagnostic headers from both JOSE layers, surfaced to the caller
// so the middleware can log them. Never includes plaintext.
type OpenHeader struct {
	JWE Header
	JWS Header
}

// Header (jose-package level) is the diagnostic header shape exposed to callers,
// distinct from the internal cryptoadapter.Header to insulate consumers from library churn.
type Header struct {
	Kid string
	Alg string
	Enc string
	Cty string
}

func cryptoHeaderToOpen(h *cryptoadapter.Header) Header {
	return Header{Kid: h.Kid, Alg: h.Alg, Enc: h.Enc, Cty: h.Cty}
}

func mapDecryptError(err error, _ *Policy, hdr *cryptoadapter.Header) *Error {
	switch {
	case errors.Is(err, cryptoadapter.ErrParseEncrypted):
		return &Error{
			Sentinel: ErrMalformed,
			Code:     codeMalformed,
			Status:   400,
			Message:  "Malformed JOSE payload",
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrKidMissing):
		return &Error{
			Sentinel: ErrKidMissing,
			Code:     codeKidMissing,
			Status:   401,
			Message:  "Missing kid header",
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrKidMismatch):
		return &Error{
			Sentinel: ErrKidUnknown,
			Code:     codeKidUnknown,
			Status:   401,
			Message:  "Unknown kid",
			Kid:      hdr.Kid,
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrDecryptFailed):
		return &Error{
			Sentinel: ErrDecryptFailed,
			Code:     codeDecryptFailed,
			Status:   401,
			Message:  msgFailedToDecryptRequestPayload,
			Kid:      hdr.Kid,
			Alg:      hdr.Alg,
			Enc:      hdr.Enc,
			Cause:    err,
		}
	default:
		return &Error{
			Sentinel: ErrDecryptFailed,
			Code:     codeDecryptFailed,
			Status:   401,
			Message:  msgFailedToDecryptRequestPayload,
			Cause:    err,
		}
	}
}

func mapVerifyError(err error, _ *Policy, hdr *cryptoadapter.Header) *Error {
	switch {
	case errors.Is(err, cryptoadapter.ErrParseSigned):
		return &Error{
			Sentinel: ErrInnerNotJWS,
			Code:     "JOSE_INNER_NOT_JWS",
			Status:   400,
			Message:  "Decrypted payload is not a JWS",
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrKidMissing):
		return &Error{
			Sentinel: ErrKidMissing,
			Code:     codeKidMissing,
			Status:   401,
			Message:  "Missing JWS kid header",
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrKidMismatch):
		return &Error{
			Sentinel: ErrKidUnknown,
			Code:     codeKidUnknown,
			Status:   401,
			Message:  "Unknown JWS kid",
			Kid:      hdr.Kid,
			Cause:    err,
		}
	case errors.Is(err, cryptoadapter.ErrVerifyFailed):
		return &Error{
			Sentinel: ErrSignatureInvalid,
			Code:     codeSignatureInvalid,
			Status:   401,
			Message:  "Invalid signature",
			Kid:      hdr.Kid,
			Alg:      hdr.Alg,
			Cause:    err,
		}
	default:
		return &Error{
			Sentinel: ErrSignatureInvalid,
			Code:     codeSignatureInvalid,
			Status:   401,
			Message:  "Invalid signature",
			Cause:    err,
		}
	}
}

// parseClaims extracts standard JWT claims from a JSON payload. Non-fatal: a payload
// that isn't a JSON object (e.g., a plain encrypted blob) returns a Claims with only
// Raw populated. Apps that need claim enforcement check Claims fields explicitly.
func parseClaims(payload []byte) *Claims {
	c := &Claims{Raw: map[string]any{}}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return c
	}
	c.Raw = raw

	if v, ok := raw["iss"].(string); ok {
		c.Issuer = v
	}
	if v, ok := raw["sub"].(string); ok {
		c.Subject = v
	}
	switch v := raw["aud"].(type) {
	case string:
		c.Audience = []string{v}
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	if v, ok := raw["jti"].(string); ok {
		c.JTI = v
	}
	c.IssuedAt = parseUnixSecs(raw["iat"])
	c.ExpiresAt = parseUnixSecs(raw["exp"])
	c.NotBefore = parseUnixSecs(raw["nbf"])

	return c
}
