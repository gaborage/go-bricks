package config

import (
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGetString(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		name:           "test-service",
		"custom.empty": "",
	})

	assert.Equal(t, "test-service", cfg.GetString(name))
	assert.Equal(t, "fallback", cfg.GetString(missing, "fallback"))
	assert.Equal(t, "", cfg.GetString("custom.empty"))
}

func TestGetNumericAndBool(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		port:          8080,
		retries:       "3",
		"custom.long": int64(42),
		threshold:     "0.75",
		enabled:       "true",
		invalidInt:    "oops",
	})

	assert.Equal(t, 8080, cfg.GetInt(port))
	assert.Equal(t, 3, cfg.GetInt(retries))
	assert.Equal(t, 7, cfg.GetInt(missing, 7))
	assert.Equal(t, 0, cfg.GetInt(invalidInt))

	assert.Equal(t, int64(42), cfg.GetInt64("custom.long"))
	assert.Equal(t, int64(5), cfg.GetInt64("custom.missing_long", 5))

	assert.InEpsilon(t, 0.75, cfg.GetFloat64(threshold), 0.001)
	assert.Equal(t, 1.5, cfg.GetFloat64("custom.missing_float", 1.5))

	assert.True(t, cfg.GetBool(enabled))
	assert.False(t, cfg.GetBool("custom.missing_bool"))
	assert.True(t, cfg.GetBool("custom.missing_bool", true))
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

	val, err := cfg.GetRequiredString(name)
	require.NoError(t, err)
	assert.Equal(t, "example", val)

	_, err = cfg.GetRequiredString(missing)
	assert.Error(t, err)

	vInt, err := cfg.GetRequiredInt(port)
	require.NoError(t, err)
	assert.Equal(t, 8080, vInt)

	_, err = cfg.GetRequiredInt(invalidInt)
	assert.Error(t, err)

	vInt64, err := cfg.GetRequiredInt64(retries)
	require.NoError(t, err)
	assert.Equal(t, int64(3), vInt64)

	vFloat, err := cfg.GetRequiredFloat64(threshold)
	require.NoError(t, err)
	assert.InEpsilon(t, 0.91, vFloat, 0.0001)

	vBool, err := cfg.GetRequiredBool(enabled)
	require.NoError(t, err)
	assert.True(t, vBool)
}

// ========================================
// NIL CONFIG AND UTILITY TESTS
// ========================================

func TestNilConfigAccessors(t *testing.T) {
	cfg := &Config{}

	assert.Equal(t, "fallback", cfg.GetString("any", "fallback"))
	assert.Equal(t, 0, cfg.GetInt("any"))
	assert.Equal(t, int64(0), cfg.GetInt64("any"))
	assert.Equal(t, 0.0, cfg.GetFloat64("any"))
	assert.False(t, cfg.GetBool("any"))

	_, err := cfg.GetRequiredInt("any")
	assert.Error(t, err)

	_, err = cfg.GetRequiredString("any")
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

	assert.Equal(t, 5, cfg.GetInt(port, 5))
	assert.Equal(t, int64(7), cfg.GetInt64("custom.int64", 7))
	assert.Equal(t, 9.9, cfg.GetFloat64("custom.float", 9.9))
	assert.True(t, cfg.GetBool("custom.bool", true))

	_, err := cfg.GetRequiredBool("custom.bool_invalid")
	assert.Error(t, err)
}

func TestRawRequiredValueErrors(t *testing.T) {
	cfg := &Config{}
	_, err := cfg.rawRequiredValue("missing")
	assert.Error(t, err)
}
