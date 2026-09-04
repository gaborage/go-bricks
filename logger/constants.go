package logger

// DefaultMaskValue is the value used to mask sensitive data.
const DefaultMaskValue = "***"

// Log level string constants matching zerolog level names.
// Exported so other packages (server, app, config) can reuse the canonical
// level identifiers without redefining them.
const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelFatal = "fatal"
	LevelPanic = "panic"
)

// Identity field keys. Two identifier spaces share every log line and hold
// different values by design:
//
//   - FieldCorrelationID is the framework's own cross-service id: the inbound
//     X-Request-ID when one arrived, else the trace id derived from a
//     traceparent, else a UUID minted at the boundary. It is always present,
//     travels on both messaging lanes' outcome lines, and survives with tracing
//     switched off.
//   - FieldTraceID and FieldSpanID are the OpenTelemetry span identifiers of
//     the span the line was written under. They appear only while a tracer
//     provider is registered and never equal the correlation id.
//
// Every stamping site uses these names so the key has one definition; the
// long-form rationale lives in wiki/observability.md ("Correlation Fields and Exemplars").
// observability/dual_processor.go reads the OTel two as OTLP record
// attributes by literal to stay free of a logger import.
const (
	FieldCorrelationID = "correlation_id"
	FieldTraceID       = "trace_id"
	FieldSpanID        = "span_id"
)

// Log entry field key constants.
const (
	fieldMessage = "message"
	fieldLevel   = "level"
)
