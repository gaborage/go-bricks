package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

const (
	logTypeAttrKey = "log.type"
)

func TestCreateOTLPHTTPLogExporter(t *testing.T) {
	tests := []struct {
		name        string
		config      LogsConfig
		traceConfig TraceConfig
		wantErr     bool
		checkFunc   func(*testing.T, sdklog.Exporter)
	}{
		{
			name: "http_with_gzip_compression",
			config: LogsConfig{
				Endpoint:    "localhost:4318",
				Protocol:    ProtocolHTTP,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(true),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "http_with_no_compression",
			config: LogsConfig{
				Endpoint:    "localhost:4318",
				Protocol:    ProtocolHTTP,
				Compression: CompressionNone,
				Insecure:    BoolPtr(true),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "http_with_custom_headers",
			config: LogsConfig{
				Endpoint:    "localhost:4318",
				Protocol:    ProtocolHTTP,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(true),
				Headers: map[string]string{
					"Authorization": "Bearer test-token",
					"X-Custom-Key":  "custom-value",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "http_secure_connection",
			config: LogsConfig{
				Endpoint:    "otlp.nr-data.net:4318",
				Protocol:    ProtocolHTTP,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(false),
				Headers: map[string]string{
					"api-key": "test-license-key",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "http_inherits_insecure_from_trace_config",
			config: LogsConfig{
				Endpoint:    "localhost:4318",
				Protocol:    ProtocolHTTP,
				Compression: CompressionGzip,
				// Insecure not set - should inherit from trace
			},
			traceConfig: TraceConfig{
				Insecure: true,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &provider{
				config: Config{
					Logs:  tt.config,
					Trace: tt.traceConfig,
				},
			}

			exporter, err := p.createOTLPHTTPLogExporter()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exporter)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, exporter)
				if tt.checkFunc != nil {
					tt.checkFunc(t, exporter)
				}

				// Cleanup
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = exporter.Shutdown(shutdownCtx) // May error if no collector
			}
		})
	}
}

func TestCreateOTLPGRPCLogExporter(t *testing.T) {
	tests := []struct {
		name        string
		config      LogsConfig
		traceConfig TraceConfig
		wantErr     bool
		checkFunc   func(*testing.T, sdklog.Exporter)
	}{
		{
			name: "grpc_with_gzip_compression",
			config: LogsConfig{
				Endpoint:    "localhost:4317",
				Protocol:    ProtocolGRPC,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(true),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "grpc_with_no_compression",
			config: LogsConfig{
				Endpoint:    "localhost:4317",
				Protocol:    ProtocolGRPC,
				Compression: CompressionNone,
				Insecure:    BoolPtr(true),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "grpc_with_custom_headers",
			config: LogsConfig{
				Endpoint:    "localhost:4317",
				Protocol:    ProtocolGRPC,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(true),
				Headers: map[string]string{
					"api-key":      "test-license-key",
					"X-Custom-Key": "custom-value",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "grpc_secure_connection",
			config: LogsConfig{
				Endpoint:    "otlp.nr-data.net:4317",
				Protocol:    ProtocolGRPC,
				Compression: CompressionGzip,
				Insecure:    BoolPtr(false),
				Headers: map[string]string{
					"api-key": "test-license-key",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
		{
			name: "grpc_inherits_insecure_from_trace_config",
			config: LogsConfig{
				Endpoint:    "localhost:4317",
				Protocol:    ProtocolGRPC,
				Compression: CompressionGzip,
				// Insecure not set - should inherit from trace
			},
			traceConfig: TraceConfig{
				Insecure: true,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, exp sdklog.Exporter) {
				assert.NotNil(t, exp)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &provider{
				config: Config{
					Logs:  tt.config,
					Trace: tt.traceConfig,
				},
			}

			exporter, err := p.createOTLPGRPCLogExporter()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exporter)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, exporter)
				if tt.checkFunc != nil {
					tt.checkFunc(t, exporter)
				}

				// Cleanup
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = exporter.Shutdown(shutdownCtx) // May error if no collector
			}
		})
	}
}

func TestCreateLogExporterInvalidProtocol(t *testing.T) {
	p := &provider{
		config: Config{
			Logs: LogsConfig{
				Endpoint: "localhost:4317",
				Protocol: "websocket", // Invalid protocol
			},
		},
	}

	exporter, err := p.createLogExporter()
	assert.Error(t, err)
	assert.Nil(t, exporter)
	assert.ErrorIs(t, err, ErrInvalidProtocol)
}

func TestCreateLogExporterStdout(t *testing.T) {
	p := &provider{
		config: Config{
			Logs: LogsConfig{
				Endpoint: EndpointStdout,
				Protocol: ProtocolHTTP, // Protocol ignored for stdout
			},
		},
	}

	exporter, err := p.createLogExporter()
	assert.NoError(t, err)
	assert.NotNil(t, exporter)

	// Cleanup
	err = exporter.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestCreateDualModeProcessor(t *testing.T) {
	tests := []struct {
		name             string
		samplingRate     *float64
		expectedNotNil   bool
		expectedShutdown bool
	}{
		{
			name:             "with_zero_sampling_rate",
			samplingRate:     Float64Ptr(0.0),
			expectedNotNil:   true,
			expectedShutdown: true,
		},
		{
			name:             "with_half_sampling_rate",
			samplingRate:     Float64Ptr(0.5),
			expectedNotNil:   true,
			expectedShutdown: true,
		},
		{
			name:             "with_full_sampling_rate",
			samplingRate:     Float64Ptr(1.0),
			expectedNotNil:   true,
			expectedShutdown: true,
		},
		{
			name:             "with_nil_sampling_rate_defaults_to_zero",
			samplingRate:     nil,
			expectedNotNil:   true,
			expectedShutdown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &provider{
				config: Config{
					Service: ServiceConfig{
						Name: "test-service",
					},
					Logs: LogsConfig{
						Endpoint:     EndpointStdout,
						SamplingRate: tt.samplingRate,
						Batch: BatchConfig{
							Timeout: 500 * time.Millisecond,
							Size:    512,
						},
						Export: ExportConfig{
							Timeout: 10 * time.Second,
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
				},
			}

			// Create a mock exporter (stdout for simplicity)
			baseExporter, err := p.createLogExporter()
			require.NoError(t, err)
			require.NotNil(t, baseExporter)

			processor, err := p.createDualModeProcessor(baseExporter)
			assert.NoError(t, err)

			if tt.expectedNotNil {
				assert.NotNil(t, processor)
			}

			if tt.expectedShutdown {
				err = processor.Shutdown(context.Background())
				assert.NoError(t, err)
			}

			// Cleanup base exporter
			_ = baseExporter.Shutdown(context.Background())
		})
	}
}

func TestCreateLogResource(t *testing.T) {
	tests := []struct {
		name      string
		logType   string
		wantErr   bool
		checkFunc func(*testing.T, *provider)
	}{
		{
			name:    "action_log_type",
			logType: "action",
			wantErr: false,
			checkFunc: func(t *testing.T, p *provider) {
				res, err := p.createLogResource("action")
				require.NoError(t, err)
				require.NotNil(t, res)

				// Verify resource has log.type attribute
				attrs := res.Attributes()
				var foundLogType bool
				for _, attr := range attrs {
					if attr.Key == logTypeAttrKey && attr.Value.AsString() == "action" {
						foundLogType = true
						break
					}
				}
				assert.True(t, foundLogType, "Resource should have log.type=action attribute")
			},
		},
		{
			name:    "trace_log_type",
			logType: "trace",
			wantErr: false,
			checkFunc: func(t *testing.T, p *provider) {
				res, err := p.createLogResource("trace")
				require.NoError(t, err)
				require.NotNil(t, res)

				// Verify resource has log.type attribute
				attrs := res.Attributes()
				var foundLogType bool
				for _, attr := range attrs {
					if attr.Key == logTypeAttrKey && attr.Value.AsString() == "trace" {
						foundLogType = true
						break
					}
				}
				assert.True(t, foundLogType, "Resource should have log.type=trace attribute")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &provider{
				config: Config{
					Service: ServiceConfig{
						Name:    "test-service",
						Version: "1.0.0",
					},
					Environment: "test",
				},
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, p)
			}
		})
	}
}

func TestCreateBatchProcessorWithResource(t *testing.T) {
	// Create base exporter
	p := &provider{
		config: Config{
			Service: ServiceConfig{
				Name: "test-service",
			},
			Logs: LogsConfig{
				Endpoint: EndpointStdout,
				Batch: BatchConfig{
					Timeout: 500 * time.Millisecond,
					Size:    512,
				},
				Export: ExportConfig{
					Timeout: 10 * time.Second,
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
		},
	}

	baseExporter, err := p.createLogExporter()
	require.NoError(t, err)
	require.NotNil(t, baseExporter)
	defer baseExporter.Shutdown(context.Background())

	// Create resource
	res, err := p.createLogResource("action")
	require.NoError(t, err)
	require.NotNil(t, res)

	// Create batch processor with resource
	processor := p.createBatchProcessorWithResource(baseExporter, res, "action")
	assert.NotNil(t, processor)

	// Verify processor can be shut down
	err = processor.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestInitLogProviderSuccess(t *testing.T) {
	p := &provider{
		config: Config{
			Service: ServiceConfig{
				Name:    "test-service",
				Version: "1.0.0",
			},
			Environment: "test",
			Logs: LogsConfig{
				Enabled:      BoolPtr(true),
				Endpoint:     EndpointStdout,
				Protocol:     ProtocolHTTP,
				Compression:  CompressionGzip,
				SamplingRate: Float64Ptr(0.1),
				Batch: BatchConfig{
					Timeout: 500 * time.Millisecond,
					Size:    512,
				},
				Export: ExportConfig{
					Timeout: 10 * time.Second,
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
		},
	}

	err := p.initLogProvider()
	assert.NoError(t, err)
	assert.NotNil(t, p.loggerProvider)

	// Cleanup
	err = p.loggerProvider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestInitLogProviderWithHookFailure(t *testing.T) {
	expectedErr := errors.New("log init hook failure")
	prevHook := logInitHook
	logInitHook = func() error {
		return expectedErr
	}
	t.Cleanup(func() {
		logInitHook = prevHook
	})

	p := &provider{
		config: Config{
			Service: ServiceConfig{
				Name: "test-service",
			},
			Logs: LogsConfig{
				Endpoint: EndpointStdout,
				Batch: BatchConfig{
					Timeout: 500 * time.Millisecond,
				},
				Export: ExportConfig{
					Timeout: 10 * time.Second,
				},
				Max: MaxConfig{
					Queue: QueueConfig{Size: 2048},
					Batch: MaxBatchConfig{Size: 512},
				},
			},
		},
	}

	err := p.initLogProvider()
	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, p.loggerProvider)
}

func TestNewProviderWithLogsEnabled(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name:    "test-service",
			Version: "1.0.0",
		},
		Environment: "test",
		Logs: LogsConfig{
			Enabled:      BoolPtr(true),
			Endpoint:     EndpointStdout,
			Protocol:     ProtocolHTTP,
			Compression:  CompressionGzip,
			SamplingRate: Float64Ptr(0.1),
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify log provider is initialized
	lp := provider.LoggerProvider()
	assert.NotNil(t, lp)

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderWithLogsHTTPAndGRPC(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		endpoint string
	}{
		{
			name:     "http_protocol",
			protocol: ProtocolHTTP,
			endpoint: "http://localhost:4318",
		},
		{
			name:     "grpc_protocol",
			protocol: ProtocolGRPC,
			endpoint: "localhost:4317",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: "test-service",
				},
				Logs: LogsConfig{
					Enabled:      BoolPtr(true),
					Endpoint:     tt.endpoint,
					Protocol:     tt.protocol,
					Compression:  CompressionGzip,
					Insecure:     BoolPtr(true),
					SamplingRate: Float64Ptr(0.5),
				},
			}

			provider, err := NewProvider(cfg)
			require.NoError(t, err)
			assert.NotNil(t, provider)

			// Verify log provider is initialized
			lp := provider.LoggerProvider()
			assert.NotNil(t, lp)

			// Cleanup - may error due to no collector, which is expected
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = provider.Shutdown(shutdownCtx)
		})
	}
}
