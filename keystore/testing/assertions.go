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

// AssertKeyNotFound verifies that retrieving a key with the given name is a miss on both
// PublicKey and PrivateKey: each lookup must return a non-nil error AND no key. Note that
// this does not distinguish between "key name not found" and "no private key configured" —
// error-ness alone is all it reads from the error.
//
// A stray key is reported by dynamic TYPE only, never by value (ADR-102).
//
// An unexpectedly FOUND public key — an error-free lookup, or a key handed back alongside
// the error — aborts the caller's test rather than recording a failure and continuing: the
// private-key assertion that follows would otherwise run against a keystore already known
// to be in the wrong state, and its result — pass or fail — says nothing useful once the
// first lookup has produced a key (ADR-101).
func AssertKeyNotFound(t *testing.T, ks app.KeyStore, name string) {
	t.Helper()
	assertKeyNotFound(t, ks, name)
}

func assertKeyNotFound(t keyStoreReporter, ks app.KeyStore, name string) {
	t.Helper()
	// The value guard precedes the error assertion on both arms so that each arm's error
	// assertion stays its block's last statement, which is what testifylint's require-error
	// rule reads to allow the private arm to stay assert.Error (ADR-101's positional rule).
	pubKey, pubErr := ks.PublicKey(name)
	if pubKey != nil {
		require.Fail(t, "unexpected key returned", "public key %q should not be found, but a %T was returned", name, pubKey)
	}
	require.Error(t, pubErr, "public key %q should not be found", name)

	privKey, privErr := ks.PrivateKey(name)
	if privKey != nil {
		assert.Fail(t, "unexpected key returned", "private key %q should not be found, but a %T was returned", name, privKey)
	}
	assert.Error(t, privErr, "private key %q should not be found", name)
}
