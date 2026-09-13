package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
)

const (
	// ktyRSA is the only "kty" this package can use; useSignature the only "use"
	// it accepts when the member is present.
	ktyRSA       = "RSA"
	useSignature = "sig"

	// keyOpVerify is the RFC 7517 section 4.3 operation a signature-verification
	// key must declare when it declares "key_ops" at all.
	keyOpVerify = "verify"

	// RSA modulus bounds. Below the floor the key is not worth verifying
	// against; above the ceiling a single signature check becomes a denial of
	// service the issuer can hand us. A key outside the range is dropped like
	// any other unusable entry, named in the WARN.
	minRSAModulusBits = 2048
	maxRSAModulusBits = 16384

	// maxRSAExponentBits bounds "e" so the int conversion below cannot overflow.
	maxRSAExponentBits = 31

	// noKidPlaceholder labels an entry with no "kid" in the dropped-keys WARN.
	noKidPlaceholder = "<no kid>"
)

// jwksDocument is the subset of RFC 7517 this package reads.
type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

type jwksKey struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
}

// parseJWKS decodes the document and keeps the RSA signing keys. Anything else —
// a non-RSA kty, an encryption-only key, a key whose "key_ops" withholds
// "verify", a key with no kid, an undecodable or
// out-of-range modulus or exponent, a duplicate kid — is DROPPED and named in
// dropped, never promoted into an error: one unusable entry must not cost the
// deployment every other key the issuer published.
//
// A document that yields no usable key at all is the caller's failure to report,
// not this function's.
func parseJWKS(body []byte) (keys map[string]*rsa.PublicKey, dropped []string, err error) {
	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, errors.New("auth: jwks document is not valid json")
	}

	keys = make(map[string]*rsa.PublicKey, len(doc.Keys))
	for i := range doc.Keys {
		entry := &doc.Keys[i]
		key, ok := parseRSAKey(entry)
		if !ok || keys[entry.Kid] != nil {
			dropped = append(dropped, kidLabel(entry.Kid))
			continue
		}
		keys[entry.Kid] = key
	}
	return keys, dropped, nil
}

// kidLabel renders a kid for the dropped-keys warning, standing in for an entry
// that carried none.
func kidLabel(kid string) string {
	if kid == "" {
		return noKidPlaceholder
	}
	return kid
}

// parseRSAKey converts one JWK into an RSA public key, reporting ok=false for
// every entry this package cannot verify with.
func parseRSAKey(entry *jwksKey) (key *rsa.PublicKey, ok bool) {
	if entry.Kty != ktyRSA || entry.Kid == "" {
		return nil, false
	}
	if entry.Use != "" && entry.Use != useSignature {
		return nil, false
	}
	if !keyOpsAllowVerify(entry.KeyOps) {
		return nil, false
	}
	modulus, ok := decodeUint(entry.N, minRSAModulusBits, maxRSAModulusBits)
	if !ok {
		return nil, false
	}
	// An RSA modulus is a product of two odd primes and so is always odd; an even
	// one is not a usable key. rsa.Verify would reject it later, so drop it here
	// where it is named in the WARN.
	if modulus.Bit(0) == 0 {
		return nil, false
	}
	exponent, ok := decodeUint(entry.E, 1, maxRSAExponentBits)
	if !ok || !exponent.IsInt64() {
		return nil, false
	}
	value := exponent.Int64()
	// An even or unit exponent is not a usable RSA public exponent; rsa.Verify
	// would reject it later, so drop it here where it is named in the WARN.
	if value < 3 || value%2 == 0 {
		return nil, false
	}
	return &rsa.PublicKey{N: modulus, E: int(value)}, true
}

// keyOpsAllowVerify reports whether "key_ops" permits signature verification.
//
// An ABSENT or empty member is permitted: RFC 7517 makes key_ops optional and
// most issuers omit it, so requiring it would reject their key sets outright.
// When it IS present it is authoritative — a key whose declared operations do
// not include "verify" is not a verification key, whatever "use" claims. That
// also settles the disagreement RFC 7517 section 4.3 says SHOULD NOT happen:
// "use":"sig" beside key_ops without "verify" is dropped.
func keyOpsAllowVerify(ops []string) bool {
	if len(ops) == 0 {
		return true
	}
	return slices.Contains(ops, keyOpVerify)
}

// decodeUint decodes a base64url big-endian unsigned integer and bounds its bit
// length. RFC 7518 mandates the unpadded encoding, so a padded value is
// rejected rather than silently accepted.
func decodeUint(encoded string, minBits, maxBits int) (value *big.Int, ok bool) {
	if encoded == "" {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	value = new(big.Int).SetBytes(raw)
	bits := value.BitLen()
	if bits < minBits || bits > maxBits {
		return nil, false
	}
	return value, true
}
