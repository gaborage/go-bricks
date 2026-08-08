package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// processorAttributeExporter wraps an exporter to inject one processor-specific attribute into
// log records. This is necessary because OTel's LoggerProvider holds a single resource for all
// processors, but we need a per-processor attribute (log.type="action" vs log.type="trace").
// Service identity is not among them: it rides the resource the provider attaches, once per
// OTLP batch (ADR-056).
type processorAttributeExporter struct {
	wrapped        sdklog.Exporter
	attr           attribute.KeyValue
	shutdownOnce   sync.Once
	shutdownResult error
}

// newProcessorAttributeExporter creates an exporter that stamps attr onto every record that does
// not already carry its key.
func newProcessorAttributeExporter(exporter sdklog.Exporter, attr attribute.KeyValue) *processorAttributeExporter {
	return &processorAttributeExporter{
		wrapped: exporter,
		attr:    attr,
	}
}

// Export enriches log records with the processor's attribute before exporting.
func (e *processorAttributeExporter) Export(ctx context.Context, records []sdklog.Record) error {
	enriched := make([]sdklog.Record, len(records))
	for i := range records {
		enriched[i] = e.enrich(&records[i])
	}

	return e.wrapped.Export(ctx, enriched)
}

// enrich returns the record to export, adding the processor's attribute only when the record does
// not already carry that key — the record's own value wins, which is what keeps a caller-set
// log.type authoritative for dual-mode routing.
func (e *processorAttributeExporter) enrich(rec *sdklog.Record) sdklog.Record {
	// The collision branch is the common case, not the exception: every record leaving the bridge
	// carries log.type. It returns a value copy that aliases rec's spilled attribute storage, which
	// is safe on two counts — this branch never mutates, and sdklog.BatchProcessor.OnEmit already
	// cloned the record into its queue, so Export only ever sees copies nobody else observes.
	// Cloning here instead would cost a heap allocation per record for a byte-identical result.
	if recordHasKey(rec, e.attr.Key) {
		return *rec
	}

	clone := rec.Clone()
	clone.AddAttributes(e.attr)
	return clone
}

// recordHasKey reports whether rec already carries key.
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
