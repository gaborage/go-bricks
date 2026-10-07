package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	koanfmaps "github.com/knadh/koanf/maps"
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

// loadSelectorTree decodes a messaging.seal.active subtree the same way.
func loadSelectorTree(t *testing.T, tree map[string]any) (*Config, error) {
	t.Helper()
	return LoadFromMap(map[string]any{"messaging": map[string]any{"seal": map[string]any{"active": tree}}})
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
		"entries":              map[string]KeyPairConfig{"signing": entry},
		"pointers":             map[string]*KeyPairConfig{"signing": &entry},
		"typed_source":         map[string]any{"signing": map[string]any{"public": KeySourceConfig{File: "pub.der"}}},
		"typed_source_pointer": map[string]any{"signing": map[string]any{"public": &KeySourceConfig{File: "pub.der"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadFromMap(map[string]any{"keystore": map[string]any{"keys": keys}})
			require.NoError(t, err)
			assert.Equal(t, "pub.der", cfg.KeyStore.Keys["signing"].Public.File)
		})
	}
}

// TestKeystoreTreeRefusals pins each refusal of the walk: its Field is the
// koanf path an operator edits, and nothing below an entry is dropped in
// silence any more.
func TestKeystoreTreeRefusals(t *testing.T) {
	tests := []struct {
		name      string
		tree      map[string]any
		wantField string
		wantMsg   string
		wantInAct string
	}{
		{
			name:      "quoted_dotted_key",
			tree:      map[string]any{"tokens.our": publicValue("x")},
			wantField: "keystore.keys",
			wantMsg:   `key "tokens.our" is one YAML key containing '.'`,
			wantInAct: "write it nested (tokens: {our: …}); the nested path is what KEYSTORE_KEYS_TOKENS_OUR_* reaches",
		},
		{
			name:      "quoted_reserved_word",
			tree:      map[string]any{"webhook.secret": publicValue("x")},
			wantField: "keystore.keys",
			wantMsg:   `key "webhook.secret" is one YAML key containing '.'`,
			wantInAct: "rename it (e.g. webhook-secret, or another segment than secret): nested under a name, secret reads as that entry's field",
		},
		{
			name:      "quoted_dotted_key_in_namespace",
			tree:      map[string]any{"payments": map[string]any{"sign.v1": publicValue("x")}},
			wantField: "keystore.keys.payments",
			wantMsg:   `key "sign.v1" is one YAML key containing '.'`,
			wantInAct: "KEYSTORE_KEYS_PAYMENTS_SIGN_V1_*",
		},
		{
			name:      "empty_key",
			tree:      map[string]any{"tokens": map[string]any{"": publicValue("x")}},
			wantField: "keystore.keys.tokens",
			wantMsg:   "holds an empty key",
		},
		{
			name:      "scalar_in_namespace",
			tree:      map[string]any{"tokens": map[string]any{"our": "x"}},
			wantField: "keystore.keys.tokens.our",
			wantMsg:   "holds a value where an entry or a further name segment was expected",
		},
		{
			name: "entry_and_parent",
			tree: map[string]any{"tokens": map[string]any{
				"public": map[string]any{"value": "x"},
				"our":    publicValue("y"),
			}},
			wantField: "keystore.keys.tokens",
			wantMsg:   `"tokens" is an entry (it sets public) and the parent of entry "tokens.our"`,
			wantInAct: "tokens → tokens.default",
		},
		{
			name: "entry_and_deep_descendant",
			tree: map[string]any{"a": map[string]any{
				"secret": map[string]any{"value": "x"},
				"b":      map[string]any{"c": publicValue("y")},
			}},
			wantField: "keystore.keys.a",
			wantMsg:   `the parent of entry "a.b.c"`,
		},
		{
			name: "unknown_field",
			tree: map[string]any{"tokens": map[string]any{"our": map[string]any{
				"public":  map[string]any{"value": "x"},
				"privkey": map[string]any{"value": "y"},
			}}},
			wantField: "keystore.keys.tokens.our.privkey",
			wantMsg:   `unknown field "privkey" in entry "tokens.our"`,
			wantInAct: "an entry takes public, private, secret or pkcs12",
		},
		{
			name: "unknown_scalar_field",
			tree: map[string]any{"tokens": map[string]any{
				"public":  map[string]any{"value": "x"},
				"comment": "rotated 2026-09",
			}},
			wantField: "keystore.keys.tokens.comment",
			wantMsg:   `unknown field "comment" in entry "tokens"`,
		},
		{
			name:      "unknown_source_key",
			tree:      map[string]any{"tokens": map[string]any{"our": map[string]any{"public": map[string]any{"vlaue": "x"}}}},
			wantField: "keystore.keys.tokens.our.public.vlaue",
			wantMsg:   `unknown field "vlaue"`,
			wantInAct: "public takes file or value",
		},
		{
			name:      "unknown_pkcs12_key",
			tree:      map[string]any{"vts": map[string]any{"pkcs12": map[string]any{"file": "vts.p12", "pasword": map[string]any{"env": "P"}}}},
			wantField: "keystore.keys.vts.pkcs12.pasword",
			wantMsg:   `unknown field "pasword"`,
			wantInAct: "pkcs12 takes file, value or password",
		},
		{
			name:      "unknown_password_key",
			tree:      map[string]any{"vts": map[string]any{"pkcs12": map[string]any{"file": "vts.p12", "password": map[string]any{"value": "hunter2"}}}},
			wantField: "keystore.keys.vts.pkcs12.password.value",
			wantMsg:   `unknown field "value"`,
			wantInAct: "password takes env or file",
		},
		{
			name:      "scalar_source",
			tree:      map[string]any{"tokens": map[string]any{"public": "pub.der"}},
			wantField: "keystore.keys.tokens.public",
			wantMsg:   "holds a single value where a map was expected",
			wantInAct: "public takes file or value",
		},
		{
			name:      "map_under_leaf",
			tree:      map[string]any{"tokens": map[string]any{"public": map[string]any{"file": map[string]any{"x": "y"}}}},
			wantField: "keystore.keys.tokens.public.file",
			wantMsg:   "holds a map where a value was expected",
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

// TestTreeKeyWithAnEmptySegment: a literal key with an empty segment has no
// nested form and no variable that reaches it, so the refusal offers neither:
// both would hold an empty key and fail again.
func TestTreeKeyWithAnEmptySegment(t *testing.T) {
	roots := []struct {
		name  string
		field string
		load  func(*testing.T, map[string]any) (*Config, error)
		value any
	}{
		{name: "keystore", field: "keystore.keys", load: loadKeyTree, value: publicValue("x")},
		{name: "selectors", field: "messaging.seal.active", load: loadSelectorTree, value: "v1"},
	}
	for _, root := range roots {
		for _, key := range []string{".", "tokens..our", ".tokens", "tokens."} {
			t.Run(root.name+"/"+key, func(t *testing.T) {
				_, err := root.load(t, map[string]any{key: root.value})
				cfgErr := requireTreeError(t, err, root.field, fmt.Sprintf("key %q has an empty segment", key))
				assert.Contains(t, cfgErr.Action, "rename it")
				assert.NotContains(t, cfgErr.Action, ": {:")
				assert.NotContains(t, cfgErr.Action, "__")
				assert.NotContains(t, cfgErr.Action, "reaches")
			})
		}
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

// TestKeystoreTreeFirstErrorIsDeterministic: the walk sorts children at every
// level, so a tree with several faults reports the same one on every run
// whatever order Go iterates its maps in.
func TestKeystoreTreeFirstErrorIsDeterministic(t *testing.T) {
	tree := map[string]any{
		"zz": map[string]any{"public": map[string]any{"vlaue": "x"}},
		"mm": map[string]any{"our": "scalar"},
		"aa": map[string]any{"b": map[string]any{"public": map[string]any{"value": "x"}, "privkey": map[string]any{}}},
		"cc": map[string]any{"d.e": publicValue("x")},
	}
	_, first := loadKeyTree(t, tree)
	require.Error(t, first)
	for range 50 {
		_, err := loadKeyTree(t, tree)
		require.EqualError(t, err, first.Error())
	}
	requireTreeError(t, first, "keystore.keys.aa.b.privkey", `unknown field "privkey"`)
}

// TestKeystoreTreeReadsSelectors: a scalar leaf at a nested path is the
// selector for the dotted family, the flat hyphenated spelling reads
// unchanged, and a literal dotted key or an empty key is refused.
func TestKeystoreTreeReadsSelectors(t *testing.T) {
	cfg, err := loadSelectorTree(t, map[string]any{
		"payments":     map[string]any{"sign": "v2", "encrypt": map[string]any{"core": "v1"}},
		"svc-sign":     "v3",
		"unset-family": nil,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"payments.sign":         "v2",
		"payments.encrypt.core": "v1",
		"svc-sign":              "v3",
		"unset-family":          "",
	}, cfg.Messaging.Seal.Active)

	_, err = loadSelectorTree(t, map[string]any{"payments.sign": "v2"})
	cfgErr := requireTreeError(t, err, "messaging.seal.active", `key "payments.sign" is one YAML key containing '.'`)
	// A selector is a leaf: one exact variable reaches it, and VAR_* would reach
	// a selector below it instead.
	assert.Contains(t, cfgErr.Action, "the nested path is what MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN reaches")
	assert.NotContains(t, cfgErr.Action, "_*")

	// Field words are reserved in keystore names only, so a selector keeps the nested advice.
	_, err = loadSelectorTree(t, map[string]any{"orders.secret": "v2"})
	cfgErr = requireTreeError(t, err, "messaging.seal.active", `key "orders.secret" is one YAML key containing '.'`)
	assert.Contains(t, cfgErr.Action, "write it nested (orders: {secret: …})")

	_, err = loadSelectorTree(t, map[string]any{"payments": map[string]any{"": "v2"}})
	requireTreeError(t, err, "messaging.seal.active.payments", "holds an empty key")
}

// TestKeystoreTreeRefusesAnEmptySelectorNamespace: an empty map in the
// selector tree selects nothing and names no further segment. Before ADR-144
// mapstructure refused it ('messaging.seal.active[payments]' expected type
// 'string', got unconvertible type 'map[string]interface {}'); the walk must
// not drop it in silence, and refuses it at its own path instead.
func TestKeystoreTreeRefusesAnEmptySelectorNamespace(t *testing.T) {
	tests := []struct {
		name      string
		tree      map[string]any
		wantField string
	}{
		{name: "one_segment", tree: map[string]any{"payments": map[string]any{}}, wantField: "messaging.seal.active.payments"},
		{name: "nested", tree: map[string]any{"payments": map[string]any{"sign": map[string]any{}}}, wantField: "messaging.seal.active.payments.sign"},
		{
			name:      "beside_a_selector",
			tree:      map[string]any{"payments": map[string]any{"sign": "v2", "encrypt": map[string]any{}}},
			wantField: "messaging.seal.active.payments.encrypt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadSelectorTree(t, tt.tree)
			cfgErr := requireTreeError(t, err, tt.wantField, "holds an empty map where a generation or a further name segment was expected")
			assert.Equal(t, "set the selector to a generation (v<N>), or remove the key", cfgErr.Action)
		})
	}

	// The real Load reaches the same walk from a YAML file, and a variable's
	// scalar on the same path is dropped over the map, not merged into it.
	t.Run("yaml_through_load", func(t *testing.T) {
		_, err := loadKeystoreYAML(t, "messaging:\n  seal:\n    active:\n      payments: {}\n", "", nil)
		requireTreeError(t, err, "messaging.seal.active.payments", "holds an empty map")
	})
	t.Run("yaml_under_a_variable_scalar", func(t *testing.T) {
		_, err := loadKeystoreYAML(t, "messaging:\n  seal:\n    active:\n      payments: {}\n", "",
			map[string]string{"MESSAGING_SEAL_ACTIVE_PAYMENTS": "v2"})
		requireTreeError(t, err, "messaging.seal.active.payments", "holds an empty map")
	})
}

// TestConfigUnmarshalReadsTheKeystoreTree: the public Config.Unmarshal door
// decodes with the same tree reader as Load, for either struct, and its
// refusal is a *ConfigError errors.As reaches through mapstructure's
// DecodeError (v2.5.0 unwraps it and joins field errors with errors.Join).
func TestConfigUnmarshalReadsTheKeystoreTree(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{
		"keystore": map[string]any{"keys": map[string]any{"tokens": map[string]any{"our": publicValue("pub")}}},
		"custom": map[string]any{
			"ks":   map[string]any{"keys": map[string]any{"tokens": map[string]any{"public": map[string]any{"value": "x"}, "our": publicValue("y")}}},
			"seal": map[string]any{"active": map[string]any{"payments": map[string]any{"sign": "v2"}}},
		},
	})
	require.NoError(t, err)

	var ks KeyStoreConfig
	require.NoError(t, cfg.Unmarshal("keystore", &ks))
	assert.Equal(t, "pub", ks.Keys["tokens.our"].Public.Value)

	var seal SealConfig
	require.NoError(t, cfg.Unmarshal("custom.seal", &seal))
	assert.Equal(t, map[string]string{"payments.sign": "v2"}, seal.Active)

	err = cfg.Unmarshal("custom.ks", &KeyStoreConfig{})
	requireTreeError(t, err, "custom.ks.keys.tokens", `(it sets public) and the parent of entry "tokens.our"`)
}

// TestConfigUnmarshalKeysMapReadsTheTree: Unmarshal straight into the keys
// map, rather than into KeyStoreConfig, reads the same dotted names. Before,
// the hook fired on the struct only, so the map door decoded the namespace
// "tokens" as a phantom entry and dropped "our" in silence. A pointer map and
// a refusal take the same path.
func TestConfigUnmarshalKeysMapReadsTheTree(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{
		"keystore": map[string]any{"keys": map[string]any{"tokens": map[string]any{"our": publicValue("pub")}}},
		"custom": map[string]any{"keys": map[string]any{"tokens": map[string]any{
			"public": map[string]any{"value": "x"},
			"our":    publicValue("y"),
		}}},
	})
	require.NoError(t, err)

	var keys map[string]KeyPairConfig
	require.NoError(t, cfg.Unmarshal("keystore.keys", &keys))
	assert.Equal(t, map[string]KeyPairConfig{"tokens.our": {Public: KeySourceConfig{Value: "pub"}}}, keys)

	var ptrKeys map[string]*KeyPairConfig
	require.NoError(t, cfg.Unmarshal("keystore.keys", &ptrKeys))
	require.Equal(t, []string{"tokens.our"}, slices.Sorted(maps.Keys(ptrKeys)))
	assert.Equal(t, "pub", ptrKeys["tokens.our"].Public.Value)

	err = cfg.Unmarshal("custom.keys", &map[string]KeyPairConfig{})
	requireTreeError(t, err, "custom.keys.tokens", `(it sets public) and the parent of entry "tokens.our"`)
}

// TestConfigUnmarshalEntryRefusesANamespace: a dotted entry is read by its
// full path; the path of its namespace decoded as one entry used to yield an
// empty KeyPairConfig with "our" dropped, and is refused instead.
func TestConfigUnmarshalEntryRefusesANamespace(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{
		"keystore": map[string]any{"keys": map[string]any{"tokens": map[string]any{"our": publicValue("pub")}}},
	})
	require.NoError(t, err)

	var entry KeyPairConfig
	require.NoError(t, cfg.Unmarshal("keystore.keys.tokens.our", &entry))
	assert.Equal(t, KeyPairConfig{Public: KeySourceConfig{Value: "pub"}}, entry)

	err = cfg.Unmarshal("keystore.keys.tokens", &KeyPairConfig{})
	cfgErr := requireTreeError(t, err, "keystore.keys.tokens", `a keystore entry was decoded from a node holding "our", which is no entry field`)
	assert.Equal(t, "unmarshal an entry by its full dotted path (e.g. keystore.keys.tokens.our), or the keys map, which reads nested names", cfgErr.Action)
}

// TestConfigUnmarshalNamesTheDecodedPath: the tree reader fires on its types
// wherever Config.Unmarshal meets them, so a refusal names the path that was
// decoded, not the keystore.keys or messaging.seal.active root Load meets.
// A custom section that holds one entry beside its own metadata is refused,
// as a keystore entry is: the reader cannot tell a sibling from a name.
func TestConfigUnmarshalNamesTheDecodedPath(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{
		"custom": map[string]any{
			"partner": map[string]any{"public": map[string]any{"value": "x"}, "kid": "partner-2026"},
			"vendor":  map[string]any{"keys": map[string]any{"a": map[string]any{"public": map[string]any{"value": "x"}, "label": "y"}}},
			"seal":    map[string]any{"active": map[string]any{"payments": map[string]any{}}},
		},
	})
	require.NoError(t, err)

	err = cfg.Unmarshal("custom.partner", &KeyPairConfig{})
	requireTreeError(t, err, "custom.partner", `a keystore entry was decoded from a node holding "kid"`)

	err = cfg.Unmarshal("custom.vendor.keys", &map[string]KeyPairConfig{})
	requireTreeError(t, err, "custom.vendor.keys.a.label", `unknown field "label" in entry "a"`)

	var vendor struct {
		Keys map[string]KeyPairConfig `koanf:"keys"`
	}
	err = cfg.Unmarshal("custom.vendor", &vendor)
	requireTreeError(t, err, "custom.vendor.keys.a.label", `unknown field "label" in entry "a"`)

	err = cfg.Unmarshal("custom.seal", &SealConfig{})
	requireTreeError(t, err, "custom.seal.active.payments", "holds an empty map")

	err = cfg.Unmarshal("custom", &struct {
		Seal SealConfig `koanf:"seal"`
	}{})
	requireTreeError(t, err, "custom.seal.active.payments", "holds an empty map")
}

// TestKeystoreTreeRefusesSequences: mapstructure's weak decoding merges a
// sequence of maps into a map, which bypassed the walk: under keystore.keys
// it decoded the namespace "tokens" as a phantom entry. A sequence is refused
// under keystore.keys and messaging.seal.active alike, empty or not; main
// accepted both as a map.
func TestKeystoreTreeRefusesSequences(t *testing.T) {
	keysAction := "write the entries as a map, one key per name segment (keys: {tokens: {our: {public: …}}})"
	selectorsAction := "write the selectors as a map, one key per name segment (active: {payments: {sign: v2}})"
	tests := []struct {
		name       string
		data       map[string]any
		wantField  string
		wantAction string
	}{
		{
			name:       "keys_sequence_of_nested_names",
			data:       map[string]any{"keystore": map[string]any{"keys": []any{map[string]any{"tokens": map[string]any{"our": publicValue("x")}}}}},
			wantField:  "keystore.keys",
			wantAction: keysAction,
		},
		{
			name:       "keys_empty_sequence",
			data:       map[string]any{"keystore": map[string]any{"keys": []any{}}},
			wantField:  "keystore.keys",
			wantAction: keysAction,
		},
		{
			name:       "selectors_sequence",
			data:       map[string]any{"messaging": map[string]any{"seal": map[string]any{"active": []any{map[string]any{"payments-sign": "v2"}}}}},
			wantField:  "messaging.seal.active",
			wantAction: selectorsAction,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFromMap(tt.data)
			cfgErr := requireTreeError(t, err, tt.wantField, "holds a sequence where a map was expected")
			assert.Equal(t, tt.wantAction, cfgErr.Action)
		})
	}

	t.Run("yaml_through_load", func(t *testing.T) {
		_, err := loadKeystoreYAML(t, `
keystore:
  keys:
    - tokens:
        our:
          public: {value: our-pub}
`, "", nil)
		requireTreeError(t, err, "keystore.keys", "holds a sequence where a map was expected")
	})
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

// loadKeystoreYAML runs the real Load in a fresh directory holding config.yaml
// (and the optional development overlay), with no ambient keystore or
// selector variable, then the variables env sets. Variable names may carry
// '-', as Docker and Kubernetes allow and a POSIX export does not.
func loadKeystoreYAML(t *testing.T, base, overlay string, env map[string]string) (*Config, error) {
	t.Helper()
	clearEnvironmentVariables()
	t.Cleanup(clearEnvironmentVariables)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "KEYSTORE_KEYS_") || strings.HasPrefix(name, "MESSAGING_SEAL_ACTIVE_") {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}
	dir := t.TempDir()
	if base != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(base), 0o600))
	}
	if overlay != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.development.yaml"), []byte(overlay), 0o600))
	}
	t.Chdir(dir)
	for name, value := range env {
		t.Setenv(name, value)
	}
	return Load()
}

// TestLoadRefusesASequenceALaterLayerReplaced: a sequence under keystore.keys
// or messaging.seal.active is refused at decode, but only one that survives the
// merge reaches decode. A map from a later layer (the env overlay, or a
// variable) replaces a sequence at its path, so the entries or selectors the
// sequence held were dropped and the config booted. Each layer's sequences are
// recorded before the merge, as its selectors are, and refused after decode.
func TestLoadRefusesASequenceALaterLayerReplaced(t *testing.T) {
	const keysSequence = `
keystore:
  keys:
    - tokens:
        our:
          public: {value: our-pub}
`
	const selectorSequence = `
messaging:
  seal:
    active:
      - payments-sign: v2
`
	keysAction := "write the entries as a map, one key per name segment (keys: {tokens: {our: {public: …}}})"
	selectorsAction := "write the selectors as a map, one key per name segment (active: {payments: {sign: v2}})"
	tests := []struct {
		name       string
		base       string
		overlay    string
		env        map[string]string
		wantField  string
		wantAction string
		wantWrap   string
	}{
		{
			name:       "keys_sequence_under_a_variable",
			base:       keysSequence,
			env:        map[string]string{"KEYSTORE_KEYS_SIGNING_PUBLIC_VALUE": "sig-pub"},
			wantField:  "keystore.keys",
			wantAction: keysAction,
			wantWrap:   "keystore config: ",
		},
		{
			name:       "keys_sequence_under_an_overlay_map",
			base:       keysSequence,
			overlay:    "keystore:\n  keys:\n    signing:\n      public: {value: sig-pub}\n",
			wantField:  "keystore.keys",
			wantAction: keysAction,
			wantWrap:   "keystore config: ",
		},
		{
			name:       "empty_keys_sequence_under_a_variable",
			base:       "keystore:\n  keys: []\n",
			env:        map[string]string{"KEYSTORE_KEYS_SIGNING_PUBLIC_VALUE": "sig-pub"},
			wantField:  "keystore.keys",
			wantAction: keysAction,
			wantWrap:   "keystore config: ",
		},
		{
			name:       "namespace_sequence_under_a_variable",
			base:       "keystore:\n  keys:\n    tokens:\n      - our:\n          public: {value: our-pub}\n",
			env:        map[string]string{"KEYSTORE_KEYS_TOKENS_PEER_PUBLIC_VALUE": "peer-pub"},
			wantField:  "keystore.keys.tokens",
			wantAction: keysAction,
			wantWrap:   "keystore config: ",
		},
		{
			name:       "capitalized_keys_sequence_beside_a_variable",
			base:       "Keystore:\n  Keys:\n    - tokens:\n        our:\n          public: {value: our-pub}\n",
			env:        map[string]string{"KEYSTORE_KEYS_SIGNING_PUBLIC_VALUE": "sig-pub"},
			wantField:  "keystore.keys",
			wantAction: keysAction,
			wantWrap:   "keystore config: ",
		},
		{
			name:       "selector_sequence_under_a_variable",
			base:       selectorSequence,
			env:        map[string]string{"MESSAGING_SEAL_ACTIVE_ORDERS": "v1"},
			wantField:  "messaging.seal.active",
			wantAction: selectorsAction,
			wantWrap:   "messaging config: ",
		},
		{
			name:       "selector_sequence_under_an_overlay_map",
			base:       selectorSequence,
			overlay:    "messaging:\n  seal:\n    active:\n      orders: v1\n",
			wantField:  "messaging.seal.active",
			wantAction: selectorsAction,
			wantWrap:   "messaging config: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadKeystoreYAML(t, tt.base, tt.overlay, tt.env)
			cfgErr := requireTreeError(t, err, tt.wantField, "holds a sequence where a map was expected")
			assert.Contains(t, cfgErr.Message, "a later layer replaced it")
			assert.Equal(t, tt.wantAction, cfgErr.Action)
			assert.Contains(t, err.Error(), "invalid configuration: "+tt.wantWrap)
		})
	}

	// Inside an entry the field schema governs: a later layer that sets a
	// source over a sequence there overrides one field of one entry and drops
	// no entry, as it did before.
	t.Run("sequence_inside_an_entry_under_a_variable_boots", func(t *testing.T) {
		cfg, err := loadKeystoreYAML(t, "keystore:\n  keys:\n    tokens:\n      public: [a, b]\n", "",
			map[string]string{"KEYSTORE_KEYS_TOKENS_PUBLIC_VALUE": "tok-pub"})
		require.NoError(t, err)
		assert.Equal(t, "tok-pub", cfg.KeyStore.Keys["tokens"].Public.Value)
	})

	// A sequence elsewhere is no keystore or selector node: it is not recorded.
	t.Run("sequence_outside_both_subtrees_boots", func(t *testing.T) {
		_, err := loadKeystoreYAML(t, "log:\n  sensitivefields: [pan]\nkeystore:\n  other: [x]\n", "", nil)
		require.NoError(t, err)
	})
}

// TestLoadFromMapDottedKeys: LoadFromMap unflattens only its top-level keys,
// so a flat dotted key reaches the nested path, while a literal dotted key
// inside a nested map is the quoted-key shape and is refused.
func TestLoadFromMapDottedKeys(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{"keystore.keys.tokens.our.public.value": "our-pub"})
	require.NoError(t, err)
	assert.Equal(t, map[string]KeyPairConfig{"tokens.our": {Public: KeySourceConfig{Value: "our-pub"}}}, cfg.KeyStore.Keys)

	_, err = LoadFromMap(map[string]any{"keystore": map[string]any{"keys": map[string]any{"tokens.our": publicValue("our-pub")}}})
	requireTreeError(t, err, "keystore.keys", `key "tokens.our" is one YAML key containing '.'`)
}

// fuzzNames maps fuzz bytes onto candidate names over a deliberately small
// alphabet, so prefix, fold and generation-marker collisions are frequent;
// ',' separates names. A byte already in the alphabet is kept, so a seed reads
// as written. Only valid entry names of one to four segments are kept,
// deduplicated, at most eight.
func fuzzNames(raw string) []string {
	const alphabet = "abcv01-.,"
	var sb strings.Builder
	for i := range len(raw) {
		c := raw[i]
		if strings.IndexByte(alphabet, c) < 0 {
			c = alphabet[int(c)%len(alphabet)]
		}
		sb.WriteByte(c)
	}
	var names []string
	for name := range strings.SplitSeq(sb.String(), ",") {
		if !keyname.ValidEntryName(name) || strings.Count(name, keyname.Sep) > 3 || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
		if len(names) == 8 {
			break
		}
	}
	return names
}

// FuzzKeystoreEnvRoundTrip: for every valid entry name, the variable
// keyToEnvVar builds for one of its sources is turned back by the unchanged
// transform into that exact path, the path unflattens and walks to exactly
// that entry, envVarForKey names the variable, and the variable is
// POSIX-exportable exactly when the name holds no '-'. This is the ADR-144
// promise: KEYSTORE_KEYS_TOKENS_OUR_PRIVATE_VALUE is entry "tokens.our".
func FuzzKeystoreEnvRoundTrip(f *testing.F) {
	for _, seed := range []string{"tokens.our", "payments.sign.v1", "tokens-our", "secret", "a-b.c-d", "x"} {
		f.Add(seed)
	}
	posix := regexp.MustCompile(`^[A-Z0-9_]+$`)
	f.Fuzz(func(t *testing.T, raw string) {
		names := fuzzNames(raw)
		if len(names) == 0 {
			t.Skip()
		}
		name := names[0]
		path := fieldKeystoreKeys + "." + name + ".public.value"
		envVar := keyToEnvVar(path)
		require.Equal(t, path, envVarToKey(envVar))
		require.Equal(t, envVar, envVarForKey(path))
		require.Equal(t, !strings.Contains(name, "-"), posix.MatchString(envVar), "variable %s", envVar)

		tree := koanfmaps.Unflatten(map[string]any{envVarToKey(envVar): "v"}, keyname.Sep)
		section, ok := stringMap(tree["keystore"])
		require.True(t, ok)
		keysTree, ok := stringMap(section["keys"])
		require.True(t, ok)
		flat, err := readKeyTree(keysTree)
		require.NoError(t, err)
		require.Equal(t, map[string]any{name: map[string]any{"public": map[string]any{"value": "v"}}}, flat)
	})
}
