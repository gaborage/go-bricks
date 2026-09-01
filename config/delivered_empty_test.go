package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadDeliveredEmptyFixture stages one TestValidateNoDeliveredEmptyDatabaseViaLoad
// case — a scratch working directory holding yaml (when non-empty) plus env — and
// returns Load()'s results from inside it. t.Cleanup rather than defer, so the
// environment is cleared at subtest end from within a helper.
func loadDeliveredEmptyFixture(t *testing.T, yaml string, env map[string]string) (*Config, error) {
	t.Helper()
	clearEnvironmentVariables()
	t.Cleanup(clearEnvironmentVariables)

	dir := t.TempDir()
	if yaml != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML), []byte(yaml), 0o600))
	}
	t.Chdir(dir)

	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

// assertDeliveredEmptyError checks a rendered startup error against one case's
// expectations. wantOrdered pins the sorted-path contract: each entry must appear
// strictly before the next.
func assertDeliveredEmptyError(t *testing.T, got string, wantContains, wantNotContain, wantOrdered []string) {
	t.Helper()
	for _, s := range wantContains {
		assert.Contains(t, got, s)
	}
	for _, s := range wantNotContain {
		assert.NotContains(t, got, s)
	}
	for i := 1; i < len(wantOrdered); i++ {
		prev, cur := strings.Index(got, wantOrdered[i-1]), strings.Index(got, wantOrdered[i])
		// Presence first: a missing entry indexes to -1, which is Less than any
		// present one, so order would be asserted against a phantom.
		require.GreaterOrEqual(t, prev, 0, "%s must appear in the message", wantOrdered[i-1])
		require.GreaterOrEqual(t, cur, 0, "%s must appear in the message", wantOrdered[i])
		assert.Less(t, prev, cur, "%s must be reported before %s", wantOrdered[i-1], wantOrdered[i])
	}
}

// TestLoadRejectsDeliveredEmptyAllowedIPs pins ADR-078. debug.allowedips defaults to the
// loopback pair, so an empty value does not relax a control — it REPLACES one with nothing,
// and with debug.bearertoken set ADR-049's registration gate is satisfied by the token alone
// and the IP whitelist is never installed. Every delivery shape that renders an empty string
// is rejected; the token's presence must not change the verdict, since the fail-open case is
// precisely the one where a token exists.
func TestLoadRejectsDeliveredEmptyAllowedIPs(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"
	const noToken = "debug:\n  enabled: true\n  allowedips: [\"10.0.0.0/8\"]\n"

	tests := []struct {
		name string
		yaml string
		env  map[string]string
	}{
		{name: "env_empty_with_token", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ""}},
		{name: "env_whitespace_with_token", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": "   "}},
		{name: "yaml_empty_string_with_token", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: \"\"\n"},
		// Without a token the deployment already failed, at registration (ADR-049). It now
		// fails EARLIER, at Load, which is the seam that can name the key.
		{name: "env_empty_without_token", yaml: header + noToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, "debug.allowedips", cfgErr.Field)
			assert.Contains(t, cfgErr.Message, "delivered empty")
			// Both ways out, so an operator who MEANT token-only is not stuck.
			assert.Contains(t, cfgErr.Action, "DEBUG_ALLOWEDIPS")
			assert.Contains(t, cfgErr.Action, "debug.allowedips: []")
		})
	}
}

// TestLoadAllowedIPsDeliberateShapesUnchanged pins the other half: the two shapes an operator
// writes on purpose still mean what they meant. The empty LIST is the sanctioned token-only
// downgrade of ADR-049 — the spelling no broken template can produce — and its survival is
// what makes rejecting the empty STRING a fix rather than a removal of choice.
func TestLoadAllowedIPsDeliberateShapesUnchanged(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const token = "  bearertoken: sekritsekritsekrit\n"

	t.Run("empty_list_is_the_deliberate_clear", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header+"debug:\n  enabled: true\n"+token+"  allowedips: []\n", nil)

		require.NoError(t, err, "an empty sequence is how ADR-049's token-only posture is written")
		assert.Empty(t, cfg.Debug.AllowedIPs)
	})

	t.Run("unset_keeps_the_loopback_default", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header, nil)

		require.NoError(t, err)
		assert.Equal(t, []string{"127.0.0.1", "::1"}, cfg.Debug.AllowedIPs)
	})

	t.Run("explicit_list_unchanged", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header+"debug:\n  enabled: true\n"+token+"  allowedips: [\"10.0.0.0/8\"]\n", nil)

		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.0/8"}, cfg.Debug.AllowedIPs)
	})
}

// TestDeliveredEmptyListCheckCoversOnlyAllowedIPs is the containment pin. The mechanism is
// list-driven so a future key joins by adding one name, which is exactly why the list needs a
// test that FAILS when a name is added silently: every other []string key must still accept a
// delivered-empty value, because clearing those tightens the posture (or fails elsewhere)
// rather than removing a control.
func TestDeliveredEmptyListCheckCoversOnlyAllowedIPs(t *testing.T) {
	assert.Equal(t, []string{"debug.allowedips"}, deliveredEmptyRejectingKeys,
		"adding a key here changes startup for every deployment that clears it — add its case below too")

	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"

	tests := []struct {
		name   string
		envVar string
		assert func(t *testing.T, cfg *Config)
	}{
		{
			// Fails CLOSED downstream: an empty allowlist restricts the scheduler API to
			// localhost, so clearing it is a tightening and must stay legal.
			name:   "scheduler_security_cidrallowlist",
			envVar: "SCHEDULER_SECURITY_CIDRALLOWLIST",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Scheduler.Security.CIDRAllowlist) },
		},
		{
			// Empty means "trust no proxy", the stricter reading.
			name:   "scheduler_security_trustedproxies",
			envVar: "SCHEDULER_SECURITY_TRUSTEDPROXIES",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Scheduler.Security.TrustedProxies) },
		},
		{
			name:   "debug_trustedproxies",
			envVar: "DEBUG_TRUSTEDPROXIES",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Debug.TrustedProxies) },
		},
		{
			// Empty here is empty: the field is REPLACED, not merged, and the
			// assertion below says so. Masking survives one seam later —
			// app_builder.go's resolveLoggerFilterConfig merges only when
			// len(SensitiveFields) > 0, so empty takes the same branch as absent
			// and hands the logger a nil FilterConfig, which
			// logger.NewSensitiveDataFilter substitutes DefaultFilterConfig for.
			// The shipped needles keep masking; only the custom extension is lost.
			name:   "log_sensitivefields",
			envVar: "LOG_SENSITIVEFIELDS",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Log.SensitiveFields) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, header, map[string]string{tt.envVar: ""})

			require.NoError(t, err, "%s clears safely; only debug.allowedips fails open", tt.envVar)
			tt.assert(t, cfg)
		})
	}

	// multitenant.resolver.order is the fifth sibling and needs its own shape: clearing it is
	// already fatal under a composite resolver (ADR-039). The property is that it keeps ITS
	// error — this check must not preempt a rejection that already names the right remedy.
	t.Run("multitenant_resolver_order_keeps_its_own_error", func(t *testing.T) {
		_, err := loadDeliveredEmptyFixture(t,
			header+"multitenant:\n  enabled: true\n  resolver:\n    type: composite\n",
			map[string]string{"MULTITENANT_RESOLVER_ORDER": ""})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "required when multitenant.resolver.type is 'composite'")
		assert.NotContains(t, err.Error(), "removes a control",
			"ADR-039 already rejects this with a better remedy; do not shadow it")
	})

	t.Run("multitenant_resolver_order_outside_composite_still_clears", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header, map[string]string{"MULTITENANT_RESOLVER_ORDER": ""})

		require.NoError(t, err)
		assert.Empty(t, cfg.Multitenant.Resolver.Order)
	})
}

// TestLoadRejectsAllowedIPsThatDecodeToNothing covers the shapes the first cut of ADR-078
// missed. The property is not "the string looks empty" but "the DECODER produces no entries",
// and those differ: splitAndTrimList drops empty parts, so a separator-only value trims
// non-empty yet yields nothing. A Helm `join ","` over unset values renders exactly that.
//
// YAML null is the second shape, and it is where this key parts company with ADR-074/077.
// There a null takes the default and is therefore absence; here it REPLACES the default, so
// the same spelling that is harmless for a numeric key removes a control for this one.
func TestLoadRejectsAllowedIPsThatDecodeToNothing(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"

	tests := []struct {
		name string
		yaml string
		env  map[string]string
	}{
		{name: "separator_only", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ","}},
		{name: "repeated_separators", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ",,,"}},
		{name: "separators_and_spaces", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": " , "}},
		{name: "yaml_bare_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips:\n"},
		{name: "yaml_explicit_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: null\n"},
		{name: "yaml_tilde_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: ~\n"},
		// Precedence: a real YAML list wiped by a null overlay is the same wipe.
		{name: "env_separator_over_real_yaml_list", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: [\"10.0.0.0/8\"]\n", env: map[string]string{"DEBUG_ALLOWEDIPS": ","}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr, "this delivery decodes to zero entries and must not boot")
			assert.Equal(t, "debug.allowedips", cfgErr.Field)
		})
	}
}

// TestLoadAllowedIPsEntryShapesStillBoot is the counterweight: a value that decodes to at
// least one entry is NOT this check's business, even when the entry is junk. Those install
// the middleware and fail closed at parse time (no networks parsed ⇒ deny), so rejecting them
// here would move a working fail-closed path into a startup abort for no security gain.
// NOTE (ADR-080): junk that trims to a non-empty string is now rejected at startup by
// validateIPOrCIDRList — see TestDebugAllowedIPsRejectsUnparseableEntries below. What
// survives here is the empty-ish entry, which this check skips and NewIPWhitelist drops
// with a WARN, so the rationale below still describes the cases it still covers.
func TestLoadAllowedIPsEntryShapesStillBoot(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{name: "list_of_empty_string", yaml: header + withToken + "  allowedips: [\"\"]\n", want: []string{""}},
		{name: "list_of_space", yaml: header + withToken + "  allowedips: [\" \"]\n", want: []string{" "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, tt.yaml, nil)

			require.NoError(t, err, "one junk entry still installs the whitelist, which then denies")
			assert.Equal(t, tt.want, cfg.Debug.AllowedIPs)
		})
	}
}
