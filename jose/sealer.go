package jose

import (
	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// ctyNestedJWS is the JWE protected-header cty the nested mode writes over its inner
// compact JWS, and the marker openBare refuses so a nested token can never be returned
// as unverified plaintext. Compared case-sensitively, exactly as written here.
const ctyNestedJWS = "JWS"

// Seal performs the outbound transformation p.Mode selects. The default signs payload as a
// compact JWS with our private key, then encrypts that JWS as a compact JWE to the peer's
// public key; SealModeBareJWE only encrypts; SealModeJWSofJWE encrypts, then signs the
// compact JWE. Returns the outermost compact serialization.
//
// On failure, returns an *Error. Pre-flight guard failures (Status 500) use Code
// JOSE_POLICY_DIRECTION_MISMATCH (nil or wrong-direction policy) or JOSE_KEYSTORE_UNAVAILABLE
// (nil resolver), or JOSE_ALGORITHM_DISALLOWED when an algorithm is outside the allowlist
// (unset Status, which callers map to 500);
// sign/encrypt failures (Status 500) use JOSE_OUTBOUND_FAILED. Key-resolution
// failures propagate the resolver's *Error verbatim (e.g. JOSE_KID_UNKNOWN), whose Status is
// resolver-defined. The Cause field carries the underlying detail for logging.
func Seal(payload []byte, p *Policy, r KeyResolver) (string, error) {
	if p == nil || p.Direction != DirectionOutbound {
		return "", &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Status:   500,
			Message:  "Seal requires an outbound policy",
		}
	}
	if r == nil {
		return "", &Error{
			Sentinel: ErrKeyResolution,
			Code:     codeKeystoreUnavailable,
			Status:   500,
			Message:  "Seal called without a KeyResolver",
		}
	}
	// Defense in depth: Open threads the allowlists into the parser, but the outbound
	// algorithms and protected headers are handed to the crypto adapter verbatim. Both
	// live callers (the tag scanner, httpclient's Build) normalize and validate first, so
	// a policy reaching here unvalidated is one that skipped that path and must fail closed.
	if err := p.Validate(); err != nil {
		return "", err
	}

	switch p.Mode {
	case SealModeJWEofJWS:
	case SealModeBareJWE:
		return sealBare(payload, p, r)
	case SealModeJWSofJWE:
		return sealJWSofJWE(payload, p, r)
	default:
		return "", errUnknownMode(p.Mode)
	}

	signKey, err := r.PrivateKey(p.SignKid)
	if err != nil {
		return "", err
	}
	encKey, err := r.PublicKey(p.EncryptKid)
	if err != nil {
		return "", err
	}

	jwsCompact, err := cryptoadapter.Sign(payload, signKey, &cryptoadapter.SignOptions{
		Kid:    p.SignKid,
		SigAlg: p.SigAlg,
		Cty:    p.Cty,
	})
	if err != nil {
		return "", signFailed(p, err)
	}

	jweCompact, err := cryptoadapter.Encrypt([]byte(jwsCompact), encKey, &cryptoadapter.EncryptOptions{
		Kid:    p.EncryptKid,
		KeyAlg: p.KeyAlg,
		Enc:    p.Enc,
		Cty:    ctyNestedJWS,
	})
	if err != nil {
		return "", encryptFailed(p, err)
	}

	return jweCompact, nil
}

// signFailed wraps a crypto-adapter sign failure, shared by every signing seal mode.
func signFailed(p *Policy, err error) *Error {
	return &Error{
		Sentinel: ErrOutboundFailed,
		Code:     codeOutboundFailed,
		Status:   500,
		Message:  "Failed to sign outbound payload",
		Kid:      p.SignKid,
		Alg:      string(p.SigAlg),
		Cause:    err,
	}
}

// encryptFailed wraps a crypto-adapter encrypt failure, shared by every seal mode.
func encryptFailed(p *Policy, err error) *Error {
	return &Error{
		Sentinel: ErrOutboundFailed,
		Code:     codeOutboundFailed,
		Status:   500,
		Message:  "Failed to encrypt outbound payload",
		Kid:      p.EncryptKid,
		Alg:      string(p.KeyAlg),
		Enc:      string(p.Enc),
		Cause:    err,
	}
}
