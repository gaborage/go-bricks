package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/observability"
)

const (
	name       = "custom.name"
	port       = "custom.port"
	retries    = "custom.retries"
	threshold  = "custom.threshold"
	enabled    = "custom.enabled"
	invalidInt = "custom.invalid_int"
	missing    = "custom.missing"

	// Test data constants
	customOne = "custom.one"
)

func setupTestConfig(t *testing.T, data map[string]any) *Config {
	t.Helper()

	k := koanf.New(".")
	err := k.Load(confmap.Provider(data, "."), nil)
	require.NoError(t, err)

	return &Config{k: k}
}

// ========================================
// BASIC ACCESSOR TESTS
// ========================================

func TestString(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		name:           "test-service",
		"custom.empty": "",
	})

	assert.Equal(t, "test-service", cfg.String(name))
	assert.Equal(t, "fallback", cfg.String(missing, "fallback"))
	assert.Equal(t, "", cfg.String("custom.empty"))
}

func TestNumericAndBool(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		port:          8080,
		retries:       "3",
		"custom.long": int64(42),
		threshold:     "0.75",
		enabled:       "true",
		invalidInt:    "oops",
	})

	assert.Equal(t, 8080, cfg.Int(port))
	assert.Equal(t, 3, cfg.Int(retries))
	assert.Equal(t, 7, cfg.Int(missing, 7))
	assert.Equal(t, 0, cfg.Int(invalidInt))

	assert.Equal(t, int64(42), cfg.Int64("custom.long"))
	assert.Equal(t, int64(5), cfg.Int64("custom.missing_long", 5))

	assert.InEpsilon(t, 0.75, cfg.Float64(threshold), 0.001)
	assert.Equal(t, 1.5, cfg.Float64("custom.missing_float", 1.5))

	assert.True(t, cfg.Bool(enabled))
	assert.False(t, cfg.Bool("custom.missing_bool"))
	assert.True(t, cfg.Bool("custom.missing_bool", true))
}

// ========================================
// REQUIRED ACCESSOR TESTS
// ========================================

func TestRequiredAccessors(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		port:       "8080",
		retries:    3,
		threshold:  "0.91",
		enabled:    true,
		name:       "example",
		invalidInt: "oops",
	})

	val, err := cfg.RequiredString(name)
	require.NoError(t, err)
	assert.Equal(t, "example", val)

	_, err = cfg.RequiredString(missing)
	assert.Error(t, err)

	vInt, err := cfg.RequiredInt(port)
	require.NoError(t, err)
	assert.Equal(t, 8080, vInt)

	_, err = cfg.RequiredInt(invalidInt)
	assert.Error(t, err)

	vInt64, err := cfg.RequiredInt64(retries)
	require.NoError(t, err)
	assert.Equal(t, int64(3), vInt64)

	vFloat, err := cfg.RequiredFloat64(threshold)
	require.NoError(t, err)
	assert.InEpsilon(t, 0.91, vFloat, 0.0001)

	vBool, err := cfg.RequiredBool(enabled)
	require.NoError(t, err)
	assert.True(t, vBool)
}

// ========================================
// NIL CONFIG AND UTILITY TESTS
// ========================================

func TestNilConfigAccessors(t *testing.T) {
	cfg := &Config{}

	assert.Equal(t, "fallback", cfg.String("any", "fallback"))
	assert.Equal(t, 0, cfg.Int("any"))
	assert.Equal(t, int64(0), cfg.Int64("any"))
	assert.Equal(t, 0.0, cfg.Float64("any"))
	assert.False(t, cfg.Bool("any"))

	_, err := cfg.RequiredInt("any")
	assert.Error(t, err)

	_, err = cfg.RequiredString("any")
	assert.Error(t, err)

	err = cfg.Unmarshal("custom", &struct{}{})
	assert.Error(t, err)

	assert.False(t, cfg.Exists("any"))
	assert.Nil(t, cfg.All())
	assert.Nil(t, cfg.Custom())
}

func TestUnmarshalAndCustom(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"custom.service.endpoint": "https://api.example.com",
		"custom.service.timeout":  "30s",
		"custom.tags":             []any{"alpha", "beta"},
		"custom.meta": map[string]any{
			"owner": "team-platform",
		},
	})

	// Unmarshal a subset
	var target struct {
		Service struct {
			Endpoint string `koanf:"endpoint"`
		} `koanf:"service"`
	}

	err := cfg.Unmarshal("custom", &target)
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com", target.Service.Endpoint)

	custom := cfg.Custom()
	require.NotNil(t, custom)
	assert.Contains(t, custom, "service")
}

func TestAllAndExists(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		customOne:    1,
		"custom.two": 2,
	})

	all := cfg.All()
	require.NotNil(t, all)
	assert.Equal(t, 1, all[customOne])

	assert.True(t, cfg.Exists(customOne))
	assert.False(t, cfg.Exists("custom.three"))
}

func TestCustomHandlesNonMap(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"custom": "not-a-map",
	})

	assert.Nil(t, cfg.Custom())
}

func TestInvalidTypesReturnDefaults(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		port:                  []string{"not", "number"},
		"custom.float":        []int{1, 2},
		"custom.bool":         []bool{true},
		"custom.int64":        []int{1},
		"custom.float64":      []string{"bad"},
		"custom.bool_invalid": struct{}{},
	})

	assert.Equal(t, 5, cfg.Int(port, 5))
	assert.Equal(t, int64(7), cfg.Int64("custom.int64", 7))
	assert.Equal(t, 9.9, cfg.Float64("custom.float", 9.9))
	assert.True(t, cfg.Bool("custom.bool", true))

	_, err := cfg.RequiredBool("custom.bool_invalid")
	assert.Error(t, err)
}

func TestRawRequiredValueErrors(t *testing.T) {
	cfg := &Config{}
	_, err := cfg.rawRequiredValue("missing")
	assert.Error(t, err)
}

// TestUnmarshalRejectsUnitlessNumericDuration proves Config.Unmarshal routes through the guard
// decoder chain: observability.Config decodes via field-name fallback under TagName "koanf".
func TestUnmarshalRejectsUnitlessNumericDuration(t *testing.T) {
	t.Run("bare_numeric_rejected", func(t *testing.T) {
		cfg := setupTestConfig(t, map[string]any{
			"observability.trace.export.timeout": 30,
		})
		var obs observability.Config
		err := cfg.Unmarshal("observability", &obs)
		require.Error(t, err)
		assert.ErrorContains(t, err, "unit-less numeric duration 30")
	})

	t.Run("string_duration_binds", func(t *testing.T) {
		cfg := setupTestConfig(t, map[string]any{
			"observability.trace.export.timeout": "30s",
		})
		var obs observability.Config
		require.NoError(t, cfg.Unmarshal("observability", &obs))
		assert.Equal(t, 30*time.Second, obs.Trace.Export.Timeout)
	})
}

// TestUnmarshalRejectsDeliveredEmptyBool pins ADR-077 on the public Config.Unmarshal seam,
// the one a consumer's own config struct reaches: an empty string bound to a bool fails
// there too, so a consumer gets the rule without opting in.
func TestUnmarshalRejectsDeliveredEmptyBool(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"custom.strict":  "",
		"custom.verbose": "true",
	})
	var out struct {
		Strict  *bool `koanf:"strict"`
		Verbose bool  `koanf:"verbose"`
	}

	err := cfg.Unmarshal("custom", &out)

	require.Error(t, err)
	assert.ErrorContains(t, err, "boolean value delivered empty")
	assert.ErrorContains(t, err, "strict", "the koanf key reaches the operator, not just the message")
}

// TestUnmarshalStringToSliceKeepsSingleElementWrap pins the public-seam behavior of
// Config.Unmarshal: a scalar string bound to a []string field keeps koanf's default
// single-element wrap ("a,b,c" -> ["a,b,c"]) and is NOT comma-split. Load's env-path
// comma-split (buildDecoderConfig's slice hook) is a separate seam and unaffected.
func TestUnmarshalStringToSliceKeepsSingleElementWrap(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"custom.tags": "a,b,c",
	})
	var out struct {
		Tags []string `koanf:"tags"`
	}
	require.NoError(t, cfg.Unmarshal("custom", &out))
	require.Len(t, out.Tags, 1)
	assert.Equal(t, "a,b,c", out.Tags[0])
}

// ========================================
// UNUSABLE-KEY WARNING TESTS
// ========================================

// captureWarns returns the JSON log lines the getters wrote while fn ran. The
// framework logger binds stdout at construction, and warnUnusable builds it
// inside the call, so the redirect has to wrap the getter itself.
//
// Every key a case touches is released from the once-per-key sentinel afterwards:
// that sentinel is process-wide, so a later test naming the same key would
// otherwise see silence.
func captureWarns(t *testing.T, keys []string, fn func()) []map[string]any {
	t.Helper()

	t.Cleanup(func() {
		for _, key := range keys {
			warnedKeys.Delete(key)
		}
	})

	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "log line is not JSON: %s", line)
		lines = append(lines, entry)
	}
	return lines
}

func TestLenientGettersWarnOnUnusableValue(t *testing.T) {
	tests := []struct {
		name      string
		rawValue  string
		wantClass string
	}{
		{name: "present_but_empty", rawValue: "", wantClass: classEmpty},
		{name: "present_but_unparseable", rawValue: "abc", wantClass: classUnparseable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// One key per getter, so the once-per-key sentinel cannot hide a getter
			// that failed to warn.
			intKey := "custom.warn_int_" + tc.name
			int64Key := "custom.warn_int64_" + tc.name
			floatKey := "custom.warn_float_" + tc.name
			boolKey := "custom.warn_bool_" + tc.name

			cfg := setupTestConfig(t, map[string]any{
				intKey:   tc.rawValue,
				int64Key: tc.rawValue,
				floatKey: tc.rawValue,
				boolKey:  tc.rawValue,
			})

			warns := captureWarns(t, []string{intKey, int64Key, floatKey, boolKey}, func() {
				// The caller's default comes back on every one of them.
				assert.Equal(t, 7, cfg.Int(intKey, 7))
				assert.Equal(t, int64(9), cfg.Int64(int64Key, 9))
				assert.InDelta(t, 1.5, cfg.Float64(floatKey, 1.5), 0)
				assert.True(t, cfg.Bool(boolKey, true))
			})

			require.Len(t, warns, 4, "one warning per unusable key")
			byType := map[string]map[string]any{}
			for _, w := range warns {
				byType[w["type"].(string)] = w
			}

			for wantType, wantKey := range map[string]string{
				"int":     intKey,
				"int64":   int64Key,
				"float64": floatKey,
				"bool":    boolKey,
			} {
				warn, ok := byType[wantType]
				require.True(t, ok, "a warning names type %s", wantType)
				assert.Equal(t, wantKey, warn["key"])
				assert.Equal(t, tc.wantClass, warn["class"])
				assert.Equal(t, "warn", warn["level"])
			}
		})
	}
}

// TestLenientGetterZeroValueWithoutDefault pins the other default shape: with no
// default argument the getter returns the zero value, and still warns.
func TestLenientGetterZeroValueWithoutDefault(t *testing.T) {
	key := "custom.warn_zero_value"
	cfg := setupTestConfig(t, map[string]any{key: ""})

	warns := captureWarns(t, []string{key}, func() {
		assert.Equal(t, 0, cfg.Int(key))
	})

	require.Len(t, warns, 1)
	assert.Equal(t, classEmpty, warns[0]["class"])
}

// TestLenientGetterWarnOmitsRawValue pins the security property: strconv quotes
// its input in the error it returns, so a warning built from err.Error() would
// carry the value a config key was hiding. No field and no message may contain it.
func TestLenientGetterWarnOmitsRawValue(t *testing.T) {
	const secretish = "sup3rs3cret-value"
	key := "custom.warn_raw_value"

	cfg := setupTestConfig(t, map[string]any{key: secretish})

	warns := captureWarns(t, []string{key}, func() {
		assert.Equal(t, 0, cfg.Int(key))
	})

	require.Len(t, warns, 1)
	for field, value := range warns[0] {
		assert.NotContains(t, fmt.Sprint(value), secretish, "field %s carries the raw value", field)
	}
	assert.Equal(t, classUnparseable, warns[0]["class"])
}

// TestLenientGetterWarnsOncePerKey is also what keeps logger construction off the
// hot path: the warning, and the logger built to carry it, happen once per key.
func TestLenientGetterWarnsOncePerKey(t *testing.T) {
	firstKey := "custom.warn_once_first"
	secondKey := "custom.warn_once_second"

	cfg := setupTestConfig(t, map[string]any{firstKey: "", secondKey: ""})

	warns := captureWarns(t, []string{firstKey, secondKey}, func() {
		cfg.Int(firstKey)
		cfg.Int(firstKey)
		cfg.Int(firstKey)
	})
	require.Len(t, warns, 1, "three reads of one unusable key warn once")
	assert.Equal(t, firstKey, warns[0]["key"])

	// A different key gets its own line: the sentinel is per key, not a global latch.
	more := captureWarns(t, []string{secondKey}, func() {
		cfg.Int(secondKey)
	})
	require.Len(t, more, 1)
	assert.Equal(t, secondKey, more[0]["key"])
}

// TestLenientGetterUsableKeysAreSilent guards the other half of the contract: a
// key that is absent or converts cleanly produces no log line at all.
func TestLenientGetterUsableKeysAreSilent(t *testing.T) {
	presentKey := "custom.silent_present"
	absentKey := "custom.silent_absent"

	cfg := setupTestConfig(t, map[string]any{presentKey: "42"})

	warns := captureWarns(t, []string{presentKey, absentKey}, func() {
		assert.Equal(t, 42, cfg.Int(presentKey))
		assert.Equal(t, 5, cfg.Int(absentKey, 5))
	})
	assert.Empty(t, warns, "a usable value and an absent key are both silent")
}

// TestLenientGetterWarnsUnderUnusableLogLevel covers the config that cannot
// configure its own reporting: an empty or unparseable log.level must not swallow
// the warning — logger.New degrades to info, which still emits a warn.
func TestLenientGetterWarnsUnderUnusableLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		level string
	}{
		{name: "empty_level", level: ""},
		{name: "unparseable_level", level: "shout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := "custom.warn_level_" + tc.name
			cfg := setupTestConfig(t, map[string]any{key: ""})
			cfg.Log.Level = tc.level

			warns := captureWarns(t, []string{key}, func() {
				assert.Equal(t, 4, cfg.Int(key, 4))
			})

			require.Len(t, warns, 1, "the warning renders whatever log.level says")
			assert.Equal(t, key, warns[0]["key"])
			assert.Equal(t, classEmpty, warns[0]["class"])
		})
	}
}

// TestLenientGetterHonorsConfigLogSection pins what the config-local logger buys:
// a hand-built Config's own log settings decide the level the warning is written
// at, with no Load() call and no framework wiring.
func TestLenientGetterHonorsConfigLogSection(t *testing.T) {
	key := "custom.warn_honors_log_section"
	cfg := setupTestConfig(t, map[string]any{key: ""})
	cfg.Log.Level = logger.LevelError

	warns := captureWarns(t, []string{key}, func() {
		assert.Equal(t, 2, cfg.Int(key, 2))
	})

	assert.Empty(t, warns, "a config that asked for error-level logging gets no warn line")
}
