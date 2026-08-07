package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// resourceAttrsInlineCap bounds the stack buffer enrichWithResource filters into. A
// production resource carries about seven attributes — service identity, telemetry.sdk.*,
// and log.type — so this covers the real shape with room to spare.
const resourceAttrsInlineCap = 16

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
	clone := rec.Clone()

	if len(e.resourceAttrs) == 0 {
		return clone
	}

	// AddAttributes overwrites on a key collision, so a resource attribute the record
	// already carries has to be dropped here — record attributes win. A collision is the
	// rule rather than the exception: every record leaves the bridge carrying log.type
	// (logger/otel_bridge.go) and every enricher resource declares one (createLogResource).
	//
	// Filtering into a fixed-size array is what removes the per-record allocation: a make()
	// sized from len(e.resourceAttrs) is heap-bound even though it never escapes, because
	// its size is not known at compile time. A larger resource just appends onto the heap.
	var inline [resourceAttrsInlineCap]attribute.KeyValue
	attrsToAdd := inline[:0]
	for _, attr := range e.resourceAttrs {
		if !recordHasKey(&clone, attr.Key) {
			attrsToAdd = append(attrsToAdd, attr)
		}
	}

	if len(attrsToAdd) > 0 {
		clone.AddAttributes(attrsToAdd...)
	}

	return clone
}

// recordHasKey reports whether rec already carries key. Scanning beats indexing at the
// sizes involved — hashing an attribute.Key costs more than the handful of string
// compares it replaces until roughly a dozen resource attributes.
func recordHasKey(rec *sdklog.Record, key attribute.Key) bool {
	found := false
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if kv.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
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
