package config

import (
	"strings"
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
	require.Error(t, err)
	assert.ErrorContains(t, err, "keystore config") //nolint:testifylint // wrapper prefix; the independent inner-cause clause follows
	require.ErrorContains(t, err, "both 'file' and 'value' set")
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
	require.ErrorContains(t, err, "both 'file' and 'value' set")
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
	require.ErrorContains(t, err, "both a symmetric 'secret' and asymmetric")
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

// TestValidateKeyStoreSecretMinLengthBelowFloorRejected pins ADR-095: the
// 32-byte floor is mandatory, so a set value below it — the former 0 opt-out
// included — fails naming the key, the floor and the ADR, keys or no keys.
func TestValidateKeyStoreSecretMinLengthBelowFloorRejected(t *testing.T) {
	secretKeys := map[string]KeyPairConfig{
		"mac": {Secret: KeySourceConfig{File: "mac.bin"}},
	}
	tests := []struct {
		name string
		min  int
		keys map[string]KeyPairConfig
	}{
		{name: "zero_former_opt_out", min: 0, keys: secretKeys},
		{name: "zero_without_keys", min: 0},
		{name: "sixteen", min: 16, keys: secretKeys},
		{name: "one_below_floor", min: 31, keys: secretKeys},
		{name: "negative", min: -1, keys: secretKeys},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &KeyStoreConfig{SecretMinLength: new(tt.min), Keys: tt.keys}

			err := checkKeyStore(cfg)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, "keystore.secretminlength", cfgErr.Field)
			require.ErrorContains(t, err, "must be at least 32")
			assert.ErrorContains(t, err, "ADR-095")
		})
	}
}

// TestValidateKeyStoreSecretMinLengthAtOrAboveFloorAllowed is the other side:
// a set value can only raise the floor, so 32 itself and anything above pass.
func TestValidateKeyStoreSecretMinLengthAtOrAboveFloorAllowed(t *testing.T) {
	tests := []struct {
		name string
		min  int
	}{
		{name: "at_floor", min: 32},
		{name: "raised", min: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &KeyStoreConfig{
				SecretMinLength: new(tt.min),
				Keys: map[string]KeyPairConfig{
					"mac": {Secret: KeySourceConfig{File: "mac.bin"}},
				},
			}
			assert.NoError(t, checkKeyStore(cfg))
		})
	}
}

// TestValidateKeyStoreSecretMinLengthBelowFloorFailsValidate reaches the
// rejection through Validate, the gate every construction path runs (ADR-064).
func TestValidateKeyStoreSecretMinLengthBelowFloorFailsValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.KeyStore.SecretMinLength = new(0)

	err := Validate(cfg)

	require.Error(t, err)
	assert.ErrorContains(t, err, "keystore config") //nolint:testifylint // wrapper prefix; the independent inner-cause clause follows
	require.ErrorContains(t, err, "keystore.secretminlength must be at least 32")
}

// TestLoadKeyStoreSecretMinLengthBelowFloorFailsStartup is the koanf door: an
// environment still carrying the old opt-out, or any value below 32, fails
// config.Load — the startup path — instead of booting with a weaker floor.
func TestLoadKeyStoreSecretMinLengthBelowFloorFailsStartup(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Setenv("KEYSTORE_SECRETMINLENGTH", "16")

	_, err := Load()

	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid configuration") //nolint:testifylint // wrapper prefix; the independent inner-cause clause follows
	require.ErrorContains(t, err, "keystore.secretminlength must be at least 32")
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

// TestCheckKeyStoreAcceptsADottedKeyName: a dotted name is a path of
// env-reachable segments (ADR-144), so it passes the name rules and reaches
// validateKeyEntry, whose Field is the real koanf path of the entry.
func TestCheckKeyStoreAcceptsADottedKeyName(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my.key": {},
	}}

	err := checkKeyStore(cfg)

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "keystore.keys.my.key.public", cfgErr.Field, "the name rules pass; the missing source is what is reported")
	assert.Equal(t, "key source required", cfgErr.Message)

	cfg.Keys["my.key"] = KeyPairConfig{Public: KeySourceConfig{Value: "cHVi"}}
	require.NoError(t, checkKeyStore(cfg))
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
			require.ErrorContains(t, err, "'pkcs12' bundle alongside")
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
	require.ErrorContains(t, err, "both 'file' and 'value' set")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12")
}

func TestValidateKeyStorePKCS12BundleRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{Password: PasswordSourceConfig{Env: "P"}}},
		},
	}
	err := checkKeyStore(cfg)
	require.ErrorContains(t, err, "key source required")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12")
}

func TestValidateKeyStorePKCS12PasswordRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12"}},
		},
	}
	err := checkKeyStore(cfg)
	require.ErrorContains(t, err, "password source required")
	assert.ErrorContains(t, err, "keystore.keys.vts.pkcs12.password")
}

func TestValidateKeyStorePKCS12PasswordBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"vts": {PKCS12: PKCS12SourceConfig{File: "vts.p12", Password: PasswordSourceConfig{Env: "P", File: "/run/secrets/p"}}},
		},
	}
	err := checkKeyStore(cfg)
	require.ErrorContains(t, err, "both 'env' and 'file' set")
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
	// The leak check runs FIRST: a require on either message clause aborts whenever the
	// wording drifts, and the property that the password literal never reaches the error
	// string is the one this test exists to pin (ADR-095).
	assert.NotContains(t, err.Error(), literal)
	require.ErrorContains(t, err, "not an environment variable name")
	require.ErrorContains(t, err, "keystore.keys.vts.pkcs12.password.env")
}

// keysNamed builds a hand-built keystore (the ADR-064 door, which never saw the
// tree reader) whose entries are all complete public entries.
func keysNamed(names ...string) *KeyStoreConfig {
	keys := make(map[string]KeyPairConfig, len(names))
	for _, name := range names {
		keys[name] = KeyPairConfig{Public: KeySourceConfig{Value: "cHVi"}}
	}
	return &KeyStoreConfig{Keys: keys}
}

// TestCheckKeyStoreNameRules pins the ADR-144 name-set rules on a hand-built
// Config: they live in check, not in the decode walk, so a Config built in
// code meets them exactly as a loaded one does.
func TestCheckKeyStoreNameRules(t *testing.T) {
	tests := []struct {
		name       string
		keys       *KeyStoreConfig
		wantField  string
		wantMsg    string
		wantInAct  string
		notInInAct string
	}{
		{name: "dotted_entry", keys: keysNamed("tokens.our")},
		{name: "dotted_and_hyphen_entries", keys: keysNamed("tokens.our", "tokens.peer", "webhook-signing", "a.b-c.d")},
		{name: "dotted_generations", keys: keysNamed("payments.sign.v1", "payments.sign.v2", "payments.encrypt.v1")},
		{name: "hyphen_generations", keys: keysNamed("payments-sign-v1", "payments-sign-v2")},
		{name: "legacy_entry_named_secret", keys: keysNamed("secret")},
		{name: "near_lookalike_is_distinct", keys: keysNamed("tokens-our", "tokens.ours")},
		{name: "fold_prefix_is_no_conflict", keys: keysNamed("tokens-our", "tokens.our.x")},
		{
			name: "entry_prefix", keys: keysNamed("tokens", "tokens.our"),
			wantField: "keystore.keys.tokens", wantMsg: `entry "tokens" is a dotted prefix of entry "tokens.our"`,
			wantInAct: "tokens → tokens.default",
		},
		{
			name: "deep_entry_prefix", keys: keysNamed("a.b", "a.b.c.d"),
			wantField: "keystore.keys.a.b", wantMsg: `entry "a.b" is a dotted prefix of entry "a.b.c.d"`,
		},
		{
			name: "lookalike_entries", keys: keysNamed("tokens-our", "tokens.our"),
			wantField: "keystore.keys.tokens.our", wantMsg: `"tokens-our" and "tokens.our" differ only in '-' versus '.'`,
			wantInAct: `override "tokens-our" with KEYSTORE_KEYS_TOKENS-OUR_* (Docker, Kubernetes), or rename it "tokens.our" everywhere`,
		},
		{
			name: "lookalike_mixed_entries", keys: keysNamed("a-b.c", "a.b-c"),
			wantField: "keystore.keys.a.b-c", wantMsg: `"a-b.c" and "a.b-c" differ only in '-' versus '.'`,
		},
		{
			name: "lookalike_families", keys: keysNamed("payments-sign-v1", "payments.sign.v2"),
			wantField: "keystore.keys", wantMsg: `families "payments-sign" (payments-sign-v1) and "payments.sign" (payments.sign.v2) differ only in '-' versus '.': a family rename is not a rotation`,
		},
		{
			name: "lookalike_families_same_version", keys: keysNamed("payments-sign-v1", "payments.sign.v1"),
			wantField: "keystore.keys", wantMsg: `families "payments-sign" (payments-sign-v1) and "payments.sign" (payments.sign.v1) differ only in '-' versus '.'`,
			wantInAct: `keep one family: set payments-sign-v1 with KEYSTORE_KEYS_PAYMENTS-SIGN-V1_* (Docker, Kubernetes) rather than a POSIX export; ` +
				`moving to "payments.sign" is a family rename, drained before the cutover`,
			notInInAct: "rename it",
		},
		{
			name: "lookalike_family_names_the_intended_generation", keys: keysNamed("payments-sign-v1", "payments.sign.v2"),
			wantField: "keystore.keys", wantMsg: "a family rename is not a rotation",
			wantInAct: "set payments-sign-v2 with KEYSTORE_KEYS_PAYMENTS-SIGN-V2_*",
		},
		{
			name: "lookalike_generation_with_the_wrong_marker", keys: keysNamed("payments-sign-v1", "payments.sign-v1"),
			wantField: "keystore.keys.payments.sign-v1", wantMsg: "a dotted family names its generations with a final v<N> segment",
			wantInAct: "rename it payments.sign.v1",
		},
		{
			name: "nested_families", keys: keysNamed("payments-v1", "payments.sign.v1"),
			wantField: "keystore.keys", wantMsg: `families "payments" and "payments.sign" nest: messaging.seal.active cannot hold a selector for both`,
		},
		{
			name: "families_nest_when_hyphen_reads_as_dot", keys: keysNamed("payments-sign-v1", "payments.sign.eu.v1"),
			wantField: "keystore.keys", wantMsg: `families "payments-sign" and "payments.sign.eu" nest when '-' is read as '.': messaging.seal.active cannot hold a selector for both`,
			wantInAct: "rename one family",
		},
		{
			name: "dotted_family_nests_below_a_longer_hyphen_family", keys: keysNamed("payments-sign-eu-v1", "payments.sign.v1"),
			wantField: "keystore.keys", wantMsg: `families "payments.sign" and "payments-sign-eu" nest when '-' is read as '.'`,
		},
		{name: "hyphen_only_families_that_fold_nest", keys: keysNamed("payments-sign-v1", "payments-sign-eu-v1")},
		{
			name: "reserved_word_after_dot", keys: keysNamed("webhook.secret"),
			wantField: "keystore.keys", wantMsg: `name "webhook.secret" uses the field name "secret" after a '.'`,
			wantInAct: "webhook-secret",
		},
		{
			name: "empty_segment", keys: keysNamed("tokens..our"),
			wantField: "keystore.keys", wantMsg: `name "tokens..our" has an empty segment`,
		},
		{
			name: "leading_dot", keys: keysNamed(".tokens"),
			wantField: "keystore.keys", wantMsg: "has an empty segment",
		},
		{
			name: "underscore_names_its_dotted_spelling", keys: keysNamed("tokens_our"),
			wantField: "keystore.keys.tokens_our", wantMsg: "not reachable by an environment variable",
			wantInAct: `write "tokens.our", which KEYSTORE_KEYS_TOKENS_OUR_* reaches`,
		},
		{
			name: "underscore_whose_spelling_is_reserved", keys: keysNamed("webhook_secret"),
			wantField: "keystore.keys.webhook_secret", wantMsg: "not reachable by an environment variable",
			notInInAct: `write "`,
		},
		{
			name: "underscore_whose_spelling_is_a_malformed_generation", keys: keysNamed("audit_v1"),
			wantField: "keystore.keys.audit_v1", wantMsg: "not reachable by an environment variable",
			notInInAct: `write "`,
		},
		{
			name: "underscore_names_its_dotted_generation", keys: keysNamed("payments_sign_v1"),
			wantField: "keystore.keys.payments_sign_v1", wantMsg: "not reachable by an environment variable",
			wantInAct: `write "payments.sign.v1", which KEYSTORE_KEYS_PAYMENTS_SIGN_V1_* reaches`,
		},
		{
			name: "uppercase_segment", keys: keysNamed("Tokens.our"),
			wantField: "keystore.keys.Tokens.our", wantMsg: "not reachable by an environment variable",
			wantInAct: `write "tokens.our"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkKeyStore(tt.keys)
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, tt.wantMsg)
			assert.Contains(t, cfgErr.Action, tt.wantInAct)
			if tt.notInInAct != "" {
				assert.NotContains(t, cfgErr.Action, tt.notInInAct)
			}
		})
	}
}

// TestCheckKeyStoreRefusesMalformedGenerations: a name carrying a generation
// marker that is no generation fails at Validate with its rename spelled out.
// The family fixes the marker: a dotted family takes a final v<N> segment, a
// family without '.' keeps -v<N>.
func TestCheckKeyStoreRefusesMalformedGenerations(t *testing.T) {
	const noFamilyAction = "name a generation <family>.v<N> (family with '.') or <family>-v<N> (family without '.')"
	tests := []struct {
		entry     string
		wantMsg   string
		wantInAct string
	}{
		{entry: "payments.sign-v1", wantMsg: "a dotted family names its generations with a final v<N> segment", wantInAct: "rename it payments.sign.v1"},
		{entry: "audit.v1", wantMsg: `family "audit" has no '.', so its generations are named audit-v<N>`, wantInAct: "rename it audit-v1, or give the family a second segment (audit.<purpose>.v1)"},
		{entry: "payments-sign.v1", wantMsg: `family "payments-sign" has no '.'`, wantInAct: "rename it payments-sign-v1"},
		{entry: "x.y.v0", wantMsg: `generation "v0" must be a positive integer without leading zeros`, wantInAct: "rename it x.y.v1"},
		{entry: "x.y.v01", wantMsg: `generation "v01" must be a positive integer`, wantInAct: "rename it x.y.v1"},
		{entry: "x-v0", wantMsg: `generation "v0" must be a positive integer`, wantInAct: "rename it x-v1"},
		{entry: "x.v1.v2", wantMsg: `"x.v1" before it is no family`, wantInAct: noFamilyAction},
		{entry: "x-v1-v2", wantMsg: `"x-v1" before it is no family`, wantInAct: noFamilyAction},
		{entry: "-v1", wantMsg: `"" before it is no family`, wantInAct: noFamilyAction},
		{entry: strings.Repeat("a", 65) + "-v1", wantMsg: "is 65 bytes, maximum is 64", wantInAct: "shorten the family"},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			err := checkKeyStore(keysNamed(tt.entry))
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, "keystore.keys."+tt.entry, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, tt.wantMsg)
			assert.Contains(t, cfgErr.Action, tt.wantInAct)
		})
	}
}
