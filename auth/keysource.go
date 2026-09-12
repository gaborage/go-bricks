package auth

import (
	"context"
	"crypto/rsa"
)

// KeySource resolves the issuer's public signing keys by "kid".
//
// It is deliberately public-key-only: a JWKS-backed source must never be able to
// satisfy a private-key interface, so this is not jose.KeyResolver and never
// grows a PrivateKey method.
//
// Implementations return ErrKidUnknown when the kid is absent from an otherwise
// usable key set, and ErrKeySetUnavailable when no usable key set exists at all
// (never fetched, or past its stale ceiling).
type KeySource interface {
	PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

var _ KeySource = (*StaticKeySource)(nil)

// StaticKeySource is an in-memory KeySource over a fixed set of keys. It suits
// tests and consumers that pin issuer keys out of band instead of fetching JWKS.
//
// The key map is copied at construction and never written afterwards, so a
// StaticKeySource is safe for concurrent use.
type StaticKeySource struct {
	keys map[string]*rsa.PublicKey
}

// NewStaticKeySource returns a KeySource over a defensive copy of keys. Entries
// with a nil key are dropped, so an unusable entry reads as an unknown kid
// rather than as a nil key handed to a verifier. A source that ends up with no
// keys at all reports ErrKeySetUnavailable on every lookup.
func NewStaticKeySource(keys map[string]*rsa.PublicKey) *StaticKeySource {
	copied := make(map[string]*rsa.PublicKey, len(keys))
	for kid, key := range keys {
		if key == nil {
			continue
		}
		copied[kid] = key
	}
	return &StaticKeySource{keys: copied}
}

// PublicKey implements KeySource.
func (s *StaticKeySource) PublicKey(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if len(s.keys) == 0 {
		return nil, ErrKeySetUnavailable
	}
	key, ok := s.keys[kid]
	if !ok {
		return nil, ErrKidUnknown
	}
	return key, nil
}
