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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// syntheticSecret is a fixed, non-real 32-byte fixture — never key material
// that could be mistaken for a real secret if echoed by a bug under test.
var syntheticSecret = bytes.Repeat([]byte{0xAB}, 32)

// assertNoKeyBytes fails when bytes were returned, reporting their LENGTH only —
// never the bytes themselves (ADR-102). It records rather than aborts; a site whose
// later assertions would be meaningless past a stray buffer needs its own require.
func assertNoKeyBytes(t *testing.T, data []byte) {
	t.Helper()
	if data != nil {
		assert.Fail(t, "unexpected key bytes returned", "expected no bytes, got %d", len(data))
	}
}

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
		assertNoKeyBytes(t, got)
		require.NoError(t, err)
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
		assertNoKeyBytes(t, got)
		require.NoError(t, err)
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
		// Reported by TYPE, never by value (ADR-102).
		if got != nil {
			assert.Fail(t, "unexpected key returned", "expected no key, got a %T", got)
		}
		require.Error(t, err)
	})

	t.Run("wrong_key_class_errors", func(t *testing.T) {
		_, err := LoadRSAPrivateKey("", base64.StdEncoding.EncodeToString(pubDER))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PKCS1 fallback also failed")
	})

	// The load hop can fail before any DER exists to parse; its error must
	// surface as LoadBytes worded it, with no prefix of the loader's own —
	// callers supply their own role wording ("sign key: %w").
	t.Run("load_error_propagates", func(t *testing.T) {
		_, err := LoadRSAPrivateKey(filepath.Join(t.TempDir(), "missing.der"), "")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "PKCS8 failed", "a load failure must not be reported as a parse failure")

		_, err = LoadRSAPrivateKey("", "!!!not-base64!!!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base64 decode")
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
		// Reported by TYPE, never by value (ADR-102).
		if got != nil {
			assert.Fail(t, "unexpected key returned", "expected no key, got a %T", got)
		}
		require.Error(t, err)
	})

	t.Run("wrong_key_class_errors", func(t *testing.T) {
		_, err := LoadRSAPublicKey("", base64.StdEncoding.EncodeToString(privDER))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ParsePKIXPublicKey")
	})

	// Mirror of the private loader's case: a load-hop failure surfaces
	// unprefixed, never mislabelled as a parse failure.
	t.Run("load_error_propagates", func(t *testing.T) {
		_, err := LoadRSAPublicKey(filepath.Join(t.TempDir(), "missing.der"), "")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "ParsePKIXPublicKey", "a load failure must not be reported as a parse failure")

		_, err = LoadRSAPublicKey("", "!!!not-base64!!!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base64 decode")
	})
}

// resolverPair is the two keys the resolver tests need: one per role.
type resolverPair struct{ sign, enc *rsa.PrivateKey }

// resolverKeys mints them once for the package. Neither resolver test mutates a key, and
// 2048-bit generation is ~37ms apiece, so four generations bought nothing over two.
var resolverKeys = sync.OnceValue(func() *resolverPair {
	sign, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("keymaterial test: generate sign key: " + err.Error())
	}
	enc, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("keymaterial test: generate encrypt key: " + err.Error())
	}
	return &resolverPair{sign: sign, enc: enc}
})

func TestProducerKeys(t *testing.T) {
	signPriv, encPriv := resolverKeys().sign, resolverKeys().enc
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

// ConsumerKeys carries the same jose resolver method set as ProducerKeys, in the
// inverse roles; this assertion is where that structural claim is pinned.
var _ jose.KeyResolver = (*ConsumerKeys)(nil)

// TestConsumerKeys mirrors TestProducerKeys with the roles swapped: the SIGN kid
// serves the PUBLIC half (verify) and the ENCRYPT kid the PRIVATE half (decrypt).
func TestConsumerKeys(t *testing.T) {
	signPriv, encPriv := resolverKeys().sign, resolverKeys().enc
	require.NotEqual(t, signPriv.N, encPriv.N, "the two roles must hold distinct keys for the cross-role cases to bite")

	keys := &ConsumerKeys{
		SignKid:    "sign-v1",
		SignPub:    &signPriv.PublicKey,
		EncryptKid: "enc-v1",
		EncPriv:    encPriv,
	}

	t.Run("sign_kid_returns_public", func(t *testing.T) {
		got, err := keys.PublicKey("sign-v1")
		require.NoError(t, err)
		assert.Equal(t, signPriv.N, got.N)
	})

	t.Run("encrypt_kid_returns_private", func(t *testing.T) {
		got, err := keys.PrivateKey("enc-v1")
		require.NoError(t, err)
		assert.Equal(t, encPriv.D, got.D)
	})

	t.Run("unknown_public_kid_errors", func(t *testing.T) {
		_, err := keys.PublicKey("nope-v9")
		require.Error(t, err)
		assert.Equal(t, `no public key registered for kid "nope-v9"`, err.Error())
	})

	t.Run("unknown_private_kid_errors", func(t *testing.T) {
		_, err := keys.PrivateKey("nope-v9")
		require.Error(t, err)
		assert.Equal(t, `no private key registered for kid "nope-v9"`, err.Error())
	})

	// The inverse of ProducerKeys' cross-role case: here the ENCRYPT kid is not a
	// public-key kid and the SIGN kid is not a private-key kid.
	t.Run("cross_role_kid_errors", func(t *testing.T) {
		_, err := keys.PublicKey("enc-v1")
		require.Error(t, err)
		assert.Equal(t, `no public key registered for kid "enc-v1"`, err.Error())

		_, err = keys.PrivateKey("sign-v1")
		require.Error(t, err)
		assert.Equal(t, `no private key registered for kid "sign-v1"`, err.Error())
	})
}

// TestConsumerKeysFailClosedOnUnsetKey pins the guard: a zero ConsumerKeys has an empty
// kid in both slots, so a lookup with the empty kid would otherwise MATCH and hand back a
// nil key with a nil error. Both doors must refuse instead.
func TestConsumerKeysFailClosedOnUnsetKey(t *testing.T) {
	cases := []struct {
		name string
		keys *ConsumerKeys
	}{
		{"zero_value", &ConsumerKeys{}},
		{"kids_set_keys_absent", &ConsumerKeys{SignKid: "sign-v1", EncryptKid: "enc-v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub, err := tc.keys.PublicKey(tc.keys.SignKid)
			// Reported by TYPE, never by value (ADR-102).
			if pub != nil {
				assert.Fail(t, "unexpected public key returned", "expected none, got a %T", pub)
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no public key registered for kid")

			priv, err := tc.keys.PrivateKey(tc.keys.EncryptKid)
			if priv != nil {
				assert.Fail(t, "unexpected private key returned", "expected none, got a %T", priv)
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no private key registered for kid")
		})
	}
}
