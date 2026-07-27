package keymaterial

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBytes(t *testing.T) {
	t.Run("file_path_reads", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "material.der")
		want := []byte{0x30, 0x03, 0x02, 0x01, 0x00}
		require.NoError(t, os.WriteFile(path, want, 0o600))

		got, err := LoadBytes(path, "")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("base64_value_decodes", func(t *testing.T) {
		want := []byte("raw key bytes")
		encoded := base64.StdEncoding.EncodeToString(want)

		got, err := LoadBytes("", encoded)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("neither_set_nil_nil", func(t *testing.T) {
		got, err := LoadBytes("", "")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("both_set_file_wins", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "material.der")
		want := []byte{0x30, 0x03, 0x02, 0x01, 0x01}
		require.NoError(t, os.WriteFile(path, want, 0o600))

		// Value side carries different (bogus) base64 to prove file, not
		// value, was actually read — not merely that value-decoding was
		// skipped without error.
		got, err := LoadBytes(path, "bm90IHRoZSBmaWxl")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("bad_base64_errors", func(t *testing.T) {
		_, err := LoadBytes("", "!!!not-base64!!!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base64 decode")
	})

	t.Run("inline_material_as_path_rejected", func(t *testing.T) {
		asFile := "-----BEGIN PRIVATE KEY-----\nZm9v\n-----END PRIVATE KEY-----\n"

		_, err := LoadBytes(asFile, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "looks like key material")
	})

	t.Run("nonexistent_file_errors", func(t *testing.T) {
		_, err := LoadBytes(filepath.Join(t.TempDir(), "missing.der"), "")
		require.Error(t, err)
	})
}

func TestParseRSAPublicKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	t.Run("valid_pkix_der", func(t *testing.T) {
		der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		require.NoError(t, err)

		got, err := ParseRSAPublicKey(der)
		require.NoError(t, err)
		assert.Equal(t, priv.N, got.N)
		assert.Equal(t, priv.E, got.E)
	})

	t.Run("garbage_der_errors", func(t *testing.T) {
		_, err := ParseRSAPublicKey([]byte{0x00, 0x01, 0x02})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ParsePKIXPublicKey")
	})

	t.Run("non_rsa_key_rejected", func(t *testing.T) {
		ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKIXPublicKey(&ecPriv.PublicKey)
		require.NoError(t, err)

		_, err = ParseRSAPublicKey(der)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected *rsa.PublicKey")
	})
}

func TestParseRSAPrivateKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	t.Run("valid_pkcs8_der", func(t *testing.T) {
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		require.NoError(t, err)

		got, err := ParseRSAPrivateKey(der)
		require.NoError(t, err)
		assert.Equal(t, priv.D, got.D)
	})

	t.Run("valid_pkcs1_der_fallback", func(t *testing.T) {
		der := x509.MarshalPKCS1PrivateKey(priv)

		got, err := ParseRSAPrivateKey(der)
		require.NoError(t, err)
		assert.Equal(t, priv.D, got.D)
	})

	t.Run("garbage_der_errors", func(t *testing.T) {
		_, err := ParseRSAPrivateKey([]byte{0x00, 0x01, 0x02})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PKCS8 failed")
		assert.Contains(t, err.Error(), "PKCS1 fallback also failed")
	})

	t.Run("non_rsa_key_rejected", func(t *testing.T) {
		ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKCS8PrivateKey(ecPriv)
		require.NoError(t, err)

		_, err = ParseRSAPrivateKey(der)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PKCS8 parsed but not RSA")
	})
}
