package jose

import (
	"errors"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// This file holds the SealModeJWSofJWE half of Seal/Open: encrypt first, then sign the
// compact JWE as the payload of an outer JWS whose protected header the mode fixes.

const (
	ctyJWE  = "JWE"
	typJOSE = "JOSE"
)

// sealJWSofJWE builds the inner JWE bare mode builds, minus cty (the wire shape carries
// none, and httpclient fills Policy.Cty for every mode), then signs that compact string
// verbatim. The outer iat is epoch SECONDS, whatever Policy.IATMillis says about the inner.
func sealJWSofJWE(payload []byte, p *Policy, r KeyResolver) (string, error) {
	signKey, err := r.PrivateKey(p.SignKid)
	if err != nil {
		return "", err
	}
	inner := *p
	inner.Cty = ""
	jweCompact, err := sealBare(payload, &inner, r)
	if err != nil {
		return "", err
	}
	jwsCompact, err := cryptoadapter.Sign([]byte(jweCompact), signKey, &cryptoadapter.SignOptions{
		Kid:    p.SignKid,
		SigAlg: p.SigAlg,
		Cty:    ctyJWE,
		Typ:    typJOSE,
		Extra:  map[string]any{"iat": time.Now().Unix()},
	})
	if err != nil {
		return "", signFailed(p, err)
	}
	return jwsCompact, nil
}

// openJWSofJWE verifies the outer JWS against exactly Policy.SigAlg, requires cty JWE, then
// hands the payload to the bare opener. A body that is not a compact JWS — a JWE-outer
// object included — is refused, never opened under another mode. The outer iat is seconds,
// so it is never reported through Header.IATMillis; neither iat is judged.
func openJWSofJWE(compact string, p *Policy, r KeyResolver) (plaintext []byte, claims *Claims, hdr OpenHeader, err error) {
	if strings.Count(compact, ".") != 2 {
		return nil, nil, OpenHeader{}, errOuterNotJWS(nil)
	}
	peeked, err := cryptoadapter.PeekProtectedHeader(compact)
	if err != nil {
		return nil, nil, OpenHeader{}, errOuterNotJWS(err)
	}
	if peeked.Alg != string(p.SigAlg) {
		return nil, nil, OpenHeader{}, &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     codeAlgorithmDisallowed,
			Status:   400,
			Message:  "Disallowed signature algorithm",
			Alg:      peeked.Alg,
		}
	}

	verKey, err := r.PublicKey(p.VerifyKid)
	if err != nil {
		return nil, nil, OpenHeader{}, err
	}
	jweCompact, jwsHdr, err := cryptoadapter.Verify(compact, verKey, &cryptoadapter.VerifyOptions{
		ExpectedKid:    p.VerifyKid,
		AllowedSigAlgs: []jose.SignatureAlgorithm{p.SigAlg},
	})
	hdr.JWS = cryptoHeaderToOpen(&jwsHdr)
	hdr.JWS.IATMillis = 0
	if errors.Is(err, cryptoadapter.ErrParseSigned) {
		return nil, nil, hdr, errOuterNotJWS(err)
	}
	if err != nil {
		return nil, nil, hdr, mapVerifyError(err, p, &jwsHdr)
	}
	if jwsHdr.Cty != ctyJWE {
		return nil, nil, hdr, &Error{
			Sentinel: ErrCtyRejected,
			Code:     codeCtyRejected,
			Status:   400,
			Message:  "Outer JWS must declare cty JWE",
			Kid:      jwsHdr.Kid,
			Alg:      jwsHdr.Alg,
		}
	}

	plaintext, claims, innerHdr, err := openBare(string(jweCompact), p, r)
	hdr.JWE = innerHdr.JWE
	return plaintext, claims, hdr, err
}

func errOuterNotJWS(cause error) *Error {
	return &Error{
		Sentinel: ErrMalformed,
		Code:     codeOuterNotJWS,
		Status:   400,
		Message:  "Payload is not a compact JWS",
		Cause:    cause,
	}
}
