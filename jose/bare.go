package jose

import (
	"maps"
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
		return "", encryptFailed(p, err)
	}
	return jweCompact, nil
}

// bareExtraHeaders returns the protected headers Seal writes: the policy's own map when
// there is no iat to stamp, otherwise a copy carrying both. The policy's map is never
// mutated.
func (p *Policy) bareExtraHeaders() map[string]any {
	// Nothing to stamp: cryptoadapter.Encrypt only ranges over Extra, never retaining or
	// mutating it, so handing it the policy's own map is safe.
	if !p.IATMillis {
		return p.ProtectedHeaders
	}
	extra := make(map[string]any)
	maps.Copy(extra, p.ProtectedHeaders)
	extra["iat"] = nowMillis()
	return extra
}

// openBare decrypts a bare JWE: no inner JWS, so nothing is verified and hdr.JWS stays
// zero. The peer is authenticated out of band by the deployment, not here. A JWE that
// declares cty=JWS is refused unconditionally, whatever Policy.Cty says, so a nested
// token can never surface here as unverified plaintext.
func openBare(compact string, p *Policy, r KeyResolver) (plaintext []byte, claims *Claims, hdr OpenHeader, err error) {
	decKey, err := r.PrivateKey(p.DecryptKid)
	if err != nil {
		return nil, nil, OpenHeader{}, err
	}

	keyAlgs, encs := inboundAllowlists(p)
	payload, jweHdr, err := cryptoadapter.Decrypt(compact, decKey, &cryptoadapter.DecryptOptions{
		ExpectedKid:       p.DecryptKid,
		AllowedKeyAlgs:    keyAlgs,
		AllowedContentEnc: encs,
	})
	hdr.JWE = cryptoHeaderToOpen(&jweHdr)
	if err != nil {
		return nil, nil, hdr, mapDecryptError(err, p, &jweHdr)
	}

	// Fail closed on a nested token: bare mode never carries an inner JWS, so cty=JWS
	// means the "plaintext" is a compact JWS nothing here verifies. Refuse it whatever
	// the policy declares. Visa MLE uses typ=JOSE, so this costs no interop.
	if jweHdr.Cty == ctyNestedJWS {
		return nil, nil, hdr, &Error{
			Sentinel: ErrCtyRejected,
			Code:     codeCtyRejected,
			Status:   400,
			Message:  "Bare policy refuses a nested cty",
			Kid:      jweHdr.Kid,
			Alg:      jweHdr.Alg,
		}
	}

	// Same permissive cty rule as the nested path, applied to the only header there is.
	if ctyErr := ctyMismatch(p.Cty, &jweHdr); ctyErr != nil {
		return nil, nil, hdr, ctyErr
	}

	return payload, parseClaims(payload), hdr, nil
}
