package configdecode

import (
	"math"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNumericToDurationGuardHookFunc pins the guard's decision table directly: non-zero
// bare numerics and booleans targeting time.Duration are rejected; zeros (incl. -0.0),
// typed time.Duration sources, strings, and non-duration targets pass untouched.
func TestNumericToDurationGuardHookFunc(t *testing.T) {
	type target struct {
		Interval time.Duration `mapstructure:"interval"`
		Port     int           `mapstructure:"port"`
	}

	tests := []struct {
		name         string
		input        map[string]any
		wantErr      bool
		errSubstr    []string
		wantInterval time.Duration
		wantPort     int
	}{
		{name: "int_rejected", input: map[string]any{"interval": 300}, wantErr: true, errSubstr: []string{"unit-less numeric duration 300", "explicit unit"}},
		{name: "int64_rejected", input: map[string]any{"interval": int64(7)}, wantErr: true, errSubstr: []string{"unit-less numeric duration 7"}},
		{name: "uint_rejected", input: map[string]any{"interval": uint(9)}, wantErr: true, errSubstr: []string{"unit-less numeric duration 9"}},
		{name: "float_rejected", input: map[string]any{"interval": 2.5}, wantErr: true, errSubstr: []string{"unit-less numeric duration 2.5"}},
		{name: "negative_rejected", input: map[string]any{"interval": -5}, wantErr: true, errSubstr: []string{"unit-less numeric duration -5"}},
		{name: "bool_true_rejected", input: map[string]any{"interval": true}, wantErr: true, errSubstr: []string{"unit-less numeric duration true"}},
		{name: "bool_false_rejected", input: map[string]any{"interval": false}, wantErr: true, errSubstr: []string{"unit-less numeric duration false"}},
		{name: "zero_int_passes", input: map[string]any{"interval": 0}},
		{name: "zero_uint_passes", input: map[string]any{"interval": uint(0)}},
		{name: "zero_float_passes", input: map[string]any{"interval": 0.0}},
		{name: "negative_zero_float_passes", input: map[string]any{"interval": math.Copysign(0, -1)}},
		{name: "typed_duration_source_passes", input: map[string]any{"interval": 5 * time.Second}, wantInterval: 5 * time.Second},
		{name: "string_duration_passes", input: map[string]any{"interval": "300s"}, wantInterval: 300 * time.Second},
		{name: "non_duration_target_untouched", input: map[string]any{"port": 8080}, wantPort: 8080},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out target
			dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
				DecodeHook: mapstructure.ComposeDecodeHookFunc(
					NumericToDurationGuardHookFunc(),
					mapstructure.StringToTimeDurationHookFunc(),
				),
				WeaklyTypedInput: true,
				Result:           &out,
			})
			require.NoError(t, err)

			err = dec.Decode(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				for _, s := range tc.errSubstr {
					assert.ErrorContains(t, err, s)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantInterval, out.Interval)
			assert.Equal(t, tc.wantPort, out.Port)
		})
	}
}
