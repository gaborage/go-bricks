package observability

import (
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testServiceName    = "test-service"
	testTraceEndpointA = "localhost:4318"
	testTraceEndpointB = "localhost:4317"
)

func TestConfigValidateNilConfig(t *testing.T) {
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
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Sample: SampleConfig{
						Rate: 0.5,
					},
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
				Enabled: true,
				Service: ServiceConfig{
					Name: "",
				},
			},
			wantErr: ErrMissingServiceName,
		},
		{
			name: "invalid sample rate - negative",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Sample: SampleConfig{
						Rate: -0.1,
					},
				},
			},
			wantErr: ErrInvalidSampleRate,
		},
		{
			name: "invalid sample rate - too high",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Sample: SampleConfig{
						Rate: 1.1,
					},
				},
			},
			wantErr: ErrInvalidSampleRate,
		},
		{
			name: "sample rate at boundary - 0.0",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Sample: SampleConfig{
						Rate: 0.0,
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "sample rate at boundary - 1.0",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Sample: SampleConfig{
						Rate: 1.0,
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "valid OTLP HTTP protocol",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Endpoint: testTraceEndpointA,
					Protocol: "http",
					Sample: SampleConfig{
						Rate: 1.0,
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "valid OTLP gRPC protocol",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Endpoint: testTraceEndpointB,
					Protocol: "grpc",
					Sample: SampleConfig{
						Rate: 1.0,
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics invalid protocol",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Enabled: BoolPtr(false),
				},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: testTraceEndpointA,
					Protocol: "websocket",
				},
			},
			wantErr: ErrInvalidProtocol,
		},
		{
			name: "metrics protocol inherits trace protocol",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Enabled:  BoolPtr(true),
					Endpoint: testTraceEndpointB,
					Protocol: ProtocolGRPC,
				},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: testTraceEndpointB,
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics protocol defaults to http when trace disabled",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Enabled: BoolPtr(false),
				},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: testTraceEndpointA,
				},
			},
			wantErr: nil,
		},
		{
			name: "invalid OTLP protocol",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Endpoint: testTraceEndpointA,
					Protocol: "websocket",
					Sample: SampleConfig{
						Rate: 1.0,
					},
				},
			},
			wantErr: ErrInvalidProtocol,
		},
		{
			name: "stdout endpoint ignores protocol validation",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Endpoint: "stdout",
					Protocol: "invalid",
					Sample: SampleConfig{
						Rate: 1.0,
					},
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

// TestConfigUnmarshalFromYAML reproduces the bug where observability config
// cannot be loaded from YAML due to InjectInto not supporting nested structs.
// This test validates that switching to Unmarshal with mapstructure tags fixes the issue.
func TestConfigUnmarshalFromYAML(t *testing.T) {
	// This test validates that mapstructure can properly unmarshal nested config structs
	// We test this directly using koanf's YAML provider rather than the full config.Load()
	// which requires a complete application config file.

	yamlContent := `
enabled: true
service:
  name: "test-service"
  version: "1.0.0"
environment: "development"
trace:
  enabled: true
  endpoint: "stdout"
  protocol: "http"
  insecure: true
  sample:
    rate: 0.5
  batch:
    timeout: 10s
    size: 256
  export:
    timeout: 20s
  max:
    queue:
      size: 1024
    batch:
      size: 128
metrics:
  enabled: true
  endpoint: "stdout"
  interval: 15s
  export:
    timeout: 25s
`

	// Use koanf directly to test unmarshaling
	k := koanf.New(".")
	err := k.Load(rawbytes.Provider([]byte(yamlContent)), yaml.Parser())
	require.NoError(t, err)

	// Try to unmarshal observability config
	var obsCfg Config
	err = k.Unmarshal("", &obsCfg)
	require.NoError(t, err, "Failed to unmarshal observability config from YAML")

	// Verify top-level fields
	assert.True(t, obsCfg.Enabled, "Enabled should be true")
	assert.Equal(t, "development", obsCfg.Environment, "Environment should be 'development'")

	// Verify nested service config
	assert.Equal(t, "test-service", obsCfg.Service.Name, "Service name should be loaded")
	assert.Equal(t, "1.0.0", obsCfg.Service.Version, "Service version should be loaded")

	// Verify nested trace config
	require.NotNil(t, obsCfg.Trace.Enabled, "Trace enabled should not be nil")
	assert.True(t, *obsCfg.Trace.Enabled, "Trace enabled should be true")
	assert.Equal(t, "stdout", obsCfg.Trace.Endpoint, "Trace endpoint should be 'stdout'")
	assert.Equal(t, "http", obsCfg.Trace.Protocol, "Trace protocol should be 'http'")
	assert.True(t, obsCfg.Trace.Insecure, "Trace insecure should be true")

	// Verify deeply nested trace sample config
	assert.Equal(t, 0.5, obsCfg.Trace.Sample.Rate, "Sample rate should be 0.5")

	// Verify deeply nested trace batch config
	assert.Equal(t, 10*time.Second, obsCfg.Trace.Batch.Timeout, "Batch timeout should be 10s")
	assert.Equal(t, 256, obsCfg.Trace.Batch.Size, "Batch size should be 256")

	// Verify deeply nested trace export config
	assert.Equal(t, 20*time.Second, obsCfg.Trace.Export.Timeout, "Export timeout should be 20s")

	// Verify deeply nested trace max config
	assert.Equal(t, 1024, obsCfg.Trace.Max.Queue.Size, "Max queue size should be 1024")
	assert.Equal(t, 128, obsCfg.Trace.Max.Batch.Size, "Max batch size should be 128")

	// Verify nested metrics config
	require.NotNil(t, obsCfg.Metrics.Enabled, "Metrics enabled should not be nil")
	assert.True(t, *obsCfg.Metrics.Enabled, "Metrics enabled should be true")
	assert.Equal(t, "stdout", obsCfg.Metrics.Endpoint, "Metrics endpoint should be 'stdout'")
	assert.Equal(t, 15*time.Second, obsCfg.Metrics.Interval, "Metrics interval should be 15s")
	assert.Equal(t, 25*time.Second, obsCfg.Metrics.Export.Timeout, "Metrics export timeout should be 25s")
}

// TestConfigApplyDefaults validates that default values are correctly applied
// to config fields that are not specified in the YAML file.
func TestConfigApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected Config
	}{
		{
			name: "empty config gets all defaults",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: "test",
				},
			},
			expected: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name:    "test",
					Version: "unknown",
				},
				Environment: "development",
				Trace: TraceConfig{
					Enabled:  BoolPtr(true),
					Endpoint: EndpointStdout,
					Protocol: ProtocolHTTP,
					Insecure: true,
					Sample: SampleConfig{
						Rate: 1.0,
					},
					Batch: BatchConfig{
						Timeout: 5 * time.Second,
						Size:    512,
					},
					Export: ExportConfig{
						Timeout: 30 * time.Second,
					},
					Max: MaxConfig{
						Queue: QueueConfig{
							Size: 2048,
						},
						Batch: MaxBatchConfig{
							Size: 512,
						},
					},
				},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: EndpointStdout,
					Interval: 10 * time.Second,
					Export: MetricsExportConfig{
						Timeout: 30 * time.Second,
					},
				},
			},
		},
		{
			name: "partial config preserves values and fills defaults",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name:    "custom-service",
					Version: "2.0.0",
				},
				Trace: TraceConfig{
					Endpoint: "http://custom:4318",
					Sample: SampleConfig{
						Rate: 0.1,
					},
				},
			},
			expected: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name:    "custom-service",
					Version: "2.0.0",
				},
				Environment: "development",
				Trace: TraceConfig{
					Enabled:  BoolPtr(true),
					Endpoint: "http://custom:4318",
					Protocol: ProtocolHTTP,
					Insecure: false, // Non-stdout endpoints default to false (secure)
					Sample: SampleConfig{
						Rate: 0.1,
					},
					Batch: BatchConfig{
						Timeout: 5 * time.Second,
						Size:    512,
					},
					Export: ExportConfig{
						Timeout: 30 * time.Second,
					},
					Max: MaxConfig{
						Queue: QueueConfig{
							Size: 2048,
						},
						Batch: MaxBatchConfig{
							Size: 512,
						},
					},
				},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: EndpointStdout,
					Interval: 10 * time.Second,
					Export: MetricsExportConfig{
						Timeout: 30 * time.Second,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			cfg.ApplyDefaults()

			assert.Equal(t, tt.expected.Service.Version, cfg.Service.Version)
			assert.Equal(t, tt.expected.Environment, cfg.Environment)

			// Compare pointer values
			if tt.expected.Trace.Enabled != nil {
				require.NotNil(t, cfg.Trace.Enabled)
				assert.Equal(t, *tt.expected.Trace.Enabled, *cfg.Trace.Enabled)
			} else {
				assert.Nil(t, cfg.Trace.Enabled)
			}

			assert.Equal(t, tt.expected.Trace.Endpoint, cfg.Trace.Endpoint)
			assert.Equal(t, tt.expected.Trace.Protocol, cfg.Trace.Protocol)
			assert.Equal(t, tt.expected.Trace.Insecure, cfg.Trace.Insecure)
			assert.Equal(t, tt.expected.Trace.Sample.Rate, cfg.Trace.Sample.Rate)
			assert.Equal(t, tt.expected.Trace.Batch.Timeout, cfg.Trace.Batch.Timeout)
			assert.Equal(t, tt.expected.Trace.Batch.Size, cfg.Trace.Batch.Size)
			assert.Equal(t, tt.expected.Trace.Export.Timeout, cfg.Trace.Export.Timeout)
			assert.Equal(t, tt.expected.Trace.Max.Queue.Size, cfg.Trace.Max.Queue.Size)
			assert.Equal(t, tt.expected.Trace.Max.Batch.Size, cfg.Trace.Max.Batch.Size)

			// Compare pointer values
			if tt.expected.Metrics.Enabled != nil {
				require.NotNil(t, cfg.Metrics.Enabled)
				assert.Equal(t, *tt.expected.Metrics.Enabled, *cfg.Metrics.Enabled)
			} else {
				assert.Nil(t, cfg.Metrics.Enabled)
			}

			assert.Equal(t, tt.expected.Metrics.Endpoint, cfg.Metrics.Endpoint)
			assert.Equal(t, tt.expected.Metrics.Interval, cfg.Metrics.Interval)
			assert.Equal(t, tt.expected.Metrics.Export.Timeout, cfg.Metrics.Export.Timeout)
		})
	}
}

// TestConfigEnvironmentVariableOverrides validates that environment variables
// can override YAML configuration values.
// Note: Environment variable override behavior is tested in the integration tests
// (bootstrap_observability_test.go) since it requires the full config.Load() flow.
// This test demonstrates the structure that would be used.
