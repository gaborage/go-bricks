package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestInjectIntoEnvStringRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		val := rapid.StringMatching(`[!-~]{1,64}`).Draw(rt, "val") // printable ASCII, no spaces
		os.Setenv("CUSTOM_PROP_VALUE", val)
		defer os.Unsetenv("CUSTOM_PROP_VALUE")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Value string `config:"custom.prop.value" default:"fallback"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if svc.Value != val {
			rt.Fatalf("env round-trip: got %q want %q", svc.Value, val)
		}
	})
}

// Plain regression test, not a property: struct tags are compile-time, so
// there is nothing meaningful to draw.
func TestInjectIntoDefaultAppliesWhenEnvAbsent(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var svc struct {
		Value string `config:"custom.prop.value" default:"placeholder"`
	}
	if err := cfg.InjectInto(&svc); err != nil {
		t.Fatalf("InjectInto: %v", err)
	}
	if svc.Value != "placeholder" {
		t.Fatalf("default not applied: got %q", svc.Value)
	}
}

func TestInjectIntoDurationRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		d := time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(rt, "dur"))
		os.Setenv("CUSTOM_PROP_TIMEOUT", d.String())
		defer os.Unsetenv("CUSTOM_PROP_TIMEOUT")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Timeout time.Duration `config:"custom.prop.timeout"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if svc.Timeout != d {
			rt.Fatalf("duration round-trip: got %v want %v", svc.Timeout, d)
		}
	})
}

func TestInjectIntoStringSliceRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		elems := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,8}`), 1, 5).Draw(rt, "elems")
		os.Setenv("CUSTOM_PROP_TAGS", strings.Join(elems, ","))
		defer os.Unsetenv("CUSTOM_PROP_TAGS")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Tags []string `config:"custom.prop.tags"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if len(svc.Tags) != len(elems) {
			rt.Fatalf("slice round-trip: got %v want %v", svc.Tags, elems)
		}
		for i := range elems {
			if svc.Tags[i] != elems[i] {
				rt.Fatalf("slice round-trip: got %v want %v", svc.Tags, elems)
			}
		}
	})
}

// TestInjectIntoNeverPanicsOnArbitraryValuesProperty guards the Load/InjectInto
// path against arbitrary environment input, including the control characters,
// unpaired-looking runes, and BOM/RTL-override code points rapid's default
// string generator deliberately seeds. A koanf/mapstructure decode error is
// acceptable behavior; a panic is the only failure this property looks for
// (rapid surfaces panics as test failures on its own, so nothing about the
// resulting value is asserted here).
func TestInjectIntoNeverPanicsOnArbitraryValuesProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		val := rapid.String().Draw(rt, "val")
		if strings.ContainsRune(val, 0) {
			return // os.Setenv rejects embedded NUL bytes; nothing to exercise
		}
		os.Setenv("CUSTOM_PROP_VALUE", val)
		defer os.Unsetenv("CUSTOM_PROP_VALUE")

		cfg, err := Load()
		if err != nil {
			return // a koanf/validation error on wild input is acceptable
		}
		var svc struct {
			Value string `config:"custom.prop.value"`
		}
		_ = cfg.InjectInto(&svc) // error acceptable; a panic is the failure
	})
}

// TestInjectIntoEnvBeatsYamlProperty pins the precedence Load's doc comment
// promises (env > yaml > defaults) for an arbitrary service-specific key: a
// yaml-set value must always lose to a differing env-set value at the same
// path. Uses the same t.TempDir+t.Chdir seam as TestLoadAppEnvSelectsEnvironmentOverlay
// (config_test.go), one fresh directory per rapid iteration.
func TestInjectIntoEnvBeatsYamlProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		yamlVal := rapid.StringMatching(`[a-zA-Z0-9]{1,32}`).Draw(rt, "yamlVal")
		envVal := rapid.StringMatching(`[!-~]{1,64}`).Draw(rt, "envVal")
		if yamlVal == envVal {
			return // degenerate draw: no observable precedence signal
		}

		dir := t.TempDir()
		t.Chdir(dir)
		yamlContent := "custom:\n  prop:\n    value: \"" + yamlVal + "\"\n"
		if err := os.WriteFile(testConfigFileYAML, []byte(yamlContent), 0o600); err != nil {
			rt.Fatalf("write config.yaml: %v", err)
		}

		os.Setenv("CUSTOM_PROP_VALUE", envVal)
		defer os.Unsetenv("CUSTOM_PROP_VALUE")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Value string `config:"custom.prop.value"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if svc.Value != envVal {
			rt.Fatalf("env-beats-yaml: got %q want %q (yaml value was %q)", svc.Value, envVal, yamlVal)
		}
	})
}
