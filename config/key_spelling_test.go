package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const twinMessagePrefix = "is written in spellings that differ only by case: "

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
			assert.NotContains(t, err.Error(), twinMessagePrefix, "the root rule owns a section spelling")
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
	_, err := loadKeystoreYAML(t, "Keystore:\n  Keys:\n    - tokens:\n        our:\n          public: {value: sentinel}\n", "",
		map[string]string{"KEYSTORE_KEYS_SIGNING_PUBLIC_VALUE": "sentinel"})

	requireSpellingError(t, err, "keystore", "is spelled Keystore;",
		"framework section names are lowercase: rename Keystore to keystore")
}

// TestLoadRefusesCaseTwins pins ADR-157's twin rule: of two sibling keys that differ only by
// case, decode reads one and drops the other without a word, so both are refused, whether
// the lowercase one is a framework default or the operator's own, and in a data-keyed map
// as much as under a struct field.
func TestLoadRefusesCaseTwins(t *testing.T) {
	const (
		allowedIPsTwin = "debug.allowedIPs, debug.allowedips (framework default)"
		allowedIPsKeep = "debug.allowedips (DEBUG_ALLOWEDIPS)"
	)
	tests := []struct {
		name  string
		yaml  string
		field string
		paths string
		keep  string
	}{
		{
			name:  "server_port_beside_its_default",
			yaml:  "server:\n  Port: 9999\n",
			field: fieldServerPort,
			paths: "server.Port, server.port (framework default)",
			keep:  "server.port (SERVER_PORT)",
		},
		{
			name:  "log_level_beside_its_default",
			yaml:  "log:\n  Level: sentinel\n",
			field: fieldLogLevel,
			paths: "log.Level, log.level (framework default)",
			keep:  "log.level (LOG_LEVEL)",
		},
		{
			name:  "allowedips_list_beside_its_default",
			yaml:  "debug:\n  allowedIPs: [\"10.0.0.0/8\"]\n",
			field: fieldDebugAllowedIPs,
			paths: allowedIPsTwin,
			keep:  allowedIPsKeep,
		},
		{
			name:  "allowedips_empty_list_beside_its_default",
			yaml:  "debug:\n  allowedIPs: []\n",
			field: fieldDebugAllowedIPs,
			paths: allowedIPsTwin,
			keep:  allowedIPsKeep,
		},
		{
			name:  "allowedips_empty_beside_its_default",
			yaml:  "debug:\n  allowedIPs: \"\"\n",
			field: fieldDebugAllowedIPs,
			paths: allowedIPsTwin,
			keep:  allowedIPsKeep,
		},
		{
			name:  "allowedips_null_beside_its_default",
			yaml:  "debug:\n  allowedIPs:\n",
			field: fieldDebugAllowedIPs,
			paths: allowedIPsTwin,
			keep:  allowedIPsKeep,
		},
		{
			name:  "long_s_beside_its_default",
			yaml:  "server:\n  hoſt: sentinel\n",
			field: "server.host",
			paths: "server.host (framework default), server.hoſt",
			keep:  "server.host (SERVER_HOST)",
		},
		{
			name:  "long_s_key_empty_beside_its_default",
			yaml:  "debug:\n  allowedipſ: \"\"\n",
			field: fieldDebugAllowedIPs,
			paths: "debug.allowedips (framework default), debug.allowedipſ",
			keep:  "debug.allowedips (DEBUG_ALLOWEDIPS)",
		},
		{
			name:  "header_map_twins",
			yaml:  "observability:\n  trace:\n    headers:\n      Authorization: sentinel-a\n      authorization: sentinel-b\n",
			field: "observability.trace.headers.authorization",
			paths: "observability.trace.headers.Authorization, observability.trace.headers.authorization",
			keep:  "observability.trace.headers.authorization (OBSERVABILITY_TRACE_HEADERS_AUTHORIZATION)",
		},
		{
			name:  "password_twins",
			yaml:  "database:\n  password: sentinel-a\n  Password: sentinel-b\n",
			field: fieldDatabasePassword,
			paths: "database.Password, database.password",
			keep:  "database.password (DATABASE_PASSWORD)",
		},
		{
			name:  "mixed_case_parent_keeps_its_case",
			yaml:  "orders:\n  Api:\n    Url: sentinel-a\n    url: sentinel-b\n",
			field: "orders.Api.url",
			paths: "orders.Api.Url, orders.Api.url",
			keep:  "orders.Api.url",
		},
		{
			name:  "section_name_below_the_root",
			yaml:  "orders:\n  Server: sentinel-a\n  server: sentinel-b\n",
			field: "orders.server",
			paths: "orders.Server, orders.server",
			keep:  "orders.server (ORDERS_SERVER)",
		},
		{
			name:  "twins_in_a_sequence_element",
			yaml:  "orders:\n  routes:\n    - Name: sentinel-a\n      name: sentinel-b\n",
			field: "orders.routes[0].name",
			paths: "orders.routes[0].Name, orders.routes[0].name",
			keep:  "orders.routes[0].name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, nil)

			requireSpellingError(t, err, tt.field, twinMessagePrefix+tt.paths+";", "keys are lowercase: write it once as "+tt.keep)
		})
	}
}

// TestLoadRefusesCaseTwinsAcrossLayersEveryRun pins that a twin is refused wherever each
// spelling came from, and on every run: decode's pick between two non-exact spellings
// follows Go map order, so each case is loaded repeatedly.
func TestLoadRefusesCaseTwinsAcrossLayersEveryRun(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		env   map[string]string
		field string
		paths string
	}{
		{
			name: "overlay_spelling_beside_base_spelling",
			files: map[string]string{
				testConfigFileYAML: "server:\n  host: 10.0.0.1\n",
				"config.prod.yaml": "server:\n  Host: 10.0.0.5\n",
			},
			env:   map[string]string{"APP_ENV": "prod"},
			field: "server.host",
			paths: "server.Host, server.host",
		},
		{
			name:  "two_spellings_in_one_layer",
			files: map[string]string{testConfigFileYAML: "server:\n  probes:\n    Port: 9090\n    PORT: 9091\n"},
			field: fieldServerProbesPort,
			paths: "server.probes.PORT, server.probes.Port",
		},
		{
			name:  "empty_spelling_beside_a_value",
			files: map[string]string{testConfigFileYAML: "server:\n  probes:\n    port: 9090\n    host: 127.0.0.2\n    Host: \"\"\n"},
			field: fieldServerProbesHost,
			paths: "server.probes.Host, server.probes.host",
		},
		{
			name:  "empty_spelling_beside_a_lowercase_list",
			files: map[string]string{testConfigFileYAML: "debug:\n  allowedips: [\"10.0.0.0/8\"]\n  allowedIPs: \"\"\n"},
			field: fieldDebugAllowedIPs,
			paths: "debug.allowedIPs, debug.allowedips",
		},
		{
			name:  "yaml_spelling_beside_its_variable",
			files: map[string]string{testConfigFileYAML: "server:\n  probes:\n    Port: 9090\n"},
			env:   map[string]string{"SERVER_PROBES_PORT": "9091"},
			field: fieldServerProbesPort,
			paths: "server.probes.Port, server.probes.port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Staged once; each Load re-reads the same files and environment.
			_, err := loadConfigFixture(t, tt.files, tt.env)
			for range 50 {
				var cfgErr *ConfigError
				require.ErrorAs(t, err, &cfgErr)
				require.Equal(t, tt.field, cfgErr.Field)
				require.Equal(t, twinMessagePrefix+tt.paths+";", cfgErr.Message)
				_, err = Load()
			}
		})
	}
}

// TestLoadFromMapKeepsALoneSpelling is the counterweight: a non-lowercase key with no twin
// decodes as before, and a lone header keeps its case.
func TestLoadFromMapKeepsALoneSpelling(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{
		"server":        map[string]any{"probes": map[string]any{"Port": 9090}},
		"observability": map[string]any{"trace": map[string]any{"headers": map[string]any{"Authorization": "Bearer x"}}},
	})

	require.NoError(t, err)
	assert.Equal(t, 9090, cfg.Server.Probes.Port)
	assert.True(t, cfg.Exists("observability.trace.headers.Authorization"))
	assert.False(t, cfg.Exists("observability.trace.headers.authorization"))
}
