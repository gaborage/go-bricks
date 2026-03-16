package keystore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestDeps(t *testing.T, cfg config.KeyStoreConfig) *app.ModuleDeps {
	t.Helper()
	return &app.ModuleDeps{
		Logger: logger.New("disabled", true),
		Config: &config.Config{
			KeyStore: cfg,
		},
	}
}

func TestKeystoreModuleName(t *testing.T) {
	m := NewKeystoreModule()
	assert.Equal(t, "keystore", m.Name())
}

func TestKeystoreModuleInitNoCerts(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{})
	m := NewKeystoreModule()

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

	m := NewKeystoreModule()
	err = m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.store)

	// Verify key retrieval works
	gotPub, err := m.store.PublicKey("signing")
	require.NoError(t, err)
	assert.True(t, privKey.PublicKey.Equal(gotPub))
}

func TestKeystoreModuleInitInvalidConfig(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"bad": {
				Public: config.KeySourceConfig{File: "a.der", Value: "also-set"},
			},
		},
	})

	m := NewKeystoreModule()
	err := m.Init(deps)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestKeystoreModuleInitMissingPublicKey(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"no-pub": {
				// Public key not configured
			},
		},
	})

	m := NewKeystoreModule()
	err := m.Init(deps)
	assert.ErrorContains(t, err, "requires either 'file' or 'value'")
}

func TestKeystoreModuleProviderInterface(t *testing.T) {
	m := NewKeystoreModule()

	// Verify it implements KeyStoreProvider
	var provider app.KeyStoreProvider = m
	assert.NotNil(t, provider)

	// Before init, store is nil
	assert.Nil(t, provider.KeyStore())
}

func TestKeystoreModuleShutdown(t *testing.T) {
	deps := newTestDeps(t, config.KeyStoreConfig{})
	m := NewKeystoreModule()
	require.NoError(t, m.Init(deps))

	err := m.Shutdown()
	assert.NoError(t, err)
}
