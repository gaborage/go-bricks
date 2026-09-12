package auth

import (
	"context"
	"crypto/rsa"
	"math/big"
)

// PublicKeyResolver resolves the issuer's public signing keys by "kid".
//
// It mirrors jose.KeyResolver, the house shape for a key-lookup interface, with
// one deliberate difference: it is public-key-only. A JWKS-backed resolver must
// never be able to satisfy a private-key interface, so this is not
// jose.KeyResolver and never grows a PrivateKey method.
//
// Implementations return ErrKidUnknown when the kid is absent from an otherwise
// usable key set, and ErrKeySetUnavailable when no usable key set exists at all
// (never fetched, or past its stale ceiling).
//
// Aliasing contract: the returned key is READ-ONLY, and the same aliasing rule
// Principal.Claims carries. A resolver resolves keys on the per-request
// verification path, so it hands back its own key rather than deep-copying a
// modulus per call; a caller that writes to the returned key — or to its N —
// corrupts verification for every concurrent request. Copy before modifying.
type PublicKeyResolver interface {
	PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

var _ PublicKeyResolver = (*StaticKeyResolver)(nil)

// StaticKeyResolver is an in-memory PublicKeyResolver over a fixed set of keys.
// It suits tests and consumers that pin issuer keys out of band instead of
// fetching JWKS.
//
// The key map and the keys in it are copied at construction and never written
// afterwards, so a StaticKeyResolver is safe for concurrent use.
type StaticKeyResolver struct {
	keys map[string]*rsa.PublicKey
}

// Structural bounds a pinned RSA public key must satisfy to be registered.
// They match the bounds the JWKS parser applies, so a key pinned in code and the
// same key fetched from a JWKS are accepted or rejected identically.
const (
	minModulusBits    = 2048
	maxModulusBits    = 16384
	minPublicExponent = 3
	maxPublicExponent = 1<<31 - 1
)

// NewStaticKeyResolver returns a PublicKeyResolver over a defensive copy of
// keys. Entries whose key is nil, or whose key is not structurally usable, are
// dropped: such a key cannot verify anything, so it reads as an unknown kid
// rather than reaching the verifier, where its failure would be reported as a
// bad signature on the caller's credential instead of an unusable key set. A
// resolver that ends up with no keys at all reports ErrKeySetUnavailable on
// every lookup.
//
// A key is structurally usable when its modulus is non-nil, positive, odd (an
// RSA modulus is a product of two odd primes) and between 2048 and 16384 bits
// inclusive, and its public exponent is odd and between 3 and 1<<31-1
// inclusive. The 2048-bit floor holds on this path too: a pinned key that a
// JWKS-backed resolver would refuse must not verify tokens merely because it was
// configured in code.
//
// Each key is cloned, modulus included, so the resolver owns its key material: a
// caller that later writes to the keys it passed in cannot retroactively change
// what this resolver verifies against. The clone is paid once, at construction.
func NewStaticKeyResolver(keys map[string]*rsa.PublicKey) *StaticKeyResolver {
	copied := make(map[string]*rsa.PublicKey, len(keys))
	for kid, key := range keys {
		if !usablePublicKey(key) {
			continue
		}
		copied[kid] = clonePublicKey(key)
	}
	return &StaticKeyResolver{keys: copied}
}

// usablePublicKey reports whether key is structurally sound enough to verify a
// signature. It is a shape check, not a proof that the modulus is a genuine RSA
// product: it rejects the malformed keys that would otherwise surface as a
// signature failure, and fails closed on anything it cannot vouch for.
func usablePublicKey(key *rsa.PublicKey) bool {
	if key == nil || key.N == nil {
		return false
	}
	// Sign() != 1 covers both a zero and a negative modulus; Bit(0) != 1 covers an
	// even one. Equality keeps the guards free of a boundary the tests cannot pin.
	if key.N.Sign() != 1 || key.N.Bit(0) != 1 {
		return false
	}
	if bits := key.N.BitLen(); bits < minModulusBits || bits > maxModulusBits {
		return false
	}
	return key.E >= minPublicExponent && key.E <= maxPublicExponent && key.E%2 == 1
}

// clonePublicKey deep-copies an RSA public key. The caller drops keys that are
// not structurally usable before calling, so the copy below is unconditional.
func clonePublicKey(key *rsa.PublicKey) *rsa.PublicKey {
	return &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
}

// PublicKey implements PublicKeyResolver.
//
// The returned key is the resolver's own and MUST NOT be mutated: it is shared
// by every concurrent lookup of the same kid. See the PublicKeyResolver
// aliasing contract.
//
// The method is exported, so it can be reached without passing through
// NewVerifierWithResolver. A nil receiver holds no key set at all, which is
// exactly the ErrKeySetUnavailable condition — a server-side misconfiguration,
// never the 401 that ErrKidUnknown would read as.
func (s *StaticKeyResolver) PublicKey(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if s == nil {
		return nil, ErrKeySetUnavailable
	}
	if len(s.keys) == 0 {
		return nil, ErrKeySetUnavailable
	}
	key, ok := s.keys[kid]
	if !ok {
		return nil, ErrKidUnknown
	}
	return key, nil
}
