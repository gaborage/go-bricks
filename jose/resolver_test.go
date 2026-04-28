package jose

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"

	keystoretest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyStoreResolverUnknownKid(t *testing.T) {
	mockKS := keystoretest.NewMockKeyStore()
	r := NewKeyStoreResolver(mockKS)

	_, err := r.PrivateKey("unknown")
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
	assert.Equal(t, "unknown", jerr.Kid)

	_, err = r.PublicKey("unknown")
	require.Error(t, err)
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}

func TestKeyStoreResolverFindsRegisteredKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	mockKS := keystoretest.NewMockKeyStore().
		WithPrivateKey("ours", priv).
		WithPublicKey("ours", &priv.PublicKey)

	r := NewKeyStoreResolver(mockKS)
	gotPriv, err := r.PrivateKey("ours")
	require.NoError(t, err)
	assert.Equal(t, priv, gotPriv)

	gotPub, err := r.PublicKey("ours")
	require.NoError(t, err)
	assert.Equal(t, &priv.PublicKey, gotPub)
}

func TestKeyStoreResolverNilKeyStore(t *testing.T) {
	r := NewKeyStoreResolver(nil)
	_, err := r.PrivateKey("any")
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KEYSTORE_UNAVAILABLE", jerr.Code)
}

func TestResolvePolicyInbound(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	mockKS := keystoretest.NewMockKeyStore().
		WithPrivateKey("ours", priv).
		WithPublicKey("ours", &priv.PublicKey).
		WithPublicKey("peer", &priv.PublicKey)

	r := NewKeyStoreResolver(mockKS)
	p := &Policy{Direction: DirectionInbound, DecryptKid: "ours", VerifyKid: "peer"}
	require.NoError(t, ResolvePolicy(r, p))
}

func TestResolvePolicyMissingKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	mockKS := keystoretest.NewMockKeyStore().
		WithPrivateKey("ours", priv).
		WithPublicKey("ours", &priv.PublicKey)

	r := NewKeyStoreResolver(mockKS)
	p := &Policy{Direction: DirectionInbound, DecryptKid: "ours", VerifyKid: "missing-peer"}

	err = ResolvePolicy(r, p)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}
