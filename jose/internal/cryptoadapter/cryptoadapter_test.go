package cryptoadapter

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k
}

func TestSignVerifyRoundtrip(t *testing.T) {
	key := newKey(t)
	payload := []byte(`{"hello":"world"}`)

	compact, err := Sign(payload, key, &SignOptions{
		Kid:    "test-key",
		SigAlg: jose.RS256,
		Cty:    "application/json",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, compact)

	got, hdr, err := Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "test-key",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, "test-key", hdr.Kid)
	assert.Equal(t, "RS256", hdr.Alg)
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := newKey(t)
	payload := []byte(`{"secret":"value"}`)

	compact, err := Encrypt(payload, &key.PublicKey, &EncryptOptions{
		Kid:    "test-key",
		KeyAlg: jose.RSA_OAEP_256,
		Enc:    jose.A256GCM,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, compact)

	got, hdr, err := Decrypt(compact, key, &DecryptOptions{
		ExpectedKid:       "test-key",
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, "test-key", hdr.Kid)
}

func TestVerifyRejectsKidMismatch(t *testing.T) {
	key := newKey(t)
	compact, err := Sign([]byte("x"), key, &SignOptions{Kid: "wrong-kid", SigAlg: jose.RS256})
	require.NoError(t, err)

	_, _, err = Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "expected-kid",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKidMismatch))
}

func TestDecryptRejectsKidMismatch(t *testing.T) {
	key := newKey(t)
	compact, err := Encrypt([]byte("x"), &key.PublicKey, &EncryptOptions{
		Kid:    "wrong-kid",
		KeyAlg: jose.RSA_OAEP_256,
		Enc:    jose.A256GCM,
	})
	require.NoError(t, err)

	_, _, err = Decrypt(compact, key, &DecryptOptions{
		ExpectedKid:       "expected-kid",
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKidMismatch))
}

func TestVerifyRejectsDisallowedAlg(t *testing.T) {
	key := newKey(t)
	compact, err := Sign([]byte("x"), key, &SignOptions{Kid: "k", SigAlg: jose.RS256})
	require.NoError(t, err)

	// Allowlist excludes RS256 — should fail at parse, not verify.
	_, _, err = Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "k",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.PS256},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrParseSigned))
}

func TestVerifyRejectsKidMissing(t *testing.T) {
	key := newKey(t)
	// Sign with empty kid — Verify must reject because policy requires a kid.
	compact, err := Sign([]byte("x"), key, &SignOptions{Kid: "", SigAlg: jose.RS256})
	require.NoError(t, err)

	_, _, err = Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "anything",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKidMissing))
}

func TestDecryptRejectsKidMissing(t *testing.T) {
	key := newKey(t)
	compact, err := Encrypt([]byte("x"), &key.PublicKey, &EncryptOptions{
		Kid: "", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM,
	})
	require.NoError(t, err)

	_, _, err = Decrypt(compact, key, &DecryptOptions{
		ExpectedKid:       "anything",
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKidMissing))
}

func TestVerifyAcceptsEmptyTypInAllowlist(t *testing.T) {
	// Sign() does not set a typ header, so the verified header has typ="". This test
	// exercises the AllowedTyps != nil branch with empty being explicitly permitted —
	// covers the contains() helper without requiring the upstream library to support
	// emitting a typ header at sign time.
	key := newKey(t)
	compact, err := Sign([]byte("x"), key, &SignOptions{Kid: "k", SigAlg: jose.RS256, Cty: "JWT"})
	require.NoError(t, err)

	_, _, err = Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "k",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
		AllowedTyps:    []string{"", "JWT"},
	})
	require.NoError(t, err)
}

func TestVerifyRejectsTypNotInAllowlist(t *testing.T) {
	key := newKey(t)
	compact, err := Sign([]byte("x"), key, &SignOptions{Kid: "k", SigAlg: jose.RS256})
	require.NoError(t, err)

	// Empty typ not in allowlist => ErrTypRejected.
	_, _, err = Verify(compact, &key.PublicKey, &VerifyOptions{
		ExpectedKid:    "k",
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
		AllowedTyps:    []string{"strict-only"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTypRejected))
}

func TestDecryptRejectsDisallowedKeyAlg(t *testing.T) {
	key := newKey(t)
	compact, err := Encrypt([]byte("x"), &key.PublicKey, &EncryptOptions{
		Kid:    "k",
		KeyAlg: jose.RSA_OAEP_256,
		Enc:    jose.A256GCM,
	})
	require.NoError(t, err)

	// Allowlist that excludes RSA-OAEP-256 (but includes a different algorithm).
	_, _, err = Decrypt(compact, key, &DecryptOptions{
		ExpectedKid:       "k",
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrParseEncrypted))
}
