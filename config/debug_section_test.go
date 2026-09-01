package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateDebugTrustedProxiesRejectsAllInvalid(t *testing.T) {
	cfg := &DebugConfig{TrustedProxies: []string{"garbage", "also-bad"}}
	assertValidationError(t, checkDebug(cfg), "debug.trustedproxies")
}

// TestValidateDebugTrustedProxiesWiredIntoValidate guards that the top-level Validate()
// actually invokes checkDebug — not just that checkDebug works in isolation — so a
// future refactor cannot silently drop the debug-config validation hook.
func TestValidateDebugTrustedProxiesWiredIntoValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Debug.TrustedProxies = []string{"bad-cidr"}
	err := Validate(cfg)
	assert.ErrorContains(t, err, "debug config:")
	assert.ErrorContains(t, err, "debug.trustedproxies")
}

func TestValidateDebugTrustedProxiesAcceptsValidCases(t *testing.T) {
	tests := []struct {
		name string
		cfg  *DebugConfig
	}{
		{name: "empty_is_valid", cfg: &DebugConfig{}},
		{name: "single_valid", cfg: &DebugConfig{TrustedProxies: []string{"10.0.0.0/8"}}},
		{name: "partial_invalid_keeps_valid", cfg: &DebugConfig{TrustedProxies: []string{"10.0.0.0/8", "bad"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, checkDebug(tt.cfg))
		})
	}
}
