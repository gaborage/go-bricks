package tracking

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// httpMeterName is the meter name following the go-bricks/<module> convention.
	httpMeterName = "go-bricks/httpclient"

	// Metric names per OTel semantic conventions for HTTP clients.
	metricRequestDuration  = "http.client.request.duration"
	metricActiveRequests   = "http.client.active_requests"
	metricRequestBodySize  = "http.client.request.body.size"
	metricResponseBodySize = "http.client.response.body.size"
	metricRetriesTotal     = "http.client.retries.total"

	// Attribute keys per OTel semantic conventions (semconv v1.37.0 equivalents).
	// peer.service and http.request.resend_count are not in v1.37.0 constants but are
	// defined in the OTel spec — declared here as local constants for consistency.
	attrPeerService     = "peer.service"
	attrServerAddress   = "server.address"
	attrServerPort      = "server.port"
	attrURLScheme       = "url.scheme"
	attrHTTPMethod      = "http.request.method"
	attrHTTPStatusCode  = "http.response.status_code"
	attrErrorType       = "error.type"
	attrHTTPResendCount = "http.request.resend_count"
	attrRetryReason     = "reason"

	// Sentinel value for unknown HTTP methods per OTel spec.
	methodOther = "_OTHER"

	// Well-known HTTP method strings used in canonicalMethod.
	methodGET     = "GET"
	methodHEAD    = "HEAD"
	methodPOST    = "POST"
	methodPUT     = "PUT"
	methodDELETE  = "DELETE"
	methodCONNECT = "CONNECT"
	methodOPTIONS = "OPTIONS"
	methodTRACE   = "TRACE"
	methodPATCH   = "PATCH"
)

var (
	// Singleton meter initialization guards.
	httpMeter   metric.Meter
	meterOnce   sync.Once
	meterInitMu sync.Mutex

	// Metric instruments.
	requestDuration  metric.Float64Histogram
	activeRequests   metric.Int64UpDownCounter
	requestBodySize  metric.Int64Histogram
	responseBodySize metric.Int64Histogram
	retriesTotal     metric.Int64Counter
)

// logMetricError logs a metric initialization error to stderr.
// Metric failures are best-effort and must not crash the application.
func logMetricError(metricName string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize metric %s: %v\n", metricName, err)
	}
}

// initHTTPMeter performs the one-time meter and instrument initialization.
// It is called via sync.Once and is protected by meterInitMu to prevent
// races between a reset (in tests) and concurrent callers.
func initHTTPMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	if httpMeter != nil {
		return
	}

	httpMeter = otel.Meter(httpMeterName)

	var err error

	// Duration histogram — explicit buckets per OTel HTTP client semconv recommendation.
	requestDuration, err = httpMeter.Float64Histogram(
		metricRequestDuration,
		metric.WithDescription("Duration of HTTP client requests"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10),
	)
	logMetricError(metricRequestDuration, err)

	// Up-down counter for in-flight requests.
	activeRequests, err = httpMeter.Int64UpDownCounter(
		metricActiveRequests,
		metric.WithDescription("Number of in-flight HTTP client requests"),
		metric.WithUnit("{request}"),
	)
	logMetricError(metricActiveRequests, err)

	// Histogram for outgoing request body size.
	requestBodySize, err = httpMeter.Int64Histogram(
		metricRequestBodySize,
		metric.WithDescription("Size of HTTP client request bodies"),
		metric.WithUnit("By"),
	)
	logMetricError(metricRequestBodySize, err)

	// Histogram for incoming response body size.
	responseBodySize, err = httpMeter.Int64Histogram(
		metricResponseBodySize,
		metric.WithDescription("Size of HTTP client response bodies"),
		metric.WithUnit("By"),
	)
	logMetricError(metricResponseBodySize, err)

	// Counter for client-side retries.
	retriesTotal, err = httpMeter.Int64Counter(
		metricRetriesTotal,
		metric.WithDescription("Total number of HTTP client retry attempts"),
		metric.WithUnit("{retry}"),
	)
	logMetricError(metricRetriesTotal, err)
}

// InitHTTPMeter initializes the HTTP client meter idempotently.
// Subsequent calls after the first are no-ops. The meter is pulled from
// otel.GetMeterProvider() lazily via sync.Once.
func InitHTTPMeter() {
	meterOnce.Do(initHTTPMeter)
}

// HTTPClientMeasurement carries all per-request data needed to emit OTel metrics.
type HTTPClientMeasurement struct {
	// PeerName is the logical peer service name (peer.service). Omitted when empty.
	PeerName string
	// Method is the HTTP method (uppercase canonical; unknown methods → "_OTHER").
	Method string
	// URL is the request URL, used to extract server.address, server.port, and url.scheme.
	URL *url.URL
	// StatusCode is the HTTP response status code. 0 means a transport error (no response).
	StatusCode int
	// ErrorType is the OTel error.type enum value. Empty string on success.
	ErrorType string
	// ResendCount is the number of prior attempts (0 = first attempt).
	ResendCount int
	// Elapsed is the total request duration, recorded as seconds in the histogram.
	Elapsed time.Duration
	// RequestBytes is the request body size in bytes. Skipped when 0.
	RequestBytes int
	// ResponseBytes is the response body size in bytes. Skipped when 0.
	ResponseBytes int
}

// RecordHTTPClientMetrics records all per-request OTel metrics after a request completes.
// It emits:
//   - http.client.request.duration (seconds)
//   - http.client.request.body.size (bytes, when > 0)
//   - http.client.response.body.size (bytes, when > 0)
func RecordHTTPClientMetrics(ctx context.Context, m *HTTPClientMeasurement) {
	InitHTTPMeter()

	method := canonicalMethod(m.Method)
	addr, port := serverAddressPort(m.URL)
	scheme := urlScheme(m.URL)

	// Base attributes shared across all instruments.
	base := baseAttrs(m.PeerName, method, addr, port, scheme)

	// Duration histogram — extended with status code, error type, resend count.
	durationAttrs := make([]attribute.KeyValue, 0, len(base)+3)
	durationAttrs = append(durationAttrs, base...)
	if m.StatusCode != 0 {
		durationAttrs = append(durationAttrs, attribute.Int(attrHTTPStatusCode, m.StatusCode))
	}
	if m.ErrorType != "" {
		durationAttrs = append(durationAttrs, attribute.String(attrErrorType, m.ErrorType))
	}
	durationAttrs = append(durationAttrs, attribute.Int(attrHTTPResendCount, m.ResendCount))

	if requestDuration != nil {
		secs := float64(m.Elapsed.Nanoseconds()) / 1e9
		requestDuration.Record(ctx, secs, metric.WithAttributes(durationAttrs...))
	}

	// Body-size histograms — only when bytes > 0.
	if requestBodySize != nil && m.RequestBytes > 0 {
		requestBodySize.Record(ctx, int64(m.RequestBytes), metric.WithAttributes(base...))
	}
	if responseBodySize != nil && m.ResponseBytes > 0 {
		responseBodySize.Record(ctx, int64(m.ResponseBytes), metric.WithAttributes(base...))
	}
}

// IncActiveRequests increments the http.client.active_requests counter.
// Call this immediately before sending a request.
// peer is the peer.service value (omitted when empty); method is the HTTP method.
func IncActiveRequests(ctx context.Context, peer, method string) {
	InitHTTPMeter()
	if activeRequests == nil {
		return
	}
	attrs := activeRequestAttrs(peer, canonicalMethod(method))
	activeRequests.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// DecActiveRequests decrements the http.client.active_requests counter.
// Call this after the request completes (in a defer alongside IncActiveRequests).
func DecActiveRequests(ctx context.Context, peer, method string) {
	InitHTTPMeter()
	if activeRequests == nil {
		return
	}
	attrs := activeRequestAttrs(peer, canonicalMethod(method))
	activeRequests.Add(ctx, -1, metric.WithAttributes(attrs...))
}

// IncRetry increments the http.client.retries.total counter.
// peer is the peer.service value (omitted when empty); method is the HTTP method
// (normalized via canonicalMethod); reason should be one of: "timeout", "5xx", "network",
// "build_response".
func IncRetry(ctx context.Context, peer, method, reason string) {
	InitHTTPMeter()
	if retriesTotal == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 3)
	if peer != "" {
		attrs = append(attrs, attribute.String(attrPeerService, peer))
	}
	attrs = append(attrs,
		attribute.String(attrHTTPMethod, canonicalMethod(method)),
		attribute.String(attrRetryReason, reason),
	)
	retriesTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// baseAttrs builds the attribute set common to all instruments.
func baseAttrs(peer, method, addr string, port int, scheme string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 5)
	if peer != "" {
		attrs = append(attrs, attribute.String(attrPeerService, peer))
	}
	attrs = append(attrs,
		attribute.String(attrHTTPMethod, method),
		attribute.String(attrServerAddress, addr),
		attribute.Int(attrServerPort, port),
		attribute.String(attrURLScheme, scheme),
	)
	return attrs
}

// activeRequestAttrs builds the minimal attribute set for the active_requests counter.
func activeRequestAttrs(peer, method string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 2)
	if peer != "" {
		attrs = append(attrs, attribute.String(attrPeerService, peer))
	}
	attrs = append(attrs, attribute.String(attrHTTPMethod, method))
	return attrs
}

// canonicalMethod returns the uppercase canonical HTTP method, or "_OTHER" for
// methods not in the OTel-defined set of well-known HTTP methods.
func canonicalMethod(method string) string {
	upper := strings.ToUpper(method)
	switch upper {
	case methodGET, methodHEAD, methodPOST, methodPUT, methodDELETE,
		methodCONNECT, methodOPTIONS, methodTRACE, methodPATCH:
		return upper
	default:
		return methodOther
	}
}

// serverAddressPort extracts the server.address and server.port from a URL.
// When the port is blank it defaults to 80 for http and 443 for https.
func serverAddressPort(u *url.URL) (addr string, port int) {
	if u == nil {
		return "", 0
	}
	addr = u.Hostname()
	portStr := u.Port()
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err == nil {
			port = p
		}
		return addr, port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return addr, 443
	default:
		return addr, 80
	}
}

// urlScheme returns the URL scheme string, or "" when the URL is nil.
func urlScheme(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme
}
