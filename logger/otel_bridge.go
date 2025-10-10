package logger

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// OTelBridge converts zerolog JSON output to OpenTelemetry log records.
// It implements io.Writer to intercept zerolog's output stream.
type OTelBridge struct {
	loggerProvider *sdklog.LoggerProvider
	logger         log.Logger
}

// NewOTelBridge creates a new bridge that converts zerolog logs to OTel log records.
func NewOTelBridge(provider *sdklog.LoggerProvider) *OTelBridge {
	if provider == nil {
		return nil
	}

	return &OTelBridge{
		loggerProvider: provider,
		logger:         provider.Logger("go-bricks"),
	}
}

// Write implements io.Writer by parsing zerolog JSON and emitting OTel log records.
func (b *OTelBridge) Write(p []byte) (n int, err error) {
	if b == nil || b.logger == nil {
		return len(p), nil
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(p, &entry); err != nil {
		// Ignore malformed or non-JSON entries (e.g., pretty logs)
		return len(p), nil
	}

	rec, ctx := buildLogRecord(entry)
	b.logger.Emit(ctx, rec)

	return len(p), nil
}

func buildLogRecord(entry map[string]any) (log.Record, context.Context) {
	var rec log.Record

	ctx := context.Background()
	if spanCtx, ok := extractSpanContext(entry); ok {
		ctx = trace.ContextWithSpanContext(ctx, spanCtx)
	}

	applyTimestamp(&rec, entry)
	applySeverity(&rec, entry)
	applyBody(&rec, entry)
	applyAttributes(&rec, entry)

	return rec, ctx
}

func applyTimestamp(rec *log.Record, entry map[string]any) {
	timeStr, ok := entry["time"].(string)
	if !ok {
		return
	}
	if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
		rec.SetTimestamp(t)
	}
}

func applySeverity(rec *log.Record, entry map[string]any) {
	levelStr, ok := entry["level"].(string)
	if !ok {
		return
	}
	rec.SetSeverity(mapZerologLevelToOTel(levelStr))
	rec.SetSeverityText(levelStr)
}

func applyBody(rec *log.Record, entry map[string]any) {
	if msg, ok := entry["message"].(string); ok {
		rec.SetBody(log.StringValue(msg))
		return
	}
	if msg, ok := entry["msg"].(string); ok {
		rec.SetBody(log.StringValue(msg))
	}
}

func applyAttributes(rec *log.Record, entry map[string]any) {
	attrs := make([]log.KeyValue, 0, len(entry))
	for k, v := range entry {
		// Skip fields handled elsewhere
		if k == "time" || k == "level" || k == "message" || k == "msg" {
			continue
		}

		attrs = append(attrs, log.KeyValue{
			Key:   k,
			Value: toLogValue(v),
		})
	}
	if len(attrs) > 0 {
		rec.AddAttributes(attrs...)
	}
}

func extractSpanContext(entry map[string]any) (trace.SpanContext, bool) {
	traceID, traceIDOK := parseTraceID(entry)
	spanID, spanIDOK := parseSpanID(entry)
	flags, flagsOK := parseTraceFlags(entry["trace_flags"])
	if flagsOK {
		delete(entry, "trace_flags")
	}

	if !traceIDOK || !spanIDOK {
		return trace.SpanContext{}, false
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: flags,
		Remote:     true,
	})
	if !sc.IsValid() {
		return trace.SpanContext{}, false
	}
	return sc, true
}

func parseTraceID(entry map[string]any) (trace.TraceID, bool) {
	traceIDStr, ok := entry["trace_id"].(string)
	if !ok {
		return trace.TraceID{}, false
	}
	traceID, err := trace.TraceIDFromHex(traceIDStr)
	if err != nil {
		return trace.TraceID{}, false
	}
	delete(entry, "trace_id")
	return traceID, traceID.IsValid()
}

func parseSpanID(entry map[string]any) (trace.SpanID, bool) {
	spanIDStr, ok := entry["span_id"].(string)
	if !ok {
		return trace.SpanID{}, false
	}
	spanID, err := trace.SpanIDFromHex(spanIDStr)
	if err != nil {
		return trace.SpanID{}, false
	}
	delete(entry, "span_id")
	return spanID, spanID.IsValid()
}

func parseTraceFlags(value any) (trace.TraceFlags, bool) {
	switch v := value.(type) {
	case string:
		// Accept both decimal and hex (e.g., "01" or "0x1")
		if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
			if parsed, err := strconv.ParseUint(v[2:], 16, 8); err == nil {
				return trace.TraceFlags(parsed), true
			}
		}
		if parsed, err := strconv.ParseUint(v, 10, 8); err == nil {
			return trace.TraceFlags(parsed), true
		}
	case float64:
		if v >= 0 && v <= math.MaxUint8 {
			return trace.TraceFlags(uint8(v)), true
		}
	case int:
		if v >= 0 && v <= math.MaxUint8 {
			return trace.TraceFlags(uint8(v)), true
		}
	}
	return 0, false
}

// mapZerologLevelToOTel maps zerolog log levels to OpenTelemetry severity levels.
func mapZerologLevelToOTel(level string) log.Severity {
	switch level {
	case "trace":
		return log.SeverityTrace // 1
	case "debug":
		return log.SeverityDebug // 5
	case "info":
		return log.SeverityInfo // 9
	case "warn", "warning":
		return log.SeverityWarn // 13
	case "error":
		return log.SeverityError // 17
	case "fatal", "panic":
		return log.SeverityFatal // 21
	default:
		return log.SeverityInfo // Default to Info for unknown levels
	}
}

// toLogValue converts a Go value to an OpenTelemetry log value.
func toLogValue(v interface{}) log.Value {
	if v == nil {
		return log.StringValue("")
	}

	switch val := v.(type) {
	case string:
		return log.StringValue(val)
	case int:
		return log.Int64Value(int64(val))
	case int64:
		return log.Int64Value(val)
	case float64:
		return log.Float64Value(val)
	case bool:
		return log.BoolValue(val)
	case []interface{}:
		// Convert array to slice of log values
		slice := make([]log.Value, len(val))
		for i, item := range val {
			slice[i] = toLogValue(item)
		}
		return log.SliceValue(slice...)
	case map[string]interface{}:
		// Convert map to key-value pairs
		kvs := make([]log.KeyValue, 0, len(val))
		for k, v := range val {
			kvs = append(kvs, log.KeyValue{
				Key:   k,
				Value: toLogValue(v),
			})
		}
		return log.MapValue(kvs...)
	default:
		// For unknown types, convert to string
		return log.StringValue(jsonStringify(val))
	}
}

// jsonStringify safely converts a value to JSON string.
func jsonStringify(v interface{}) string {
	bytes, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(bytes)
}
