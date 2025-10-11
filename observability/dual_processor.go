package observability

import (
	"context"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

const (
	logTypeAction = "action"
	logTypeTrace  = "trace"
)

// DualModeLogProcessor routes log records to different processors based on log.type attribute.
// It implements the dual-mode logging architecture where:
//   - Action logs (log.type="action") are exported with all severities (100% sampling)
//   - Trace logs (log.type="trace") are filtered to WARN+ only (~95% volume reduction)
type DualModeLogProcessor struct {
	actionProcessor sdklog.Processor // Handles action logs (request summaries)
	traceProcessor  sdklog.Processor // Handles trace logs (application debug logs)
}

// NewDualModeLogProcessor creates a new dual-mode log processor.
func NewDualModeLogProcessor(actionProcessor, traceProcessor sdklog.Processor) *DualModeLogProcessor {
	return &DualModeLogProcessor{
		actionProcessor: actionProcessor,
		traceProcessor:  traceProcessor,
	}
}

// OnEmit routes the log record to the appropriate processor based on log.type attribute.
func (p *DualModeLogProcessor) OnEmit(ctx context.Context, rec *sdklog.Record) error {
	logType := extractLogType(rec)

	// Action logs: export all severities (INFO, WARN, ERROR)
	if logType == logTypeAction {
		return p.actionProcessor.OnEmit(ctx, rec)
	}

	// Trace logs and unknown types: only export WARN+ (filter out INFO/DEBUG)
	// Note: OpenTelemetry severity levels: Trace=1, Debug=5, Info=9, Warn=13, Error=17, Fatal=21
	if rec.Severity() >= log.SeverityWarn { // 13 = WARN
		return p.traceProcessor.OnEmit(ctx, rec)
	}

	// Drop INFO/DEBUG logs (this achieves ~95% volume reduction)
	return nil
}

// Enabled checks if the processor should process the given record.
// Note: The Processor interface does not require Enabled() method,
// so we implement basic severity-based filtering logic here.
func (p *DualModeLogProcessor) Enabled(_ context.Context, rec *sdklog.Record) bool {
	logType := extractLogType(rec)

	// Action logs: all severities enabled
	if logType == logTypeAction {
		return true
	}

	// Trace logs and unknown types: WARN+ only
	return rec.Severity() >= log.SeverityWarn
}

// Shutdown shuts down both processors.
func (p *DualModeLogProcessor) Shutdown(ctx context.Context) error {
	// Shutdown both processors, return first error encountered
	errAction := p.actionProcessor.Shutdown(ctx)
	errTrace := p.traceProcessor.Shutdown(ctx)

	if errAction != nil {
		return errAction
	}
	return errTrace
}

// ForceFlush flushes both processors.
func (p *DualModeLogProcessor) ForceFlush(ctx context.Context) error {
	// Flush both processors, return first error encountered
	errAction := p.actionProcessor.ForceFlush(ctx)
	errTrace := p.traceProcessor.ForceFlush(ctx)

	if errAction != nil {
		return errAction
	}
	return errTrace
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
