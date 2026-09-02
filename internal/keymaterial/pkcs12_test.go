package keymaterial

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"software.sslmate.com/src/go-pkcs12"

	testconsts "github.com/gaborage/go-bricks/testing"
)

func selfSignedCert(t *testing.T, key crypto.Signer) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "keymaterial-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func TestParsePKCS12RSA(t *testing.T) {
	password := testconsts.FakePassword("p12")
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	leaf := selfSignedCert(t, rsaKey)

	t.Run("modern_bundle_yields_rsa_key", func(t *testing.T) {
		pfx, err := pkcs12.Modern.Encode(rsaKey, leaf, nil, password)
		require.NoError(t, err)

		got, err := ParsePKCS12RSA(pfx, password)
		require.NoError(t, err)
		assert.True(t, rsaKey.Equal(got))
	})

	t.Run("legacy_bundle_with_ca_chain_yields_rsa_key", func(t *testing.T) {
		caKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		ca := selfSignedCert(t, caKey)
		pfx, err := pkcs12.Legacy.Encode(rsaKey, leaf, []*x509.Certificate{ca}, password)
		require.NoError(t, err)

		got, err := ParsePKCS12RSA(pfx, password)
		require.NoError(t, err)
		assert.True(t, rsaKey.Equal(got))
	})

	t.Run("wrong_password_is_distinct_and_never_echoed", func(t *testing.T) {
		pfx, err := pkcs12.Modern.Encode(rsaKey, leaf, nil, password)
		require.NoError(t, err)
		wrong := testconsts.FakePassword("wrong")

		_, err = ParsePKCS12RSA(pfx, wrong)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "password incorrect")
		assert.NotContains(t, err.Error(), wrong)
		assert.NotContains(t, err.Error(), password)
	})

	t.Run("corrupt_bundle_is_a_decode_error", func(t *testing.T) {
		_, err := ParsePKCS12RSA([]byte("not a pkcs12 bundle at all"), password)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode")
		assert.NotContains(t, err.Error(), "password incorrect")
		assert.NotContains(t, err.Error(), password)
	})

	t.Run("ec_only_bundle_names_the_rsa_allowlist", func(t *testing.T) {
		ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		pfx, err := pkcs12.Modern.Encode(ecKey, selfSignedCert(t, ecKey), nil, password)
		require.NoError(t, err)

		_, err = ParsePKCS12RSA(pfx, password)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "*ecdsa.PrivateKey")
		assert.Contains(t, err.Error(), "only RSA")
		assert.NotContains(t, err.Error(), password)
	})

	t.Run("certificate_not_matching_key_rejected", func(t *testing.T) {
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		pfx, err := pkcs12.Modern.Encode(rsaKey, selfSignedCert(t, otherKey), nil, password)
		require.NoError(t, err)

		_, err = ParsePKCS12RSA(pfx, password)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})
}

func TestLoadPassword(t *testing.T) {
	password := testconsts.FakePassword("load")

	t.Run("env_set_returns_value", func(t *testing.T) {
		t.Setenv("KEYMATERIAL_TEST_P12_PASSWORD", password)

		got, err := LoadPassword("KEYMATERIAL_TEST_P12_PASSWORD", "")
		require.NoError(t, err)
		assert.Equal(t, password, got)
	})

	t.Run("env_unset_errors_name_elided", func(t *testing.T) {
		_, err := LoadPassword("KEYMATERIAL_TEST_P12_PASSWORD_UNSET", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not set")
		assert.NotContains(t, err.Error(), "KEYMATERIAL_TEST_P12_PASSWORD_UNSET")
	})

	t.Run("env_empty_errors", func(t *testing.T) {
		t.Setenv("KEYMATERIAL_TEST_P12_PASSWORD_EMPTY", "")

		_, err := LoadPassword("KEYMATERIAL_TEST_P12_PASSWORD_EMPTY", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not set")
	})

	t.Run("file_read_strips_trailing_newline_only", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "p12.pass")
		require.NoError(t, os.WriteFile(path, []byte(" "+password+"\r\n"), 0o600))

		got, err := LoadPassword("", path)
		require.NoError(t, err)
		assert.Equal(t, " "+password, got)
	})

	t.Run("file_unreadable_errors_path_elided", func(t *testing.T) {
		misFiled := password

		_, err := LoadPassword("", misFiled)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "elided")
		assert.NotContains(t, err.Error(), misFiled)
	})

	t.Run("file_empty_errors", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "p12.pass")
		require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))

		_, err := LoadPassword("", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("neither_set_errors", func(t *testing.T) {
		_, err := LoadPassword("", "")
		require.Error(t, err)
	})
}
