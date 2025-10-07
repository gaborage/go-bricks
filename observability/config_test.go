package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	testServiceName = "test-service"
)

func TestConfig_Validate_NilConfig(t *testing.T) {
	var nilConfig *Config
	err := nilConfig.Validate()
	assert.ErrorIs(t, err, ErrNilConfig)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "valid config",
			config: Config{
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					SampleRate: 0.5,
				},
			},
			wantErr: nil,
		},
		{
			name: "disabled config does not validate",
			config: Config{
				Enabled: false,
			},
			wantErr: nil,
		},
		{
			name: "missing service name",
			config: Config{
				Enabled:     true,
				ServiceName: "",
			},
			wantErr: ErrMissingServiceName,
		},
		{
			name: "invalid sample rate - negative",
			config: Config{
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					SampleRate: -0.1,
				},
			},
			wantErr: ErrInvalidSampleRate,
		},
		{
			name: "invalid sample rate - too high",
			config: Config{
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					SampleRate: 1.1,
				},
			},
			wantErr: ErrInvalidSampleRate,
		},
		{
			name: "sample rate at boundary - 0.0",
			config: Config{
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					SampleRate: 0.0,
				},
			},
			wantErr: nil,
		},
		{
			name: "sample rate at boundary - 1.0",
			config: Config{
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					SampleRate: 1.0,
				},
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
