package configdecode

import (
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmptyStringToScalarGuardHookFunc pins the guard's decision table: an empty or
// whitespace-only string targeting any numeric or bool field is rejected, pointer targets
// included; every other source/target pair decodes as before.
func TestEmptyStringToScalarGuardHookFunc(t *testing.T) {
	type target struct {
		Count    int           `mapstructure:"count"`
		Ratio    float64       `mapstructure:"ratio"`
		Size     uint          `mapstructure:"size"`
		MinLen   *int          `mapstructure:"minlen"`
		Flag     bool          `mapstructure:"flag"`
		Critical *bool         `mapstructure:"critical"`
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
		{name: "empty_bool_rejected", input: map[string]any{"flag": ""}, wantErr: true, errSubstr: "boolean value delivered empty"},
		{name: "empty_bool_pointer_rejected", input: map[string]any{"critical": ""}, wantErr: true, errSubstr: "boolean value delivered empty"},
		{name: "whitespace_bool_rejected", input: map[string]any{"flag": "   "}, wantErr: true, errSubstr: "boolean value delivered empty"},
		{
			// A named string type has Kind String but is not a string, same as the
			// numeric case: without the reflect read it reaches the weak "" -> false.
			name:      "named_string_type_to_bool_rejected",
			input:     map[string]any{"flag": envName("")},
			wantErr:   true,
			errSubstr: "boolean value delivered empty",
		},
		{
			// A named string type has Kind String but is not a string: a concrete type
			// assertion drops it, and the weak conversion then makes it a zero.
			name:      "named_string_type_rejected",
			input:     map[string]any{"count": envName("")},
			wantErr:   true,
			errSubstr: "delivered empty",
		},
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
			name:   "explicit_bool_false_passes",
			input:  map[string]any{"flag": "false"},
			assert: func(t *testing.T, got target) { assert.False(t, got.Flag) },
		},
		{
			name:   "explicit_bool_true_passes",
			input:  map[string]any{"flag": "true"},
			assert: func(t *testing.T, got target) { assert.True(t, got.Flag) },
		},
		{
			name:   "explicit_bool_numeric_spelling_passes",
			input:  map[string]any{"flag": "1"},
			assert: func(t *testing.T, got target) { assert.True(t, got.Flag) },
		},
		{
			// The damaging shape ADR-077 closes: a non-nil *false reads as an operator
			// choice, so an explicit one must still reach the target unchanged.
			name:   "explicit_bool_pointer_false_passes",
			input:  map[string]any{"critical": "false"},
			assert: func(t *testing.T, got target) { require.NotNil(t, got.Critical); assert.False(t, *got.Critical) },
		},
		{
			name:   "bool_source_passes",
			input:  map[string]any{"flag": true},
			assert: func(t *testing.T, got target) { assert.True(t, got.Flag) },
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
					EmptyStringToScalarGuardHookFunc(),
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

// TestEmptyStringToScalarGuardNamesTheField pins that the rejection reaches the operator
// with the field path attached — mapstructure wraps hook errors with the key it was
// decoding, which is the only place the key name exists at this seam.
func TestEmptyStringToScalarGuardNamesTheField(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "numeric_key", input: map[string]any{"secretminlength": ""}, want: "secretminlength"},
		{name: "bool_key", input: map[string]any{"critical": ""}, want: "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out struct {
				SecretMinLength *int  `mapstructure:"secretminlength"`
				Critical        *bool `mapstructure:"critical"`
			}
			dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
				DecodeHook:       EmptyStringToScalarGuardHookFunc(),
				WeaklyTypedInput: true,
				Result:           &out,
			})
			require.NoError(t, err)

			err = dec.Decode(tt.input)

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

// envName is a named string type, the shape a consumer config field takes when it wants a
// domain type rather than a bare string.
type envName string
