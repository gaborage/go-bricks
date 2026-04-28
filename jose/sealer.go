package jose

import (
	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// Seal performs the outbound transformation: sign payload as a compact JWS with our
// private key, then encrypt that JWS as a compact JWE to the peer's public key. Returns
// the compact JWE string.
//
// On any failure, returns an *Error with Code/Status mapped to JOSE_OUTBOUND_FAILED (500)
// — outbound failures are framework/operator errors (missing keys, bad config), never
// peer-induced. The Cause field carries the underlying detail for logging.
func Seal(payload []byte, p *Policy, r KeyResolver) (string, error) {
	if p == nil || p.Direction != DirectionOutbound {
		return "", &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     "JOSE_POLICY_DIRECTION_MISMATCH",
			Status:   500,
			Message:  "Seal requires an outbound policy",
		}
	}
	if r == nil {
		return "", &Error{
			Sentinel: ErrKeyResolution,
			Code:     "JOSE_KEYSTORE_UNAVAILABLE",
			Status:   500,
			Message:  "Seal called without a KeyResolver",
		}
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
			Code:     "JOSE_OUTBOUND_FAILED",
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
			Code:     "JOSE_OUTBOUND_FAILED",
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
