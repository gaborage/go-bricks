package jose_test

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
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

// recordingKS is a fakeKS that also keeps the keystore's role log door.
type recordingKS struct {
	fakeKS
	recorded [][2]string
}

func (r *recordingKS) RecordResolution(entry, role string) {
	r.recorded = append(r.recorded, [2]string{entry, role})
}

// TestResolvePolicyTagsBothKidsAsJoseRoute pins the dual-role bookkeeping: a
// resolved policy tags exactly its two kids under "jose-route", a refused one tags
// nothing, and a store without the door is left alone.
func TestResolvePolicyTagsBothKidsAsJoseRoute(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &recordingKS{fakeKS: fakeKS{
		priv: map[string]*rsa.PrivateKey{"ours": priv},
		pub:  map[string]*rsa.PublicKey{"ours": &priv.PublicKey, "peer": &priv.PublicKey},
	}}
	r := jose.NewKeyStoreResolver(ks)

	require.NoError(t, jose.ResolvePolicy(r, &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "ours", VerifyKid: "peer"}))
	assert.Equal(t, [][2]string{{"ours", "jose-route"}, {"peer", "jose-route"}}, ks.recorded)

	require.NoError(t, jose.ResolvePolicy(r, &jose.Policy{Direction: jose.DirectionOutbound, SignKid: "ours", EncryptKid: "peer"}))
	assert.Len(t, ks.recorded, 4, "outbound tags its two kids too")

	before := len(ks.recorded)
	require.Error(t, jose.ResolvePolicy(r, &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "ours", VerifyKid: "missing"}))
	assert.Len(t, ks.recorded, before, "a refused policy tags nothing")
	require.NoError(t, jose.ResolvePolicy(r, nil))
	assert.Len(t, ks.recorded, before, "a nil policy tags nothing")

	// Per-message resolution never records.
	_, err = r.PublicKey("peer")
	require.NoError(t, err)
	assert.Len(t, ks.recorded, before)

	// A store without the door: same policy resolves, nothing to record, no panic.
	plain := jose.NewKeyStoreResolver(&ks.fakeKS)
	require.NoError(t, jose.ResolvePolicy(plain, &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "ours", VerifyKid: "peer"}))
	assert.NotPanics(t, func() { (*jose.KeyStoreResolver)(nil).RecordResolution("x", "y") })
}
