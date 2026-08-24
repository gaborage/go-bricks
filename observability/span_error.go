package observability

import (
	"fmt"

	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"
)

// RecordErrorByType reports err on span by its Go type and never by its message,
// and is the only way framework code records an error on a span.
//
// SECURITY: a span exception event and a span status description are both
// off-platform sinks — they leave with the tracing exporter, under that vendor's
// retention, access model and export path, and the logger's SensitiveDataFilter
// never sees them. The error is consumer-authored, so its message may embed a
// secret (a driver error echoing the row that collided, a transport error
// carrying a query-string token, a handler error formatting its input), and
// field-name masking cannot help: the key is fixed and the secret is in the
// value. The outer `%T` is framework-shaped and carries no consumer data, so it
// is what both sinks get. See ADR-083; ADR-081 applies the same rule to
// recovered panic values.
//
// The type is the OUTER one — no unwrap walk — matching ADR-081's spelling.
// A nil span, a nil err, or a span that is not recording is a no-op — the last
// so a tracing-disabled or sampled-out deployment does not pay for the type
// rendering and the attribute slice on every error.
func RecordErrorByType(span trace.Span, err error) {
	if span == nil || err == nil || !span.IsRecording() {
		return
	}

	errType := fmt.Sprintf("%T", err)
	// semconv spells both the event and the attribute; exception.message is
	// deliberately absent.
	span.AddEvent(string(semconv.ExceptionEventName), trace.WithAttributes(
		semconv.ExceptionType(errType),
	))
	span.SetStatus(codes.Error, errType)
}
