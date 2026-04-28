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
var allowedSigAlgs = []jose.SignatureAlgorithm{
	jose.RS256,
	jose.PS256,
	jose.ES256,
}

// allowedKeyAlgs are the JWE key-wrapping algorithms. Only RSA-OAEP variants are accepted;
// RSA1_5 (PKCS#1 v1.5) is excluded due to padding-oracle risk.
var allowedKeyAlgs = []jose.KeyAlgorithm{
	jose.RSA_OAEP_256,
}

// allowedContentEncs are the JWE content-encryption algorithms. AEAD only.
var allowedContentEncs = []jose.ContentEncryption{
	jose.A256GCM,
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
