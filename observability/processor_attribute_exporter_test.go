package observability

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/log/logtest"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"

	"github.com/gaborage/go-bricks/logger"
)

// Keyed off semconv rather than literals so the fixtures track the keys provider.go
// actually puts on the resource.
const (
	serviceNameKey = semconv.ServiceNameKey
	serviceVerKey  = semconv.ServiceVersionKey
	deployEnvKey   = semconv.DeploymentEnvironmentNameKey
	hostNameKey    = semconv.HostNameKey
)

// fakeLogExporter records what the wrapped exporter receives and returns configurable errors.
type fakeLogExporter struct {
	batches       [][]sdklog.Record
	exportErr     error
	shutdownCount int
	shutdownErr   error
	flushCount    int
	flushErr      error
}

func (f *fakeLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	batch := make([]sdklog.Record, len(records))
	copy(batch, records)
	f.batches = append(f.batches, batch)
	return f.exportErr
}

func (f *fakeLogExporter) Shutdown(_ context.Context) error {
	f.shutdownCount++
	return f.shutdownErr
}

func (f *fakeLogExporter) ForceFlush(_ context.Context) error {
	f.flushCount++
	return f.flushErr
}

// newTraceEnricher wraps wrapped with the production stamp shape: a single log.type attribute.
func newTraceEnricher(wrapped sdklog.Exporter) *processorAttributeExporter {
	return newProcessorAttributeExporter(wrapped, attribute.String(logTypeKey, logTypeTrace))
}

func newTestRecord(attrs ...attribute.KeyValue) sdklog.Record {
	factory := logtest.RecordFactory{Attributes: attrs}
	return factory.NewRecord()
}

func collectAttrs(rec *sdklog.Record) map[attribute.Key]attribute.Value {
	attrs := make(map[attribute.Key]attribute.Value, rec.AttributesLen())
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	return attrs
}

func TestProcessorAttributeExporterEnrich(t *testing.T) {
	tests := []struct {
		name        string
		recordAttrs []attribute.KeyValue
		wantAttrs   map[attribute.Key]attribute.Value
	}{
		{
			name:        "record_without_attributes_receives_the_stamp",
			recordAttrs: nil,
			wantAttrs: map[attribute.Key]attribute.Value{
				logTypeKey: attribute.StringValue(logTypeTrace),
			},
		},
		{
			name:        "record_without_the_key_keeps_its_own_attributes_and_gains_the_stamp",
			recordAttrs: []attribute.KeyValue{attribute.String("msg.id", "abc"), attribute.Int("attempt", 2)},
			wantAttrs: map[attribute.Key]attribute.Value{
				"msg.id":   attribute.StringValue("abc"),
				"attempt":  attribute.IntValue(2),
				logTypeKey: attribute.StringValue(logTypeTrace),
			},
		},
		{
			name:        "collision_leaves_the_record_value_in_place",
			recordAttrs: []attribute.KeyValue{attribute.String(logTypeKey, logTypeAction), attribute.Int("attempt", 2)},
			wantAttrs: map[attribute.Key]attribute.Value{
				logTypeKey: attribute.StringValue(logTypeAction),
				"attempt":  attribute.IntValue(2),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enricher := newTraceEnricher(&fakeLogExporter{})
			rec := newTestRecord(tt.recordAttrs...)

			enriched := enricher.enrich(&rec)

			assert.Equal(t, tt.wantAttrs, collectAttrs(&enriched))
		})
	}
}

// TestProcessorAttributeExporterRecordAttributeWinsOverCollidingStampAttribute pins
// the precedence contract: AddAttributes would overwrite, so the processor's value must
// never reach a record that already carries that key. This is what keeps a caller-set
// log.type authoritative for dual-mode routing.
func TestProcessorAttributeExporterRecordAttributeWinsOverCollidingStampAttribute(t *testing.T) {
	enricher := newProcessorAttributeExporter(&fakeLogExporter{},
		attribute.String(logTypeKey, "stamp-wins-would-be-a-bug"),
	)
	rec := newTestRecord(attribute.String(logTypeKey, logTypeAction))

	enriched := enricher.enrich(&rec)

	assert.Equal(t, attribute.StringValue(logTypeAction), collectAttrs(&enriched)[logTypeKey])
}

func TestProcessorAttributeExporterEnrichDoesNotMutateOriginal(t *testing.T) {
	t.Run("add_branch_clones_before_stamping", func(t *testing.T) {
		enricher := newTraceEnricher(&fakeLogExporter{})
		rec := newTestRecord(attribute.String("msg.id", "abc"))

		enriched := enricher.enrich(&rec)

		assert.Equal(t, map[attribute.Key]attribute.Value{
			"msg.id": attribute.StringValue("abc"),
		}, collectAttrs(&rec), "the stamp must not land on the caller's record")
		assert.Len(t, collectAttrs(&enriched), 2)
	})

	// The collision branch skips the clone and returns a value copy aliasing the original's
	// attribute storage. It adds nothing, so the original must come back untouched.
	t.Run("no_add_branch_leaves_the_original_untouched", func(t *testing.T) {
		enricher := newTraceEnricher(&fakeLogExporter{})
		rec := newTestRecord(attribute.String(logTypeKey, logTypeAction), attribute.String("msg.id", "abc"))

		enriched := enricher.enrich(&rec)

		want := map[attribute.Key]attribute.Value{
			logTypeKey: attribute.StringValue(logTypeAction),
			"msg.id":   attribute.StringValue("abc"),
		}
		assert.Equal(t, want, collectAttrs(&rec), "the original must be unchanged")
		assert.Equal(t, want, collectAttrs(&enriched), "the returned record must match it exactly")
	})
}

func TestProcessorAttributeExporterExport(t *testing.T) {
	t.Run("enriches_every_record_in_the_batch", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTraceEnricher(wrapped)

		records := []sdklog.Record{
			newTestRecord(attribute.String(logTypeKey, logTypeAction)),
			newTestRecord(serviceNameKey.String("record-value")),
			newTestRecord(),
		}

		require.NoError(t, enricher.Export(context.Background(), records))

		require.Len(t, wrapped.batches, 1)
		exported := wrapped.batches[0]
		require.Len(t, exported, 3)

		assert.Equal(t, map[attribute.Key]attribute.Value{
			logTypeKey: attribute.StringValue(logTypeAction),
		}, collectAttrs(&exported[0]), "a record's own log.type survives")
		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("record-value"),
			logTypeKey:     attribute.StringValue(logTypeTrace),
		}, collectAttrs(&exported[1]))
		assert.Equal(t, map[attribute.Key]attribute.Value{
			logTypeKey: attribute.StringValue(logTypeTrace),
		}, collectAttrs(&exported[2]))

		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("record-value"),
		}, collectAttrs(&records[1]), "originals must stay untouched")
	})

	t.Run("propagates_wrapped_exporter_error", func(t *testing.T) {
		wantErr := errors.New("export failed")
		wrapped := &fakeLogExporter{exportErr: wantErr}
		enricher := newTraceEnricher(wrapped)

		err := enricher.Export(context.Background(), []sdklog.Record{newTestRecord()})

		assert.ErrorIs(t, err, wantErr)
		assert.Len(t, wrapped.batches, 1)
	})
}

func TestProcessorAttributeExporterShutdown(t *testing.T) {
	t.Run("delegates_once_and_memoizes_result", func(t *testing.T) {
		wantErr := errors.New("shutdown failed")
		wrapped := &fakeLogExporter{shutdownErr: wantErr}
		enricher := newTraceEnricher(wrapped)

		first := enricher.Shutdown(context.Background())
		second := enricher.Shutdown(context.Background())

		assert.ErrorIs(t, first, wantErr)
		assert.ErrorIs(t, second, wantErr)
		assert.Equal(t, 1, wrapped.shutdownCount)
	})

	t.Run("memoizes_nil_result", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTraceEnricher(wrapped)

		assert.NoError(t, enricher.Shutdown(context.Background()))
		assert.NoError(t, enricher.Shutdown(context.Background()))
		assert.Equal(t, 1, wrapped.shutdownCount)
	})
}

func TestProcessorAttributeExporterForceFlush(t *testing.T) {
	t.Run("delegates_to_wrapped", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTraceEnricher(wrapped)

		assert.NoError(t, enricher.ForceFlush(context.Background()))
		assert.Equal(t, 1, wrapped.flushCount)
	})

	t.Run("propagates_wrapped_exporter_error", func(t *testing.T) {
		wantErr := errors.New("flush failed")
		wrapped := &fakeLogExporter{flushErr: wantErr}
		enricher := newTraceEnricher(wrapped)

		assert.ErrorIs(t, enricher.ForceFlush(context.Background()), wantErr)
		assert.Equal(t, 1, wrapped.flushCount)
	})
}

// actionLogRecord mirrors the framework's highest-volume record: the HTTP action log built in
// server/logger.go, which stamps its own log.type and carries ~16 attributes. Size matters here —
// sdklog.Record keeps 5 attributes inline and spills the rest to a heap-backed slice that Clone
// duplicates, so a fixture under that threshold reports zero allocations no matter what enrich does.
func actionLogRecord() sdklog.Record {
	return newTestRecord(
		attribute.String(logTypeKey, logTypeAction),
		attribute.String("request_id", "01J8ZC2K3M4N5P6Q7R8S9T0V1W"),
		attribute.String("correlation_id", "0af7651916cd43dd8448eb211c80319c"),
		attribute.String("http.request.method", "POST"),
		attribute.Int("http.response.status_code", 201),
		attribute.Int64("http.server.request.duration", 1_250_000),
		attribute.String("url.path", "/api/v1/payments"),
		attribute.String("http.route", "/api/v1/payments"),
		attribute.String("client.address", "10.0.0.7"),
		attribute.String("user_agent.original", "Go-http-client/2.0"),
		attribute.String("result_code", "OK"),
		attribute.Int64("amqp_published", 2),
		attribute.Int64("amqp_elapsed", 3_200),
		attribute.Int64("db_queries", 4),
		attribute.Int64("db_elapsed", 88_000),
		attribute.String("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"),
	)
}

// smallLabeledRecord collides on log.type like actionLogRecord but stays inside the inline
// attribute slots, so it isolates the cost of the collision check from the cost of the spill.
func smallLabeledRecord() sdklog.Record {
	return newTestRecord(
		attribute.String(logTypeKey, logTypeAction),
		attribute.String("http.method", "POST"),
		attribute.Int("http.status_code", 201),
	)
}

// unlabeledRecord is what third-party code emitting straight through the OTel API produces:
// no log.type, so the enricher performs one real injection instead of filtering it away.
func unlabeledRecord() sdklog.Record {
	return newTestRecord(
		attribute.String("http.method", "POST"),
		attribute.Int("http.status_code", 201),
	)
}

func BenchmarkProcessorAttributeExporterEnrich(b *testing.B) {
	enricher := newTraceEnricher(&fakeLogExporter{})

	benchmarks := []struct {
		name   string
		record func() sdklog.Record
	}{
		{name: "action_log_record_collides_16_attrs", record: actionLogRecord},
		{name: "small_record_collides_3_attrs", record: smallLabeledRecord},
		{name: "record_without_log_type", record: unlabeledRecord},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			rec := bm.record()
			b.ReportAllocs()
			for b.Loop() {
				_ = enricher.enrich(&rec)
			}
		})
	}
}

// identityTestResource builds the resource from the same provider fixture logs_test.go wires its
// processors with. Sharing it is load-bearing: identityResourceKeys is asserted with NotContains
// against resources built in both files, so two fixtures that drifted apart — one dropping
// Environment, say — would turn those assertions vacuously green.
func identityTestResource(t *testing.T) *resource.Resource {
	t.Helper()
	res, err := batchProcessorTestProvider().createResource(context.Background())
	require.NoError(t, err)
	return res
}

// identityResourceKeys are the attributes createResource puts on every resource:
// the service identity plus the telemetry.sdk.* triplet from resource.Default.
var identityResourceKeys = []attribute.Key{
	serviceNameKey,
	serviceVerKey,
	deployEnvKey,
	"telemetry.sdk.name",
	"telemetry.sdk.language",
	"telemetry.sdk.version",
}

// TestLoggerProviderResourceCarriesServiceIdentity pins the premise the delta
// enricher rests on: the resource the provider is constructed with already
// reaches the exporter on every record, so identity never needs copying to
// record level to survive the wire.
func TestLoggerProviderResourceCarriesServiceIdentity(t *testing.T) {
	res := identityTestResource(t)

	wrapped := &fakeLogExporter{}
	logProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(wrapped)),
	)
	t.Cleanup(func() {
		require.NoError(t, logProvider.Shutdown(context.Background()))
	})

	bridge := logger.NewOTelBridge(logProvider)
	require.NotNil(t, bridge)
	_, err := bridge.Write([]byte(`{"level":"info","message":"m"}`))
	require.NoError(t, err)
	require.NoError(t, logProvider.ForceFlush(context.Background()))

	require.NotEmpty(t, wrapped.batches)
	require.NotEmpty(t, wrapped.batches[0])

	recordRes := wrapped.batches[0][0].Resource()
	require.NotNil(t, recordRes)

	resourceAttrs := make(map[attribute.Key]attribute.Value, len(recordRes.Attributes()))
	for _, kv := range recordRes.Attributes() {
		resourceAttrs[kv.Key] = kv.Value
	}
	for _, key := range identityResourceKeys {
		assert.Contains(t, resourceAttrs, key, "%s must ride the OTLP resource block", key)
	}
	assert.Equal(t, attribute.StringValue("real-svc"), resourceAttrs[serviceNameKey])
}

// exportThroughBridge runs one zerolog JSON line through the real OTel bridge with the
// production wiring shape: identity on the provider resource, the enricher stamping only
// the log.type delta. Returns the records a backend would see.
func exportThroughBridge(t *testing.T, res *resource.Resource, line string) []sdklog.Record {
	t.Helper()

	wrapped := &fakeLogExporter{}
	enriched := newProcessorAttributeExporter(wrapped, attribute.String(logTypeKey, logTypeTrace))
	logProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(enriched)),
	)
	t.Cleanup(func() {
		require.NoError(t, logProvider.Shutdown(context.Background()))
	})

	bridge := logger.NewOTelBridge(logProvider)
	require.NotNil(t, bridge)

	_, err := bridge.Write([]byte(line))
	require.NoError(t, err)
	require.NoError(t, logProvider.ForceFlush(context.Background()))

	var records []sdklog.Record
	for _, batch := range wrapped.batches {
		records = append(records, batch...)
	}
	return records
}

// The composed invariant behind the bridge's reserved-namespace remap (#915) after ADR-056:
// the bridge frees the reserved key, the caller's value survives under the app. prefix, and
// the framework's true identity reaches the backend on the resource block — not as a
// record-level duplicate.
func TestExportedRecordCarriesIdentityOnlyInResourceBlock(t *testing.T) {
	res := identityTestResource(t)

	records := exportThroughBridge(t, res, `{"level":"info","message":"m","service.name":"spoofed-svc"}`)
	require.NotEmpty(t, records)
	attrs := collectAttrs(&records[0])

	assert.NotContains(t, attrs, serviceNameKey,
		"service.name must not appear at record level: the bridge remapped the caller's value and the enricher no longer duplicates the resource's")

	require.Contains(t, attrs, attribute.Key("app.service.name"), "the caller's field must survive under the app. prefix")
	assert.Equal(t, attribute.StringValue("spoofed-svc"), attrs[attribute.Key("app.service.name")])

	recordRes := records[0].Resource()
	require.NotNil(t, recordRes)
	assert.Contains(t, recordRes.Attributes(), serviceNameKey.String("real-svc"),
		"the framework's true identity must still reach the backend on the resource block")
}

// TestExportedRecordCarriesNoIdentityDuplicates is the #914 regression pin: handing the
// enricher the merged resource again would put every identity key back on every record.
func TestExportedRecordCarriesNoIdentityDuplicates(t *testing.T) {
	res := identityTestResource(t)

	records := exportThroughBridge(t, res, `{"level":"info","message":"m"}`)
	require.NotEmpty(t, records)
	attrs := collectAttrs(&records[0])

	assert.Contains(t, attrs, attribute.Key(logTypeKey), "log.type is the one attribute the processor still stamps")
	for _, key := range identityResourceKeys {
		assert.NotContains(t, attrs, key,
			"%s must appear once per batch in ResourceLogs.resource, never as a record-level duplicate", key)
	}
}

// TestUnlabeledRecordReceivesLogTypeStamp pins why the enricher exists at all: a record emitted
// straight through the OTel API carries no log.type, and the enricher is what labels it. This
// constructs the enricher directly, so it pins the stamping behavior itself — removing the
// enricher from createBatchProcessor is caught by TestCreateBatchProcessorStampsOwnLogType.
func TestUnlabeledRecordReceivesLogTypeStamp(t *testing.T) {
	wrapped := &fakeLogExporter{}
	enricher := newProcessorAttributeExporter(wrapped, attribute.String(logTypeKey, logTypeTrace))

	require.NoError(t, enricher.Export(context.Background(), []sdklog.Record{newTestRecord()}))

	require.Len(t, wrapped.batches, 1)
	require.Len(t, wrapped.batches[0], 1)
	assert.Equal(t, attribute.StringValue(logTypeTrace), collectAttrs(&wrapped.batches[0][0])[logTypeKey])
}

// Drift guard: every identity key the real resource carries must be protected
// by the bridge's reserved-namespace guard. A new resource attribute added in
// createResource (or a semconv rename) that the bridge does not cover fails
// here instead of silently reopening the #915 shadowing hole.
func TestBridgeGuardCoversEveryResourceIdentityKey(t *testing.T) {
	res := identityTestResource(t)

	for _, kv := range res.Attributes() {
		key := string(kv.Key)
		t.Run(key, func(t *testing.T) {
			line := fmt.Sprintf(`{"level":"info","message":"m",%q:"spoofed"}`, key)
			records := exportThroughBridge(t, res, line)
			require.NotEmpty(t, records)
			attrs := collectAttrs(&records[0])

			assert.NotContains(t, attrs, kv.Key,
				"identity key %s appears at record level — either the bridge stopped reserving it or the enricher re-injects identity; extend the bridge's reserved namespaces or fix the enricher delta", key)

			remapped := attribute.Key("app." + key)
			require.Contains(t, attrs, remapped,
				"the caller's value for %s must be preserved under the app. prefix, not dropped", key)
			assert.Equal(t, attribute.StringValue("spoofed"), attrs[remapped])

			recordRes := records[0].Resource()
			require.NotNil(t, recordRes)
			assert.Contains(t, recordRes.Attributes(), kv,
				"the real value for %s must still reach the backend on the resource block", key)
		})
	}
}
