package secretfile

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testconsts "github.com/gaborage/go-bricks/testing"
)

// ed25519KeyPEM returns a PKCS#8 Ed25519 key: the compact shape whose DER is
// short enough to slip MaxPathEcho and whose PEM length is not a multiple of 3.
func ed25519KeyPEM(t *testing.T) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestLooksLikeKeyMaterialDetection(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	require.NoError(t, err)
	rawPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	edPEM := ed25519KeyPEM(t)
	unpadded := base64.RawStdEncoding.EncodeToString(edPEM)
	require.NotEqual(t, base64.StdEncoding.EncodeToString(edPEM), unpadded,
		"this case is vacuous unless the two encodings actually differ")

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "raw_pem_body", value: string(rawPEM), want: true},
		{name: "base64_pem_padded", value: base64.StdEncoding.EncodeToString(rawPEM), want: true},
		{name: "base64_pem_unpadded_ed25519", value: unpadded, want: true},
		{name: "base64_pkcs8_der", value: base64.StdEncoding.EncodeToString(privDER), want: true},
		{name: "base64_pkix_public_der", value: base64.StdEncoding.EncodeToString(pubDER), want: true},

		// The nine-path false-positive fence — must not be trimmed.
		// Sibling TestLooksLikeKeyMaterialDetectsKeysNotPaths fences a different
		// class (base64-alphabet paths); extend both or neither.
		{name: "path_etc_keys_private_der", value: "/etc/keys/private.der", want: false},
		{name: "path_keys_pub_der", value: "keys/pub.der", want: false},
		{name: "path_dot_testdata_key_der", value: "./testdata/key.der", want: false},
		{name: "path_windows_style", value: `C:\keys\private.der`, want: false},
		{name: "path_run_secrets_signing_key", value: "/run/secrets/signing-key", want: false},
		{name: "path_bare_privatekey", value: "privatekey", want: false},
		{name: "path_bare_testdata", value: "testdata", want: false},
		{name: "path_bare_keys", value: "keys", want: false},
		{name: "path_serviceaccount_token", value: "/var/run/secrets/kubernetes.io/serviceaccount/token", want: false},

		// Documented residual: raw symmetric secrets have no ASN.1 structure and
		// cannot be sniffed. Pinned so a future change that starts catching this
		// is a visible, deliberate decision.
		{name: "raw_hmac_shaped_secret", value: base64.StdEncoding.EncodeToString([]byte("an-explicitly-32-byte-mac-key!!!")), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LooksLikeKeyMaterial(tt.value))
		})
	}
}

func TestSafeRefEliding(t *testing.T) {
	t.Run("short_value_is_quoted", func(t *testing.T) {
		got := SafeRef("/etc/keys/private.der")
		assert.Equal(t, `"/etc/keys/private.der"`, got)
	})

	t.Run("over_long_value_is_elided", func(t *testing.T) {
		secret := strings.Repeat("s3cr3t", 100)
		got := SafeRef(secret)
		assert.Contains(t, got, "char value elided")
		assert.NotContains(t, got, secret)
	})
}

func TestErrnoStripsPathError(t *testing.T) {
	t.Run("path_error_is_stripped", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.pem")
		_, err := os.ReadFile(missing)
		require.Error(t, err)

		stripped := Errno(err)
		assert.NotContains(t, stripped.Error(), missing)
		assert.ErrorIs(t, stripped, fs.ErrNotExist)
	})

	t.Run("non_path_error_passes_through_unchanged", func(t *testing.T) {
		plain := errors.New("plain error, not a PathError")
		assert.Same(t, plain, Errno(plain))
	})
}

func TestReadErrorDoesNotEchoOverLongValues(t *testing.T) {
	t.Run("over_long_value_is_not_echoed", func(t *testing.T) {
		v := strings.Repeat("s3cr3t", 100)
		require.Greater(t, len(v), MaxPathEcho, "the point of this case is that eliding cannot save us otherwise")

		_, readErr := os.ReadFile(v)
		require.Error(t, readErr)

		err := ReadError(v, readErr)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), v)
		assert.Contains(t, err.Error(), "char value elided")
		assert.Contains(t, err.Error(), "read file")
	})

	t.Run("short_missing_path_is_quoted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.pem")

		_, readErr := os.ReadFile(path)
		require.Error(t, readErr)

		err := ReadError(path, readErr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read file")
		assert.Contains(t, err.Error(), fmt.Sprintf("%q", path))
		assert.ErrorIs(t, err, fs.ErrNotExist)
	})
}

// TestLoadPEM pins the shared file/value resolver httpclient's and the server
// TLS listener's loadPEM wrappers both delegate to.
func TestLoadPEM(t *testing.T) {
	t.Run("both_set_errors", func(t *testing.T) {
		_, err := LoadPEM("test: prefix:", "some/path.pem", "c29tZXZhbHVl", "cert")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "set file or value, not both")
	})

	t.Run("file_path_reads", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "material.pem")
		want := testconsts.PEMFixture("CERTIFICATE")
		require.NoError(t, os.WriteFile(path, want, 0o600))

		got, err := LoadPEM("test: prefix:", path, "", "cert")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("key_material_path_rejected", func(t *testing.T) {
		// A raw PEM header hits LooksLikeKeyMaterial's cheapest branch
		// (strings.Contains) — no crypto needed to prove LoadPEM's file
		// branch calls the guard and propagates its rejection. Exhaustive
		// DER/key-shape detection is already pinned by
		// TestLooksLikeKeyMaterialDetection and
		// TestLooksLikeKeyMaterialDetectsKeysNotPaths.
		asFile := string(testconsts.PEMFixture("PRIVATE KEY"))

		_, err := LoadPEM("test: prefix:", asFile, "", "key")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "looks like key material")
	})

	t.Run("base64_value_decodes", func(t *testing.T) {
		want := []byte("hello world")
		encoded := base64.StdEncoding.EncodeToString(want)

		got, err := LoadPEM("test: prefix:", "", encoded, "cert")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	// bad_base64_never_echoes_value pins that a base64-decode failure never
	// echoes the value, at any length — not just via SafeRef's elision
	// bound. A raw (non-base64) PEM string pasted into the value field fails
	// decode here, and for a short key type (Ed25519) SafeRef's %q-length
	// bound would not trip, so echoing anything derived from value would
	// leak the material itself.
	t.Run("bad_base64_never_echoes_value", func(t *testing.T) {
		tests := []struct {
			name string
			v    string
		}{
			{name: "short_invalid", v: "!!!not-base64!!!"},
			{name: "long_invalid", v: strings.Repeat("!not-base64!", 30)},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := LoadPEM("test: prefix:", "", tt.v, "cert")
				require.Error(t, err)
				assert.NotContains(t, err.Error(), tt.v)
			})
		}
	})

	t.Run("neither_set_nil_nil", func(t *testing.T) {
		got, err := LoadPEM("test: prefix:", "", "", "cert")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// TestParseTLSMinVersion pins the shared min-version enum parser httpclient's
// and the server TLS listener's wrappers both delegate to.
func TestParseTLSMinVersion(t *testing.T) {
	tests := []struct {
		name    string
		v       string
		want    uint16
		wantErr bool
	}{
		{name: "empty_floors_to_12", v: "", want: tls.VersionTLS12},
		{name: "explicit_13", v: "1.3", want: tls.VersionTLS13},
		{name: "rejected_11", v: "1.1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTLSMinVersion("test: prefix:", tt.v)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "minversion")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Pins both directions of the detector: real key material in every encoding it
// must catch, and ordinary paths — including the base64-alphabet ones that used
// to trip the bare 0x30-tag check — that it must let through.
func TestLooksLikeKeyMaterialDetectsKeysNotPaths(t *testing.T) {
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edPKCS8, err := x509.MarshalPKCS8PrivateKey(edKey)
	require.NoError(t, err)
	edPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edPKCS8})

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecSEC1, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	ecPKCS8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	require.NoError(t, err)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaPKCS1 := x509.MarshalPKCS1PrivateKey(rsaKey)
	rsaPKCS8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "ed25519_pkcs8", value: base64.StdEncoding.EncodeToString(edPKCS8), want: true},
		{name: "ecdsa_p256_sec1", value: base64.StdEncoding.EncodeToString(ecSEC1), want: true},
		{name: "ecdsa_p256_pkcs8", value: base64.StdEncoding.EncodeToString(ecPKCS8), want: true},
		{name: "rsa2048_pkcs1", value: base64.StdEncoding.EncodeToString(rsaPKCS1), want: true},
		{name: "rsa2048_pkcs8", value: base64.StdEncoding.EncodeToString(rsaPKCS8), want: true},
		{name: "raw_pem_body", value: string(edPEM), want: true},
		{name: "path_main", value: "MAIN", want: false},
		{name: "path_mdkeys", value: "MDkeys", want: false},
		{name: "path_mikeys", value: "MIkeys", want: false},
		{name: "path_misckey", value: "MIsckey", want: false},
		{name: "path_mfkeys", value: "MFkeys", want: false},
		{name: "path_mockkeys", value: "MOCKkeys", want: false},
		{name: "path_certs", value: "certs", want: false},
		{name: "path_mycert", value: "MyCert", want: false},
		{name: "path_tls_server_pem", value: "tls/server.pem", want: false},
		{name: "path_absolute_cert_pem", value: "/etc/ssl/cert.pem", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LooksLikeKeyMaterial(tt.value))
		})
	}
}
