package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireSpellingError asserts the first ConfigError of a refused load and that no
// configured value reached the rendered error.
func requireSpellingError(t *testing.T, err error, field, message, action string) {
	t.Helper()
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, errCategoryInvalid, cfgErr.Category)
	assert.Equal(t, field, cfgErr.Field)
	assert.Equal(t, message, cfgErr.Message)
	assert.Equal(t, action, cfgErr.Action)
	assert.NotContains(t, err.Error(), "sentinel")
}

// TestLoadFromMapRefusesMisspelledSections pins ADR-157's root rule: a root key that folds
// to a framework section name decodes into that section, but Exists and the observability
// bootstrap look the section up by its exact name, so it is refused unless spelled as it.
func TestLoadFromMapRefusesMisspelledSections(t *testing.T) {
	section := func(key string) map[string]any { return map[string]any{key: map[string]any{"name": "sentinel"}} }
	tests := []struct {
		name      string
		data      map[string]any
		field     string
		spellings string
	}{
		{name: "app_beside_its_defaults", data: section("App"), field: "app", spellings: "App"},
		{name: "observability_without_defaults", data: section("Observability"), field: "observability", spellings: "Observability"},
		{name: "database_without_defaults", data: section("Database"), field: "database", spellings: "Database"},
		{name: "long_s_section", data: section("meſſaging"), field: "messaging", spellings: "meſſaging"},
		{
			name:      "every_spelling_in_one_error",
			data:      map[string]any{"APP": map[string]any{"name": "sentinel"}, "App": map[string]any{"name": "sentinel"}},
			field:     "app",
			spellings: "APP, App",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFromMap(tt.data)

			requireSpellingError(t, err, tt.field, "is spelled "+tt.spellings+";",
				"framework section names are lowercase: rename "+tt.spellings+" to "+tt.field)
		})
	}

	t.Run("rendered", func(t *testing.T) {
		_, err := LoadFromMap(section("App"))
		require.Error(t, err)
		assert.Equal(t, "config_invalid: app is spelled App; framework section names are lowercase: rename App to app", err.Error())
	})

	t.Run("lowercase_sections_and_other_root_keys_load", func(t *testing.T) {
		_, err := LoadFromMap(map[string]any{"app": map[string]any{"name": "a"}, "Orders": map[string]any{"url": "u"}})
		require.NoError(t, err)
	})
}

// TestLoadRefusesACapitalizedSectionBeforeItsSequence pins that the root rule runs at decode,
// before the per-layer sequence check (ADR-144) would judge the same section.
func TestLoadRefusesACapitalizedSectionBeforeItsSequence(t *testing.T) {
	_, err := loadConfigFixture(t,
		map[string]string{testConfigFileYAML: "Keystore:\n  Keys:\n    - tokens:\n        our:\n          public: {value: sentinel}\n"},
		map[string]string{"KEYSTORE_KEYS_SIGNING_PUBLIC_VALUE": "sentinel"})

	requireSpellingError(t, err, "keystore", "is spelled Keystore;",
		"framework section names are lowercase: rename Keystore to keystore")
}
