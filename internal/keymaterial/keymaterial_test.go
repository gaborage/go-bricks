package keymaterial

import (
	"bytes"
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

	"github.com/gaborage/go-bricks/jose"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// syntheticSecret is a fixed, non-real 32-byte fixture — never key material
// that could be mistaken for a real secret if echoed by a bug under test.
var syntheticSecret = bytes.Repeat([]byte{0xAB}, 32)

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
		asFile := string(testconsts.PEMFixture("PRIVATE KEY"))

		_, err := LoadBytes(asFile, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "looks like key material")
	})

	t.Run("nonexistent_file_errors", func(t *testing.T) {
		_, err := LoadBytes(filepath.Join(t.TempDir(), "missing.der"), "")
		require.Error(t, err)
	})
}

func TestLoadSecretBytes(t *testing.T) {
	t.Run("file_path_reads", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.bin")
		require.NoError(t, os.WriteFile(path, syntheticSecret, 0o600))

		got, err := LoadSecretBytes(path, "")
		require.NoError(t, err)
		assert.Equal(t, syntheticSecret, got)
	})

	t.Run("base64_value_decodes", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString(syntheticSecret)

		got, err := LoadSecretBytes("", encoded)
		require.NoError(t, err)
		assert.Equal(t, syntheticSecret, got)
	})

	t.Run("neither_set_nil_nil", func(t *testing.T) {
		got, err := LoadSecretBytes("", "")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("pem_in_file_field_rejected_echo_free", func(t *testing.T) {
		asFile := string(testconsts.PEMFixture("PRIVATE KEY"))

		_, err := LoadSecretBytes(asFile, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "looks like key material")
		assert.NotContains(t, err.Error(), asFile)
	})

	t.Run("bad_base64_errors_value_elided", func(t *testing.T) {
		_, err := LoadSecretBytes("", "!!!not-base64!!!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "elided")
	})

	// The regression pin: a base64-encoded 32-byte raw symmetric secret filed
	// under the wrong field (secret.file instead of secret.value) is below
	// LooksLikeKeyMaterial's 48-byte DER floor, so it reaches os.ReadFile and
	// fails to read. LoadSecretBytes must never echo the fixture value or any
	// path fragment derived from it into the returned error.
	t.Run("mis_filed_secret_never_echoed", func(t *testing.T) {
		misFiled := base64.StdEncoding.EncodeToString(syntheticSecret)
		require.Less(t, len(syntheticSecret), 48, "fixture must decode to fewer than minDERKeyBytes to exercise the regression (LooksLikeKeyMaterial's DER floor)")

		_, err := LoadSecretBytes(misFiled, "")
		require.Error(t, err)
		msg := err.Error()
		assert.NotContains(t, msg, misFiled, "the fixture value must not be echoed")
		for i := 0; i+8 <= len(misFiled); i += 8 {
			assert.NotContains(t, msg, misFiled[i:i+8], "no path fragment of the fixture value may be echoed")
		}
		assert.Contains(t, msg, "elided")
	})

	// Mirror assertion: the pre-existing LoadBytes (RSA path) DOES echo the
	// same input on the same failure — pins the intentional asymmetry between
	// the RSA and secret loaders (RSA is shape-detected first; secrets are not).
	t.Run("mirror_load_bytes_does_echo_same_input", func(t *testing.T) {
		misFiled := base64.StdEncoding.EncodeToString(syntheticSecret)

		_, err := LoadBytes(misFiled, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), misFiled, "LoadBytes is expected to echo — this pins the asymmetry, not a bug")
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

// ProducerKeys carries jose's resolver method set without importing jose from
// the package itself; this assertion is where that structural claim is pinned.
var _ jose.KeyResolver = (*ProducerKeys)(nil)

func TestLoadRSAPrivateKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)

	t.Run("file_path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sign.der")
		require.NoError(t, os.WriteFile(path, privDER, 0o600))

		got, err := LoadRSAPrivateKey(path, "")
		require.NoError(t, err)
		assert.Equal(t, priv.D, got.D)
	})

	t.Run("base64_value", func(t *testing.T) {
		got, err := LoadRSAPrivateKey("", base64.StdEncoding.EncodeToString(privDER))
		require.NoError(t, err)
		assert.Equal(t, priv.D, got.D)
	})

	t.Run("neither_set_errors", func(t *testing.T) {
		got, err := LoadRSAPrivateKey("", "")
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("wrong_key_class_errors", func(t *testing.T) {
		_, err := LoadRSAPrivateKey("", base64.StdEncoding.EncodeToString(pubDER))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PKCS1 fallback also failed")
	})
}

func TestLoadRSAPublicKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)

	t.Run("file_path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "enc.der")
		require.NoError(t, os.WriteFile(path, pubDER, 0o600))

		got, err := LoadRSAPublicKey(path, "")
		require.NoError(t, err)
		assert.Equal(t, priv.N, got.N)
	})

	t.Run("base64_value", func(t *testing.T) {
		got, err := LoadRSAPublicKey("", base64.StdEncoding.EncodeToString(pubDER))
		require.NoError(t, err)
		assert.Equal(t, priv.N, got.N)
	})

	t.Run("neither_set_errors", func(t *testing.T) {
		got, err := LoadRSAPublicKey("", "")
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("wrong_key_class_errors", func(t *testing.T) {
		_, err := LoadRSAPublicKey("", base64.StdEncoding.EncodeToString(privDER))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ParsePKIXPublicKey")
	})
}

func TestProducerKeys(t *testing.T) {
	signPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	require.NotEqual(t, signPriv.N, encPriv.N, "the two roles must hold distinct keys for the cross-role cases to bite")

	keys := &ProducerKeys{
		SignKid:    "sign-v1",
		SignPriv:   signPriv,
		EncryptKid: "enc-v1",
		EncPub:     &encPriv.PublicKey,
	}

	t.Run("sign_kid_returns_private", func(t *testing.T) {
		got, err := keys.PrivateKey("sign-v1")
		require.NoError(t, err)
		assert.Equal(t, signPriv.D, got.D)
	})

	t.Run("encrypt_kid_returns_public", func(t *testing.T) {
		got, err := keys.PublicKey("enc-v1")
		require.NoError(t, err)
		assert.Equal(t, encPriv.N, got.N)
	})

	t.Run("unknown_private_kid_errors", func(t *testing.T) {
		_, err := keys.PrivateKey("nope-v9")
		require.Error(t, err)
		assert.Equal(t, `no private key registered for kid "nope-v9"`, err.Error())
	})

	t.Run("unknown_public_kid_errors", func(t *testing.T) {
		_, err := keys.PublicKey("nope-v9")
		require.Error(t, err)
		assert.Equal(t, `no public key registered for kid "nope-v9"`, err.Error())
	})

	// A kid valid in the OTHER role must not resolve: the sign kid is not a
	// public-key kid and the encrypt kid is not a private-key kid.
	t.Run("cross_role_kid_errors", func(t *testing.T) {
		_, err := keys.PrivateKey("enc-v1")
		require.Error(t, err)
		assert.Equal(t, `no private key registered for kid "enc-v1"`, err.Error())

		_, err = keys.PublicKey("sign-v1")
		require.Error(t, err)
		assert.Equal(t, `no public key registered for kid "sign-v1"`, err.Error())
	})
}
