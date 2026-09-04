package observability

import (
	"context"
	"errors"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

const (
	logTypeKey    = "log.type"
	logTypeAction = "action"
	logTypeTrace  = "trace"

	// samplingDenominator gives log sampling 0.01% resolution. Rates below
	// half a unit (0.00005) round to a zero threshold and sample nothing.
	samplingDenominator = 10000
)

// DualModeLogProcessor routes log records to different processors based on log.type attribute.
// It implements the dual-mode logging architecture where:
//   - Action logs (log.type="action") are exported with all severities (100% sampling)
//   - Trace logs (log.type="trace"): ERROR/WARN always exported, INFO/DEBUG sampled by rate
type DualModeLogProcessor struct {
	actionProcessor sdklog.Processor // Handles action logs (request summaries)
	traceProcessor  sdklog.Processor // Handles trace logs (application debug logs)
	samplingRate    float64          // Sampling rate for INFO/DEBUG trace logs (0.0-1.0)
	sampleThreshold uint64           // samplingRate scaled to samplingDenominator; precomputed (rate is immutable)
}

// NewDualModeLogProcessor creates a new dual-mode log processor.
// samplingRate controls what fraction of INFO/DEBUG trace logs to export (0.0 to 1.0).
// ERROR/WARN logs and action logs are always exported at 100%.
func NewDualModeLogProcessor(actionProcessor, traceProcessor sdklog.Processor, samplingRate float64) *DualModeLogProcessor {
	if actionProcessor == nil {
		panic("observability: actionProcessor cannot be nil") // NOSONAR: Fail-fast on invalid initialization (manifesto: configuration errors crash at startup)
	}
	if traceProcessor == nil {
		panic("observability: traceProcessor cannot be nil") // NOSONAR: Fail-fast on invalid initialization (manifesto: configuration errors crash at startup)
	}

	// Only a finite rate strictly between 0 and 1 needs a threshold: <=0 and
	// NaN both fail this range (NaN compares false against every bound) and
	// leave sampleThreshold at its zero value, which the shouldSample fast
	// paths and hash-modulo fallback all treat as "drop"; >=1 is handled by
	// the shouldSample fast path and never consults the threshold.
	var sampleThreshold uint64
	if samplingRate > 0 && samplingRate < 1.0 {
		sampleThreshold = uint64(math.Round(samplingRate * samplingDenominator))
	}

	return &DualModeLogProcessor{
		actionProcessor: actionProcessor,
		traceProcessor:  traceProcessor,
		samplingRate:    samplingRate,
		sampleThreshold: sampleThreshold,
	}
}

// OnEmit routes the log record to the appropriate processor based on log.type attribute.
func (p *DualModeLogProcessor) OnEmit(ctx context.Context, rec *sdklog.Record) error {
	logType := enrichAndClassify(ctx, rec)

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
		return uint64(ts)%samplingDenominator < p.sampleThreshold
	}

	// Use first 8 bytes of trace ID for deterministic sampling
	// This ensures all logs in the same trace are sampled together
	traceBytes := traceID[:]
	hash := uint64(traceBytes[0]) | uint64(traceBytes[1])<<8 | uint64(traceBytes[2])<<16 | uint64(traceBytes[3])<<24 |
		uint64(traceBytes[4])<<32 | uint64(traceBytes[5])<<40 | uint64(traceBytes[6])<<48 | uint64(traceBytes[7])<<56

	return hash%samplingDenominator < p.sampleThreshold
}

// Shutdown shuts down both processors.
func (p *DualModeLogProcessor) Shutdown(ctx context.Context) error {
	errAction := p.actionProcessor.Shutdown(ctx)
	errTrace := p.traceProcessor.Shutdown(ctx)

	return errors.Join(errTrace, errAction)
}

// ForceFlush flushes both processors.
func (p *DualModeLogProcessor) ForceFlush(ctx context.Context) error {
	errAction := p.actionProcessor.ForceFlush(ctx)
	errTrace := p.traceProcessor.ForceFlush(ctx)

	return errors.Join(errTrace, errAction)
}

// enrichAndClassify populates the record's canonical trace fields (TraceID,
// SpanID, TraceFlags) and returns its log.type, in a single attribute walk.
// Trace data comes from two sources (in order of preference):
//  1. Span context in the provided context (primary source from active traces)
//  2. String attributes "trace_id" and "span_id" in the log record (fallback for parsed logs)
//
// This dual-source approach ensures canonical fields are populated even when:
//   - Logs are parsed from JSON without active trace context (OTelBridge)
//   - Logs are forwarded through async processors that may lose context
//   - External systems emit logs with trace correlation attributes
//
// Context is always consulted first and never requires a walk; the record's
// attributes are walked only as a fallback, and only then is trace data
// collected during that same walk.
func enrichAndClassify(ctx context.Context, rec *sdklog.Record) string {
	wantAttrTrace := !enrichFromContext(ctx, rec) // reads ctx only; no walk
	logType, c := collectRouting(rec, wantAttrTrace)
	if wantAttrTrace {
		applyCollectedTrace(rec, &c)
	}
	return logType
}

// collectRouting is the single walk shared by enrichAndClassify. It always
// searches for log.type (defaulting to "trace" if absent, or if present with
// a non-string value), and additionally collects trace correlation
// attributes when wantTrace is set. It stops as soon as everything it is
// looking for has been found.
func collectRouting(rec *sdklog.Record, wantTrace bool) (logType string, c traceAttributeCollector) {
	logType = logTypeTrace // Default to trace logs
	foundLogType := false

	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if kv.Key == logTypeKey {
			if kv.Value.Type() == attribute.STRING {
				logType = kv.Value.AsString()
			}
			foundLogType = true
		} else if wantTrace {
			c.collect(kv)
		}
		return !foundLogType || (wantTrace && !c.done())
	})

	return logType, c
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
func (c *traceAttributeCollector) collect(kv attribute.KeyValue) bool {
	switch kv.Key {
	// Literal on purpose: these mirror logger.FieldTraceID / logger.FieldSpanID, and
	// observability does not import logger.
	case "trace_id":
		if kv.Value.Type() == attribute.STRING {
			c.traceIDStr = kv.Value.AsString()
			c.foundTraceID = true
		}
	case "span_id":
		if kv.Value.Type() == attribute.STRING {
			c.spanIDStr = kv.Value.AsString()
			c.foundSpanID = true
		}
	case "trace_flags":
		if kv.Value.Type() == attribute.INT64 {
			flagsInt := kv.Value.AsInt64()
			if flagsInt >= 0 && flagsInt <= 255 {
				c.traceFlags = trace.TraceFlags(uint8(flagsInt))
				c.foundFlags = true
			}
		}
	}
	return !c.done()
}

// done reports whether all three trace correlation fields have been found.
func (c *traceAttributeCollector) done() bool {
	return c.foundTraceID && c.foundSpanID && c.foundFlags
}

// applyCollectedTrace validates and populates a record's canonical trace
// fields from a traceAttributeCollector populated by collectRouting's walk.
// This handles logs parsed from JSON (OTelBridge) or forwarded from external systems.
func applyCollectedTrace(rec *sdklog.Record, c *traceAttributeCollector) {
	if !c.foundTraceID || !c.foundSpanID {
		return
	}

	// Parse and validate trace ID
	traceID, err := trace.TraceIDFromHex(c.traceIDStr)
	if err != nil || !traceID.IsValid() {
		return
	}

	// Parse and validate span ID
	spanID, err := trace.SpanIDFromHex(c.spanIDStr)
	if err != nil || !spanID.IsValid() {
		return
	}

	// Populate canonical fields (only after validation to avoid zeroing on parse errors)
	rec.SetTraceID(traceID)
	rec.SetSpanID(spanID)
	if c.foundFlags {
		rec.SetTraceFlags(c.traceFlags)
	}
	// If trace_flags not found, TraceFlags defaults to 0 (not sampled) which is valid
}
