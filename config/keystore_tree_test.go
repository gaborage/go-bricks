package config

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// loadKeyTree decodes a keystore.keys subtree through LoadFromMap, the
// decoder Load uses, without the section checks.
func loadKeyTree(t *testing.T, tree map[string]any) (*Config, error) {
	t.Helper()
	return LoadFromMap(map[string]any{"keystore": map[string]any{"keys": tree}})
}

func publicValue(v string) map[string]any {
	return map[string]any{"public": map[string]any{"value": v}}
}

// requireTreeError asserts the decode failed with a *ConfigError at field whose
// message contains msg: mapstructure wraps the hook's error in a DecodeError and
// joins struct-field errors, and both unwrap.
func requireTreeError(t *testing.T, err error, field, msg string) *ConfigError {
	t.Helper()
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, field, cfgErr.Field)
	assert.Contains(t, cfgErr.Message, msg)
	return cfgErr
}

// TestKeystoreTreeReadsNestedNames: the nested path is the entry name, joined
// with '.', and a namespace holds any number of entries.
func TestKeystoreTreeReadsNestedNames(t *testing.T) {
	cfg, err := loadKeyTree(t, map[string]any{
		"tokens": map[string]any{"our": publicValue("our-pub"), "peer": publicValue("peer-pub")},
		"payments": map[string]any{
			"sign":    map[string]any{"v1": publicValue("sign-pub")},
			"encrypt": map[string]any{"v1": publicValue("enc-pub")},
		},
		"webhook-signing": map[string]any{"secret": map[string]any{"file": "/run/secrets/hmac"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"payments.encrypt.v1", "payments.sign.v1", "tokens.our", "tokens.peer", "webhook-signing"},
		slices.Sorted(maps.Keys(cfg.KeyStore.Keys)))
	assert.Equal(t, "our-pub", cfg.KeyStore.Keys["tokens.our"].Public.Value)
	assert.Equal(t, "sign-pub", cfg.KeyStore.Keys["payments.sign.v1"].Public.Value)
	assert.Equal(t, "/run/secrets/hmac", cfg.KeyStore.Keys["webhook-signing"].Secret.File)
}

// TestKeystoreTreeAcceptsShapesThatBootToday: a null or empty entry still
// reaches the source check, a legacy one-segment entry named after a field is
// an entry, and a field matched as mapstructure matches it still reads.
func TestKeystoreTreeAcceptsShapesThatBootToday(t *testing.T) {
	tests := []struct {
		name  string
		tree  map[string]any
		check func(t *testing.T, keys map[string]KeyPairConfig)
	}{
		{name: "null_entry", tree: map[string]any{"tokens": map[string]any{"our": nil}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Contains(t, keys, "tokens.our")
			assert.Empty(t, keys["tokens.our"].Public)
		}},
		{name: "empty_entry", tree: map[string]any{"tokens": map[string]any{"our": map[string]any{}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Contains(t, keys, "tokens.our")
		}},
		{name: "legacy_entry_named_secret", tree: map[string]any{"secret": map[string]any{"secret": map[string]any{"value": "c2VjcmV0"}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "c2VjcmV0", keys["secret"].Secret.Value)
		}},
		{name: "field_name_case", tree: map[string]any{"tokens": map[string]any{"Public": map[string]any{"File": "pub.der"}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "pub.der", keys["tokens"].Public.File)
		}},
		{name: "pkcs12_password_env", tree: map[string]any{"vts": map[string]any{"pkcs12": map[string]any{
			"file": "vts.p12", "password": map[string]any{"env": "VTS_P12_PASSWORD"},
		}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "VTS_P12_PASSWORD", keys["vts"].PKCS12.Password.Env)
		}},
		// A LoadFromMap caller may hand typed entries; mapstructure assigned them
		// directly before the walk existed.
		{name: "typed_entry", tree: map[string]any{"signing": KeyPairConfig{Public: KeySourceConfig{File: "pub.der"}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "pub.der", keys["signing"].Public.File)
		}},
		{name: "typed_pointer_entry", tree: map[string]any{"signing": &KeyPairConfig{Public: KeySourceConfig{File: "pub.der"}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "pub.der", keys["signing"].Public.File)
		}},
		{name: "typed_entry_under_namespace", tree: map[string]any{"tokens": map[string]KeyPairConfig{"our": {Public: KeySourceConfig{File: "pub.der"}}}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Equal(t, "pub.der", keys["tokens.our"].Public.File)
		}},
		{name: "null_field", tree: map[string]any{"tokens": map[string]any{"public": publicValue("x")["public"], "private": nil}}, check: func(t *testing.T, keys map[string]KeyPairConfig) {
			assert.Empty(t, keys["tokens"].Private)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadKeyTree(t, tt.tree)
			require.NoError(t, err)
			tt.check(t, cfg.KeyStore.Keys)
		})
	}
}

// TestLoadFromMapAcceptsATypedKeysMap: a LoadFromMap caller that builds
// keystore.keys as a typed map, of entries or of pointers to them, decodes as
// it did before the walk: each typed entry is an entry as it stands.
func TestLoadFromMapAcceptsATypedKeysMap(t *testing.T) {
	entry := KeyPairConfig{Public: KeySourceConfig{File: "pub.der"}}
	for name, keys := range map[string]any{
		"entries":  map[string]KeyPairConfig{"signing": entry},
		"pointers": map[string]*KeyPairConfig{"signing": &entry},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadFromMap(map[string]any{"keystore": map[string]any{"keys": keys}})
			require.NoError(t, err)
			assert.Equal(t, "pub.der", cfg.KeyStore.Keys["signing"].Public.File)
		})
	}
}

// TestKeystoreTreeRefusals pins each refusal of the walk: its Field is the
// koanf path an operator edits.
func TestKeystoreTreeRefusals(t *testing.T) {
	tests := []struct {
		name      string
		tree      map[string]any
		wantField string
		wantMsg   string
		wantInAct string
	}{
		{
			name:      "scalar_in_namespace",
			tree:      map[string]any{"tokens": map[string]any{"our": "x"}},
			wantField: "keystore.keys.tokens.our",
			wantMsg:   "holds a value where an entry or a further name segment was expected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadKeyTree(t, tt.tree)
			cfgErr := requireTreeError(t, err, tt.wantField, tt.wantMsg)
			assert.Contains(t, cfgErr.Action, tt.wantInAct)
		})
	}
}

// TestStringMapConvertsStringKeyedMaps: a string-keyed map of another type (a
// LoadFromMap caller's map[string]string) reads as the same tree, and a map
// with other keys or a scalar is passed through for mapstructure to judge.
func TestStringMapConvertsStringKeyedMaps(t *testing.T) {
	got, ok := stringMap(map[string]string{"file": "pub.der"})
	require.True(t, ok)
	assert.Equal(t, map[string]any{"file": "pub.der"}, got)

	_, ok = stringMap(map[int]any{1: "x"})
	assert.False(t, ok)
	_, ok = stringMap("scalar")
	assert.False(t, ok)

	cfg, err := loadKeyTree(t, map[string]any{"tokens": map[string]any{"our": map[string]any{"public": map[string]string{"value": "pub"}}}})
	require.NoError(t, err)
	assert.Equal(t, "pub", cfg.KeyStore.Keys["tokens.our"].Public.Value)
}

// TestKeyEntryFieldsArePinnedToKeyPairConfig: keyname.FieldSegments is the set
// a dotted name may not use after a '.', and the tree reader recognizes an
// entry by KeyPairConfig's tags. A field added to the struct without the
// reserved set would let a name read as that field.
func TestKeyEntryFieldsArePinnedToKeyPairConfig(t *testing.T) {
	var tags []string
	for field := range reflect.TypeFor[KeyPairConfig]().Fields() {
		tags = append(tags, field.Tag.Get("mapstructure"))
	}
	assert.ElementsMatch(t, keyname.FieldSegments, tags)
	assert.ElementsMatch(t, keyname.FieldSegments, keyEntrySchema.names())
}
