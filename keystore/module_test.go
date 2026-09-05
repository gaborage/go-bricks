package keystore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// newTestDeps builds ModuleDeps from a literal KeyStoreConfig with a silent
// logger; a nil SecretMinLength reads as the default through SecretFloor.
func newTestDeps(t *testing.T, cfg config.KeyStoreConfig) *app.ModuleDeps {
	t.Helper()
	return &app.ModuleDeps{
		Logger: logger.New("disabled", true),
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

// TestKeystoreModuleInitUnsetFloorRejectsShortSecret pins the literal door's
// default: a config that never set SecretMinLength enforces the 32-byte floor.
func TestKeystoreModuleInitUnsetFloorRejectsShortSecret(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"mac": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}},
		},
	})

	err := NewModule().Init(deps)

	assert.ErrorContains(t, err, `key "mac": secret is 16 bytes, minimum is 32`)
}

// TestKeystoreModuleInitRejectsSubFloorConfig pins ADR-095's backstop on the
// one door that skips config.Validate: a hand-built ModuleDeps handed straight
// to Init. Every framework construction path validates (ADR-064), so this is
// the only way a sub-32 floor reaches the module — and 0 is the widening value,
// admitting a secret the floor exists to reject. Init refuses rather than
// clamps, and loads nothing.
func TestKeystoreModuleInitRejectsSubFloorConfig(t *testing.T) {
	shortSecret := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")) // 16 bytes
	tests := []struct {
		name string
		min  int
		keys map[string]config.KeyPairConfig
	}{
		{
			name: "zero_would_admit_a_short_secret",
			min:  0,
			keys: map[string]config.KeyPairConfig{
				"weak": {Secret: config.KeySourceConfig{Value: shortSecret}},
			},
		},
		{
			name: "sixteen_would_admit_a_short_secret",
			min:  16,
			keys: map[string]config.KeyPairConfig{
				"weak": {Secret: config.KeySourceConfig{Value: shortSecret}},
			},
		},
		{name: "zero_without_keys", min: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := newTestDeps(t, config.KeyStoreConfig{SecretMinLength: new(tt.min), Keys: tt.keys})
			m := NewModule()

			err := m.Init(deps)

			require.Error(t, err)
			require.ErrorContains(t, err, "must be at least 32")
			require.ErrorContains(t, err, "ADR-095")
			assert.Nil(t, m.store, "nothing may load behind a refused floor")
		})
	}
}

// TestKeystoreModuleInitRaisedFloorBinds pins the other direction of the
// wiring: a raised floor reaches the store, so a 32-byte secret fails a
// 64-byte one.
func TestKeystoreModuleInitRaisedFloorBinds(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		SecretMinLength: new(64),
		Keys: map[string]config.KeyPairConfig{
			"mac": {Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString([]byte("a-32-byte-symmetric-mac-key!!!!!"))}},
		},
	})

	err := NewModule().Init(deps)

	assert.ErrorContains(t, err, `key "mac": secret is 32 bytes, minimum is 64`)
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
