package keystore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// captureStdout redirects os.Stdout for the duration of fn and returns everything
// written. logger.New writes to os.Stdout (no buffer-injectable constructor), so a
// logger must be constructed inside fn — after the swap — for its writes to land
// in the pipe. These tests are not parallel, so swapping the process-global is safe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	rp, wp, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = wp
	defer func() { os.Stdout = old }()

	fn()

	require.NoError(t, wp.Close())
	data, readErr := io.ReadAll(rp)
	require.NoError(t, readErr)
	return string(data)
}

// newTestDeps builds ModuleDeps from a literal KeyStoreConfig with a silent
// logger; a nil SecretMinLength reads as the default through SecretFloor.
func newTestDeps(t *testing.T, cfg config.KeyStoreConfig) *app.ModuleDeps {
	t.Helper()
	return newTestDepsWithLogger(t, cfg, logger.New("disabled", true))
}

func newTestDepsWithLogger(t *testing.T, cfg config.KeyStoreConfig, log logger.Logger) *app.ModuleDeps {
	t.Helper()
	return &app.ModuleDeps{
		Logger: log,
		Config: &config.Config{KeyStore: cfg},
	}
}

func TestKeystoreModuleName(t *testing.T) {
	m := NewModule()
	assert.Equal(t, "keystore", m.Name())
}

func TestKeystoreModuleInitNoCerts(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{})
	m := NewModule()

	err := m.Init(deps)
	require.NoError(t, err)
	assert.Nil(t, m.store, "store should be nil when no keys configured")
}

func TestKeystoreModuleInitWithValidKeys(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	require.NoError(t, err)

	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"signing": {
				Public:  config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(pubDER)},
				Private: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(privDER)},
			},
		},
	})

	m := NewModule()
	err = m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.store)

	// Verify key retrieval works
	gotPub, err := m.store.PublicKey("signing")
	require.NoError(t, err)
	assert.True(t, privKey.PublicKey.Equal(gotPub))
}

func TestKeystoreModuleInitWithSecret(t *testing.T) {
	secret := []byte("a-32-byte-symmetric-mac-key!!!!!")
	deps := newTestDeps(t, config.KeyStoreConfig{
		SecretMinLength: new(32),
		Keys: map[string]config.KeyPairConfig{
			"mac": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(secret)}},
		},
	})

	m := NewModule()
	require.NoError(t, m.Init(deps))

	got, err := m.store.Secret("mac")
	require.NoError(t, err)
	assert.Equal(t, secret, got)
}

func TestKeystoreModuleInitSecretBelowMinLengthFails(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		SecretMinLength: new(32),
		Keys: map[string]config.KeyPairConfig{
			"mac": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString([]byte("short"))}},
		},
	})

	err := NewModule().Init(deps)
	require.Error(t, err)
	assert.ErrorContains(t, err, "minimum is 32")
}

func TestKeystoreModuleInitFileNotFound(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"bad": {
				Public: config.KeySourceConfig{File: "/nonexistent/pub.der"},
			},
		},
	})

	m := NewModule()
	err := m.Init(deps)
	assert.ErrorContains(t, err, "read file")
}

func TestKeystoreModuleInitMissingPublicKey(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"no-pub": {
				// Public key not configured
			},
		},
	})

	m := NewModule()
	err := m.Init(deps)
	assert.ErrorContains(t, err, "public key is required")
}

func TestKeystoreModuleProviderInterface(t *testing.T) {
	m := NewModule()

	// Verify it implements KeyStoreProvider
	var provider app.KeyStoreProvider = m
	assert.NotNil(t, provider)

	// Before init, store is nil
	assert.Nil(t, provider.KeyStore())
}

func TestKeystoreModuleShutdown(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{})
	m := NewModule()
	require.NoError(t, m.Init(deps))

	err := m.Shutdown()
	assert.NoError(t, err)
}

// TestKeystoreModuleInitFloorDisabledWarns pins ADR-065's two deprecation
// WARNs: keystore.secretminlength: 0 fires the floor-off WARN once, and each
// admitted secret shorter than the recommended 32 bytes fires a per-key WARN
// naming the key and its length — never the secret material itself.
func TestKeystoreModuleInitFloorDisabledWarns(t *testing.T) {
	secret := []byte("0123456789abcdef") // 16 bytes, admitted only because the floor is off
	cfg := config.KeyStoreConfig{
		SecretMinLength: new(0),
		Keys: map[string]config.KeyPairConfig{
			"weak": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(secret)}},
		},
	}

	out := captureStdout(t, func() {
		require.NoError(t, NewModule().Init(newTestDepsWithLogger(t, cfg, logger.New("warn", false))))
	})

	assert.Contains(t, out, "secret length floor disabled", "must WARN that the floor is off")
	assert.Contains(t, out, `"name":"weak"`, "must name the admitted short key")
	assert.Contains(t, out, `"bytes":16`, "must report the secret's byte length")
	assert.Contains(t, out, `"recommended":32`, "must report the recommended floor")
	assert.NotContains(t, out, string(secret), "must never log the secret material")
	assert.NotContains(t, out, base64.StdEncoding.EncodeToString(secret), "must never log the encoded secret material")
}

// TestKeystoreModuleInitNoWarnsAtRecommendedFloor is the negative case: a
// floor at the recommendation emits neither deprecation WARN.
func TestKeystoreModuleInitNoWarnsAtRecommendedFloor(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef") // exactly 32 bytes — the boundary the WARN must not fire at
	cfg := config.KeyStoreConfig{
		SecretMinLength: new(config.DefaultKeyStoreSecretMinLength),
		Keys: map[string]config.KeyPairConfig{
			"strong": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(secret)}},
		},
	}

	out := captureStdout(t, func() {
		require.NoError(t, NewModule().Init(newTestDepsWithLogger(t, cfg, logger.New("warn", false))))
	})

	assert.NotContains(t, out, "floor disabled")
	assert.NotContains(t, out, "below the recommended")
}
