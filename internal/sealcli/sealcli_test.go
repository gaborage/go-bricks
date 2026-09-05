package sealcli

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	signUse      = "used to sign the outbound JWS"
	encryptUse   = "used to encrypt the subject member"
	testSignKid  = "svc-sign-v1"
	testEncKid   = "aud-encrypt-v1"
	missingPath  = "/nonexistent/never-read.der"
	signKeyFile  = "sign-key-file"
	signKeyValue = "sign-key-value"
	encKeyFile   = "encrypt-key-file"
	encKeyValue  = "encrypt-key-value"
	payloadJSON  = `{"order_id":"o-1"}`
)

// newFlagSet builds a silent FlagSet so a parse error never writes to the test log.
func newFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// writeFile writes data under t.TempDir() and returns the path.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// rsaFixtures returns a PKCS#8 private DER and a PKIX public DER from two
// distinct keys, so a resolver handing back the wrong one is detectable.
func rsaFixtures(t *testing.T) (privDER, pubDER []byte, priv *rsa.PrivateKey, pub *rsa.PublicKey) {
	t.Helper()
	signPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privDER, err = x509.MarshalPKCS8PrivateKey(signPriv)
	require.NoError(t, err)
	pubDER, err = x509.MarshalPKIXPublicKey(&encPriv.PublicKey)
	require.NoError(t, err)
	return privDER, pubDER, signPriv, &encPriv.PublicKey
}

func TestPositionalPath(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		path, err := PositionalPath(newFlagSet(t), nil)
		require.NoError(t, err)
		assert.Empty(t, path)
	})

	t.Run("one_argument", func(t *testing.T) {
		fs := newFlagSet(t)
		keys := KeyFlags(fs, signUse, encryptUse)
		path, err := PositionalPath(fs, []string{"-sign-key-file", "s.der", "payload.json"})
		require.NoError(t, err)
		assert.Equal(t, "payload.json", path)
		assert.Equal(t, "s.der", keys.SignFile, "flags before the positional must still bind")
	})

	t.Run("dash_argument", func(t *testing.T) {
		path, err := PositionalPath(newFlagSet(t), []string{"-"})
		require.NoError(t, err)
		assert.Equal(t, "-", path)
	})

	t.Run("two_arguments", func(t *testing.T) {
		_, err := PositionalPath(newFlagSet(t), []string{"a", "b"})
		require.Error(t, err)
		assert.Equal(t, "expected at most one payload-file argument", err.Error())
		assert.False(t, errors.Is(err, ErrUsage), "the FlagSet never printed this one, so the caller must")
	})

	t.Run("unknown_flag_wrapped_in_err_usage", func(t *testing.T) {
		_, err := PositionalPath(newFlagSet(t), []string{"-nope"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUsage), "got %q", err.Error())
	})

	t.Run("help_wrapped_in_err_usage", func(t *testing.T) {
		_, err := PositionalPath(newFlagSet(t), []string{"-h"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, flag.ErrHelp), "the caller exits 0 on this one")
		assert.True(t, errors.Is(err, ErrUsage), "and must not reprint what the FlagSet printed")
	})
}

func TestKeyFlags(t *testing.T) {
	fs := newFlagSet(t)
	keys := KeyFlags(fs, signUse, encryptUse)
	require.NotNil(t, keys)

	wantUsage := map[string]string{
		signKeyFile:  "path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) " + signUse,
		signKeyValue: "base64-encoded DER RSA private key (alternative to -sign-key-file; argv is process-visible — fixture keys only)",
		encKeyFile:   "path to a DER-encoded RSA public key (PKIX) " + encryptUse,
		encKeyValue:  "base64-encoded DER RSA public key (alternative to -encrypt-key-file)",
	}
	for name, usage := range wantUsage {
		f := fs.Lookup(name)
		require.NotNil(t, f, "flag -%s not registered", name)
		assert.Equal(t, usage, f.Usage, "usage text of -%s", name)
		assert.Empty(t, f.DefValue, "default of -%s", name)
	}

	require.NoError(t, fs.Parse([]string{"-sign-key-file", "x", "-encrypt-key-value", "y"}))
	assert.Equal(t, "x", keys.SignFile)
	assert.Empty(t, keys.SignValue)
	assert.Empty(t, keys.EncryptFile)
	assert.Equal(t, "y", keys.EncryptValue)
}

const (
	signRefusal    = "exactly one of -sign-key-file or -sign-key-value is required"
	encryptRefusal = "exactly one of -encrypt-key-file or -encrypt-key-value is required"
)

// refusalCases are the four wrong-source shapes. Every path named is one that
// does not exist, so a refusal string proves the check ran before any I/O —
// reaching a loader would surface a read error instead.
var refusalCases = []struct {
	name string
	keys KeySources
	want string
}{
	{"both_sign_sources", KeySources{SignFile: missingPath, SignValue: "AAAA", EncryptFile: missingPath}, signRefusal},
	{"neither_sign_source", KeySources{EncryptFile: missingPath}, signRefusal},
	{"both_encrypt_sources", KeySources{SignFile: missingPath, EncryptFile: missingPath, EncryptValue: "AAAA"}, encryptRefusal},
	{"neither_encrypt_source", KeySources{SignFile: missingPath}, encryptRefusal},
}

func TestKeySourcesValidate(t *testing.T) {
	for _, tt := range refusalCases {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.keys.Validate()
			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}

	t.Run("one_of_each_pair", func(t *testing.T) {
		k := KeySources{SignFile: missingPath, EncryptValue: "AAAA"}
		assert.NoError(t, k.Validate(), "Validate must not touch the filesystem")
	})
}

func TestKeySourcesLoad(t *testing.T) {
	// Load re-runs Validate, so the same four shapes must refuse here too —
	// with the refusal string, never a read error from the nonexistent paths.
	for _, tt := range refusalCases {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := tt.keys.Load(testSignKid, testEncKid)
			require.Error(t, err)
			assert.Nil(t, keys)
			assert.Equal(t, tt.want, err.Error())
		})
	}

	privDER, pubDER, wantPriv, wantPub := rsaFixtures(t)

	t.Run("sign_load_error_prefixed", func(t *testing.T) {
		k := KeySources{SignFile: missingPath, EncryptValue: base64.StdEncoding.EncodeToString(pubDER)}
		_, err := k.Load(testSignKid, testEncKid)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "sign key: "), "got %q", err.Error())
	})

	t.Run("encrypt_load_error_prefixed", func(t *testing.T) {
		k := KeySources{
			SignValue:   base64.StdEncoding.EncodeToString(privDER),
			EncryptFile: missingPath,
		}
		_, err := k.Load(testSignKid, testEncKid)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "encrypt key: "), "got %q", err.Error())
	})

	t.Run("happy_path", func(t *testing.T) {
		k := KeySources{
			SignFile:     writeFile(t, "sign.der", privDER),
			EncryptValue: base64.StdEncoding.EncodeToString(pubDER),
		}
		keys, err := k.Load(testSignKid, testEncKid)
		require.NoError(t, err)
		require.NotNil(t, keys)
		assert.Equal(t, testSignKid, keys.SignKid)
		assert.Equal(t, testEncKid, keys.EncryptKid)

		priv, err := keys.PrivateKey(testSignKid)
		require.NoError(t, err)
		assert.True(t, wantPriv.Equal(priv), "sign key round-tripped to a different key")

		pub, err := keys.PublicKey(testEncKid)
		require.NoError(t, err)
		assert.True(t, wantPub.Equal(pub), "encrypt key round-tripped to a different key")

		// The kids are not interchangeable: each door knows only its own.
		_, err = keys.PrivateKey(testEncKid)
		assert.Error(t, err)
		_, err = keys.PublicKey(testSignKid)
		assert.Error(t, err)
	})
}

func TestReadPayload(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		path := writeFile(t, "payload.json", []byte(payloadJSON))
		// stdin holds different bytes, so reading it instead would be visible.
		data, err := ReadPayload(path, bytes.NewBufferString("STDIN"))
		require.NoError(t, err)
		assert.Equal(t, payloadJSON, string(data))
	})

	stdinCases := []struct{ name, path string }{
		{"dash_reads_stdin", "-"},
		{"empty_reads_stdin", ""},
	}
	for _, tt := range stdinCases {
		t.Run(tt.name, func(t *testing.T) {
			data, err := ReadPayload(tt.path, bytes.NewBufferString(payloadJSON))
			require.NoError(t, err)
			assert.Equal(t, payloadJSON, string(data))
		})
	}

	t.Run("unreadable_file", func(t *testing.T) {
		_, err := ReadPayload(missingPath, bytes.NewBufferString(payloadJSON))
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "read payload file: "), "got %q", err.Error())
	})

	t.Run("stdin_error_prefixed", func(t *testing.T) {
		_, err := ReadPayload("-", iotest{})
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "read stdin: "), "got %q", err.Error())
	})
}

// iotest is a Reader that always fails, exercising the read stdin: prefix.
type iotest struct{}

func (iotest) Read([]byte) (int, error) { return 0, assert.AnError }
