package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPresenceRecordsWhatTheLayersDelivered pins the presence seam (ADR-104): a key counts
// as delivered only when one of the operator's layers — the base YAML, the environment
// overlay, or an environment variable — actually put it into the tree. Defaults preloaded
// by the framework, leaves of an empty map, and a scalar the env merge guard drops are all
// absent, whatever koanf's own Exists says about them.
func TestPresenceRecordsWhatTheLayersDelivered(t *testing.T) {
	tests := []struct {
		name          string
		files         map[string]string
		env           map[string]string
		wantDelivered []string
		wantAbsent    []string
	}{
		{
			name:          "base_yaml_key_is_delivered",
			files:         map[string]string{testConfigFileYAML: "app:\n  namespace: from-base\n"},
			wantDelivered: []string{"app.namespace"},
		},
		{
			name: "overlay_only_key_is_delivered",
			files: map[string]string{
				testConfigFileYAML:    "app:\n  env: staging\n",
				"config.staging.yaml": "custom:\n  overlay: from-overlay\n",
			},
			wantDelivered: []string{"app.env", "custom.overlay"},
		},
		{
			name:          "env_var_only_key_is_delivered",
			env:           map[string]string{"CUSTOM_FROMENV": "yes"},
			wantDelivered: []string{"custom.fromenv"},
		},
		{
			name:          "yaml_null_is_delivered",
			files:         map[string]string{testConfigFileYAML: "custom:\n  nulled:\n"},
			wantDelivered: []string{"custom.nulled"},
		},
		{
			name:       "empty_map_delivers_no_leaf",
			files:      map[string]string{testConfigFileYAML: "database: {}\n"},
			wantAbsent: []string{"database.host", "database.type"},
		},
		{
			// CACHE_REDIS clears the bare-section TransformFunc (cache.redis is not a
			// top-level section) but skipScalarOverMapMerge drops it over the existing
			// cache.redis map, so it never reaches the tree and is never delivered.
			// The sibling variable is the control: it proves the env layer recorded at
			// all, so the absence below is the guard's doing and not a dead fixture.
			name:          "scalar_dropped_by_the_merge_guard_is_absent",
			env:           map[string]string{"CACHE_REDIS": "redis://stray:6379", "CACHE_ENABLED": "true"},
			wantDelivered: []string{"cache.enabled"},
			wantAbsent:    []string{"cache.redis"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfigFixture(t, tc.files, tc.env)
			require.NoError(t, err)
			require.NotNil(t, cfg)

			for _, key := range tc.wantDelivered {
				assert.True(t, cfg.delivered(key), "%s must be recorded as delivered", key)
			}
			for _, key := range tc.wantAbsent {
				assert.False(t, cfg.delivered(key), "%s must not be recorded as delivered", key)
			}
		})
	}
}

// TestPresenceAbsentForConfigLiteral pins the literal path: a Config assembled by hand has
// no source at all, so every key reads absent and the presence doors are inert without a
// nil check of their own.
func TestPresenceAbsentForConfigLiteral(t *testing.T) {
	cfg := &Config{}

	assert.False(t, cfg.delivered("database.host"))
	assert.False(t, cfg.delivered("debug.allowedips"))
	assert.False(t, cfg.delivered(""))
}

// TestPresenceAbsentForPreloadedDefault is the assertion that replaces the retired preload
// deny-list: a key koanf resolves purely from the framework's loaded defaults still resolves
// through Exists, and is still delivered by nobody.
func TestPresenceAbsentForPreloadedDefault(t *testing.T) {
	cfg, err := loadConfigFixture(t, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	for _, key := range []string{fieldServerPort, "app.name", "debug.allowedips"} {
		assert.True(t, cfg.Exists(key), "%s must still resolve through koanf", key)
		assert.False(t, cfg.delivered(key), "%s must not be recorded as delivered", key)
	}
}
