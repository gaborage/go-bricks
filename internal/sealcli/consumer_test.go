package sealcli

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	verifyUse  = "used to verify the sealed document's signature"
	decryptUse = "used to decrypt the subject member"
)

func TestConsumerKeyFlags(t *testing.T) {
	fs := newFlagSet(t)
	keys := ConsumerKeyFlags(fs, verifyUse, decryptUse)
	require.NotNil(t, keys)

	// The roles are the mirror of KeyFlags': sign is PUBLIC here, encrypt PRIVATE.
	wantUsage := map[string]string{
		signKeyFile:  "path to a DER-encoded RSA public key (PKIX) " + verifyUse,
		signKeyValue: "base64-encoded DER RSA public key (alternative to -sign-key-file)",
		encKeyFile:   "path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) " + decryptUse,
		encKeyValue:  "base64-encoded DER RSA private key (alternative to -encrypt-key-file; argv is process-visible — fixture keys only)",
	}
	for name, usage := range wantUsage {
		f := fs.Lookup(name)
		require.NotNil(t, f, "flag -%s not registered", name)
		assert.Equal(t, usage, f.Usage, "usage text of -%s", name)
		assert.Empty(t, f.DefValue, "default of -%s", name)
	}

	require.NoError(t, fs.Parse([]string{"-sign-key-value", "x", "-encrypt-key-file", "y"}))
	assert.Empty(t, keys.SignFile)
	assert.Equal(t, "x", keys.SignValue)
	assert.Equal(t, "y", keys.EncryptFile)
	assert.Empty(t, keys.EncryptValue)
}

// consumerRefusalCases are the four wrong-source shapes, with the same strings the
// producer pair refuses by: an operator reads one vocabulary across both binaries.
var consumerRefusalCases = []struct {
	name string
	keys ConsumerKeySources
	want string
}{
	{"both_sign_sources", ConsumerKeySources{SignFile: missingPath, SignValue: "AAAA", EncryptFile: missingPath}, signRefusal},
	{"neither_sign_source", ConsumerKeySources{EncryptFile: missingPath}, signRefusal},
	{"both_encrypt_sources", ConsumerKeySources{SignFile: missingPath, EncryptFile: missingPath, EncryptValue: "AAAA"}, encryptRefusal},
	{"neither_encrypt_source", ConsumerKeySources{SignFile: missingPath}, encryptRefusal},
}

func TestConsumerKeySourcesValidate(t *testing.T) {
	for _, tt := range consumerRefusalCases {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.keys.Validate()
			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}

	t.Run("one_of_each_pair", func(t *testing.T) {
		k := ConsumerKeySources{SignFile: missingPath, EncryptValue: "AAAA"}
		assert.NoError(t, k.Validate(), "Validate must not touch the filesystem")
	})
}

func TestConsumerKeySourcesLoad(t *testing.T) {
	for _, tt := range consumerRefusalCases {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := tt.keys.Load(testSignKid, testEncKid)
			// Reported by TYPE, never by value (ADR-102).
			if keys != nil {
				assert.Fail(t, "unexpected keys returned", "expected no keys, got a %T", keys)
			}
			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}

	// rsaFixtures hands back a PKCS#8 private DER and a PKIX public DER of two
	// DISTINCT keys; the consumer roles take them the other way round, so the
	// public DER is the verify half here and the private DER the decrypt half.
	privDER, pubDER, wantPriv, wantPub := rsaFixtures(t)

	t.Run("sign_load_error_prefixed", func(t *testing.T) {
		k := ConsumerKeySources{SignFile: missingPath, EncryptValue: base64.StdEncoding.EncodeToString(privDER)}
		_, err := k.Load(testSignKid, testEncKid)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "sign key: "), "got %q", err.Error())
	})

	t.Run("encrypt_load_error_prefixed", func(t *testing.T) {
		k := ConsumerKeySources{
			SignValue:   base64.StdEncoding.EncodeToString(pubDER),
			EncryptFile: missingPath,
		}
		_, err := k.Load(testSignKid, testEncKid)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "encrypt key: "), "got %q", err.Error())
	})

	t.Run("happy_path", func(t *testing.T) {
		k := ConsumerKeySources{
			SignFile:     writeFile(t, "sign.pub.der", pubDER),
			EncryptValue: base64.StdEncoding.EncodeToString(privDER),
		}
		keys, err := k.Load(testSignKid, testEncKid)
		require.NoError(t, err)
		require.NotNil(t, keys)
		assert.Equal(t, testSignKid, keys.SignKid)
		assert.Equal(t, testEncKid, keys.EncryptKid)

		pub, err := keys.PublicKey(testSignKid)
		require.NoError(t, err)
		assert.True(t, wantPub.Equal(pub), "verify key round-tripped to a different key")

		priv, err := keys.PrivateKey(testEncKid)
		require.NoError(t, err)
		assert.True(t, wantPriv.Equal(priv), "decrypt key round-tripped to a different key")

		// The kids are not interchangeable: each door knows only its own.
		_, err = keys.PublicKey(testEncKid)
		require.Error(t, err)
		_, err = keys.PrivateKey(testSignKid)
		assert.Error(t, err)
	})
}
