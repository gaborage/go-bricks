package logger

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Reserved resource-identity attribute namespaces (OTel semantic conventions).
// Caller log fields matching these are remapped under callerAttrPrefix so a log
// call can never shadow the provider's resource identity at record level, where
// record attributes deliberately win over resource attributes on collision (#915).
// log.type is intentionally NOT reserved: dual-mode routing depends on callers
// (e.g. request middleware) setting it. Only top-level keys are guarded: nested
// map values flatten under their parent key and cannot collide with bare
// resource keys.
const (
	reservedServicePrefix      = "service."
	reservedTelemetrySDKPrefix = "telemetry.sdk."
	// Same semconv constant observability/provider.go stamps on the resource,
	// so the guard and the resource cannot drift apart on this key.
	reservedDeploymentEnvKey = string(semconv.DeploymentEnvironmentNameKey)

	// callerAttrPrefix relocates colliding caller fields
	// (e.g. service.name -> app.service.name), preserving the value.
	callerAttrPrefix = "app."

	// maxRemapWarnKeysLen bounds the key list carried by the one-time remap
	// WARN; field names are caller-influenced and must not grow the record.
	maxRemapWarnKeysLen = 256

	// logTypeAttrKey routes records through dual-mode processing
	// (observability.DualModeLogProcessor); deliberately caller-settable.
	logTypeAttrKey = "log.type"

	fieldTraceFlags = "trace_flags"

	// maxRecordAttributes caps the attributes on one record. observability sets
	// the SDK's own limit to the same value (its tests pin the pair), so the
	// bridge is the only truncation point and picks what survives.
	maxRecordAttributes = 128

	// droppedAttrsKey carries the number of caller fields the cap dropped, on
	// the truncated record and on the one-time truncation WARN.
	droppedAttrsKey = "log.attributes.dropped"
)

// OTelBridge converts zerolog JSON output to OpenTelemetry log records.
// It implements io.Writer to intercept zerolog's output stream.
type OTelBridge struct {
	loggerProvider   *sdklog.LoggerProvider
	logger           log.Logger
	remapWarnOnce    sync.Once
	truncateWarnOnce sync.Once
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

	var entry map[string]any
	if json.Unmarshal(p, &entry) != nil {
		// Ignore malformed or non-JSON entries (e.g., pretty/console logs). As an
		// io.Writer we MUST report the bytes as consumed (len(p), nil); surfacing the
		// parse error would break zerolog's writer chain and could drop or duplicate
		// log lines. The swallow is intentional, hence the nilerr suppression.
		//nolint:nilerr // deliberate pass-through of non-JSON writes; see comment above
		return len(p), nil
	}

	rec, ctx, changes := buildLogRecord(entry)
	b.logger.Emit(ctx, rec)

	if len(changes.remapped) > 0 {
		b.remapWarnOnce.Do(func() {
			b.emitRemapWarning(ctx, changes.remapped)
		})
	}
	if changes.dropped > 0 {
		b.truncateWarnOnce.Do(func() {
			b.emitTruncationWarning(ctx, changes.dropped)
		})
	}

	return len(p), nil
}

// emitRemapWarning emits a WARN naming the remapped keys, once per bridge
// instance (the framework wires a single bridge per process). It carries key
// names only, never field values.
func (b *OTelBridge) emitRemapWarning(ctx context.Context, remapped []string) {
	sort.Strings(remapped)
	keys := strings.Join(remapped, ",")
	// Unconditional bound: ToValidUTF8 repairs a rune split by the byte cut and
	// returns the string unchanged when nothing was cut.
	keys = strings.ToValidUTF8(keys[:min(len(keys), maxRemapWarnKeysLen)], "")

	b.emitWarning(ctx,
		"log fields shadow reserved resource attribute namespaces; remapped with \""+callerAttrPrefix+"\" prefix",
		attribute.String("reserved.keys", keys))
}

// emitTruncationWarning emits a WARN carrying the number of caller fields the
// attribute cap dropped, once per bridge instance, on the first truncated
// record. It carries the count only, never a caller key or value.
func (b *OTelBridge) emitTruncationWarning(ctx context.Context, dropped int) {
	b.emitWarning(ctx,
		"log fields exceed the "+strconv.Itoa(maxRecordAttributes)+"-attribute record cap; the lexicographically last were dropped",
		attribute.Int(droppedAttrsKey, dropped))
}

// emitWarning builds a trace-routed WARN record directly and emits it via
// b.logger — never through zerolog (the bridge sits inside zerolog's writer
// chain; logging through it recurses). It bypasses the SensitiveDataFilter, so
// callers must never pass caller field values.
func (b *OTelBridge) emitWarning(ctx context.Context, body string, attrs ...attribute.KeyValue) {
	var rec log.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(log.SeverityWarn)
	rec.SetSeverityText(LevelWarn)
	rec.SetBody(attribute.StringValue(body))
	rec.AddAttributes(attribute.String(logTypeAttrKey, "trace"))
	rec.AddAttributes(attrs...)
	b.logger.Emit(ctx, rec)
}

// isReservedAttrKey reports whether a caller field key would shadow the
// provider's resource identity. Trailing dots in the prefixes are load-bearing:
// bare "service", "servicex", or "deployment.environment" must not match.
func isReservedAttrKey(k string) bool {
	return strings.HasPrefix(k, reservedServicePrefix) ||
		strings.HasPrefix(k, reservedTelemetrySDKPrefix) ||
		k == reservedDeploymentEnvKey
}

// fieldChanges reports how buildLogRecord altered the caller's fields.
type fieldChanges struct {
	remapped []string // original names of keys moved under callerAttrPrefix
	dropped  int      // fields the attribute cap left out
}

func buildLogRecord(entry map[string]any) (rec log.Record, ctx context.Context, changes fieldChanges) {
	ctx = context.Background()
	var traceIDStr, spanIDStr string
	var traceFlags trace.TraceFlags
	hasTraceContext := false

	if spanCtx, ok := extractSpanContext(entry); ok {
		ctx = trace.ContextWithSpanContext(ctx, spanCtx)

		// Extract trace_id, span_id, and trace_flags for log attributes
		// These string/int attributes enable backend queries and serve as fallback
		// for enriching canonical OTLP trace fields when context is unavailable.
		if spanCtx.IsValid() {
			traceIDStr = spanCtx.TraceID().String()
			spanIDStr = spanCtx.SpanID().String()
			traceFlags = spanCtx.TraceFlags()
			hasTraceContext = true
		}
	}

	applyTimestamp(&rec, entry)
	applySeverity(&rec, entry)
	applyBody(&rec, entry)

	// Routing and correlation attributes lead the record, so no caller field
	// count can truncate them away. Default to trace logs unless the caller
	// sets log.type; dual-mode routing depends on every record carrying one.
	if v, ok := entry[logTypeAttrKey]; ok {
		rec.AddAttributes(attribute.KeyValue{Key: logTypeAttrKey, Value: toLogValue(v)})
	} else {
		rec.AddAttributes(attribute.String(logTypeAttrKey, "trace"))
	}

	// String trace_id/span_id enable text queries; Int trace_flags enables
	// fallback enrichment of the canonical fields.
	if hasTraceContext {
		rec.AddAttributes(
			attribute.String(FieldTraceID, traceIDStr),
			attribute.String(FieldSpanID, spanIDStr),
			attribute.Int64(fieldTraceFlags, int64(traceFlags)),
		)
	}

	changes = applyAttributes(&rec, entry, hasTraceContext, maxRecordAttributes-rec.AttributesLen())

	return rec, ctx, changes
}

func applyTimestamp(rec *log.Record, entry map[string]any) {
	timeStr, ok := entry["time"].(string)
	if !ok {
		return
	}
	if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil { // NOSONAR: Parse error intentional - invalid timestamps use zero value (OTel default)
		rec.SetTimestamp(t)
	}
}

func applySeverity(rec *log.Record, entry map[string]any) {
	levelStr, ok := entry[fieldLevel].(string)
	if !ok {
		return
	}
	rec.SetSeverity(mapZerologLevelToOTel(levelStr))
	rec.SetSeverityText(levelStr)
}

func applyBody(rec *log.Record, entry map[string]any) {
	if msg, ok := entry["message"].(string); ok {
		rec.SetBody(attribute.StringValue(msg))
		return
	}
	if msg, ok := entry["msg"].(string); ok {
		rec.SetBody(attribute.StringValue(msg))
	}
}

// applyAttributes copies entry fields onto the record within budget slots. It
// reports the original names of any keys remapped out of the reserved resource
// namespaces (nil in the common no-collision case) and how many fields the
// budget dropped. With emittedTrace set, the caller's trace keys are skipped:
// the bridge already wrote them from the span context, and an unparsed caller
// value would overwrite it (the SDK keeps the last value).
func applyAttributes(rec *log.Record, entry map[string]any, emittedTrace bool, budget int) (changes fieldChanges) {
	attrs := make([]attribute.KeyValue, 0, len(entry))
	for k, v := range entry {
		// Skip fields handled elsewhere
		if k == "time" || k == fieldLevel || k == fieldMessage || k == "msg" || k == logTypeAttrKey {
			continue
		}
		if emittedTrace && (k == FieldTraceID || k == FieldSpanID || k == fieldTraceFlags) {
			continue
		}

		if isReservedAttrKey(k) {
			changes.remapped = append(changes.remapped, k)
			k = callerAttrPrefix + k
		}

		attrs = append(attrs, attribute.KeyValue{
			Key:   attribute.Key(k),
			Value: toLogValue(v),
		})
	}

	attrs, changes.dropped = capAttributes(attrs, budget)
	if changes.dropped > 0 {
		rec.AddAttributes(attribute.Int(droppedAttrsKey, changes.dropped))
	}
	if len(attrs) > 0 {
		rec.AddAttributes(attrs...)
	}
	return changes
}

// capAttributes returns attrs untouched when they fit budget. Otherwise it keeps
// the lexicographically first budget-1 by key, leaving one slot for the
// dropped-count stamp, and reports how many it dropped. A caller field named
// like the stamp is dropped too: kept, it would overwrite the stamp.
func capAttributes(attrs []attribute.KeyValue, budget int) (kept []attribute.KeyValue, dropped int) {
	if len(attrs) <= budget {
		return attrs, 0
	}
	total := len(attrs)
	attrs = slices.DeleteFunc(attrs, func(kv attribute.KeyValue) bool {
		return kv.Key == droppedAttrsKey
	})
	slices.SortFunc(attrs, func(a, b attribute.KeyValue) int {
		return strings.Compare(string(a.Key), string(b.Key))
	})
	kept = attrs[:budget-1]
	return kept, total - len(kept)
}

func extractSpanContext(entry map[string]any) (trace.SpanContext, bool) {
	traceID, traceIDOK := parseTraceID(entry)
	spanID, spanIDOK := parseSpanID(entry)
	flags, flagsOK := parseTraceFlags(entry[fieldTraceFlags])
	if flagsOK {
		delete(entry, fieldTraceFlags)
	}

	if (!traceIDOK || !spanIDOK) && entry != nil {
		if parsedTraceID, parsedSpanID, parsedFlags, ok := parseTraceParent(entry["traceparent"]); ok {
			traceID = parsedTraceID
			spanID = parsedSpanID
			traceIDOK = true
			spanIDOK = true
			if !flagsOK {
				flags = parsedFlags
			}
		}
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
	traceIDStr, ok := entry[FieldTraceID].(string)
	if !ok {
		return trace.TraceID{}, false
	}
	traceID, err := trace.TraceIDFromHex(traceIDStr)
	if err != nil {
		return trace.TraceID{}, false
	}
	delete(entry, FieldTraceID)
	return traceID, traceID.IsValid()
}

func parseSpanID(entry map[string]any) (trace.SpanID, bool) {
	spanIDStr, ok := entry[FieldSpanID].(string)
	if !ok {
		return trace.SpanID{}, false
	}
	spanID, err := trace.SpanIDFromHex(spanIDStr)
	if err != nil {
		return trace.SpanID{}, false
	}
	delete(entry, FieldSpanID)
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
		if parsed, err := strconv.ParseUint(v, 10, 8); err == nil { // NOSONAR: Parse error intentional - invalid trace flags return false (not found)
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

func parseTraceParent(value any) (trace.TraceID, trace.SpanID, trace.TraceFlags, bool) {
	str, ok := value.(string)
	if !ok || str == "" {
		return trace.TraceID{}, trace.SpanID{}, 0, false
	}

	parts := strings.Split(str, "-")
	if len(parts) != 4 {
		return trace.TraceID{}, trace.SpanID{}, 0, false
	}

	traceID, err := trace.TraceIDFromHex(parts[1])
	if err != nil {
		return trace.TraceID{}, trace.SpanID{}, 0, false
	}

	spanID, err := trace.SpanIDFromHex(parts[2])
	if err != nil {
		return trace.TraceID{}, trace.SpanID{}, 0, false
	}

	flagsValue, err := strconv.ParseUint(parts[3], 16, 8)
	if err != nil {
		return trace.TraceID{}, trace.SpanID{}, 0, false
	}

	return traceID, spanID, trace.TraceFlags(uint8(flagsValue)), true
}

// mapZerologLevelToOTel maps zerolog log levels to OpenTelemetry severity levels.
func mapZerologLevelToOTel(level string) log.Severity {
	switch level {
	case LevelTrace:
		return log.SeverityTrace // 1
	case LevelDebug:
		return log.SeverityDebug // 5
	case LevelInfo:
		return log.SeverityInfo // 9
	case LevelWarn, "warning":
		return log.SeverityWarn // 13
	case LevelError:
		return log.SeverityError // 17
	case LevelFatal, LevelPanic:
		return log.SeverityFatal // 21
	default:
		return log.SeverityInfo // Default to Info for unknown levels
	}
}

// toLogValue converts a Go value to an OpenTelemetry attribute value.
func toLogValue(v any) attribute.Value {
	if v == nil {
		return attribute.StringValue("")
	}

	switch val := v.(type) {
	case string:
		return attribute.StringValue(val)
	case int:
		return attribute.Int64Value(int64(val))
	case int64:
		return attribute.Int64Value(val)
	case float64:
		return attribute.Float64Value(val)
	case bool:
		return attribute.BoolValue(val)
	case []any:
		// Convert array to slice of attribute values
		slice := make([]attribute.Value, len(val))
		for i, item := range val {
			slice[i] = toLogValue(item)
		}
		return attribute.SliceValue(slice...)
	case map[string]any:
		// Convert map to key-value pairs
		kvs := make([]attribute.KeyValue, 0, len(val))
		for k, v := range val {
			kvs = append(kvs, attribute.KeyValue{
				Key:   attribute.Key(k),
				Value: toLogValue(v),
			})
		}
		return attribute.MapValue(kvs...)
	default:
		// For unknown types, convert to string
		return attribute.StringValue(jsonStringify(val))
	}
}

// jsonStringify safely converts a value to JSON string.
func jsonStringify(v any) string {
	bytes, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(bytes)
}
