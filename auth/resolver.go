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

// NewStaticKeyResolver returns a PublicKeyResolver over a defensive copy of
// keys. Entries with a nil key are dropped, so an unusable entry reads as an
// unknown kid rather than as a nil key handed to a verifier. A resolver that
// ends up with no keys at all reports ErrKeySetUnavailable on every lookup.
//
// Each key is cloned, modulus included, so the resolver owns its key material: a
// caller that later writes to the keys it passed in cannot retroactively change
// what this resolver verifies against. The clone is paid once, at construction.
func NewStaticKeyResolver(keys map[string]*rsa.PublicKey) *StaticKeyResolver {
	copied := make(map[string]*rsa.PublicKey, len(keys))
	for kid, key := range keys {
		if key == nil {
			continue
		}
		copied[kid] = clonePublicKey(key)
	}
	return &StaticKeyResolver{keys: copied}
}

// clonePublicKey deep-copies an RSA public key. A nil modulus is carried across
// as nil rather than copied, because big.Int.Set panics on one and a key that
// arrived unusable must stay a lookup failure downstream, not a panic here.
func clonePublicKey(key *rsa.PublicKey) *rsa.PublicKey {
	cloned := &rsa.PublicKey{E: key.E}
	if key.N != nil {
		cloned.N = new(big.Int).Set(key.N)
	}
	return cloned
}

// PublicKey implements PublicKeyResolver.
//
// The returned key is the resolver's own and MUST NOT be mutated: it is shared
// by every concurrent lookup of the same kid. See the PublicKeyResolver
// aliasing contract.
func (s *StaticKeyResolver) PublicKey(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if len(s.keys) == 0 {
		return nil, ErrKeySetUnavailable
	}
	key, ok := s.keys[kid]
	if !ok {
		return nil, ErrKidUnknown
	}
	return key, nil
}
