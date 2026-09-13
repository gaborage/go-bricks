package cryptoadapter

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
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
	assert.ErrorIs(t, err, ErrKidMismatch)
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
	assert.ErrorIs(t, err, ErrKidMismatch)
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
	assert.ErrorIs(t, err, ErrParseSigned)
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
	assert.ErrorIs(t, err, ErrKidMissing)
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
	assert.ErrorIs(t, err, ErrKidMissing)
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
	assert.ErrorIs(t, err, ErrParseEncrypted)
}

// bigExtra returns an extra protected header whose value pads segment 0 by n bytes.
func bigExtra(bytes int) map[string]any {
	return map[string]any{"pad": strings.Repeat("a", bytes)}
}

// A compact whose protected header is valid but oversized must be refused before go-jose
// parses it: go-jose imposes no bound of its own, so without the check these open fine.
func TestDecryptAndVerifyRejectOversizedProtectedHeader(t *testing.T) {
	key := newKey(t)

	signed, err := Sign([]byte(`{"a":1}`), key, &SignOptions{
		Kid: "test-key", SigAlg: jose.RS256, Extra: bigExtra(maxPeekHeaderBytes),
	})
	require.NoError(t, err)
	require.Greater(t, len(strings.Split(signed, ".")[0]), maxPeekHeaderBytes)

	payload, _, err := Verify(signed, &key.PublicKey, &VerifyOptions{
		ExpectedKid: "test-key", AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	assert.Nil(t, payload, "a refusal must return no payload")
	assert.ErrorIs(t, err, ErrParseSigned)
	assert.ErrorIs(t, err, ErrHeaderTooLarge, "the size refusal stays distinguishable on the verify door too")

	encrypted, err := Encrypt([]byte(`{"a":1}`), &key.PublicKey, &EncryptOptions{
		Kid: "test-key", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM, Extra: bigExtra(maxPeekHeaderBytes),
	})
	require.NoError(t, err)
	require.Greater(t, len(strings.Split(encrypted, ".")[0]), maxPeekHeaderBytes)

	plaintext, _, err := Decrypt(encrypted, key, &DecryptOptions{
		ExpectedKid:       "test-key",
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	assert.Nil(t, plaintext, "a refusal must return no plaintext")
	assert.ErrorIs(t, err, ErrParseEncrypted)
	assert.ErrorIs(t, err, ErrHeaderTooLarge, "the size refusal stays distinguishable inside the chain")

	// A generic parse failure must NOT carry the size class, or the distinction is useless.
	_, _, err = Verify("not.a.jws", &key.PublicKey, &VerifyOptions{AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256}})
	assert.ErrorIs(t, err, ErrParseSigned)
	assert.NotErrorIs(t, err, ErrHeaderTooLarge)
}

// Both doors must carry that refusal, not only the shared gate.
func TestDecryptAndVerifyRefuseOneByteOverTheCap(t *testing.T) {
	key := newKey(t)
	over := strings.Repeat("A", maxPeekHeaderBytes+1)

	_, _, err := Verify(over+".payload.signature", &key.PublicKey, &VerifyOptions{
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	assert.ErrorIs(t, err, ErrParseSigned)
	assert.ErrorIs(t, err, ErrHeaderTooLarge)

	_, _, err = Decrypt(over+".key.iv.ciphertext.tag", key, &DecryptOptions{
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	assert.ErrorIs(t, err, ErrParseEncrypted)
	assert.ErrorIs(t, err, ErrHeaderTooLarge)
}

// go-jose strips surrounding whitespace before parsing, so a token read from a file with a
// trailing newline opened fine before the bound existed; it must keep opening.
func TestVerifyToleratesSurroundingWhitespace(t *testing.T) {
	key := newKey(t)
	signed, err := Sign([]byte(`{"a":1}`), key, &SignOptions{Kid: "k", SigAlg: jose.RS256})
	require.NoError(t, err)

	payload, _, err := Verify(" "+signed+"\n", &key.PublicKey, &VerifyOptions{
		ExpectedKid: "k", AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(payload))
}

// go-jose accepts JSON serialization as well as compact, and a JSON body's dot-delimited
// runs say nothing about its "protected" member: a decoy holding two dots would carry an
// unbounded header past a length check on the first run alone. Both doors take compact only.
func TestDecryptAndVerifyRejectJSONSerialization(t *testing.T) {
	key := newKey(t)
	huge := strings.Repeat("a", 1<<20)

	jsonJWS := `{"dec":"a.b.c","protected":"` + huge + `","payload":"e30","signature":"x"}`
	_, _, err := Verify(jsonJWS, &key.PublicKey, &VerifyOptions{
		AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	assert.ErrorIs(t, err, ErrParseSigned)
	assert.ErrorIs(t, err, ErrNotCompact, "the refusal must come from the shape guard, not from go-jose downstream")

	jsonJWE := `{"dec":"a.b.c","protected":"` + huge + `","ciphertext":"x","tag":"y","iv":"z"}`
	_, _, err = Decrypt(jsonJWE, key, &DecryptOptions{
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	assert.ErrorIs(t, err, ErrParseEncrypted)
	assert.ErrorIs(t, err, ErrNotCompact)
}

// The bound must not be so tight that a fat but legitimate header is refused.
func TestDecryptAndVerifyAcceptHeaderUnderTheBound(t *testing.T) {
	key := newKey(t)
	// 10 KiB of padding (~13.4 KiB once the header is base64url-encoded): a size proxy for a
	// fat real header, above the ~7.1 KiB a jwk plus a 3-cert RSA-2048 x5c chain reaches. It
	// cannot be the real thing, since jwk and x5c are reserved params CheckExtra rejects, so
	// the Sign seam cannot emit one.
	signed, err := Sign([]byte(`{"a":1}`), key, &SignOptions{
		Kid: "test-key", SigAlg: jose.RS256, Extra: bigExtra(10 * 1024),
	})
	require.NoError(t, err)

	payload, _, err := Verify(signed, &key.PublicKey, &VerifyOptions{
		ExpectedKid: "test-key", AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(payload))
}
