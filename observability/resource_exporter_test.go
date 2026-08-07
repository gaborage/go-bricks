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

func newTestEnricher(t *testing.T, wrapped sdklog.Exporter, resourceAttrs ...attribute.KeyValue) *resourceAttributeExporter {
	t.Helper()
	enricher, ok := newResourceAttributeExporter(wrapped, resource.NewSchemaless(resourceAttrs...)).(*resourceAttributeExporter)
	require.True(t, ok, "newResourceAttributeExporter must return *resourceAttributeExporter")
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

func TestResourceAttributeExporterEnrichWithResource(t *testing.T) {
	tests := []struct {
		name          string
		resourceAttrs []attribute.KeyValue
		recordAttrs   []attribute.KeyValue
		wantAttrs     map[attribute.Key]attribute.Value
	}{
		{
			name:          "no_resource_attributes_returns_clone_unchanged",
			resourceAttrs: nil,
			recordAttrs:   []attribute.KeyValue{attribute.String("msg.id", "abc")},
			wantAttrs: map[attribute.Key]attribute.Value{
				"msg.id": attribute.StringValue("abc"),
			},
		},
		{
			name: "record_without_attributes_receives_all_resource_attributes",
			resourceAttrs: []attribute.KeyValue{
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
			name: "no_collision_adds_every_resource_attribute",
			resourceAttrs: []attribute.KeyValue{
				serviceNameKey.String("billing"),
				deployEnvKey.String("production"),
				hostNameKey.String("node-7"),
			},
			recordAttrs: []attribute.KeyValue{
				attribute.String(logTypeAttr, "action"),
				attribute.Int("attempt", 2),
			},
			wantAttrs: map[attribute.Key]attribute.Value{
				logTypeAttr:    attribute.StringValue("action"),
				"attempt":      attribute.IntValue(2),
				serviceNameKey: attribute.StringValue("billing"),
				deployEnvKey:   attribute.StringValue("production"),
				hostNameKey:    attribute.StringValue("node-7"),
			},
		},
		{
			name: "partial_collision_adds_only_non_colliding_resource_attributes",
			resourceAttrs: []attribute.KeyValue{
				serviceNameKey.String("resource-service"),
				serviceVerKey.String("1.2.3"),
				deployEnvKey.String("production"),
			},
			recordAttrs: []attribute.KeyValue{
				serviceNameKey.String("record-service"),
				attribute.String(logTypeAttr, "trace"),
			},
			wantAttrs: map[attribute.Key]attribute.Value{
				serviceNameKey: attribute.StringValue("record-service"),
				serviceVerKey:  attribute.StringValue("1.2.3"),
				deployEnvKey:   attribute.StringValue("production"),
				logTypeAttr:    attribute.StringValue("trace"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enricher := newTestEnricher(t, &fakeLogExporter{}, tt.resourceAttrs...)
			rec := newTestRecord(tt.recordAttrs...)

			enriched := enricher.enrichWithResource(&rec)

			assert.Equal(t, tt.wantAttrs, collectAttrs(&enriched))
		})
	}
}

// TestResourceAttributeExporterRecordAttributeWinsOverCollidingResourceAttribute pins
// the precedence contract: AddAttributes would overwrite, so the resource value must
// never reach a record that already carries that key.
func TestResourceAttributeExporterRecordAttributeWinsOverCollidingResourceAttribute(t *testing.T) {
	enricher := newTestEnricher(t, &fakeLogExporter{},
		serviceNameKey.String("resource-wins-would-be-a-bug"),
	)
	rec := newTestRecord(serviceNameKey.String("record-value"))

	enriched := enricher.enrichWithResource(&rec)

	assert.Equal(t, attribute.StringValue("record-value"), collectAttrs(&enriched)[serviceNameKey])
}

func TestResourceAttributeExporterEnrichDoesNotMutateOriginal(t *testing.T) {
	enricher := newTestEnricher(t, &fakeLogExporter{},
		serviceNameKey.String("billing"),
		deployEnvKey.String("production"),
	)
	rec := newTestRecord(attribute.String(logTypeAttr, "action"))

	enriched := enricher.enrichWithResource(&rec)

	assert.Equal(t, map[attribute.Key]attribute.Value{
		logTypeAttr: attribute.StringValue("action"),
	}, collectAttrs(&rec))
	assert.Len(t, collectAttrs(&enriched), 3)
}

func TestResourceAttributeExporterExport(t *testing.T) {
	t.Run("enriches_every_record_in_the_batch", func(t *testing.T) {
		wrapped := &fakeLogExporter{}
		enricher := newTestEnricher(t, wrapped, serviceNameKey.String("billing"))

		records := []sdklog.Record{
			newTestRecord(attribute.String(logTypeAttr, "action")),
			newTestRecord(serviceNameKey.String("record-value")),
			newTestRecord(),
		}

		require.NoError(t, enricher.Export(context.Background(), records))

		require.Len(t, wrapped.batches, 1)
		exported := wrapped.batches[0]
		require.Len(t, exported, 3)

		assert.Equal(t, map[attribute.Key]attribute.Value{
			logTypeAttr:    attribute.StringValue("action"),
			serviceNameKey: attribute.StringValue("billing"),
		}, collectAttrs(&exported[0]))
		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("record-value"),
		}, collectAttrs(&exported[1]))
		assert.Equal(t, map[attribute.Key]attribute.Value{
			serviceNameKey: attribute.StringValue("billing"),
		}, collectAttrs(&exported[2]))

		assert.Equal(t, map[attribute.Key]attribute.Value{
			logTypeAttr: attribute.StringValue("action"),
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

// TestResourceAttributeExporterResourceLargerThanInlineBuffer covers the append that
// spills past resourceAttrsInlineCap onto the heap. OTEL_RESOURCE_ATTRIBUTES can inflate a
// resource well beyond the production handful, and the filter must stay correct there —
// including precedence, which is what an off-by-one in the spill would break first.
func TestResourceAttributeExporterResourceLargerThanInlineBuffer(t *testing.T) {
	const resourceCount = resourceAttrsInlineCap * 2

	resourceAttrs := make([]attribute.KeyValue, 0, resourceCount)
	for i := range resourceCount {
		resourceAttrs = append(resourceAttrs, attribute.String(fmt.Sprintf("res.k%02d", i), fmt.Sprintf("resource-%d", i)))
	}
	enricher := newTestEnricher(t, &fakeLogExporter{}, resourceAttrs...)

	// One key is shadowed by the record, and it sits inside the inline range so the
	// spilled tail has to stay aligned with the resource slice after the drop.
	rec := newTestRecord(attribute.String("res.k03", "record-value"))

	enriched := enricher.enrichWithResource(&rec)

	attrs := collectAttrs(&enriched)
	assert.Len(t, attrs, resourceCount)
	assert.Equal(t, attribute.StringValue("record-value"), attrs["res.k03"])
	assert.Equal(t, attribute.StringValue("resource-0"), attrs["res.k00"])
	assert.Equal(t, attribute.StringValue("resource-31"), attrs["res.k31"])
	assert.Equal(t, resourceAttrs, enricher.resourceAttrs, "the exporter's own slice must not be filtered in place")
}

func TestResourceAttributeExporterShutdown(t *testing.T) {
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

func TestResourceAttributeExporterForceFlush(t *testing.T) {
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

// typicalEnricher mirrors what createLogResource hands the exporter in production: the
// service identity and telemetry.sdk.* attributes from resource.Default, plus the log.type
// that makes this resource processor-specific in the first place.
func typicalEnricher() *resourceAttributeExporter {
	return &resourceAttributeExporter{
		wrapped: &fakeLogExporter{},
		resourceAttrs: []attribute.KeyValue{
			attribute.String(logTypeAttr, "trace"),
			serviceNameKey.String("billing"),
			serviceVerKey.String("1.2.3"),
			deployEnvKey.String("production"),
			hostNameKey.String("node-7"),
			attribute.String("telemetry.sdk.name", "opentelemetry"),
			attribute.String("telemetry.sdk.language", "go"),
		},
	}
}

// typicalRecord is what the OTel bridge emits: application attributes plus the log.type it
// stamps on every record, which collides with the resource's own log.type.
func typicalRecord() sdklog.Record {
	return newTestRecord(
		attribute.String(logTypeAttr, "action"),
		attribute.String("http.method", "POST"),
		attribute.Int("http.status_code", 201),
	)
}

// shadowingRecord additionally carries a key from the service identity, so two resource
// attributes get dropped rather than one.
func shadowingRecord() sdklog.Record {
	return newTestRecord(
		attribute.String(logTypeAttr, "action"),
		serviceNameKey.String("record-value"),
		attribute.Int("http.status_code", 201),
	)
}

func BenchmarkResourceAttributeExporterEnrichWithResource(b *testing.B) {
	enricher := typicalEnricher()

	benchmarks := []struct {
		name   string
		record func() sdklog.Record
	}{
		{name: "typical_record", record: typicalRecord},
		{name: "record_shadows_service_name", record: shadowingRecord},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			rec := bm.record()
			b.ReportAllocs()
			for b.Loop() {
				_ = enricher.enrichWithResource(&rec)
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

// exportThroughBridge runs one zerolog JSON line through the real OTel bridge
// and a resource-enriching exporter, returning the records a backend would see.
func exportThroughBridge(t *testing.T, res *resource.Resource, line string) []sdklog.Record {
	t.Helper()

	wrapped := &fakeLogExporter{}
	enriched := newResourceAttributeExporter(wrapped, res)
	logProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(enriched)))
	t.Cleanup(func() {
		_ = logProvider.Shutdown(context.Background())
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

// The composed invariant behind the bridge's reserved-namespace remap (#915):
// because the bridge frees the reserved key, this exporter's record-over-resource
// precedence backfills the framework's true identity, while the caller's value
// survives under the app. prefix.
func TestResourceEnrichmentRestoresIdentityAfterBridgeRemap(t *testing.T) {
	res := identityTestResource(t)

	records := exportThroughBridge(t, res, `{"level":"info","message":"m","service.name":"spoofed-svc"}`)
	require.NotEmpty(t, records)
	attrs := collectAttrs(&records[0])

	require.Contains(t, attrs, serviceNameKey, "resource identity must be backfilled on the exported record")
	assert.Equal(t, attribute.StringValue("real-svc"), attrs[serviceNameKey],
		"the exported service.name must be the resource's, not the caller's")

	require.Contains(t, attrs, attribute.Key("app.service.name"), "the caller's field must survive under the app. prefix")
	assert.Equal(t, attribute.StringValue("spoofed-svc"), attrs[attribute.Key("app.service.name")])
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

			require.Contains(t, attrs, kv.Key, "identity key %s must reach the backend", key)
			assert.NotEqual(t, attribute.StringValue("spoofed"), attrs[kv.Key],
				"resource identity key %s is spoofable at record level — extend the bridge's reserved namespaces", key)

			remapped := attribute.Key("app." + key)
			require.Contains(t, attrs, remapped,
				"the caller's value for %s must be preserved under the app. prefix, not dropped", key)
			assert.Equal(t, attribute.StringValue("spoofed"), attrs[remapped])
		})
	}
}
