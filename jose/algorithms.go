package jose

import jose "github.com/go-jose/go-jose/v4"

const (
	DefaultSigAlg = jose.RS256
	DefaultKeyAlg = jose.RSA_OAEP_256
	DefaultEnc    = jose.A256GCM
	DefaultCty    = "application/json"
)

// allowedSigAlgs is the strict allowlist of JWS signature algorithms accepted on inbound
// payloads and selectable for outbound. Symmetric algorithms (HS*) are forbidden because
// the framework's key model is asymmetric (RSA keypairs in keystore). alg=none is forbidden
// at the parser level by passing this allowlist into go-jose's ParseSigned.
//
// ES256 is intentionally absent: the keystore module returns *rsa.PrivateKey/PublicKey
// only, so an ES256 selection would crash at runtime when the cryptoadapter passes the
// RSA key into go-jose's ECDSA signer. Re-add only after extending keystore.KeyStore to
// surface ECDSA keys — see the "JOSE: ECDSA Keystore Support" backlog entry.
var allowedSigAlgs = []jose.SignatureAlgorithm{
	jose.RS256,
	jose.PS256,
}

// allowedKeyAlgs are the JWE key-wrapping algorithms. Only RSA-OAEP variants are accepted;
// RSA1_5 (PKCS#1 v1.5) is excluded due to padding-oracle risk.
var allowedKeyAlgs = []jose.KeyAlgorithm{
	jose.RSA_OAEP_256,
}

// allowedContentEncs are the JWE content-encryption algorithms accepted on the nested
// JWE-of-JWS path. AEAD only.
var allowedContentEncs = []jose.ContentEncryption{
	jose.A256GCM,
}

// allowedContentEncsBare are the JWE content-encryption algorithms accepted in
// SealModeBareJWE. A128GCM is admitted here and nowhere else: Visa Message Level
// Encryption specifies it, and a bare JWE carries no inner signature whose strength the
// content encryption would have to match. Still AEAD only.
var allowedContentEncsBare = []jose.ContentEncryption{
	jose.A128GCM,
	jose.A256GCM,
}

// contentEncsForMode returns the allowlist backing a SealMode; nil for an unknown mode,
// so an unrecognized mode fails closed at every seam that consults it.
func contentEncsForMode(mode SealMode) []jose.ContentEncryption {
	switch mode {
	case SealModeJWEofJWS:
		return allowedContentEncs
	case SealModeBareJWE:
		return allowedContentEncsBare
	default:
		return nil
	}
}

// IsAllowedEncFor reports whether enc is permitted in the given seal mode. Use it instead
// of IsAllowedEnc wherever a Policy's Mode is known; IsAllowedEnc keeps the JWE-of-JWS
// meaning.
func IsAllowedEncFor(mode SealMode, enc jose.ContentEncryption) bool {
	for _, e := range contentEncsForMode(mode) {
		if e == enc {
			return true
		}
	}
	return false
}

// AllowedContentEncsFor returns a copy of the content-encryption allowlist for the given
// seal mode, for callers threading it into go-jose primitives (e.g. jose.ParseEncrypted).
// An unknown mode yields an empty list, which rejects every token.
func AllowedContentEncsFor(mode SealMode) []jose.ContentEncryption {
	src := contentEncsForMode(mode)
	out := make([]jose.ContentEncryption, len(src))
	copy(out, src)
	return out
}

func IsAllowedSigAlg(alg jose.SignatureAlgorithm) bool {
	for _, a := range allowedSigAlgs {
		if a == alg {
			return true
		}
	}
	return false
}

func IsAllowedKeyAlg(alg jose.KeyAlgorithm) bool {
	for _, a := range allowedKeyAlgs {
		if a == alg {
			return true
		}
	}
	return false
}

func IsAllowedEnc(enc jose.ContentEncryption) bool {
	for _, e := range allowedContentEncs {
		if e == enc {
			return true
		}
	}
	return false
}

// AllowedSigAlgs returns a copy of the signature-algorithm allowlist for callers that
// need to pass it to go-jose primitives (e.g., jose.ParseSigned). Returning a copy
// prevents external mutation.
func AllowedSigAlgs() []jose.SignatureAlgorithm {
	out := make([]jose.SignatureAlgorithm, len(allowedSigAlgs))
	copy(out, allowedSigAlgs)
	return out
}

func AllowedKeyAlgs() []jose.KeyAlgorithm {
	out := make([]jose.KeyAlgorithm, len(allowedKeyAlgs))
	copy(out, allowedKeyAlgs)
	return out
}

func AllowedContentEncs() []jose.ContentEncryption {
	out := make([]jose.ContentEncryption, len(allowedContentEncs))
	copy(out, allowedContentEncs)
	return out
}
