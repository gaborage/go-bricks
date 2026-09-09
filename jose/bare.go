package jose

import (
	"time"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// This file holds the SealModeBareJWE half of Seal/Open: encryption with no inner JWS,
// as Visa Message Level Encryption specifies. The peer is authenticated out of band
// (X-Pay-Token, mTLS); nothing here signs or verifies anything.

// nowMillis is the seal-time clock for the bare-mode iat header, swapped by in-package
// tests. Unix epoch MILLISECONDS, per the Visa MLE convention.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

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

// openBare decrypts a bare JWE: no inner JWS, so nothing is verified and hdr.JWS stays
// zero. The peer is authenticated out of band by the deployment, not here.
func openBare(compact string, p *Policy, r KeyResolver) (plaintext []byte, claims *Claims, hdr OpenHeader, err error) {
	decKey, err := r.PrivateKey(p.DecryptKid)
	if err != nil {
		return nil, nil, OpenHeader{}, err
	}

	payload, jweHdr, err := cryptoadapter.Decrypt(compact, decKey, &cryptoadapter.DecryptOptions{
		ExpectedKid:       p.DecryptKid,
		AllowedKeyAlgs:    AllowedKeyAlgs(),
		AllowedContentEnc: AllowedContentEncsFor(p.Mode),
	})
	hdr.JWE = cryptoHeaderToOpen(&jweHdr)
	if err != nil {
		return nil, nil, hdr, mapDecryptError(err, p, &jweHdr)
	}

	// Same permissive cty rule as the nested path, applied to the only header there is:
	// a peer that declares a cty must agree with the policy, one that omits it is fine.
	if p.Cty != "" && jweHdr.Cty != "" && jweHdr.Cty != p.Cty {
		return nil, nil, hdr, &Error{
			Sentinel: ErrCtyRejected,
			Code:     codeCtyRejected,
			Status:   400,
			Message:  "Disallowed cty header",
			Kid:      jweHdr.Kid,
			Alg:      jweHdr.Alg,
		}
	}

	return payload, parseClaims(payload), hdr, nil
}
