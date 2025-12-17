package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

const (
	logTypeAction = "action"
	logTypeTrace  = "trace"
)

// DualModeLogProcessor routes log records to different processors based on log.type attribute.
// It implements the dual-mode logging architecture where:
//   - Action logs (log.type="action") are exported with all severities (100% sampling)
//   - Trace logs (log.type="trace"): ERROR/WARN always exported, INFO/DEBUG sampled by rate
type DualModeLogProcessor struct {
	actionProcessor sdklog.Processor // Handles action logs (request summaries)
	traceProcessor  sdklog.Processor // Handles trace logs (application debug logs)
	samplingRate    float64          // Sampling rate for INFO/DEBUG trace logs (0.0-1.0)
}

// NewDualModeLogProcessor creates a new dual-mode log processor.
// samplingRate controls what fraction of INFO/DEBUG trace logs to export (0.0 to 1.0).
// ERROR/WARN logs and action logs are always exported at 100%.
func NewDualModeLogProcessor(actionProcessor, traceProcessor sdklog.Processor, samplingRate float64) *DualModeLogProcessor {
	if actionProcessor == nil {
		panic("observability: actionProcessor cannot be nil") //nolint:S8148 // NOSONAR: Fail-fast on invalid initialization (manifesto: configuration errors crash at startup)
	}
	if traceProcessor == nil {
		panic("observability: traceProcessor cannot be nil") //nolint:S8148 // NOSONAR: Fail-fast on invalid initialization (manifesto: configuration errors crash at startup)
	}

	return &DualModeLogProcessor{
		actionProcessor: actionProcessor,
		traceProcessor:  traceProcessor,
		samplingRate:    samplingRate,
	}
}

// OnEmit routes the log record to the appropriate processor based on log.type attribute.
func (p *DualModeLogProcessor) OnEmit(ctx context.Context, rec *sdklog.Record) error {
	// Enrich record with trace context from context parameter
	enrichTraceContext(ctx, rec)

	logType := extractLogType(rec)

	// Action logs: export all severities (INFO, WARN, ERROR)
	if logType == logTypeAction {
		return p.actionProcessor.OnEmit(ctx, rec)
	}

	// Trace logs and unknown types: ERROR/WARN always exported
	// Note: OpenTelemetry severity levels: Trace=1, Debug=5, Info=9, Warn=13, Error=17, Fatal=21
	if rec.Severity() >= log.SeverityWarn { // 13 = WARN
		return p.traceProcessor.OnEmit(ctx, rec)
	}

	// INFO/DEBUG logs: apply deterministic trace-based sampling
	if p.shouldSample(rec) {
		return p.traceProcessor.OnEmit(ctx, rec)
	}

	// Drop unsampled INFO/DEBUG logs
	return nil
}

// Enabled checks if the processor should process logs with the given parameters.
// The EnabledParameters only provides severity and scope (not full record attributes),
// so we use severity-based pre-filtering. Full routing (action vs trace logs)
// happens in OnEmit where we have access to the complete record.
//
//nolint:gocritic // hugeParam: EnabledParameters passed by value per OTel SDK interface contract
func (p *DualModeLogProcessor) Enabled(_ context.Context, param sdklog.EnabledParameters) bool {
	// ERROR/WARN: always enabled (exported regardless of sampling)
	if param.Severity >= log.SeverityWarn {
		return true
	}

	// INFO/DEBUG: enabled if sampling rate > 0
	// Note: Action logs are always exported via OnEmit routing, but we can't
	// distinguish them here (no access to log.type attribute in EnabledParameters)
	return p.samplingRate > 0
}

// shouldSample determines if an INFO/DEBUG log should be sampled based on trace ID.
// Uses deterministic sampling: all logs in the same trace are sampled together.
func (p *DualModeLogProcessor) shouldSample(rec *sdklog.Record) bool {
	// Fast path: rate 0 drops all, rate 1 keeps all
	if p.samplingRate <= 0 {
		return false
	}
	if p.samplingRate >= 1.0 {
		return true
	}

	// Deterministic sampling based on trace ID
	// Use the first 8 bytes of trace ID as uint64 for consistent hashing
	traceID := rec.TraceID()
	if !traceID.IsValid() {
		// No trace ID: fall back to random-ish sampling using record timestamp
		ts := rec.Timestamp().UnixNano()
		if ts < 0 {
			ts = 0
		}
		return uint64(ts)%100 < uint64(p.samplingRate*100) //nolint:gosec // G115 - ts guaranteed non-negative by guard above
	}

	// Use first 8 bytes of trace ID for deterministic sampling
	// This ensures all logs in the same trace are sampled together
	traceBytes := traceID[:]
	hash := uint64(traceBytes[0]) | uint64(traceBytes[1])<<8 | uint64(traceBytes[2])<<16 | uint64(traceBytes[3])<<24 |
		uint64(traceBytes[4])<<32 | uint64(traceBytes[5])<<40 | uint64(traceBytes[6])<<48 | uint64(traceBytes[7])<<56

	return hash%100 < uint64(p.samplingRate*100)
}

// Shutdown shuts down both processors.
func (p *DualModeLogProcessor) Shutdown(ctx context.Context) error {
	// Shutdown both processors, return first error encountered
	errAction := p.actionProcessor.Shutdown(ctx)
	errTrace := p.traceProcessor.Shutdown(ctx)

	return errors.Join(errTrace, errAction)
}

// ForceFlush flushes both processors.
func (p *DualModeLogProcessor) ForceFlush(ctx context.Context) error {
	// Flush both processors, return first error encountered
	errAction := p.actionProcessor.ForceFlush(ctx)
	errTrace := p.traceProcessor.ForceFlush(ctx)

	return errors.Join(errTrace, errAction)
}

// extractLogType safely extracts the log.type attribute from a record.
// Returns "trace" as default if the attribute is not found (for third-party/legacy logs).
func extractLogType(rec *sdklog.Record) string {
	logType := logTypeTrace // Default to trace logs

	rec.WalkAttributes(func(kv log.KeyValue) bool {
		if kv.Key == "log.type" {
			if kv.Value.Kind() == log.KindString {
				logType = kv.Value.AsString()
			}
			return false // Stop iteration once found
		}
		return true // Continue searching
	})

	return logType
}

// enrichTraceContext populates the SDK log record's canonical trace fields (TraceID, SpanID, TraceFlags)
// from two sources (in order of preference):
//  1. Span context in the provided context (primary source from active traces)
//  2. String attributes "trace_id" and "span_id" in the log record (fallback for parsed logs)
//
// This dual-source approach ensures canonical fields are populated even when:
//   - Logs are parsed from JSON without active trace context (OTelBridge)
//   - Logs are forwarded through async processors that may lose context
//   - External systems emit logs with trace correlation attributes
//
// The trace IDs are also kept as string attributes for text-based queryability.
func enrichTraceContext(ctx context.Context, rec *sdklog.Record) {
	// Primary source: Extract from context if available
	if enrichFromContext(ctx, rec) {
		return
	}

	// Fallback source: Extract from record attributes when context doesn't have trace
	enrichFromAttributes(rec)
}

// enrichFromContext populates canonical trace fields from the span context.
// Returns true if fields were successfully populated.
func enrichFromContext(ctx context.Context, rec *sdklog.Record) bool {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return false
	}

	traceID := spanCtx.TraceID()
	spanID := spanCtx.SpanID()
	if !traceID.IsValid() || !spanID.IsValid() {
		return false
	}

	rec.SetTraceID(traceID)
	rec.SetSpanID(spanID)
	rec.SetTraceFlags(spanCtx.TraceFlags())
	return true
}

// traceAttributeCollector holds parsed trace correlation attributes.
type traceAttributeCollector struct {
	traceIDStr   string
	spanIDStr    string
	traceFlags   trace.TraceFlags
	foundTraceID bool
	foundSpanID  bool
	foundFlags   bool
}

// collect extracts trace correlation attributes from a key-value pair.
// Returns false to stop iteration when all required attributes are found.
func (c *traceAttributeCollector) collect(kv log.KeyValue) bool {
	switch kv.Key {
	case "trace_id":
		if kv.Value.Kind() == log.KindString {
			c.traceIDStr = kv.Value.AsString()
			c.foundTraceID = true
		}
	case "span_id":
		if kv.Value.Kind() == log.KindString {
			c.spanIDStr = kv.Value.AsString()
			c.foundSpanID = true
		}
	case "trace_flags":
		if kv.Value.Kind() == log.KindInt64 {
			flagsInt := kv.Value.AsInt64()
			if flagsInt >= 0 && flagsInt <= 255 {
				c.traceFlags = trace.TraceFlags(uint8(flagsInt))
				c.foundFlags = true
			}
		}
	}
	// Continue iteration until all required fields found
	return !c.foundTraceID || !c.foundSpanID || !c.foundFlags
}

// enrichFromAttributes populates canonical trace fields from log record attributes.
// This handles logs parsed from JSON (OTelBridge) or forwarded from external systems.
func enrichFromAttributes(rec *sdklog.Record) {
	collector := &traceAttributeCollector{}
	rec.WalkAttributes(collector.collect)

	if !collector.foundTraceID || !collector.foundSpanID {
		return
	}

	// Parse and validate trace ID
	traceID, err := trace.TraceIDFromHex(collector.traceIDStr)
	if err != nil || !traceID.IsValid() {
		return
	}

	// Parse and validate span ID
	spanID, err := trace.SpanIDFromHex(collector.spanIDStr)
	if err != nil || !spanID.IsValid() {
		return
	}

	// Populate canonical fields (only after validation to avoid zeroing on parse errors)
	rec.SetTraceID(traceID)
	rec.SetSpanID(spanID)
	if collector.foundFlags {
		rec.SetTraceFlags(collector.traceFlags)
	}
	// If trace_flags not found, TraceFlags defaults to 0 (not sampled) which is valid
}
