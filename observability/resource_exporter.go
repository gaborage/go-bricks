package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// resourceAttributeExporter wraps an exporter to inject resource attributes into log records.
// This is necessary because OTel's LoggerProvider uses a single resource for all processors,
// but we need processor-specific attributes (e.g., log.type="action" vs log.type="trace").
type resourceAttributeExporter struct {
	wrapped        sdklog.Exporter
	resourceAttrs  []attribute.KeyValue
	shutdownOnce   sync.Once
	shutdownResult error
}

// newResourceAttributeExporter creates an exporter that enriches records with resource attributes.
func newResourceAttributeExporter(exporter sdklog.Exporter, res *resource.Resource) sdklog.Exporter {
	// Attributes() returns a fresh slice, so it can be retained without aliasing the resource.
	return &resourceAttributeExporter{
		wrapped:       exporter,
		resourceAttrs: res.Attributes(),
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
	existingKeys := make(map[attribute.Key]struct{})
	clone.WalkAttributes(func(kv attribute.KeyValue) bool {
		existingKeys[kv.Key] = struct{}{}
		return true
	})

	// Collect resource attributes that are not already present
	attrsToAdd := make([]attribute.KeyValue, 0, len(e.resourceAttrs))
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
