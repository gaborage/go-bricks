package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// resourceAttributeExporter wraps an exporter to inject resource attributes into log records.
// This is necessary because OTel's LoggerProvider uses a single resource for all processors,
// but we need processor-specific attributes (e.g., log.type="action" vs log.type="trace").
type resourceAttributeExporter struct {
	wrapped        sdklog.Exporter
	resourceAttrs  []log.KeyValue
	shutdownOnce   sync.Once
	shutdownResult error
}

// newResourceAttributeExporter creates an exporter that enriches records with resource attributes.
func newResourceAttributeExporter(exporter sdklog.Exporter, res *resource.Resource) sdklog.Exporter {
	attrs := res.Attributes()
	converted := make([]log.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		converted = append(converted, convertToLogAttribute(attr))
	}

	return &resourceAttributeExporter{
		wrapped:       exporter,
		resourceAttrs: converted,
	}
}

// Export enriches log records with resource attributes before exporting.
func (e *resourceAttributeExporter) Export(ctx context.Context, records []sdklog.Record) error {
	// Clone and enrich each record to avoid mutating originals
	enriched := make([]sdklog.Record, len(records))
	for i := range records {
		enriched[i] = e.enrichWithResource(&records[i])
	}

	return e.wrapped.Export(ctx, enriched)
}

// enrichWithResource creates a copy of the record with resource attributes injected.
// Uses immutable cloning pattern to prevent race conditions in concurrent scenarios.
func (e *resourceAttributeExporter) enrichWithResource(rec *sdklog.Record) sdklog.Record {
	// Clone the record to avoid mutating the original
	clone := rec.Clone()

	if len(e.resourceAttrs) == 0 {
		return clone
	}

	// Build a lookup of existing attribute keys in the cloned record
	existingKeys := make(map[string]struct{})
	clone.WalkAttributes(func(kv log.KeyValue) bool {
		existingKeys[kv.Key] = struct{}{}
		return true
	})

	// Collect resource attributes that are not already present
	attrsToAdd := make([]log.KeyValue, 0, len(e.resourceAttrs))
	for _, attr := range e.resourceAttrs {
		if _, exists := existingKeys[attr.Key]; exists {
			continue
		}
		attrsToAdd = append(attrsToAdd, attr)
	}

	// Add resource attributes to the cloned record
	if len(attrsToAdd) > 0 {
		clone.AddAttributes(attrsToAdd...)
	}

	return clone
}

// Shutdown shuts down the wrapped exporter.
func (e *resourceAttributeExporter) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		e.shutdownResult = e.wrapped.Shutdown(ctx)
	})
	return e.shutdownResult
}

// ForceFlush flushes the wrapped exporter.
func (e *resourceAttributeExporter) ForceFlush(ctx context.Context) error {
	return e.wrapped.ForceFlush(ctx)
}

// convertToLogAttribute converts an OTel resource attribute.KeyValue to a log.KeyValue.
func convertToLogAttribute(kv attribute.KeyValue) log.KeyValue {
	return log.KeyValue{
		Key:   string(kv.Key),
		Value: convertAttributeValue(kv.Value),
	}
}

// convertAttributeValue converts an attribute.Value to a log.Value.
func convertAttributeValue(v attribute.Value) log.Value {
	switch v.Type() {
	case attribute.BOOL:
		return log.BoolValue(v.AsBool())
	case attribute.INT64:
		return log.Int64Value(v.AsInt64())
	case attribute.FLOAT64:
		return log.Float64Value(v.AsFloat64())
	case attribute.STRING:
		return log.StringValue(v.AsString())
	case attribute.BOOLSLICE:
		bools := v.AsBoolSlice()
		values := make([]log.Value, len(bools))
		for i, b := range bools {
			values[i] = log.BoolValue(b)
		}
		return log.SliceValue(values...)
	case attribute.INT64SLICE:
		ints := v.AsInt64Slice()
		values := make([]log.Value, len(ints))
		for i, n := range ints {
			values[i] = log.Int64Value(n)
		}
		return log.SliceValue(values...)
	case attribute.FLOAT64SLICE:
		floats := v.AsFloat64Slice()
		values := make([]log.Value, len(floats))
		for i, f := range floats {
			values[i] = log.Float64Value(f)
		}
		return log.SliceValue(values...)
	case attribute.STRINGSLICE:
		strings := v.AsStringSlice()
		values := make([]log.Value, len(strings))
		for i, s := range strings {
			values[i] = log.StringValue(s)
		}
		return log.SliceValue(values...)
	default:
		// Fallback for unknown types
		return log.StringValue(v.AsString())
	}
}
