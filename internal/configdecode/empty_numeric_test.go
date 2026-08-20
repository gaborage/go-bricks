package configdecode

import (
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmptyStringToNumericGuardHookFunc pins the guard's decision table: an empty or
// whitespace-only string targeting any numeric field is rejected, pointer targets
// included; every other source/target pair decodes as before.
func TestEmptyStringToNumericGuardHookFunc(t *testing.T) {
	type target struct {
		Count    int           `mapstructure:"count"`
		Ratio    float64       `mapstructure:"ratio"`
		Size     uint          `mapstructure:"size"`
		MinLen   *int          `mapstructure:"minlen"`
		Name     string        `mapstructure:"name"`
		Interval time.Duration `mapstructure:"interval"`
	}

	tests := []struct {
		name      string
		input     map[string]any
		wantErr   bool
		errSubstr string
		assert    func(t *testing.T, got target)
	}{
		{name: "empty_int_rejected", input: map[string]any{"count": ""}, wantErr: true, errSubstr: "delivered empty"},
		{name: "empty_float_rejected", input: map[string]any{"ratio": ""}, wantErr: true, errSubstr: "delivered empty"},
		{name: "empty_uint_rejected", input: map[string]any{"size": ""}, wantErr: true, errSubstr: "delivered empty"},
		{name: "empty_int_pointer_rejected", input: map[string]any{"minlen": ""}, wantErr: true, errSubstr: "delivered empty"},
		{name: "whitespace_int_rejected", input: map[string]any{"count": "   "}, wantErr: true, errSubstr: "delivered empty"},
		{
			name:   "empty_string_target_passes",
			input:  map[string]any{"name": ""},
			assert: func(t *testing.T, got target) { assert.Empty(t, got.Name) },
		},
		{
			name:   "explicit_zero_passes",
			input:  map[string]any{"count": "0"},
			assert: func(t *testing.T, got target) { assert.Equal(t, 0, got.Count) },
		},
		{
			name:   "explicit_value_passes",
			input:  map[string]any{"count": "7"},
			assert: func(t *testing.T, got target) { assert.Equal(t, 7, got.Count) },
		},
		{
			name:   "explicit_pointer_zero_passes",
			input:  map[string]any{"minlen": "0"},
			assert: func(t *testing.T, got target) { require.NotNil(t, got.MinLen); assert.Equal(t, 0, *got.MinLen) },
		},
		{
			name:   "numeric_source_passes",
			input:  map[string]any{"count": 5},
			assert: func(t *testing.T, got target) { assert.Equal(t, 5, got.Count) },
		},
		{
			name:   "duration_string_passes",
			input:  map[string]any{"interval": "5s"},
			assert: func(t *testing.T, got target) { assert.Equal(t, 5*time.Second, got.Interval) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out target
			dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
				DecodeHook: mapstructure.ComposeDecodeHookFunc(
					EmptyStringToNumericGuardHookFunc(),
					NumericToDurationGuardHookFunc(),
					mapstructure.StringToTimeDurationHookFunc(),
				),
				WeaklyTypedInput: true,
				Result:           &out,
			})
			require.NoError(t, err)

			err = dec.Decode(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errSubstr)
				return
			}
			require.NoError(t, err)
			tt.assert(t, out)
		})
	}
}

// TestEmptyStringToNumericGuardNamesTheField pins that the rejection reaches the operator
// with the field path attached — mapstructure wraps hook errors with the key it was
// decoding, which is the only place the key name exists at this seam.
func TestEmptyStringToNumericGuardNamesTheField(t *testing.T) {
	var out struct {
		SecretMinLength *int `mapstructure:"secretminlength"`
	}
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       EmptyStringToNumericGuardHookFunc(),
		WeaklyTypedInput: true,
		Result:           &out,
	})
	require.NoError(t, err)

	err = dec.Decode(map[string]any{"secretminlength": ""})

	require.Error(t, err)
	assert.ErrorContains(t, err, "secretminlength")
}
