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
	testAuthToken      = "Bearer test-token"
	xTraceHeaderKey    = "X-Trace-Key"
	testTraceValue     = "trace-value"
	xLogsHeaderKey     = "X-Logs-Key"
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
						Rate: Float64Ptr(0.5),
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
						Rate: Float64Ptr(-0.1),
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
						Rate: Float64Ptr(1.1),
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
						Rate: Float64Ptr(0.0),
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
						Rate: Float64Ptr(1.0),
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "invalid log sampling rate - negative",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Logs: LogsConfig{
					Enabled:      BoolPtr(true),
					SamplingRate: Float64Ptr(-0.1),
				},
			},
			wantErr: ErrInvalidLogSamplingRate,
		},
		{
			name: "invalid log sampling rate - too high",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Logs: LogsConfig{
					Enabled:      BoolPtr(true),
					SamplingRate: Float64Ptr(1.1),
				},
			},
			wantErr: ErrInvalidLogSamplingRate,
		},
		{
			name: "valid log sampling rate - 0.5",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Logs: LogsConfig{
					Enabled:      BoolPtr(true),
					SamplingRate: Float64Ptr(0.5),
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
					Endpoint: "http://" + testTraceEndpointA,
					Protocol: "http",
					Sample: SampleConfig{
						Rate: Float64Ptr(1.0),
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
						Rate: Float64Ptr(1.0),
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
					Endpoint: "http://" + testTraceEndpointA,
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
						Rate: Float64Ptr(1.0),
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
						Rate: Float64Ptr(1.0),
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
	require.NotNil(t, obsCfg.Trace.Sample.Rate, "Sample rate should not be nil")
	assert.Equal(t, 0.5, *obsCfg.Trace.Sample.Rate, "Sample rate should be 0.5")

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
						Rate: Float64Ptr(1.0),
					},
					Batch: BatchConfig{
						Timeout: 500 * time.Millisecond, // Development environment with stdout endpoint
						Size:    512,
					},
					Export: ExportConfig{
						Timeout: 10 * time.Second, // Development + stdout = 10s
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
						Timeout: 10 * time.Second, // Development + stdout = 10s
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
						Rate: Float64Ptr(0.1),
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
						Rate: Float64Ptr(0.1),
					},
					Batch: BatchConfig{
						Timeout: 500 * time.Millisecond, // Development environment gets fast export
						Size:    512,
					},
					Export: ExportConfig{
						Timeout: 10 * time.Second, // Development environment = 10s
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
						Timeout: 10 * time.Second, // Development + stdout = 10s
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

// TestLogsHeadersMapIsolation verifies that logs headers are properly cloned
// from trace headers to avoid aliasing (mutations to one shouldn't affect the other).
func TestLogsHeadersMapIsolation(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: "test",
		},
		Trace: TraceConfig{
			Headers: map[string]string{
				"Authorization": testAuthToken,
				xTraceHeaderKey: testTraceValue,
			},
		},
	}

	// Apply defaults (this is where the cloning happens)
	cfg.ApplyDefaults()

	// Verify logs inherited trace headers
	require.NotNil(t, cfg.Logs.Headers, "Logs headers should be inherited from trace")
	assert.Equal(t, testAuthToken, cfg.Logs.Headers["Authorization"])
	assert.Equal(t, testTraceValue, cfg.Logs.Headers[xTraceHeaderKey])

	// Mutate logs headers
	cfg.Logs.Headers["Authorization"] = "Bearer logs-token"
	cfg.Logs.Headers[xLogsHeaderKey] = "logs-value"
	delete(cfg.Logs.Headers, xTraceHeaderKey)

	// Verify trace headers remain unchanged (no aliasing)
	assert.Equal(t, testAuthToken, cfg.Trace.Headers["Authorization"],
		"Trace Authorization should be unchanged after mutating logs headers")
	assert.Equal(t, testTraceValue, cfg.Trace.Headers[xTraceHeaderKey],
		"Trace X-Trace-Key should be unchanged after mutating logs headers")
	assert.NotContains(t, cfg.Trace.Headers, xLogsHeaderKey,
		"Trace headers should not have keys added to logs headers")

	// Verify logs headers have the mutations
	assert.Equal(t, "Bearer logs-token", cfg.Logs.Headers["Authorization"])
	assert.Equal(t, "logs-value", cfg.Logs.Headers[xLogsHeaderKey])
	assert.NotContains(t, cfg.Logs.Headers, xTraceHeaderKey)
}

// TestCloneHeaderMapNil verifies that cloning a nil map returns nil.
func TestCloneHeaderMapNil(t *testing.T) {
	result := cloneHeaderMap(nil)
	assert.Nil(t, result, "Cloning nil map should return nil")
}

// TestCloneHeaderMapEmpty verifies that cloning an empty map returns an empty map.
func TestCloneHeaderMapEmpty(t *testing.T) {
	original := make(map[string]string)
	clone := cloneHeaderMap(original)

	require.NotNil(t, clone, "Clone should not be nil")
	assert.Empty(t, clone, "Clone should be empty")

	// Verify they're different map instances
	clone["test"] = "value"
	assert.NotContains(t, original, "test", "Original map should not be affected by mutations to clone")
}

// TestCloneHeaderMapWithValues verifies proper cloning behavior.
func TestCloneHeaderMapWithValues(t *testing.T) {
	original := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	clone := cloneHeaderMap(original)

	// Verify contents match
	require.NotNil(t, clone)
	assert.Equal(t, len(original), len(clone))
	for k, v := range original {
		assert.Equal(t, v, clone[k], "Clone should have same value for key %s", k)
	}

	// Verify they're different map instances by mutating clone
	clone["key1"] = "modified"
	clone["key4"] = "new"
	delete(clone, "key2")

	// Original should be unchanged
	assert.Equal(t, "value1", original["key1"], "Original should not be affected")
	assert.Equal(t, "value2", original["key2"], "Original should not be affected")
	assert.NotContains(t, original, "key4", "Original should not have new keys from clone")
}

// TestConfigEnvironmentVariableOverrides validates that environment variables
// can override YAML configuration values.
// Note: Environment variable override behavior is tested in the integration tests
// (bootstrap_observability_test.go) since it requires the full config.Load() flow.
// This test demonstrates the structure that would be used.

func TestValidateEndpointFormat(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		protocol string
		wantErr  error
	}{
		{
			name:     "grpc with correct format",
			endpoint: "otlp.nr-data.net:4317",
			protocol: ProtocolGRPC,
			wantErr:  nil,
		},
		{
			name:     "grpc with https scheme - invalid",
			endpoint: "https://otlp.nr-data.net:4317",
			protocol: ProtocolGRPC,
			wantErr:  ErrInvalidEndpointFormat,
		},
		{
			name:     "grpc with http scheme - invalid",
			endpoint: "http://localhost:4317",
			protocol: ProtocolGRPC,
			wantErr:  ErrInvalidEndpointFormat,
		},
		{
			name:     "http with https scheme - valid",
			endpoint: "https://otlp.nr-data.net:4318/v1/traces",
			protocol: ProtocolHTTP,
			wantErr:  nil,
		},
		{
			name:     "http with http scheme - valid",
			endpoint: "http://localhost:4318/v1/traces",
			protocol: ProtocolHTTP,
			wantErr:  nil,
		},
		{
			name:     "http without scheme - invalid",
			endpoint: "localhost:4318",
			protocol: ProtocolHTTP,
			wantErr:  ErrInvalidEndpointFormat,
		},
		{
			name:     "stdout endpoint - always valid",
			endpoint: EndpointStdout,
			protocol: ProtocolGRPC,
			wantErr:  nil,
		},
		{
			name:     "empty endpoint - always valid",
			endpoint: "",
			protocol: ProtocolGRPC,
			wantErr:  nil,
		},
		{
			name:     "grpc localhost without scheme - valid",
			endpoint: "localhost:4317",
			protocol: ProtocolGRPC,
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpointFormat(tt.endpoint, tt.protocol)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigValidateEndpointFormat(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "trace grpc with correct format",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Endpoint: "otlp.nr-data.net:4317",
					Protocol: ProtocolGRPC,
				},
			},
			wantErr: nil,
		},
		{
			name: "trace grpc with https - invalid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Endpoint: "https://otlp.nr-data.net:4317",
					Protocol: ProtocolGRPC,
				},
			},
			wantErr: ErrInvalidEndpointFormat,
		},
		{
			name: "metrics http with https - valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: "https://otlp.nr-data.net:4318/v1/metrics",
					Protocol: ProtocolHTTP,
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics http without scheme - invalid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: "otlp.nr-data.net:4318",
					Protocol: ProtocolHTTP,
				},
			},
			wantErr: ErrInvalidEndpointFormat,
		},
		{
			name: "logs grpc with http scheme - invalid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Logs: LogsConfig{
					Enabled:  BoolPtr(true),
					Endpoint: "http://localhost:4317",
					Protocol: ProtocolGRPC,
				},
			},
			wantErr: ErrInvalidEndpointFormat,
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

func TestValidateCompression(t *testing.T) {
	tests := []struct {
		name        string
		compression string
		wantErr     error
	}{
		{
			name:        "gzip_compression_valid",
			compression: "gzip",
			wantErr:     nil,
		},
		{
			name:        "no_compression_valid",
			compression: "none",
			wantErr:     nil,
		},
		{
			name:        "empty_compression_valid_will_use_default",
			compression: "",
			wantErr:     nil,
		},
		{
			name:        "invalid_compression_brotli",
			compression: "brotli",
			wantErr:     ErrInvalidCompression,
		},
		{
			name:        "invalid_compression_deflate",
			compression: "deflate",
			wantErr:     ErrInvalidCompression,
		},
		{
			name:        "invalid_compression_uppercase_GZIP",
			compression: "GZIP",
			wantErr:     ErrInvalidCompression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCompression(tt.compression)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigValidateCompression(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "trace_with_gzip_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "gzip",
				},
			},
			wantErr: nil,
		},
		{
			name: "trace_with_no_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "none",
				},
			},
			wantErr: nil,
		},
		{
			name: "trace_with_invalid_compression",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "brotli",
				},
			},
			wantErr: ErrInvalidCompression,
		},
		{
			name: "metrics_with_gzip_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Compression: "gzip",
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics_with_invalid_compression",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Compression: "deflate",
				},
			},
			wantErr: ErrInvalidCompression,
		},
		{
			name: "logs_with_gzip_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Logs: LogsConfig{
					Enabled:     BoolPtr(true),
					Compression: "gzip",
				},
			},
			wantErr: nil,
		},
		{
			name: "logs_with_no_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Logs: LogsConfig{
					Enabled:     BoolPtr(true),
					Compression: "none",
				},
			},
			wantErr: nil,
		},
		{
			name: "logs_with_invalid_compression",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Logs: LogsConfig{
					Enabled:     BoolPtr(true),
					Compression: "GZIP",
				},
			},
			wantErr: ErrInvalidCompression,
		},
		{
			name: "all_signals_with_gzip_compression_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "gzip",
				},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Compression: "gzip",
				},
				Logs: LogsConfig{
					Enabled:     BoolPtr(true),
					Compression: "gzip",
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

func TestConfigCompressionDefaults(t *testing.T) {
	tests := []struct {
		name               string
		config             Config
		expectedTraceComp  string
		expectedMetricComp string
		expectedLogComp    string
	}{
		{
			name: "defaults_to_gzip_for_all_signals",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
			},
			expectedTraceComp:  "gzip",
			expectedMetricComp: "gzip",
			expectedLogComp:    "gzip",
		},
		{
			name: "respects_explicit_none_compression",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "none",
				},
				Metrics: MetricsConfig{
					Compression: "none",
				},
				Logs: LogsConfig{
					Compression: "none",
				},
			},
			expectedTraceComp:  "none",
			expectedMetricComp: "none",
			expectedLogComp:    "none",
		},
		{
			name: "mixed_compression_settings",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Trace: TraceConfig{
					Compression: "gzip",
				},
				Metrics: MetricsConfig{
					Compression: "none",
				},
				// Logs uses default
			},
			expectedTraceComp:  "gzip",
			expectedMetricComp: "none",
			expectedLogComp:    "gzip", // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.ApplyDefaults()
			assert.Equal(t, tt.expectedTraceComp, tt.config.Trace.Compression)
			assert.Equal(t, tt.expectedMetricComp, tt.config.Metrics.Compression)
			assert.Equal(t, tt.expectedLogComp, tt.config.Logs.Compression)
		})
	}
}

func TestConfigEnvironmentAwareExportTimeouts(t *testing.T) {
	tests := []struct {
		name                   string
		environment            string
		traceEndpoint          string
		metricsEndpoint        string
		logsEndpoint           string
		expectedTraceTimeout   time.Duration
		expectedMetricsTimeout time.Duration
		expectedLogsTimeout    time.Duration
	}{
		{
			name:                   "development_environment_defaults_to_10s",
			environment:            EnvironmentDevelopment,
			traceEndpoint:          "http://localhost:4318",
			metricsEndpoint:        "http://localhost:4318",
			logsEndpoint:           "http://localhost:4318",
			expectedTraceTimeout:   10 * time.Second,
			expectedMetricsTimeout: 10 * time.Second,
			expectedLogsTimeout:    10 * time.Second,
		},
		{
			name:                   "production_environment_defaults_to_60s",
			environment:            "production",
			traceEndpoint:          "otlp.nr-data.net:4317",
			metricsEndpoint:        "otlp.nr-data.net:4317",
			logsEndpoint:           "otlp.nr-data.net:4317",
			expectedTraceTimeout:   60 * time.Second,
			expectedMetricsTimeout: 60 * time.Second,
			expectedLogsTimeout:    60 * time.Second,
		},
		{
			name:                   "staging_environment_defaults_to_60s",
			environment:            "staging",
			traceEndpoint:          "otlp.nr-data.net:4317",
			metricsEndpoint:        "otlp.nr-data.net:4317",
			logsEndpoint:           "otlp.nr-data.net:4317",
			expectedTraceTimeout:   60 * time.Second,
			expectedMetricsTimeout: 60 * time.Second,
			expectedLogsTimeout:    60 * time.Second,
		},
		{
			name:                   "stdout_endpoint_overrides_environment_to_10s",
			environment:            "production",
			traceEndpoint:          EndpointStdout,
			metricsEndpoint:        EndpointStdout,
			logsEndpoint:           EndpointStdout,
			expectedTraceTimeout:   10 * time.Second,
			expectedMetricsTimeout: 10 * time.Second,
			expectedLogsTimeout:    10 * time.Second,
		},
		{
			name:                   "default_environment_treated_as_development",
			environment:            "",
			traceEndpoint:          "http://localhost:4318",
			metricsEndpoint:        "http://localhost:4318",
			logsEndpoint:           "http://localhost:4318",
			expectedTraceTimeout:   10 * time.Second,
			expectedMetricsTimeout: 10 * time.Second,
			expectedLogsTimeout:    10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: "test-service",
				},
				Environment: tt.environment,
				Trace: TraceConfig{
					Endpoint: tt.traceEndpoint,
				},
				Metrics: MetricsConfig{
					Endpoint: tt.metricsEndpoint,
				},
				Logs: LogsConfig{
					Endpoint: tt.logsEndpoint,
				},
			}

			cfg.ApplyDefaults()

			assert.Equal(t, tt.expectedTraceTimeout, cfg.Trace.Export.Timeout,
				"Trace export timeout mismatch for environment=%s, endpoint=%s", tt.environment, tt.traceEndpoint)
			assert.Equal(t, tt.expectedMetricsTimeout, cfg.Metrics.Export.Timeout,
				"Metrics export timeout mismatch for environment=%s, endpoint=%s", tt.environment, tt.metricsEndpoint)
			assert.Equal(t, tt.expectedLogsTimeout, cfg.Logs.Export.Timeout,
				"Logs export timeout mismatch for environment=%s, endpoint=%s", tt.environment, tt.logsEndpoint)
		})
	}
}

func TestConfigExplicitExportTimeoutPreserved(t *testing.T) {
	tests := []struct {
		name            string
		environment     string
		explicitTimeout time.Duration
	}{
		{
			name:            "explicit_90s_preserved_in_development",
			environment:     EnvironmentDevelopment,
			explicitTimeout: 90 * time.Second,
		},
		{
			name:            "explicit_5s_preserved_in_production",
			environment:     "production",
			explicitTimeout: 5 * time.Second,
		},
		{
			name:            "explicit_120s_preserved",
			environment:     "staging",
			explicitTimeout: 120 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: "test-service",
				},
				Environment: tt.environment,
				Trace: TraceConfig{
					Endpoint: "otlp.nr-data.net:4317",
					Export: ExportConfig{
						Timeout: tt.explicitTimeout,
					},
				},
				Metrics: MetricsConfig{
					Endpoint: "otlp.nr-data.net:4317",
					Export: MetricsExportConfig{
						Timeout: tt.explicitTimeout,
					},
				},
				Logs: LogsConfig{
					Endpoint: "otlp.nr-data.net:4317",
					Export: ExportConfig{
						Timeout: tt.explicitTimeout,
					},
				},
			}

			cfg.ApplyDefaults()

			assert.Equal(t, tt.explicitTimeout, cfg.Trace.Export.Timeout,
				"Explicit trace export timeout should be preserved")
			assert.Equal(t, tt.explicitTimeout, cfg.Metrics.Export.Timeout,
				"Explicit metrics export timeout should be preserved")
			assert.Equal(t, tt.explicitTimeout, cfg.Logs.Export.Timeout,
				"Explicit logs export timeout should be preserved")
		})
	}
}

func TestValidateTemporality(t *testing.T) {
	tests := []struct {
		name        string
		temporality string
		wantErr     error
	}{
		{
			name:        "delta_temporality_valid",
			temporality: "delta",
			wantErr:     nil,
		},
		{
			name:        "cumulative_temporality_valid",
			temporality: "cumulative",
			wantErr:     nil,
		},
		{
			name:        "empty_temporality_valid_will_use_default",
			temporality: "",
			wantErr:     nil,
		},
		{
			name:        "invalid_temporality_incremental",
			temporality: "incremental",
			wantErr:     ErrInvalidTemporality,
		},
		{
			name:        "invalid_temporality_uppercase_DELTA",
			temporality: "DELTA",
			wantErr:     ErrInvalidTemporality,
		},
		{
			name:        "invalid_temporality_stateful",
			temporality: "stateful",
			wantErr:     ErrInvalidTemporality,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTemporality(tt.temporality)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateHistogramAggregation(t *testing.T) {
	tests := []struct {
		name        string
		aggregation string
		wantErr     error
	}{
		{
			name:        "exponential_aggregation_valid",
			aggregation: "exponential",
			wantErr:     nil,
		},
		{
			name:        "explicit_aggregation_valid",
			aggregation: "explicit",
			wantErr:     nil,
		},
		{
			name:        "empty_aggregation_valid_will_use_default",
			aggregation: "",
			wantErr:     nil,
		},
		{
			name:        "invalid_aggregation_histogram",
			aggregation: "histogram",
			wantErr:     ErrInvalidHistogramAggregation,
		},
		{
			name:        "invalid_aggregation_uppercase_EXPONENTIAL",
			aggregation: "EXPONENTIAL",
			wantErr:     ErrInvalidHistogramAggregation,
		},
		{
			name:        "invalid_aggregation_linear",
			aggregation: "linear",
			wantErr:     ErrInvalidHistogramAggregation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHistogramAggregation(tt.aggregation)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigValidateTemporality(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "metrics_with_delta_temporality_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Temporality: "delta",
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics_with_cumulative_temporality_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Temporality: "cumulative",
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics_with_invalid_temporality",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:     BoolPtr(true),
					Temporality: "incremental",
				},
			},
			wantErr: ErrInvalidTemporality,
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

func TestConfigValidateHistogramAggregation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "metrics_with_exponential_aggregation_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:              BoolPtr(true),
					HistogramAggregation: "exponential",
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics_with_explicit_aggregation_valid",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:              BoolPtr(true),
					HistogramAggregation: "explicit",
				},
			},
			wantErr: nil,
		},
		{
			name: "metrics_with_invalid_aggregation",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Enabled:              BoolPtr(true),
					HistogramAggregation: "linear",
				},
			},
			wantErr: ErrInvalidHistogramAggregation,
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

func TestConfigTemporalityDefaults(t *testing.T) {
	tests := []struct {
		name                 string
		config               Config
		expectedTemporality  string
		expectedHistogramAgg string
	}{
		{
			name: "defaults_to_cumulative_and_explicit",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
			},
			expectedTemporality:  "cumulative",
			expectedHistogramAgg: "explicit",
		},
		{
			name: "respects_explicit_delta_and_exponential",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Temporality:          "delta",
					HistogramAggregation: "exponential",
				},
			},
			expectedTemporality:  "delta",
			expectedHistogramAgg: "exponential",
		},
		{
			name: "mixed_delta_temporality_with_explicit_histogram",
			config: Config{
				Enabled: true,
				Service: ServiceConfig{Name: testServiceName},
				Metrics: MetricsConfig{
					Temporality: "delta",
					// HistogramAggregation uses default
				},
			},
			expectedTemporality:  "delta",
			expectedHistogramAgg: "explicit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.ApplyDefaults()
			assert.Equal(t, tt.expectedTemporality, tt.config.Metrics.Temporality)
			assert.Equal(t, tt.expectedHistogramAgg, tt.config.Metrics.HistogramAggregation)
		})
	}
}
