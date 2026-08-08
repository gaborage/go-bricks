package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// stampAttrsInlineCap bounds the stack buffer enrich filters into. The production delta is
// a single attribute — log.type — so this leaves headroom for a second or third without
// reaching the heap.
const stampAttrsInlineCap = 4

// processorAttributeExporter wraps an exporter to inject processor-specific attributes into
// log records. This is necessary because OTel's LoggerProvider holds a single resource for
// all processors, but we need processor-specific attributes (e.g., log.type="action" vs
// log.type="trace"). Service identity is not among them: it rides the resource the provider
// attaches, once per OTLP batch (ADR-056).
type processorAttributeExporter struct {
	wrapped        sdklog.Exporter
	attrs          []attribute.KeyValue
	shutdownOnce   sync.Once
	shutdownResult error
}

// newProcessorAttributeExporter creates an exporter that stamps attrs onto every record that
// does not already carry the key. attrs is only ever read, so aliasing a caller's slice is safe.
func newProcessorAttributeExporter(exporter sdklog.Exporter, attrs ...attribute.KeyValue) sdklog.Exporter {
	return &processorAttributeExporter{
		wrapped: exporter,
		attrs:   attrs,
	}
}

// Export enriches log records with the processor's attributes before exporting.
func (e *processorAttributeExporter) Export(ctx context.Context, records []sdklog.Record) error {
	// Clone and enrich each record to avoid mutating originals
	enriched := make([]sdklog.Record, len(records))
	for i := range records {
		enriched[i] = e.enrich(&records[i])
	}

	return e.wrapped.Export(ctx, enriched)
}

// enrich creates a copy of the record with the processor's attributes injected.
// Uses immutable cloning pattern to prevent race conditions in concurrent scenarios.
func (e *processorAttributeExporter) enrich(rec *sdklog.Record) sdklog.Record {
	clone := rec.Clone()

	if len(e.attrs) == 0 {
		return clone
	}

	// AddAttributes overwrites on a key collision, so an attribute the record already carries
	// has to be dropped here — record attributes win. That is the common case rather than the
	// exception: every record leaves the bridge carrying log.type (logger/otel_bridge.go), and
	// that value is the one dual-mode routing already keyed on.
	//
	// Filtering into a fixed-size array is what removes the per-record allocation: a make()
	// sized from len(e.attrs) is heap-bound even though it never escapes, because its size is
	// not known at compile time. A longer delta just appends onto the heap.
	var inline [stampAttrsInlineCap]attribute.KeyValue
	attrsToAdd := inline[:0]
	for _, attr := range e.attrs {
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
// compares it replaces until roughly a dozen attributes.
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
func (e *processorAttributeExporter) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		e.shutdownResult = e.wrapped.Shutdown(ctx)
	})
	return e.shutdownResult
}

// ForceFlush flushes the wrapped exporter.
func (e *processorAttributeExporter) ForceFlush(ctx context.Context) error {
	return e.wrapped.ForceFlush(ctx)
}
