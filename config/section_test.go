package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sectionEngineTestCases names one root and one non-root section per kind this engine
// addresses, so a guarantee the engine makes can be pinned once and proven identical across
// every kind it serves — rather than once per kind, which is what cache_section.go used to be.
func sectionEngineTestCases() []struct {
	name string
	root section
	sub  section
} {
	return []struct {
		name string
		root section
		sub  section
	}{
		{name: "database", root: rootDatabaseSection(), sub: tenantDatabaseSection("acme")},
		{name: "cache", root: rootCacheSection(), sub: tenantCacheSection("acme")},
	}
}

// TestSectionQualifyRootIsIdentity pins the identity guarantee every kind's root section gets
// from the shared engine: the SAME error value comes back, not a copy that merely compares
// equal — what keeps a single-tenant deployment's errors byte-identical. cache_section.go used
// to prove this only for cache; the engine now proves it once, for every kind it serves.
func TestSectionQualifyRootIsIdentity(t *testing.T) {
	for _, tt := range sectionEngineTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := NewMissingFieldError(tt.root.rootField+".host", "X_HOST", tt.root.rootField+".host")

			got := tt.root.qualify(err)

			assert.Same(t, err, got)
		})
	}
}

// TestSectionQualifyWrapsANonConfigError covers the branch that has no field to move: a plain
// error is wrapped with the section path in the message instead — the one place a path is
// allowed into the message rather than the field. Both kinds share the same fallback.
func TestSectionQualifyWrapsANonConfigError(t *testing.T) {
	for _, tt := range sectionEngineTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := errors.New("dial refused")

			got := tt.sub.qualify(sentinel)

			require.ErrorIs(t, got, sentinel)
			assert.Contains(t, got.Error(), tt.sub.path)
		})
	}
}

// TestSectionQualifyClonesDetails pins that the qualified copy owns its Details: a caller
// mutating the returned slice must not reach the error it was built from. Both kinds share the
// same qualifyConfigError call and therefore the same guarantee.
func TestSectionQualifyClonesDetails(t *testing.T) {
	for _, tt := range sectionEngineTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			orig := NewConnectionError(tt.sub.rootField, "dial refused", []string{"check the host"})
			require.NotEmpty(t, orig.Details)

			var qualified *ConfigError
			require.ErrorAs(t, tt.sub.qualify(orig), &qualified)
			qualified.Details[0] = "mutated"

			assert.NotEqual(t, "mutated", orig.Details[0])
		})
	}
}

// TestQualifyCacheConfigErrorForKeyAddressesResourceKey drives the EXPORTED door the app's
// runtime cache factory calls, so the key-to-section translation is pinned at the surface a
// consumer sees rather than only through checkTenantCache.
func TestQualifyCacheConfigErrorForKeyAddressesResourceKey(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		err        error
		wantField  string
		wantAction string
	}{
		{
			name:      "root_key_leaves_the_field_alone",
			key:       "",
			err:       NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host"),
			wantField: "cache.redis.host",
			wantAction: "set CACHE_REDIS_HOST env var or add cache.redis.host to " +
				"config.yaml",
		},
		{
			name:      "tenant_key_addresses_the_leaf",
			key:       "acme",
			err:       NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host"),
			wantField: "multitenant.tenants.acme.cache.redis.host",
			wantAction: "set MULTITENANT_TENANTS_ACME_CACHE_REDIS_HOST env var or add " +
				"multitenant.tenants.acme.cache.redis.host to config.yaml",
		},
		{
			name:      "bare_cache_field_names_the_section",
			key:       "acme",
			err:       NewValidationError(fieldCache, "configuration is nil"),
			wantField: "multitenant.tenants.acme.cache",
		},
		{
			name:      "a_field_outside_the_cache_head_stays_under_the_section",
			key:       "acme",
			err:       NewValidationError("redis.host", "nonsense"),
			wantField: "multitenant.tenants.acme.cache.redis.host",
		},
		{
			name:       "underscored_key_drops_the_env_half",
			key:        "acme_corp",
			err:        NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host"),
			wantField:  "multitenant.tenants.acme_corp.cache.redis.host",
			wantAction: "add multitenant.tenants.acme_corp.cache.redis.host to config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QualifyCacheConfigErrorForKey(tt.err, tt.key)

			var cfgErr *ConfigError
			require.ErrorAs(t, got, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			if tt.wantAction != "" {
				assert.Equal(t, tt.wantAction, cfgErr.Action)
			}
		})
	}
}

// TestQualifyCacheConfigErrorForKeyNilErrorIsNil pins the door's own nil guard: a nil error
// comes back nil regardless of key, so a caller need not check before qualifying. This is the
// door's own convenience rather than a generic engine guarantee — section.qualify is never
// invoked with a nil err by any other caller, so the guard lives here, not on the engine.
func TestQualifyCacheConfigErrorForKeyNilErrorIsNil(t *testing.T) {
	assert.Nil(t, QualifyCacheConfigErrorForKey(nil, "acme"))
	assert.Nil(t, QualifyCacheConfigErrorForKey(nil, ""))
}

// TestCheckTenantCacheAddressesTheTenant pins the STARTUP door's spelling across the extraction:
// it now delegates to the exported door, and this is what proves the move changed nothing.
func TestCheckTenantCacheAddressesTheTenant(t *testing.T) {
	err := checkTenantCache("acme", &CacheConfig{Enabled: true, Type: CacheTypeRedis})

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "multitenant.tenants.acme.cache.redis.host", cfgErr.Field)
	assert.Equal(t, "missing", cfgErr.Category)
}

// TestRequalifyActionRewritesOnlyItsOwnGeneratedHints is the guard half of the widening: the
// function now reads the key out of the hint rather than rebuilding it from Field, which is
// what lets the not-configured hint travel — and every untouched row here is what keeps that
// from becoming "rewrite any action that looks like a hint". The table already spans both
// kinds (a database hint sits beside the cache ones), which is what keeps this one test from
// needing a per-kind twin.
func TestRequalifyActionRewritesOnlyItsOwnGeneratedHints(t *testing.T) {
	const (
		orig      = "cache"
		qualified = "multitenant.tenants.acme.cache"
	)
	tests := []struct {
		name           string
		action         string
		origF          string
		qualF          string
		envUnreachable bool
		want           string
	}{
		{
			name:   "missing_field_hint_for_the_field_itself",
			action: "set CACHE_REDIS_HOST env var or add cache.redis.host to config.yaml",
			origF:  "cache.redis.host",
			qualF:  "multitenant.tenants.acme.cache.redis.host",
			want: "set MULTITENANT_TENANTS_ACME_CACHE_REDIS_HOST env var or add " +
				"multitenant.tenants.acme.cache.redis.host to config.yaml",
		},
		{
			name:   "not_configured_hint_names_a_key_under_the_field",
			action: "to enable: set CACHE_ENABLED env var or add cache.enabled to config.yaml",
			origF:  orig,
			qualF:  qualified,
			want: "to enable: set MULTITENANT_TENANTS_ACME_CACHE_ENABLED env var or add " +
				"multitenant.tenants.acme.cache.enabled to config.yaml",
		},
		{
			// The YAML-only hint has no "set X env var or " lead, so its "add " sits at index 0 —
			// the boundary yamlKeyFromAction reads the key from. A key that cannot round-trip to
			// an environment variable is how that form is generated in the first place.
			name:   "yaml_only_hint_is_repointed_too",
			action: "add cache.some_thing to config.yaml",
			origF:  orig,
			qualF:  qualified,
			want:   "add multitenant.tenants.acme.cache.some_thing to config.yaml",
		},
		{
			name:   "hint_naming_a_key_outside_the_field_is_untouched",
			action: "set DATABASE_HOST env var or add database.host to config.yaml",
			origF:  orig,
			qualF:  qualified,
			want:   "set DATABASE_HOST env var or add database.host to config.yaml",
		},
		{
			name:   "hand_written_action_is_untouched",
			action: "must be one of: redis",
			origF:  "cache.type",
			qualF:  "multitenant.tenants.acme.cache.type",
			want:   "must be one of: redis",
		},
		{
			name:   "prose_that_merely_ends_like_a_hint_is_untouched",
			action: "either all tenants must have cache.enabled or add cache.enabled to config.yaml",
			origF:  orig,
			qualF:  qualified,
			want:   "either all tenants must have cache.enabled or add cache.enabled to config.yaml",
		},
		{
			name:   "hint_whose_env_half_is_not_the_keys_own_is_untouched",
			action: "set SOMETHING_ELSE env var or add cache.enabled to config.yaml",
			origF:  orig,
			qualF:  qualified,
			want:   "set SOMETHING_ELSE env var or add cache.enabled to config.yaml",
		},
		{
			name:   "empty_action_stays_empty",
			action: "",
			origF:  orig,
			qualF:  qualified,
			want:   "",
		},
		{
			name:   "empty_orig_field_leaves_the_action_alone",
			action: "add cache.enabled to config.yaml",
			origF:  "",
			qualF:  qualified,
			want:   "add cache.enabled to config.yaml",
		},
		{
			// An env-unreachable section keeps the YAML path and drops the variable, even
			// through the not-configured lead-in — the lead is preserved, the env half is not.
			name:           "not_configured_hint_loses_its_env_half_when_env_is_unreachable",
			action:         "to enable: set CACHE_ENABLED env var or add cache.enabled to config.yaml",
			origF:          orig,
			qualF:          "multitenant.tenants.acme.corp.cache",
			envUnreachable: true,
			want:           "to enable: add multitenant.tenants.acme.corp.cache.enabled to config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, requalifyAction(tt.action, tt.origF, tt.qualF, tt.envUnreachable))
		})
	}
}

// dottedKeyTestCases names, per kind, a section built from a tenant id carrying a dot — the
// free-form spelling TenantStore.AddTenant accepts — beside the YAML path that id must produce
// verbatim. Every kind that splices a free-form key into its path owes the same suppression, so
// the guarantee is pinned once across all of them rather than once for cache.
func dottedKeyTestCases() []struct {
	name     string
	sub      section
	field    string
	envVar   string
	yamlPath string
	wantPath string
} {
	return []struct {
		name     string
		sub      section
		field    string
		envVar   string
		yamlPath string
		wantPath string
	}{
		{
			name:     "database",
			sub:      tenantDatabaseSection("acme.corp"),
			field:    "database.host",
			envVar:   "DATABASE_HOST",
			yamlPath: "database.host",
			wantPath: "multitenant.tenants.acme.corp.database.host",
		},
		{
			name:     "cache",
			sub:      tenantCacheSection("acme.corp"),
			field:    "cache.redis.host",
			envVar:   "CACHE_REDIS_HOST",
			yamlPath: "cache.redis.host",
			wantPath: "multitenant.tenants.acme.corp.cache.redis.host",
		},
	}
}

// TestSectionQualifySuppressesTheEnvHintForADottedKey pins the enforced half of the trap
// missingFieldAction documents: MULTITENANT_TENANTS_ACME_CORP_<...> unflattens to tenant "acme",
// sub-key "corp", so the engine emits the YAML-only hint for a section whose free-form key
// carries a dot. The YAML path keeps the id verbatim and is what the operator must edit.
func TestSectionQualifySuppressesTheEnvHintForADottedKey(t *testing.T) {
	for _, tt := range dottedKeyTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := NewMissingFieldError(tt.field, tt.envVar, tt.yamlPath)

			var qualified *ConfigError
			require.ErrorAs(t, tt.sub.qualify(err), &qualified)

			assert.True(t, tt.sub.envUnreachable)
			assert.Equal(t, tt.wantPath, qualified.Field)
			assert.Equal(t, "add "+tt.wantPath+" to config.yaml", qualified.Action)
			assert.NotContains(t, qualified.Action, "env var")
		})
	}
}

// TestSectionQualifyKeepsTheEnvHintForADotFreeKey is the other half of the same dimension: an
// ordinary tenant id still gets the variable, so the suppression above is attributable to the
// dot rather than to the qualification itself.
func TestSectionQualifyKeepsTheEnvHintForADotFreeKey(t *testing.T) {
	for _, tt := range sectionEngineTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			field := tt.sub.rootField + ".host"
			err := NewMissingFieldError(field, "X_HOST", field)

			var qualified *ConfigError
			require.ErrorAs(t, tt.sub.qualify(err), &qualified)

			assert.False(t, tt.sub.envUnreachable)
			assert.Contains(t, qualified.Action, "env var")
		})
	}
}

// TestNamedDatabaseSectionMarksADottedName covers the third free-form splice — databases.<name>
// — which reaches the same flattening trap through a different constructor.
func TestNamedDatabaseSectionMarksADottedName(t *testing.T) {
	assert.False(t, namedDatabaseSection("reporting").envUnreachable)
	assert.True(t, namedDatabaseSection("report.db").envUnreachable)
	assert.False(t, rootDatabaseSection().envUnreachable)
	assert.False(t, rootCacheSection().envUnreachable)
}
