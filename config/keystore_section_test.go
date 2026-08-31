package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateKeyStoreEmpty(t *testing.T) {
	cfg := &KeyStoreConfig{}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreValid(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"signing": {
				Public:  KeySourceConfig{File: "pub.der"},
				Private: KeySourceConfig{Value: "base64data"},
			},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStorePublicKeyRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"missing": {
				Public: KeySourceConfig{},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "key source required")
}

func TestValidateKeyStoreBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"both": {
				Public: KeySourceConfig{File: "a.der", Value: "also"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateKeyStorePrivateOptional(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"pub-only": {
				Public: KeySourceConfig{File: "pub.der"},
			},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreWiredIntoValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.KeyStore = KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"bad": {
				Public: KeySourceConfig{File: "a.der", Value: "also"},
			},
		},
	}
	err := Validate(cfg)
	assert.ErrorContains(t, err, "keystore config")
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateKeyStoreSecretValid(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mac-file":  {Secret: KeySourceConfig{File: "mac.bin"}},
			"mac-value": {Secret: KeySourceConfig{Value: "base64data"}},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreSecretRequiresSource(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"empty-secret": {Secret: KeySourceConfig{}},
		},
	}
	// An entry with no material at all falls back to the public-key path.
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "key source required")
}

func TestValidateKeyStoreSecretBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mac": {Secret: KeySourceConfig{File: "mac.bin", Value: "also"}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
	assert.ErrorContains(t, err, "keystore.keys.mac.secret")
}

func TestValidateKeyStoreMixedEntrySecretPlusPublic(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mixed": {
				Public: KeySourceConfig{File: "pub.der"},
				Secret: KeySourceConfig{File: "mac.bin"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both a symmetric 'secret' and asymmetric")
	assert.ErrorContains(t, err, "keystore.keys.mixed")
}

func TestValidateKeyStoreMixedEntrySecretPlusPrivate(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mixed": {
				Private: KeySourceConfig{Value: "privb64"},
				Secret:  KeySourceConfig{Value: "macb64"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both a symmetric 'secret' and asymmetric")
}

func TestValidateKeyStoreSecretMinLengthNil(t *testing.T) {
	cfg := &KeyStoreConfig{}
	assert.NoError(t, checkKeyStore(cfg), "nil is left for normalize to fill; check must not reject it")
}

func TestValidateKeyStoreSecretMinLengthNegative(t *testing.T) {
	cfg := &KeyStoreConfig{SecretMinLength: new(-1)}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "keystore.secretminlength")
	assert.ErrorContains(t, err, "must be non-negative")
}

func TestValidateKeyStoreSecretMinLengthZeroAllowed(t *testing.T) {
	cfg := &KeyStoreConfig{
		SecretMinLength: new(0),
		Keys: map[string]KeyPairConfig{
			"mac": {Secret: KeySourceConfig{File: "mac.bin"}},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

// TestCheckKeyStoreRejectsUnreachableKeyNames: a keystore entry's name reaches
// the same env transform, and is rejected before its sources are read.
func TestCheckKeyStoreRejectsUnreachableKeyNames(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my_key": {},
	}}

	err := checkKeyStore(cfg)

	assertSectionNameRejected(t, err, "keystore.keys.my_key")
}

// TestCheckKeyStoreRejectsADottedKeyName: a '.' is koanf's path delimiter, so a
// dotted name makes the constructed keystore.keys.<name> Field ambiguous — is
// "keystore.keys.my.key" the entry "my.key" or a "key" under "my"? The parent
// field is reported instead, exactly as the databases and static-tenant rules
// already do, and this must run BEFORE the reachability grammar so the
// ambiguous path is never built.
func TestCheckKeyStoreRejectsADottedKeyName(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my.key": {},
	}}

	err := checkKeyStore(cfg)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "keystore.keys", cfgErr.Field, "the parent field, since a dotted name cannot carry an unambiguous path")
	assert.ErrorContains(t, err, "'.'")
}

// TestCheckKeyStoreAcceptsReachableKeyNames is the boundary's other side: a
// conforming name reaches validateKeyEntry, which then judges its sources.
func TestCheckKeyStoreAcceptsReachableKeyNames(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my-key": {Secret: KeySourceConfig{Value: "c2VjcmV0LWJ5dGVzLXRoYXQtYXJlLWxvbmctZW5vdWdo"}},
	}}

	require.NoError(t, checkKeyStore(cfg))
}
