package tracking

import (
	"context"
	"net/url"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// httpTracerName is the tracer name, matching the meter and sibling-package
// conventions (go-bricks/database, go-bricks/messaging).
const httpTracerName = "go-bricks/httpclient"

// Span attribute keys not surfaced by the metric attribute constants above.
const (
	attrNetworkProtocolName = "network.protocol.name"
	attrURLPath             = "url.path"
	attrResponseBodySize    = "http.response.body.size"

	// networkProtocolHTTP is the constant value reported for network.protocol.name.
	// HTTP/1.1 vs HTTP/2 is not distinguished — OTel's recommended low-cardinality
	// default for client spans without route templating.
	networkProtocolHTTP = "http"
)

var (
	// httpTracer is the package-level tracer initialised lazily on first use.
	httpTracer trace.Tracer

	// tracerOnce + tracerInitMu mirror the metric init pattern: sync.Once for the
	// one-shot init, sync.Mutex so ResetTracerForTesting cannot race with InitHTTPTracer.
	tracerOnce   sync.Once
	tracerInitMu sync.Mutex
)

// HTTPSpanInfo carries the per-span data needed to construct a CLIENT-kind span
// for an outbound HTTP call. It is the span-side twin of HTTPClientMeasurement;
// every span-eligible field on the measurement also lives here.
type HTTPSpanInfo struct {
	// PeerName is the logical peer service name (peer.service). Omitted when empty.
	PeerName string
	// Method is the HTTP method (canonicalised to uppercase; "_OTHER" for non-standard).
	Method string
	// URL provides server.address, server.port, url.scheme, and url.path.
	URL *url.URL
	// ResendCount is the number of prior attempts (0 = first). Omitted when 0,
	// and always omitted on the parent "Do" span (the logical rollup).
	ResendCount int
}

// initHTTPTracer performs the one-time tracer lookup. Caller MUST hold tracerInitMu.
func initHTTPTracer() {
	if httpTracer != nil {
		return
	}
	httpTracer = otel.Tracer(httpTracerName)
}

// InitHTTPTracer initialises the HTTP client tracer idempotently. Subsequent
// calls after the first are no-ops. Holds tracerInitMu so concurrent calls and
// ResetTracerForTesting cannot race on tracerOnce.
func InitHTTPTracer() {
	tracerInitMu.Lock()
	defer tracerInitMu.Unlock()
	tracerOnce.Do(initHTTPTracer)
}

// StartHTTPClientSpan opens a CLIENT-kind span as a child of any active span on ctx.
// Returns the new context (with the span attached) and the span itself.
//
// When the global tracer provider is a no-op (observability.enabled: false), the
// returned span is non-recording; every span method becomes a no-op and the
// per-request cost reduces to one cached otel.Tracer() lookup.
//
// The same function serves both the parent "Do" span (info.ResendCount = 0) and
// per-attempt spans (info.ResendCount = attempt index). Caller decides by passing
// the appropriate context and info.
func StartHTTPClientSpan(ctx context.Context, info *HTTPSpanInfo) (context.Context, trace.Span) {
	InitHTTPTracer()
	if httpTracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	method := canonicalMethod(info.Method)
	ctx, span := httpTracer.Start(ctx, spanName(info, method), trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(spanStartAttributes(info, method)...)
	return ctx, span
}

// EndHTTPClientSpan applies the final response/error attributes and the status
// mapping, then ends the span. statusCode == 0 signals a transport error (no
// response received).
//
// Status mapping (OTel HTTP client semantic conventions):
//   - 100-499 (incl. 4xx)  → status unset (default OK).
//   - 500-599              → codes.Error, message "HTTP {code}", NO RecordError.
//   - transport error (0)  → codes.Error + RecordError(err) + error.type attribute.
//
// The 4xx-as-OK rule is the OTel *client* convention — a 4xx is a normal
// flow-control response from the server, not a client-side fault.
//
// On nil-or-no-op span, the call is a zero-cost no-op.
func EndHTTPClientSpan(span trace.Span, statusCode int, errType string, responseBytes int, err error) {
	if span == nil {
		return
	}
	if statusCode != 0 {
		span.SetAttributes(attribute.Int(attrHTTPStatusCode, statusCode))
	}
	if responseBytes > 0 {
		span.SetAttributes(attribute.Int(attrResponseBodySize, responseBytes))
	}
	switch {
	case statusCode == 0 && err != nil:
		if errType != "" {
			span.SetAttributes(attribute.String(attrErrorType, errType))
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, errType)
	case statusCode >= 500 && statusCode < 600:
		span.SetStatus(codes.Error, "HTTP "+strconv.Itoa(statusCode))
	default:
		// 100-499: leave status unset (default OK per OTel HTTP client convention).
	}
	span.End()
}

// spanName builds the span name. PeerName when present gives a low-cardinality,
// human-friendly grouping for SLO dashboards (e.g. "POST stripe"); without it
// we fall back to OTel's recommended "HTTP {METHOD}" pattern. URL paths are
// never in the name — without route templating the path is a cardinality bomb.
func spanName(info *HTTPSpanInfo, method string) string {
	if info.PeerName != "" {
		return method + " " + info.PeerName
	}
	return "HTTP " + method
}

// spanStartAttributes builds the attribute set applied at span start. peer.service
// is omitted when PeerName is empty; url.path is omitted when the URL has no path;
// http.request.resend_count is omitted when 0 (first attempt or the Do rollup).
func spanStartAttributes(info *HTTPSpanInfo, method string) []attribute.KeyValue {
	addr, port := serverAddressPort(info.URL)
	scheme := urlScheme(info.URL)
	attrs := make([]attribute.KeyValue, 0, 8)
	if info.PeerName != "" {
		attrs = append(attrs, attribute.String(attrPeerService, info.PeerName))
	}
	attrs = append(attrs,
		attribute.String(attrHTTPMethod, method),
		attribute.String(attrServerAddress, addr),
		attribute.Int(attrServerPort, port),
		attribute.String(attrURLScheme, scheme),
		attribute.String(attrNetworkProtocolName, networkProtocolHTTP),
	)
	if p := urlPath(info.URL); p != "" {
		attrs = append(attrs, attribute.String(attrURLPath, p))
	}
	if info.ResendCount > 0 {
		attrs = append(attrs, attribute.Int(attrHTTPResendCount, info.ResendCount))
	}
	return attrs
}

// urlPath returns the path component of u. url.URL.Path already excludes the
// query string and userinfo, so this is effectively a nil-safe accessor.
func urlPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}
