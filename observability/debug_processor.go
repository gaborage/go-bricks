package observability

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// debugSpanProcessor wraps a span processor to add lifecycle logging.
// This provides visibility into span creation, ending, and export batching.
// Useful for debugging why spans might not be exported as expected.
type debugSpanProcessor struct {
	wrapped sdktrace.SpanProcessor
}

// newDebugSpanProcessor wraps a span processor with debug logging.
func newDebugSpanProcessor(wrapped sdktrace.SpanProcessor) sdktrace.SpanProcessor {
	return &debugSpanProcessor{wrapped: wrapped}
}

// OnStart is called when a span starts.
// This is the proof that spans ARE being created by the middleware.
func (d *debugSpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	debugLogger.Printf("[SPAN] STARTED: %s (trace=%s, span=%s)",
		s.Name(), s.SpanContext().TraceID(), s.SpanContext().SpanID())
	d.wrapped.OnStart(parent, s)
}

// OnEnd is called when a span ends.
// For BatchSpanProcessor, this queues the span for eventual export.
func (d *debugSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	debugLogger.Printf("[SPAN] ENDED: %s (trace=%s, duration=%v)",
		s.Name(), s.SpanContext().TraceID(), s.EndTime().Sub(s.StartTime()))
	d.wrapped.OnEnd(s)
}

// Shutdown shuts down the wrapped processor.
func (d *debugSpanProcessor) Shutdown(ctx context.Context) error {
	debugLogger.Println("[SPAN] Processor shutting down")
	return d.wrapped.Shutdown(ctx)
}

// ForceFlush flushes the wrapped processor.
func (d *debugSpanProcessor) ForceFlush(ctx context.Context) error {
	debugLogger.Println("[SPAN] Forcing flush of pending spans")
	return d.wrapped.ForceFlush(ctx)
}
