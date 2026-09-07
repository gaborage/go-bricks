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

// presenceIdentityDatabaseYAML is a complete, valid root database section: the base layer
// delivers every identity key, so an overlay that nulls the section is the only thing that
// can move the verdict.
const presenceIdentityDatabaseYAML = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n" +
	"database:\n  type: postgresql\n  host: db.internal\n  port: 5432\n" +
	"  database: appdb\n  username: app\n  password: s3cretpw\n"

// TestPresenceEvictedWhenOverlayNullsTheSection pins the eviction half of Presence
// (ADR-104): recording is append-only, but delivery is only meaningful for a key the FINAL
// tree still holds. An overlay that writes a bare `database:` replaces the merged section
// with a YAML null, so ADR-047 reads absence — and Presence must agree, or ADR-051's door
// aborts startup naming identity keys the tree no longer carries.
func TestPresenceEvictedWhenOverlayNullsTheSection(t *testing.T) {
	cfg, err := loadConfigFixture(t, map[string]string{
		testConfigFileYAML: presenceIdentityDatabaseYAML,
		"config.prod.yaml": "database:\n",
	}, map[string]string{"APP_ENV": "prod"})

	require.NoError(t, err, "a nulled section is ADR-047 absence, not a delivered-empty identity")
	require.NotNil(t, cfg)
	assert.False(t, cfg.delivered("database.host"), "the overlay's null evicted the base layer's delivery")
	assert.False(t, IsDatabaseConfigured(&cfg.Database), "the decoded section carries no identity")
}

// TestPresenceEvictedWhenOverlayNullsDebugSection is the same eviction for the other door
// (ADR-078): debug.allowedips was delivered by the base layer and nulled by the overlay, so
// the delivered-empty check must not fire on a key the final tree does not hold. The
// delivered-empty message is asserted against first, so a failure here is attributed to this
// door rather than to any other reason a nulled debug section might be rejected; Load
// succeeding is the pre-ADR-104 behavior this must restore.
func TestPresenceEvictedWhenOverlayNullsDebugSection(t *testing.T) {
	const base = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n" +
		"debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: \"10.0.0.1\"\n"

	cfg, err := loadConfigFixture(t, map[string]string{
		testConfigFileYAML: base,
		"config.prod.yaml": "debug:\n",
	}, map[string]string{"APP_ENV": "prod"})
	if err != nil {
		assert.NotContains(t, err.Error(), "delivered empty",
			"the overlay's null evicted debug.allowedips; the delivered-empty door must stay quiet")
	}
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.delivered(fieldDebugAllowedIPs), "the overlay's null evicted the base layer's delivery")
	assert.Empty(t, cfg.Debug.AllowedIPs, "the nulled section carries no allowlist")
}
