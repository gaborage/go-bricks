package jose

import (
	"time"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// nowMillis is the seal-time clock for the bare-mode iat header, swapped by in-package
// tests. Unix epoch MILLISECONDS, per the Visa MLE convention.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

// Seal performs the outbound transformation: sign payload as a compact JWS with our
// private key, then encrypt that JWS as a compact JWE to the peer's public key. Returns
// the compact JWE string.
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
	if err := p.validateMode(); err != nil {
		return "", err
	}
	if err := p.validateAlgorithms(); err != nil {
		return "", err
	}
	if err := p.validateDirection(); err != nil {
		return "", err
	}

	if p.Mode == SealModeBareJWE {
		return sealBare(payload, p, r)
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
		return "", &Error{
			Sentinel: ErrOutboundFailed,
			Code:     codeOutboundFailed,
			Status:   500,
			Message:  "Failed to sign outbound payload",
			Kid:      p.SignKid,
			Alg:      string(p.SigAlg),
			Cause:    err,
		}
	}

	jweCompact, err := cryptoadapter.Encrypt([]byte(jwsCompact), encKey, &cryptoadapter.EncryptOptions{
		Kid:    p.EncryptKid,
		KeyAlg: p.KeyAlg,
		Enc:    p.Enc,
		Cty:    "JWS",
	})
	if err != nil {
		return "", &Error{
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

	return jweCompact, nil
}

// sealBare encrypts payload directly to the peer's public key, with no inner JWS. The
// peer's identity is established out of band, so nothing here signs anything.
func sealBare(payload []byte, p *Policy, r KeyResolver) (string, error) {
	encKey, err := r.PublicKey(p.EncryptKid)
	if err != nil {
		return "", err
	}

	jweCompact, err := cryptoadapter.Encrypt(payload, encKey, &cryptoadapter.EncryptOptions{
		Kid:    p.EncryptKid,
		KeyAlg: p.KeyAlg,
		Enc:    p.Enc,
		Cty:    p.Cty,
		Typ:    p.Typ,
		Extra:  p.bareExtraHeaders(),
	})
	if err != nil {
		return "", &Error{
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
	return jweCompact, nil
}

// bareExtraHeaders merges the policy's protected headers with the stamped iat, without
// mutating the policy's map. Returns nil when there is nothing to write.
func (p *Policy) bareExtraHeaders() map[string]any {
	if len(p.ProtectedHeaders) == 0 && !p.IATMillis {
		return nil
	}
	extra := make(map[string]any, len(p.ProtectedHeaders)+1)
	for k, v := range p.ProtectedHeaders {
		extra[k] = v
	}
	if p.IATMillis {
		extra["iat"] = nowMillis()
	}
	return extra
}
