package testing

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/keystore"
)

func TestMockKeyStoreSecretRoundTrip(t *testing.T) {
	want := []byte("symmetric-mac-key-material")
	m := NewMockKeyStore().WithSecret("mac", want)

	got, err := m.Secret("mac")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestMockKeyStoreSecretNotFound(t *testing.T) {
	_, err := NewMockKeyStore().Secret("missing")
	assert.ErrorContains(t, err, `secret "missing" not found`)
}

func TestMockKeyStoreWithSecretError(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := NewMockKeyStore().WithSecret("mac", []byte("x")).WithSecretError(sentinel).Secret("mac")
	assert.ErrorIs(t, err, sentinel)
}

func TestMockKeyStoreSecretIsolatedFromCallerMutation(t *testing.T) {
	src := []byte("original-key")
	m := NewMockKeyStore().WithSecret("mac", src)
	src[0] ^= 0xFF // mutate the input after storing

	got, err := m.Secret("mac")
	require.NoError(t, err)
	assert.Equal(t, []byte("original-key"), got, "WithSecret must copy its input")

	got[1] ^= 0xFF // mutate the returned slice
	again, err := m.Secret("mac")
	require.NoError(t, err)
	assert.Equal(t, []byte("original-key"), again, "Secret must return a defensive copy")
}

func TestMockKeyStoreGenerationsKeepsDeclaredOrder(t *testing.T) {
	mock := NewMockKeyStore().
		WithGeneration("svc-sign", "v2", keystore.RolePrivate).
		WithGeneration("svc-sign", "v1", keystore.RolePublicOnly)

	var enumerator keystore.FamilyEnumerator = mock
	got := enumerator.Generations("svc-sign")
	require.Len(t, got, 2)
	assert.Equal(t, "svc-sign-v2", got[0].Kid())
	assert.Equal(t, keystore.RolePrivate, got[0].Role)
	assert.Equal(t, "svc-sign-v1", got[1].Kid())
	assert.Empty(t, enumerator.Generations("nobody"))

	got[0].Version = "tampered"
	assert.Equal(t, "v2", mock.Generations("svc-sign")[0].Version)
}
