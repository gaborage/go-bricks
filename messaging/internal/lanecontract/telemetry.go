package lanecontract

import (
	"encoding/hex"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// The metrics the pipeline records for one delivery, and the attributes both
// lanes' spans and records carry.
const (
	metricConsumeDuration = "messaging.client.operation.duration"
	metricConsumeCount    = "messaging.client.consumed.messages"

	attrSystem      = "messaging.system"
	attrOperation   = "messaging.operation.name"
	attrDestination = "messaging.destination.name"
	attrBodySize    = "messaging.message.body.size"
	attrErrorType   = "error.type"

	spanOperation = "receive"
)

// AssertTelemetry checks what one delivery reported to OpenTelemetry: one
// consumer span of the shared shape, one consume record at completion, and an
// exemplar tying the record back to that span.
//
// The exemplar resolves to a per-message ORPHAN trace, because the consume span
// is deliberately a root on both lanes — re-parenting a consume trace onto its
// producer is out of scope. So this asserts the exemplar EXISTS and names the
// span, and nothing about what the trace contains.
func AssertTelemetry(t T, lane *Lane, scenario *Scenario, observed *Observed) {
	t.Helper()

	require.Len(t, observed.Spans, 1, "one delivery opens exactly one consumer span")
	span := observed.Spans[0]

	assert.Equal(t, lane.Destination+" "+spanOperation, span.Name,
		"the span is named <destination> receive on both lanes")
	assert.Equal(t, oteltrace.SpanKindConsumer, span.SpanKind, "a consume span is Consumer-kind")
	assert.False(t, span.Parent.IsValid(),
		"the consume span is a root: re-parenting onto the producer is out of scope, so its exemplar names an orphan trace")

	assertSpanAttributes(t, lane, scenario, &span)
	assertConsumeRecord(t, scenario, observed, &span)
}

// assertSpanAttributes pins the four attributes both lanes set and confines the
// lane's own to what it declared.
func assertSpanAttributes(t T, lane *Lane, scenario *Scenario, span *sdktrace.SpanStub) {
	t.Helper()

	assertAttr(t, span.Attributes, attrSystem, "rabbitmq")
	assertAttr(t, span.Attributes, attrOperation, spanOperation)
	assertAttr(t, span.Attributes, attrDestination, lane.Destination)
	assertAttr(t, span.Attributes, attrBodySize, int64(len(scenario.Body)))

	shared := []string{attrSystem, attrOperation, attrDestination, attrBodySize}
	var extras []string
	for _, kv := range span.Attributes {
		if key := string(kv.Key); !slices.Contains(shared, key) {
			extras = append(extras, key)
		}
	}
	for _, key := range extras {
		assert.Contains(t, lane.SpanExtraKeys, key,
			"the span carries attribute %q, which the lane did not declare in SpanExtraKeys", key)
	}
}

// assertConsumeRecord pins one record at completion, error.type iff the delivery
// failed, an exemplar naming the span, and no per-message value on any attribute.
func assertConsumeRecord(t T, scenario *Scenario, observed *Observed, span *sdktrace.SpanStub) {
	t.Helper()

	points := consumeDataPoints(t, observed)
	require.Len(t, points, 1, "one delivery records exactly one %s data point", metricConsumeCount)
	assert.Equal(t, int64(1), points[0].Value, "the record counts the delivery once")

	failed := scenario.Outcome != delivery.Succeeded
	_, hasErrorType := points[0].Attributes.Value(attrErrorType)
	assert.Equal(t, failed, hasErrorType, "error.type is present exactly when the delivery failed")
	assertNoPerMessageValue(t, scenario, observed, points[0].Attributes)

	// Exemplars are live by default (the SDK's zero-config filter is trace-based
	// and the sampler is always-on here), so a missing one means the record was
	// taken outside the span rather than at completion inside it.
	exemplars := obtest.SumExemplars[int64](t, observed.Metrics, metricConsumeCount)
	require.NotEmpty(t, exemplars, "the consume record carries an exemplar, so it was recorded inside the span")
	// The span ID is the load-bearing half: a trace holds many spans, so matching
	// only the trace would accept a record taken under a SIBLING span. Both IDs
	// are []byte on the exemplar and fixed-size arrays on the span context, so
	// compare hex renderings — assert.Equal across those types always fails.
	assert.Equal(t, span.SpanContext.SpanID().String(), hex.EncodeToString(exemplars[0].SpanID),
		"the exemplar names the delivery's own span, not a sibling in the same trace")
	assert.Equal(t, span.SpanContext.TraceID().String(), hex.EncodeToString(exemplars[0].TraceID),
		"the exemplar names the delivery's own trace")
}

// assertNoPerMessageValue keeps a per-message value off the metric attributes.
// The trace ID is the one every lane has to hand and the one that would do the
// damage: the SDK's cardinality limit is 2000 attribute sets, past which every
// further set is silently replaced by otel.metric.overflow=true — no error, no
// log, aggregation destroyed.
func assertNoPerMessageValue(t T, scenario *Scenario, observed *Observed, attrs attribute.Set) {
	t.Helper()

	// Prefer the id the carrier declared over the one the handler read: a lane
	// that drops the carrier reports an empty handler id, and comparing against
	// that would make this check vacuous on exactly the lane it must catch.
	perMessage := observed.HandlerTraceID
	if carried, declared := carriedTraceID(scenario.Carrier); declared {
		perMessage = carried
	}
	for _, kv := range attrs.ToSlice() {
		assert.NotEqual(t, perMessage, kv.Value.String(),
			"metric attribute %q carries the per-message trace ID, which would blow the SDK's cardinality limit", kv.Key)
	}
}

// consumeDataPoints is the consume counter's data points, failing loudly when
// the metric is absent or a different shape rather than yielding an empty slice
// a caller would read as "nothing recorded".
func consumeDataPoints(t T, observed *Observed) []metricdata.DataPoint[int64] {
	t.Helper()

	metric := obtest.FindMetric(observed.Metrics, metricConsumeCount)
	require.NotNil(t, metric, "one delivery records one %s", metricConsumeCount)

	data, ok := metric.Data.(metricdata.Sum[int64])
	require.True(t, ok, "metric %s is %T, not an int64 sum", metricConsumeCount, metric.Data)

	return data.DataPoints
}

// RunTelemetry puts a lane through every identity scenario and asserts what it
// reported. It reuses IdentityScenarios so the two families provably cover the
// same deliveries rather than drifting into separate sets.
func RunTelemetry(t *testing.T, lane *Lane) {
	t.Helper()

	for _, scenario := range IdentityScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			observed := lane.Deliver(t, scenario)

			AssertTelemetry(t, lane, &scenario, &observed)
		})
	}
}

// assertAttr reports the attribute's value, failing when it is absent rather
// than when it merely differs — an absent attribute and a wrong one are
// different defects and a lane can have either.
func assertAttr(t T, attrs []attribute.KeyValue, key string, want any) {
	t.Helper()

	for _, kv := range attrs {
		if string(kv.Key) == key {
			assert.Equal(t, want, kv.Value.AsInterface(), "span attribute %s has the wrong value", key)
			return
		}
	}
	t.Errorf("span attribute %s is absent", key)
}
