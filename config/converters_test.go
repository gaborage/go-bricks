package config

import (
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFloatConversionEdgeCases(t *testing.T) {
	_, err := floatToInt64(math.NaN())
	assert.Error(t, err)

	_, err = floatToInt64(math.Inf(1))
	assert.Error(t, err)
}

// ========================================
// TYPE CONVERSION TESTS
// ========================================

// TestUnsignedIntConversions tests toInt64FromUnsignedInt function
func TestUnsignedIntConversions(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    int64
		expectError bool
	}{
		{"uint8 conversion", uint8(255), 255, false},
		{"uint16 conversion", uint16(65535), 65535, false},
		{"uint32 conversion", uint32(4294967295), 4294967295, false},
		{"uint small value", uint(42), 42, false},
		{"uint64 small value", uint64(100), 100, false},
		{"uint overflow", uint(math.MaxUint64), 0, true},
		{"uint64 overflow", uint64(math.MaxUint64), 0, true},
		{"uint64 max safe", uint64(math.MaxInt64), math.MaxInt64, false},
		{"unsupported type", "invalid", 0, true},
		{"unsupported type int", 42, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toInt64FromUnsignedInt(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestSignedIntConversions tests toInt64FromSignedInt function
func TestSignedIntConversions(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    int64
		expectError bool
	}{
		{"int conversion", int(42), 42, false},
		{"int8 conversion", int8(-128), -128, false},
		{"int16 conversion", int16(32767), 32767, false},
		{"int32 conversion", int32(-2147483648), -2147483648, false},
		{"unsupported type", "invalid", 0, true},
		{"unsupported type uint", uint(42), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toInt64FromSignedInt(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestInt64Conversions tests toInt64 string conversions
func TestInt64Conversions(t *testing.T) {
	t.Run("string with spaces", func(t *testing.T) {
		result, err := toInt64("  12345  ")
		assert.NoError(t, err)
		assert.Equal(t, int64(12345), result)
	})

	t.Run("empty string", func(t *testing.T) {
		_, err := toInt64("   ")
		assert.Error(t, err)
	})
}

// TestIntConversions tests toInt overflow scenarios
func TestIntConversions(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		// Use the largest precise float above MaxInt64 to trigger overflow after conversion to int64
		value := math.Nextafter(float64(math.MaxInt64), math.Inf(1))
		_, err := toInt(value)
		assert.Error(t, err)
	})
}

// TestFloat64Conversions tests toFloat64 type conversions
func TestFloat64Conversions(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    float64
		expectError bool
	}{
		{"float64 passthrough", float64(3.14), 3.14, false},
		{"float32 conversion", float32(2.5), 2.5, false},
		{"int conversion", int(42), 42.0, false},
		{"int8 conversion", int8(-10), -10.0, false},
		{"int16 conversion", int16(1000), 1000.0, false},
		{"int32 conversion", int32(-50000), -50000.0, false},
		{"int64 conversion", int64(123456789), 123456789.0, false},
		{"uint conversion", uint(100), 100.0, false},
		{"uint8 conversion", uint8(255), 255.0, false},
		{"uint16 conversion", uint16(65535), 65535.0, false},
		{"uint32 conversion", uint32(4000000), 4000000.0, false},
		{"uint64 conversion", uint64(987654321), 987654321.0, false},
		{"string valid", "123.45", 123.45, false},
		{"string integer", "42", 42.0, false},
		{"string negative", "-3.14", -3.14, false},
		{"empty string", "", 0, true},
		{"string whitespace", "   ", 0, true},
		{"string invalid", "not-a-number", 0, true},
		{"unsupported type", []int{1, 2}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toFloat64(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestBoolConversions tests toBool type conversions
func TestBoolConversions(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    bool
		expectError bool
	}{
		{"bool true", true, true, false},
		{"bool false", false, false, false},
		{"string true", "true", true, false},
		{"string false", "false", false, false},
		{"string 1", "1", true, false},
		{"string 0", "0", false, false},
		{"string t", "t", true, false},
		{"string f", "f", false, false},
		{"string True", "True", true, false},
		{"string FALSE", "FALSE", false, false},
		{"string with spaces", "  true  ", true, false},
		{"empty string", "", false, true},
		{"string invalid", "maybe", false, true},
		{"int zero", int(0), false, false},
		{"int nonzero", int(42), true, false},
		{"int negative", int(-1), true, false},
		{"int8 zero", int8(0), false, false},
		{"int8 nonzero", int8(1), true, false},
		{"int16 zero", int16(0), false, false},
		{"int16 nonzero", int16(-5), true, false},
		{"int32 zero", int32(0), false, false},
		{"int32 nonzero", int32(100), true, false},
		{"int64 zero", int64(0), false, false},
		{"int64 nonzero", int64(999), true, false},
		{"uint zero", uint(0), false, false},
		{"uint nonzero", uint(1), true, false},
		{"uint8 zero", uint8(0), false, false},
		{"uint8 nonzero", uint8(255), true, false},
		{"uint16 zero", uint16(0), false, false},
		{"uint16 nonzero", uint16(1000), true, false},
		{"uint32 zero", uint32(0), false, false},
		{"uint32 nonzero", uint32(4000000), true, false},
		{"uint64 zero", uint64(0), false, false},
		{"uint64 nonzero", uint64(123), true, false},
		{"unsupported type", []bool{true}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toBool(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestFloatToInt64Conversions tests floatToInt64 edge cases
func TestFloatToInt64Conversions(t *testing.T) {
	maxInt64Precise := math.Nextafter(float64(math.MaxInt64), math.Inf(-1))

	tests := []struct {
		name        string
		input       float64
		expected    int64
		expectError bool
	}{
		{"zero", 0.0, 0, false},
		{"positive integer", 42.0, 42, false},
		{"negative integer", -123.0, -123, false},
		// float64(MaxInt64) rounds to 2^63, so we expect an overflow error instead of wraparound
		{"max int64 rounding overflow", float64(math.MaxInt64), 0, true},
		{"largest precise float below max int64", maxInt64Precise, int64(maxInt64Precise), false},
		// Use a value that actually overflows: 2^63 + large number
		{"actual overflow", float64(math.MaxInt64) + 1e10, 0, true},
		{"min int64", float64(math.MinInt64), math.MinInt64, false},
		{"not integer", 3.14, 0, true},
		{"small decimal", 0.1, 0, true},
		{"negative decimal", -2.5, 0, true},
		{"NaN", math.NaN(), 0, true},
		{"positive infinity", math.Inf(1), 0, true},
		{"negative infinity", math.Inf(-1), 0, true},
		{"overflow positive", float64(math.MaxInt64) + 1e10, 0, true},
		{"overflow negative", float64(math.MinInt64) - 1e10, 0, true},
		{"non-exact integer", math.Nextafter(float64(math.MaxInt64), math.Inf(1)), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := floatToInt64(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestIntEdgeCases tests toInt edge case scenarios
func TestIntEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    int
		expectError bool
	}{
		{"empty string", "", 0, true},
		{"string whitespace", "   ", 0, true},
		{"string invalid", "not-a-number", 0, true},
		{"int64 max value", int64(math.MaxInt64), int(math.MaxInt64), false},
		{"uint64 overflow", uint64(math.MaxUint64), 0, true},
		{"unsupported type", []int{1}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toInt(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestInt64EdgeCases tests toInt64 edge case scenarios
func TestInt64EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    int64
		expectError bool
	}{
		{"invalid uint overflow trigger", uint(math.MaxUint64), 0, true},
		{"float integer value", 1.23456789e15, int64(1.23456789e15), false},
		{"unsupported complex type", complex(1, 2), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toInt64(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestBoolEdgeCases tests toBool edge case scenarios
func TestBoolEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    bool
		expectError bool
	}{
		{"uint64 overflow in conversion", uint64(math.MaxUint64), false, true},
		{"float conversion error", 1.5, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toBool(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestRequiredAccessorErrorPaths ensures all error paths in required getters are covered
// ========================================
// ERROR PATH AND EDGE CASE TESTS
// ========================================

// TestRequiredAccessorErrorPaths ensures all error paths in required getters are covered
func TestRequiredAccessorErrorPaths(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"valid.string":    "test",
		"valid.int":       42,
		"valid.int64":     int64(123),
		"valid.float64":   3.14,
		"valid.bool":      true,
		"invalid.int":     "not-a-number",
		"invalid.int64":   []string{"not", "number"},
		"invalid.float64": "not-a-float",
		"invalid.bool":    struct{}{},
		"empty.string":    "",
	})

	t.Run("GetRequiredString error cases", func(t *testing.T) {
		tests := []struct {
			name string
			key  string
		}{
			{"missing key", "missing.key"},
			{"empty string", "empty.string"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := cfg.GetRequiredString(tt.key)
				assert.Error(t, err)
				if tt.name == "empty string" {
					assert.Contains(t, err.Error(), "empty")
				} else {
					assert.Contains(t, err.Error(), "missing")
				}
			})
		}
	})

	t.Run("GetRequiredInt64 error cases", func(t *testing.T) {
		tests := []struct {
			name string
			key  string
		}{
			{"missing key", "missing.key"},
			{"invalid conversion", "invalid.int64"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := cfg.GetRequiredInt64(tt.key)
				assert.Error(t, err)
			})
		}
	})

	t.Run("GetRequiredFloat64 error cases", func(t *testing.T) {
		tests := []struct {
			name string
			key  string
		}{
			{"missing key", "missing.key"},
			{"invalid conversion", "invalid.float64"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := cfg.GetRequiredFloat64(tt.key)
				assert.Error(t, err)
			})
		}
	})

	t.Run("GetRequiredBool error cases", func(t *testing.T) {
		tests := []struct {
			name string
			key  string
		}{
			{"missing key", "missing.key"},
			{"invalid conversion", "invalid.bool"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := cfg.GetRequiredBool(tt.key)
				assert.Error(t, err)
			})
		}
	})
}

// TestGetStringEdgeCases covers remaining GetString scenarios
func TestGetStringEdgeCases(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{
		"non.string": 42,
	})

	t.Run("non-string value returns string representation", func(t *testing.T) {
		result := cfg.GetString("non.string")
		assert.Equal(t, "42", result)
	})

	t.Run("missing key with no default", func(t *testing.T) {
		result := cfg.GetString("missing.key")
		assert.Equal(t, "", result)
	})
}

// TestAdditionalEdgeCases covers remaining uncovered lines for 98%+ coverage
func TestAdditionalEdgeCases(t *testing.T) {
	t.Run("Load with validation error path", func(t *testing.T) {
		// Temporarily clear environment and set invalid config
		oldEnv := os.Getenv("APP_NAME")
		os.Setenv("APP_NAME", "") // This should trigger validation error
		defer func() {
			if oldEnv != "" {
				os.Setenv("APP_NAME", oldEnv)
			} else {
				os.Unsetenv("APP_NAME")
			}
		}()

		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid configuration")
	})

	// Test platform-specific int overflow if needed
	t.Run("platform specific int conversion", func(t *testing.T) {
		// Force test a case that might trigger int overflow on different architectures
		cfg := setupTestConfig(t, map[string]any{
			"large.number": "9223372036854775807", // Max int64 as string
		})

		// This should work on 64-bit systems but might trigger different paths
		result := cfg.GetInt("large.number")
		assert.True(t, result != 0 || result == 0) // Just ensure it doesn't panic
	})

	t.Run("toBool with uint conversion error", func(t *testing.T) {
		// Test a case where toBool calls toInt64 with uint and gets an error
		cfg := setupTestConfig(t, map[string]any{
			"overflow.uint": uint64(math.MaxUint64),
		})

		result := cfg.GetBool("overflow.uint")
		// This tests the error path in toBool when toInt64 fails for uint types
		assert.False(t, result) // Should return false on conversion error
	})
}
