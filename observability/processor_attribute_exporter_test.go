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

func newTestEnricher(t *testing.T, wrapped sdklog.Exporter, stampAttrs ...attribute.KeyValue) *processorAttributeExporter {
	t.Helper()
	enricher, ok := newProcessorAttributeExporter(wrapped, stampAttrs...).(*processorAttributeExporter)
	require.True(t, ok, "newProcessorAttributeExporter must return *processorAttributeExporter")
	return enricher
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
		stampAttrs  []attribute.KeyValue
		recordAttrs []attribute.KeyValue
		wantAttrs   map[attribute.Key]attribute.Value
	}{
		{
			name:        "no_stamp_attributes_returns_clone_unchanged",
			stampAttrs:  nil,
			recordAttrs: []attribute.KeyValue{attribute.String("msg.id", "abc")},
			wantAttrs: map[attribute.Key]attribute.Value{
				"msg.id": attribute.StringValue("abc"),
			},
		},
		{
			name: "record_without_attributes_receives_all_stamp_attributes",
			stampAttrs: []attribute.KeyValue{
				serviceNameKey.String("billing"),
				serviceVerKey.String("1.2.3"),
			},
			recordAttrs: nil,
			wantAttrs: map[attribute.Key]attribute.Value{
				serviceNameKey: attribute.StringValue("billing"),
				serviceVerKey:  attribute.StringValue("1.2.3"),
			},
		},
		{
			name: "no_collision_adds_every_stamp_attribute",
			stampAttrs: []attribute.KeyValue{
				serviceNameKey.String("billing"),
				deployEnvKey.String("production"),
				hostNameKey.String("node-7"),
			},
			recordAttrs: []attribute.KeyValue{
				attribute.String(logTypeKey, logTypeAction),
				attribute.Int("attempt", 2),
			},
			wantAttrs: map[attribute.Key]attribute.Value{
				logTypeKey:     attribute.StringValue(logTypeAction),
				"attempt":      attribute.IntValue(2),
				serviceNameKey: attribute.StringValue("billing"),
				deployEnvKey:   attribute.StringValue("production"),
				hostNameKey:    attribute.StringValue("node-7"),
			},
		},
		{
			name: "partial_collision_adds_only_non_colliding_stamp_attributes",
			stampAttrs: []attribute.KeyValue{
				serviceNameKey.String("stamped-service"),
				serviceVerKey.String("1.2.3"),
				deployEnvKey.String("production"),
			},
			recordAttrs: []attribute.KeyValue{
				serviceNameKey.String("record-service"),
				attribute.String(logTypeKey, logTypeTrace),
			},
			wantAttrs: map[attribute.Key]attribute.Value{
				serviceNameKey: attribute.StringValue("record-service"),
				serviceVerKey:  attribute.StringValue("1.2.3"),
				deployEnvKey:   attribute.StringValue("production"),
				logTypeKey:     attribute.StringValue(logTypeTrace),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enricher := newTestEnricher(t, &fakeLogExporter{}, tt.stampAttrs...)
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
	enricher := newTestEnricher(t, &fakeLogExporter{},
		attribute.String(logTypeKey, "stamp-wins-would-be-a-bug"),
	)
	rec := newTestRecord(attribute.String(logTypeKey, logTypeAction))

	enriched := enricher.enrich(&rec)

	assert.Equal(t, attribute.StringValue(logTypeAction), collectAttrs(&enriched)[logTypeKey])
}

func TestProcessorAttributeExporterEnrichDoesNotMutateOriginal(t *testing.T) {
	enricher := newTestEnricher(t, &fakeLogExporter{},
		serviceNameKey.String("billing"),
		deployEnvKey.String("production"),
	)
	rec := newTestRecord(attribute.String(logTypeKey, logTypeAction))

	enriched := enricher.enrich(&rec)

	assert.Equal(t, map[attribute.Key]attribute.Value{
		logTypeKey: attribute.StringValue(logTypeAction),
	}, collectAttrs(&rec))
	assert.Len(t, collectAttrs(&enriched), 3)
}

func TestProcessorAttributeExporterExport(t *testing.T) {
	t.Run("enriches_every_record_in_the_batch", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTestEnricher(t, wrapped, serviceNameKey.String("billing"))

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
			logTypeKey:     attribute.StringValue(logTypeAction),
			serviceNameKey: attribute.StringValue("billing"),
		}, collectAttrs(&exported[0]))
		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("record-value"),
		}, collectAttrs(&exported[1]))
		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("billing"),
		}, collectAttrs(&exported[2]))

		assert.Equal(t, map[attribute.Key]attribute.Value{
			logTypeKey: attribute.StringValue(logTypeAction),
		}, collectAttrs(&records[0]), "originals must stay untouched")
	})

	t.Run("propagates_wrapped_exporter_error", func(t *testing.T) {
		wantErr := errors.New("export failed")
		wrapped := &fakeLogExporter{exportErr: wantErr}
		enricher := newTestEnricher(t, wrapped, serviceNameKey.String("billing"))

		err := enricher.Export(context.Background(), []sdklog.Record{newTestRecord()})

		assert.ErrorIs(t, err, wantErr)
		assert.Len(t, wrapped.batches, 1)
	})
}

// TestProcessorAttributeExporterMoreAttrsThanInlineBuffer covers the append that spills past
// stampAttrsInlineCap onto the heap. Production stamps a single attribute, but the constructor
// is variadic, so the spill path stays reachable and must stay correct — including precedence,
// which is what an off-by-one in the spill would break first.
func TestProcessorAttributeExporterMoreAttrsThanInlineBuffer(t *testing.T) {
	const stampCount = stampAttrsInlineCap * 2

	stampAttrs := make([]attribute.KeyValue, 0, stampCount)
	for i := range stampCount {
		stampAttrs = append(stampAttrs, attribute.String(fmt.Sprintf("res.k%02d", i), fmt.Sprintf("resource-%d", i)))
	}
	enricher := newTestEnricher(t, &fakeLogExporter{}, stampAttrs...)

	// One key is shadowed by the record, and it sits inside the inline range so the
	// spilled tail has to stay aligned with the stamp slice after the drop.
	rec := newTestRecord(attribute.String("res.k03", "record-value"))

	enriched := enricher.enrich(&rec)

	attrs := collectAttrs(&enriched)
	assert.Len(t, attrs, stampCount)
	assert.Equal(t, attribute.StringValue("record-value"), attrs["res.k03"])
	assert.Equal(t, attribute.StringValue("resource-0"), attrs["res.k00"])

	lastKey := attribute.Key(fmt.Sprintf("res.k%02d", stampCount-1))
	assert.Equal(t, attribute.StringValue(fmt.Sprintf("resource-%d", stampCount-1)), attrs[lastKey])
	assert.Equal(t, stampAttrs, enricher.attrs, "the exporter's own slice must not be filtered in place")
}

func TestProcessorAttributeExporterShutdown(t *testing.T) {
	t.Run("delegates_once_and_memoizes_result", func(t *testing.T) {
		wantErr := errors.New("shutdown failed")
		wrapped := &fakeLogExporter{shutdownErr: wantErr}
		enricher := newTestEnricher(t, wrapped)

		first := enricher.Shutdown(context.Background())
		second := enricher.Shutdown(context.Background())

		assert.ErrorIs(t, first, wantErr)
		assert.ErrorIs(t, second, wantErr)
		assert.Equal(t, 1, wrapped.shutdownCount)
	})

	t.Run("memoizes_nil_result", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTestEnricher(t, wrapped)

		assert.NoError(t, enricher.Shutdown(context.Background()))
		assert.NoError(t, enricher.Shutdown(context.Background()))
		assert.Equal(t, 1, wrapped.shutdownCount)
	})
}

func TestProcessorAttributeExporterForceFlush(t *testing.T) {
	t.Run("delegates_to_wrapped", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTestEnricher(t, wrapped)

		assert.NoError(t, enricher.ForceFlush(context.Background()))
		assert.Equal(t, 1, wrapped.flushCount)
	})

	t.Run("propagates_wrapped_exporter_error", func(t *testing.T) {
		wantErr := errors.New("flush failed")
		wrapped := &fakeLogExporter{flushErr: wantErr}
		enricher := newTestEnricher(t, wrapped)

		assert.ErrorIs(t, enricher.ForceFlush(context.Background()), wantErr)
		assert.Equal(t, 1, wrapped.flushCount)
	})
}

// typicalEnricher mirrors what createBatchProcessor hands the exporter in production: the
// single log.type delta that makes this processor distinguishable. Service identity is
// absent by design — it rides the provider's resource, once per batch.
func typicalEnricher() *processorAttributeExporter {
	return &processorAttributeExporter{
		wrapped: &fakeLogExporter{},
		attrs:   []attribute.KeyValue{attribute.String(logTypeKey, logTypeTrace)},
	}
}

// typicalRecord is what the OTel bridge emits: application attributes plus the log.type it
// stamps on every record, which collides with the processor's own log.type.
func typicalRecord() sdklog.Record {
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
	enricher := typicalEnricher()

	benchmarks := []struct {
		name   string
		record func() sdklog.Record
	}{
		{name: "record_carries_log_type", record: typicalRecord},
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

func identityTestResource(t *testing.T) *resource.Resource {
	t.Helper()
	p := &provider{
		config: Config{
			Service: ServiceConfig{
				Name:    "real-svc",
				Version: "1.2.3",
			},
			Environment: "production",
		},
	}
	res, err := p.createResource(context.Background())
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

// TestUnlabeledRecordReceivesLogTypeStamp pins why the enricher exists at all: a record
// emitted straight through the OTel API carries no log.type, and the trace processor's
// enricher is what labels it. Deleting the enricher must fail here.
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
