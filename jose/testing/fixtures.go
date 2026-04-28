package testing

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/jose"
)

// GenerateTestKeyPair returns a freshly-minted 2048-bit RSA key pair suitable for
// JOSE round-trip tests. The smaller key size is chosen for test speed — production
// keystores should use 3072+ bits.
func GenerateTestKeyPair(t testing.TB) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("jositest: failed to generate key pair: %v", err)
	}
	return priv, &priv.PublicKey
}

// NewTestResolver builds a jose.KeyResolver from a flat kid → key map. Values may be
// *rsa.PrivateKey (used for both PrivateKey and PublicKey lookups via the embedded
// public key) or *rsa.PublicKey (used only for PublicKey lookups; PrivateKey lookup
// against a public-only entry returns ErrKidUnknown).
//
// Unknown kids return a *jose.Error wrapping jose.ErrKidUnknown — the same behavior
// as the production KeyStoreResolver — so tests exercise the same error paths as
// production code.
func NewTestResolver(keys map[string]any) jose.KeyResolver {
	r := &mapResolver{
		priv: map[string]*rsa.PrivateKey{},
		pub:  map[string]*rsa.PublicKey{},
	}
	for kid, k := range keys {
		switch v := k.(type) {
		case *rsa.PrivateKey:
			r.priv[kid] = v
			r.pub[kid] = &v.PublicKey
		case *rsa.PublicKey:
			r.pub[kid] = v
		default:
			panic(fmt.Sprintf("jositest: unsupported key type for kid %q: %T", kid, k))
		}
	}
	return r
}

type mapResolver struct {
	priv map[string]*rsa.PrivateKey
	pub  map[string]*rsa.PublicKey
}

func (r *mapResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if k, ok := r.priv[kid]; ok {
		return k, nil
	}
	return nil, &jose.Error{
		Sentinel: jose.ErrKidUnknown,
		Code:     "JOSE_KID_UNKNOWN",
		Message:  "test resolver: private key not registered",
		Kid:      kid,
	}
}

func (r *mapResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	if k, ok := r.pub[kid]; ok {
		return k, nil
	}
	return nil, &jose.Error{
		Sentinel: jose.ErrKidUnknown,
		Code:     "JOSE_KID_UNKNOWN",
		Message:  "test resolver: public key not registered",
		Kid:      kid,
	}
}
