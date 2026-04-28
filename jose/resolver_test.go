package jose_test

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/jose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKS is a minimal in-test stub satisfying jose.KeyStoreLike. Inlined here rather
// than imported from keystore/testing/ to avoid the test-only cycle:
// jose_test → keystore/testing → app → jose.
type fakeKS struct {
	priv map[string]*rsa.PrivateKey
	pub  map[string]*rsa.PublicKey
}

func (f *fakeKS) PrivateKey(name string) (*rsa.PrivateKey, error) {
	if k, ok := f.priv[name]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("private key %q not registered", name)
}

func (f *fakeKS) PublicKey(name string) (*rsa.PublicKey, error) {
	if k, ok := f.pub[name]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("public key %q not registered", name)
}

func TestKeyStoreResolverUnknownKid(t *testing.T) {
	r := jose.NewKeyStoreResolver(&fakeKS{
		priv: map[string]*rsa.PrivateKey{},
		pub:  map[string]*rsa.PublicKey{},
	})

	_, err := r.PrivateKey("unknown")
	var jerr *jose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
	assert.Equal(t, "unknown", jerr.Kid)

	_, err = r.PublicKey("unknown")
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}

func TestKeyStoreResolverFindsRegisteredKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &fakeKS{
		priv: map[string]*rsa.PrivateKey{"ours": priv},
		pub:  map[string]*rsa.PublicKey{"ours": &priv.PublicKey},
	}
	r := jose.NewKeyStoreResolver(ks)

	gotPriv, err := r.PrivateKey("ours")
	require.NoError(t, err)
	assert.Equal(t, priv, gotPriv)

	gotPub, err := r.PublicKey("ours")
	require.NoError(t, err)
	assert.Equal(t, &priv.PublicKey, gotPub)
}

func TestKeyStoreResolverNilKeyStore(t *testing.T) {
	r := jose.NewKeyStoreResolver(nil)
	_, err := r.PrivateKey("any")
	var jerr *jose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, "JOSE_KEYSTORE_UNAVAILABLE", jerr.Code)
}

func TestResolvePolicyInbound(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &fakeKS{
		priv: map[string]*rsa.PrivateKey{"ours": priv},
		pub:  map[string]*rsa.PublicKey{"ours": &priv.PublicKey, "peer": &priv.PublicKey},
	}
	r := jose.NewKeyStoreResolver(ks)
	p := &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "ours", VerifyKid: "peer"}
	require.NoError(t, jose.ResolvePolicy(r, p))
}

func TestResolvePolicyMissingKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &fakeKS{
		priv: map[string]*rsa.PrivateKey{"ours": priv},
		pub:  map[string]*rsa.PublicKey{"ours": &priv.PublicKey},
	}
	r := jose.NewKeyStoreResolver(ks)
	p := &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "ours", VerifyKid: "missing-peer"}

	err = jose.ResolvePolicy(r, p)
	var jerr *jose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}
