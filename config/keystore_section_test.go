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

func TestValidateKeyStorePKCS12Valid(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts-file": {PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: "VTS_P12_PASSWORD"}}},
			"vts-b64":  {PKCS12: PKCS12SourceConfig{Value: "base64data", Password: PasswordSourceConfig{File: "/run/secrets/vts-p12"}}},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStorePKCS12MixedEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry KeyPairConfig
	}{
		{"with_secret", KeyPairConfig{
			Secret: KeySourceConfig{File: "mac.bin"},
			PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: "P"}},
		}},
		{"with_public", KeyPairConfig{
			Public: KeySourceConfig{File: "pub.der"},
			PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: "P"}},
		}},
		{"with_private", KeyPairConfig{
			Private: KeySourceConfig{Value: "privb64"},
			PKCS12:  PKCS12SourceConfig{Value: "p12b64", Password: PasswordSourceConfig{Env: "P"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{"mixed": tt.entry}}
			err := checkKeyStore(cfg)
			assert.ErrorContains(t, err, "'pkcs12' bundle alongside")
			assert.ErrorContains(t, err, "keystore.keys.mixed")
		})
	}
}

func TestValidateKeyStorePKCS12BundleBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12", Value: "also", Password: PasswordSourceConfig{Env: "P"}}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12")
}

func TestValidateKeyStorePKCS12BundleRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{Password: PasswordSourceConfig{Env: "P"}}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "key source required")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12")
}

func TestValidateKeyStorePKCS12PasswordRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12"}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "password source required")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12.password")
}

func TestValidateKeyStorePKCS12PasswordBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: "P", File: "/run/secrets/p"}}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'env' and 'file' set")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12.password")
}

func TestValidateKeyStorePKCS12PasswordEnvMustBeAName(t *testing.T) {
	literal := "hunter 2!"
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: literal}}},
		},
	}
	err := checkKeyStore(cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not an environment variable name")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12.password.env")
	assert.NotContains(t, err.Error(), literal)
}
