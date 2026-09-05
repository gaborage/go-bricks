package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/gaborage/go-bricks/logger"
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

			exporter, err := p.createOTLPHTTPLogExporter(context.Background())

			if tt.wantErr {
				assert.Nil(t, exporter)
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
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

			exporter, err := p.createOTLPGRPCLogExporter(context.Background())

			if tt.wantErr {
				assert.Nil(t, exporter)
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
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

	exporter, err := p.createLogExporter(context.Background())
	require.Error(t, err)
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

	exporter, err := p.createLogExporter(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, exporter)

	// Cleanup
	err = exporter.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestCreateDualModeProcessor(t *testing.T) {
	tests := []struct {
		name         string
		samplingRate *float64
	}{
		{name: "with_zero_sampling_rate", samplingRate: Float64Ptr(0.0)},
		{name: "with_half_sampling_rate", samplingRate: Float64Ptr(0.5)},
		{name: "with_full_sampling_rate", samplingRate: Float64Ptr(1.0)},
		{name: "with_nil_sampling_rate_defaults_to_zero", samplingRate: nil},
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
			baseExporter, err := p.createLogExporter(context.Background())
			require.NoError(t, err)
			require.NotNil(t, baseExporter)

			processor := p.createDualModeProcessor(baseExporter)
			assert.NotNil(t, processor)
			shutdownErr := processor.Shutdown(context.Background())

			// Cleanup base exporter
			_ = baseExporter.Shutdown(context.Background())
			assert.NoError(t, shutdownErr)
		})
	}
}

func TestCreateBatchProcessor(t *testing.T) {
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

	baseExporter, err := p.createLogExporter(context.Background())
	require.NoError(t, err)
	require.NotNil(t, baseExporter)
	defer baseExporter.Shutdown(context.Background())

	// Create batch processor for the action log type
	processor := p.createBatchProcessor(baseExporter, "action")
	assert.NotNil(t, processor)

	// Verify processor can be shut down
	err = processor.Shutdown(context.Background())
	assert.NoError(t, err)
}

// batchProcessorTestProvider builds a provider whose Logs config is valid for
// createBatchProcessor, with the identity that createResource stamps on the resource.
func batchProcessorTestProvider() *provider {
	return &provider{
		config: Config{
			Service: ServiceConfig{
				Name:    "real-svc",
				Version: "1.2.3",
			},
			Environment: "production",
			Logs: LogsConfig{
				Batch:  BatchConfig{Timeout: 50 * time.Millisecond, Size: 512},
				Export: ExportConfig{Timeout: 10 * time.Second},
				Max: MaxConfig{
					Queue: QueueConfig{Size: 2048},
					Batch: MaxBatchConfig{Size: 512},
				},
			},
		},
	}
}

// wireProvider assembles the production shape initLogProvider builds — identity on the provider
// resource, the caller's processor behind it — around an inspectable exporter. build receives the
// fake so the caller decides which of the provider's processor constructors is under test.
func wireProvider(t *testing.T, build func(p *provider, exp sdklog.Exporter) sdklog.Processor) (*sdklog.LoggerProvider, *fakeLogExporter) {
	t.Helper()

	p := batchProcessorTestProvider()
	res, err := p.createResource(context.Background())
	require.NoError(t, err)

	fake := &fakeLogExporter{}
	logProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(build(p, fake)),
	)
	t.Cleanup(func() {
		require.NoError(t, logProvider.Shutdown(context.Background()))
	})
	return logProvider, fake
}

func wireBatchProcessor(t *testing.T, logType string) (*sdklog.LoggerProvider, *fakeLogExporter) {
	t.Helper()
	return wireProvider(t, func(p *provider, exp sdklog.Exporter) sdklog.Processor {
		return p.createBatchProcessor(exp, logType)
	})
}

// emitAPIRecord emits one record straight through the OTel log API — carrying no log.type, the
// shape that lets a processor's own stamp show — flushes the batch, and returns its attributes.
func emitAPIRecord(t *testing.T, logProvider *sdklog.LoggerProvider, fake *fakeLogExporter, sev log.Severity) map[attribute.Key]attribute.Value {
	t.Helper()

	var rec log.Record
	rec.SetSeverity(sev)
	logProvider.Logger("third-party").Emit(context.Background(), rec)

	// BatchProcessor buffers; without the flush the batch never reaches the exporter.
	require.NoError(t, logProvider.ForceFlush(context.Background()))

	require.NotEmpty(t, fake.batches)
	require.NotEmpty(t, fake.batches[0])
	return collectAttrs(&fake.batches[0][0])
}

// TestCreateBatchProcessorStampsOwnLogType drives the real production wiring rather than a
// hand-assembled enricher, and pins what createBatchProcessor actually hands
// newProcessorAttributeExporter. The record is emitted straight through the OTel log API, so
// it carries no log.type of its own — the one shape that can tell the processors apart.
// Deleting the enricher from the wiring, or swapping the two log-type labels at the
// createDualModeProcessor call sites, fails here.
func TestCreateBatchProcessorStampsOwnLogType(t *testing.T) {
	tests := []struct {
		name    string
		logType string
	}{
		{name: "trace_processor_stamps_trace", logType: logTypeTrace},
		{name: "action_processor_stamps_action", logType: logTypeAction},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logProvider, fake := wireBatchProcessor(t, tt.logType)
			attrs := emitAPIRecord(t, logProvider, fake, log.SeverityInfo)

			assert.Equal(t, attribute.StringValue(tt.logType), attrs[logTypeKey],
				"the processor must stamp its own log type on a record that carries none")
			for _, key := range identityResourceKeys {
				assert.NotContains(t, attrs, key,
					"%s must reach the backend on the resource block only, never as a record-level duplicate", key)
			}
		})
	}
}

// TestCreateDualModeProcessorLabelsUnlabeledRecordsAsTrace pins which label each processor is
// wired with inside createDualModeProcessor, not just what createBatchProcessor does with the
// label it is handed. A record carrying no log.type routes to the trace processor by default
// (collectRouting), so it must come out stamped "trace" — swapping logTypeAction/logTypeTrace at
// the two call sites fails here and nowhere else.
func TestCreateDualModeProcessorLabelsUnlabeledRecordsAsTrace(t *testing.T) {
	logProvider, fake := wireProvider(t, func(p *provider, exp sdklog.Exporter) sdklog.Processor {
		return p.createDualModeProcessor(exp)
	})

	// ERROR is exported unconditionally on the trace path; INFO/DEBUG would be dropped by the
	// default 0.0 sampling rate and the assertion below would never run.
	attrs := emitAPIRecord(t, logProvider, fake, log.SeverityError)

	assert.Equal(t, attribute.StringValue(logTypeTrace), attrs[logTypeKey],
		"an unlabeled record routes to the trace processor, so it must be stamped trace")
}

// TestCreateBatchProcessorEmitsNoIdentityDuplicatesThroughBridge is the #914 regression pin on
// the production path: the whole chain from a zerolog line through the real OTel bridge and the
// processor createBatchProcessor built. Handing the enricher res.Attributes() again fails here.
func TestCreateBatchProcessorEmitsNoIdentityDuplicatesThroughBridge(t *testing.T) {
	logProvider, fake := wireBatchProcessor(t, logTypeTrace)

	bridge := logger.NewOTelBridge(logProvider)
	require.NotNil(t, bridge)
	_, err := bridge.Write([]byte(`{"level":"info","message":"m"}`))
	require.NoError(t, err)
	require.NoError(t, logProvider.ForceFlush(context.Background()))

	require.NotEmpty(t, fake.batches)
	require.NotEmpty(t, fake.batches[0])
	attrs := collectAttrs(&fake.batches[0][0])

	assert.Equal(t, attribute.StringValue(logTypeTrace), attrs[logTypeKey])
	for _, key := range identityResourceKeys {
		assert.NotContains(t, attrs, key,
			"%s must appear once per batch in ResourceLogs.resource, never on the record", key)
	}

	recordRes := fake.batches[0][0].Resource()
	require.NotNil(t, recordRes)
	assert.Contains(t, recordRes.Attributes(), serviceNameKey.String("real-svc"),
		"identity must still reach the backend on the resource block")
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

	err := p.initLogProvider(context.Background())
	require.NoError(t, err)
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

	err := p.initLogProvider(context.Background())
	assert.Nil(t, p.loggerProvider)
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
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
