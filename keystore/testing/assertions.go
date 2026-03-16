package testing

import (
	"testing"

	"github.com/gaborage/go-bricks/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertPublicKeyAvailable verifies that a public key with the given name
// can be successfully retrieved from the KeyStore.
func AssertPublicKeyAvailable(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	key, err := ks.PublicKey(name)
	require.NoError(t, err, "public key %q should be available", name)
	assert.NotNil(t, key, "public key %q should not be nil", name)
}

// AssertPrivateKeyAvailable verifies that a private key with the given name
// can be successfully retrieved from the KeyStore.
func AssertPrivateKeyAvailable(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	key, err := ks.PrivateKey(name)
	require.NoError(t, err, "private key %q should be available", name)
	assert.NotNil(t, key, "private key %q should not be nil", name)
}

// AssertKeyNotFound verifies that retrieving a key with the given name returns an error
// from both PublicKey and PrivateKey. Note that this does not distinguish between
// "key name not found" and "no private key configured" — it only asserts that both
// lookups return a non-nil error.
func AssertKeyNotFound(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	_, pubErr := ks.PublicKey(name)
	assert.Error(t, pubErr, "public key %q should not be found", name)

	_, privErr := ks.PrivateKey(name)
	assert.Error(t, privErr, "private key %q should not be found", name)
}
