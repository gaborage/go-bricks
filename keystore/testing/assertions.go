package testing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
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

// AssertSecretAvailable verifies that a non-empty symmetric secret with the
// given name can be successfully retrieved from the KeyStore.
func AssertSecretAvailable(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	secret, err := ks.Secret(name)
	require.NoError(t, err, "secret %q should be available", name)
	assert.NotEmpty(t, secret, "secret %q should not be empty", name)
}

// keyStoreReporter is the slice of *testing.T the assertions need: testify's
// require.TestingT plus Helper. It exists so the helpers' own failure paths can be
// exercised by a recording double — the same shape as observability/testing.TB and
// messaging/internal/lanecontract.T. (cache/testing's testReporter is Helper+Errorf
// only, which cannot express an abort, so it is not the precedent here.)
type keyStoreReporter interface {
	require.TestingT
	Helper()
}

// AssertKeyNotFound verifies that retrieving a key with the given name returns an error
// from both PublicKey and PrivateKey. Note that this does not distinguish between
// "key name not found" and "no private key configured" — it only asserts that both
// lookups return a non-nil error.
//
// An unexpectedly FOUND public key aborts the caller's test rather than recording a
// failure and continuing: the private-key assertion that follows would otherwise run
// against a keystore already known to be in the wrong state, and its result — pass or
// fail — says nothing useful once the first lookup has succeeded (ADR-101).
func AssertKeyNotFound(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	assertKeyNotFound(t, ks, name)
}

func assertKeyNotFound(t keyStoreReporter, ks app.KeyStore, name string) {
	t.Helper()
	_, pubErr := ks.PublicKey(name)
	require.Error(t, pubErr, "public key %q should not be found", name)

	_, privErr := ks.PrivateKey(name)
	assert.Error(t, privErr, "private key %q should not be found", name)
}
