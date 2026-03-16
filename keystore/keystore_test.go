package keystore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateTestKeys creates a fresh RSA key pair for testing.
func generateTestKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return privKey, &privKey.PublicKey
}

// writeDERFile writes DER-encoded bytes to a temp file and returns the path.
func writeDERFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, data, 0o600)
	require.NoError(t, err)
	return path
}

// marshalPublicKeyDER returns the DER-encoded PKIX form of a public key.
func marshalPublicKeyDER(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return der
}

// marshalPrivateKeyDER returns the DER-encoded PKCS8 form of a private key.
func marshalPrivateKeyDER(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return der
}

func TestNewStoreWithFileSource(t *testing.T) {
	privKey, pubKey := generateTestKeys(t)
	dir := t.TempDir()

	pubPath := writeDERFile(t, dir, "pub.der", marshalPublicKeyDER(t, pubKey))
	privPath := writeDERFile(t, dir, "priv.der", marshalPrivateKeyDER(t, privKey))

	s, err := newStore(map[string]config.KeyPairConfig{
		"signing": {
			Public:  config.KeySourceConfig{File: pubPath},
			Private: config.KeySourceConfig{File: privPath},
		},
	})

	require.NoError(t, err)

	gotPub, err := s.PublicKey("signing")
	require.NoError(t, err)
	assert.True(t, pubKey.Equal(gotPub))

	gotPriv, err := s.PrivateKey("signing")
	require.NoError(t, err)
	assert.True(t, privKey.Equal(gotPriv))
}

func TestNewStoreWithBase64Source(t *testing.T) {
	privKey, pubKey := generateTestKeys(t)

	pubB64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pubKey))
	privB64 := base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, privKey))

	s, err := newStore(map[string]config.KeyPairConfig{
		"encryption": {
			Public:  config.KeySourceConfig{Value: pubB64},
			Private: config.KeySourceConfig{Value: privB64},
		},
	})

	require.NoError(t, err)

	gotPub, err := s.PublicKey("encryption")
	require.NoError(t, err)
	assert.True(t, pubKey.Equal(gotPub))

	gotPriv, err := s.PrivateKey("encryption")
	require.NoError(t, err)
	assert.True(t, privKey.Equal(gotPriv))
}

func TestNewStoreMultipleKeyPairs(t *testing.T) {
	priv1, pub1 := generateTestKeys(t)
	priv2, pub2 := generateTestKeys(t)

	pub1B64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pub1))
	priv1B64 := base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, priv1))
	pub2B64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pub2))
	priv2B64 := base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, priv2))

	s, err := newStore(map[string]config.KeyPairConfig{
		"signing": {
			Public:  config.KeySourceConfig{Value: pub1B64},
			Private: config.KeySourceConfig{Value: priv1B64},
		},
		"encryption": {
			Public:  config.KeySourceConfig{Value: pub2B64},
			Private: config.KeySourceConfig{Value: priv2B64},
		},
	})

	require.NoError(t, err)

	gotPub1, err := s.PublicKey("signing")
	require.NoError(t, err)
	assert.True(t, pub1.Equal(gotPub1))

	gotPub2, err := s.PublicKey("encryption")
	require.NoError(t, err)
	assert.True(t, pub2.Equal(gotPub2))
	assert.False(t, pub1.Equal(gotPub2), "different key pairs should have different public keys")
}

func TestNewStorePrivateKeyOptional(t *testing.T) {
	_, pubKey := generateTestKeys(t)
	pubB64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pubKey))

	s, err := newStore(map[string]config.KeyPairConfig{
		"verify-only": {
			Public: config.KeySourceConfig{Value: pubB64},
			// No private key
		},
	})

	require.NoError(t, err)

	gotPub, err := s.PublicKey("verify-only")
	require.NoError(t, err)
	assert.True(t, pubKey.Equal(gotPub))

	_, err = s.PrivateKey("verify-only")
	assert.ErrorContains(t, err, "no private key configured")
}

func TestPublicKeyNotFound(t *testing.T) {
	s := &store{keys: map[string]*keyPair{}}

	_, err := s.PublicKey("nonexistent")
	assert.ErrorContains(t, err, `key "nonexistent" not found`)
}

func TestPrivateKeyNotFound(t *testing.T) {
	s := &store{keys: map[string]*keyPair{}}

	_, err := s.PrivateKey("nonexistent")
	assert.ErrorContains(t, err, `key "nonexistent" not found`)
}

func TestNewStoreFileNotFound(t *testing.T) {
	_, err := newStore(map[string]config.KeyPairConfig{
		"missing": {
			Public: config.KeySourceConfig{File: "/nonexistent/path.der"},
		},
	})

	assert.ErrorContains(t, err, "read file")
}

func TestNewStoreInvalidBase64(t *testing.T) {
	_, err := newStore(map[string]config.KeyPairConfig{
		"bad": {
			Public: config.KeySourceConfig{Value: "not-valid-base64!!!"},
		},
	})

	assert.ErrorContains(t, err, "base64 decode")
}

func TestNewStoreInvalidDER(t *testing.T) {
	badB64 := base64.StdEncoding.EncodeToString([]byte("not a real DER key"))

	_, err := newStore(map[string]config.KeyPairConfig{
		"corrupt": {
			Public: config.KeySourceConfig{Value: badB64},
		},
	})

	assert.ErrorContains(t, err, "ParsePKIXPublicKey")
}

func TestNewStoreInvalidPrivateKeyDER(t *testing.T) {
	_, pubKey := generateTestKeys(t)
	pubB64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pubKey))
	badPrivB64 := base64.StdEncoding.EncodeToString([]byte("not a real private key"))

	_, err := newStore(map[string]config.KeyPairConfig{
		"bad-priv": {
			Public:  config.KeySourceConfig{Value: pubB64},
			Private: config.KeySourceConfig{Value: badPrivB64},
		},
	})

	assert.ErrorContains(t, err, "PKCS1 fallback also failed")
}

func TestNewStorePKCS1Fallback(t *testing.T) {
	privKey, pubKey := generateTestKeys(t)

	// Marshal as PKCS1 (legacy format)
	pkcs1DER := x509.MarshalPKCS1PrivateKey(privKey)

	pubB64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pubKey))
	privB64 := base64.StdEncoding.EncodeToString(pkcs1DER)

	s, err := newStore(map[string]config.KeyPairConfig{
		"legacy": {
			Public:  config.KeySourceConfig{Value: pubB64},
			Private: config.KeySourceConfig{Value: privB64},
		},
	})

	require.NoError(t, err)

	gotPriv, err := s.PrivateKey("legacy")
	require.NoError(t, err)
	assert.True(t, privKey.Equal(gotPriv))
}

func TestNewStorePublicKeyRequired(t *testing.T) {
	_, err := newStore(map[string]config.KeyPairConfig{
		"no-public": {
			// No public key configured
			Private: config.KeySourceConfig{Value: "dW51c2Vk"},
		},
	})

	assert.ErrorContains(t, err, "public key is required")
}

func TestNewStoreMismatchedKeyPair(t *testing.T) {
	// Generate two separate key pairs — use pub from one, priv from the other
	_, pub1 := generateTestKeys(t)
	priv2, _ := generateTestKeys(t)

	pub1B64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pub1))
	priv2B64 := base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, priv2))

	_, err := newStore(map[string]config.KeyPairConfig{
		"mismatched": {
			Public:  config.KeySourceConfig{Value: pub1B64},
			Private: config.KeySourceConfig{Value: priv2B64},
		},
	})

	assert.ErrorContains(t, err, "public and private keys do not match")
}

func TestLoadDERBytesFromFile(t *testing.T) {
	dir := t.TempDir()
	expected := []byte("test-der-content")
	path := writeDERFile(t, dir, "test.der", expected)

	data, err := loadDERBytes(config.KeySourceConfig{File: path}, "test", "public")
	require.NoError(t, err)
	assert.Equal(t, expected, data)
}

func TestLoadDERBytesFromBase64(t *testing.T) {
	expected := []byte("test-der-content")
	b64 := base64.StdEncoding.EncodeToString(expected)

	data, err := loadDERBytes(config.KeySourceConfig{Value: b64}, "test", "public")
	require.NoError(t, err)
	assert.Equal(t, expected, data)
}

func TestLoadDERBytesNeitherSet(t *testing.T) {
	data, err := loadDERBytes(config.KeySourceConfig{}, "test", "public")
	require.NoError(t, err)
	assert.Nil(t, data)
}
