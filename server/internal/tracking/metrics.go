package tracking

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// Meter name for HTTP server metrics instrumentation
	httpMeterName = "go-bricks/http-server"

	// Metric names following OpenTelemetry semantic conventions v1.38.0
	metricHTTPRequestDuration = "http.server.request.duration" // Histogram in seconds
	metricHTTPActiveRequests  = "http.server.active_requests"  // UpDownCounter

	// Attribute keys per OTel semantic conventions
	attrHTTPRequestMethod  = "http.request.method"
	attrHTTPResponseStatus = "http.response.status_code"
	attrHTTPRoute          = "http.route"
	attrURLScheme          = "url.scheme"
	attrErrorType          = "error.type"
)

// HTTP request duration histogram buckets per OTel semantic conventions
// These are the recommended boundaries for HTTP request latency measurement
var httpDurationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10,
}

var (
	// Singleton meter initialization
	httpMeter     metric.Meter
	meterOnce     sync.Once
	meterInitMu   sync.Mutex
	metricsInited bool

	// Metric instruments
	httpDurationHistogram   metric.Float64Histogram
	httpActiveRequestsGauge metric.Int64UpDownCounter
)

// logMetricError logs a metric initialization error to stderr.
// This is a best-effort operation - metrics failures should not break the application.
func logMetricError(metricName string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize HTTP metric %s: %v\n", metricName, err)
	}
}

// initHTTPMeter initializes the OpenTelemetry meter and HTTP metric instruments.
// This function is thread-safe and idempotent: it can be called multiple times but
// performs initialization only once.
func initHTTPMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	// Prevent re-initialization if already set
	if httpMeter != nil {
		return
	}

	// Get meter from global meter provider
	httpMeter = otel.Meter(httpMeterName)

	// Initialize histogram for request duration with OTel-recommended buckets
	var err error
	httpDurationHistogram, err = httpMeter.Float64Histogram(
		metricHTTPRequestDuration,
		metric.WithDescription("Duration of HTTP server requests"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(httpDurationBuckets...),
	)
	logMetricError(metricHTTPRequestDuration, err)

	// Initialize UpDownCounter for active requests
	httpActiveRequestsGauge, err = httpMeter.Int64UpDownCounter(
		metricHTTPActiveRequests,
		metric.WithDescription("Number of active HTTP server requests"),
		metric.WithUnit("{request}"),
	)
	logMetricError(metricHTTPActiveRequests, err)

	metricsInited = true
}

// ensureHTTPMeterInitialized ensures the HTTP meter is initialized.
// This is called lazily on first middleware use.
func ensureHTTPMeterInitialized() {
	meterOnce.Do(initHTTPMeter)
}

// recordActiveRequestDelta safely records active request count changes.
// This helper encapsulates nil-checking to reduce cognitive complexity in the middleware.
func recordActiveRequestDelta(ctx context.Context, delta int64, attrs []attribute.KeyValue) {
	if httpActiveRequestsGauge != nil {
		httpActiveRequestsGauge.Add(ctx, delta, metric.WithAttributes(attrs...))
	}
}

// recordRequestDuration safely records request duration to the histogram.
// This helper encapsulates nil-checking to reduce cognitive complexity in the middleware.
func recordRequestDuration(ctx context.Context, duration time.Duration, attrs []attribute.KeyValue) {
	if httpDurationHistogram != nil {
		httpDurationHistogram.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
	}
}

// HTTPMetricsConfig holds configuration for HTTP metrics middleware.
type HTTPMetricsConfig struct {
	// Skipper defines a function to skip middleware for certain requests.
	// Default: nil (process all requests)
	Skipper func(c echo.Context) bool
}

// httpMetricsHandler encapsulates the middleware logic to reduce cognitive complexity.
// By moving the handler logic to a method, we eliminate nested closure complexity penalties.
type httpMetricsHandler struct {
	config HTTPMetricsConfig
}

// handle processes an HTTP request and records metrics.
// This method is extracted from the nested closures to reduce cognitive complexity.
func (h *httpMetricsHandler) handle(c echo.Context, next echo.HandlerFunc) error {
	// Check skipper
	if h.config.Skipper != nil && h.config.Skipper(c) {
		return next(c)
	}

	// Extract request attributes (available before handler)
	req := c.Request()
	ctx := req.Context()
	method := req.Method
	scheme := extractScheme(c)

	// Build and record active request increment
	baseAttrs := buildBaseAttributes(method, scheme)
	recordActiveRequestDelta(ctx, 1, baseAttrs)

	// Record start time and call the next handler
	start := time.Now()
	err := next(c)
	duration := time.Since(start)

	// Decrement active requests
	recordActiveRequestDelta(ctx, -1, baseAttrs)

	// Build duration attributes and record histogram
	durationAttrs := buildDurationAttributes(method, scheme, c.Response().Status, c.Path(), err)
	recordRequestDuration(ctx, duration, durationAttrs)

	return err
}

// HTTPMetrics returns middleware that records HTTP server metrics.
// It tracks request duration and active request count per OTel semantic conventions.
//
// Metrics recorded:
//   - http.server.request.duration: Histogram of request durations in seconds
//   - http.server.active_requests: UpDownCounter of concurrent requests
//
// Attributes included:
//   - http.request.method: HTTP method (GET, POST, etc.)
//   - url.scheme: Request scheme (http, https)
//   - http.response.status_code: Response status code
//   - http.route: Route pattern from Echo (e.g., "/users/:id")
//   - error.type: Error category for 4xx/5xx responses
func HTTPMetrics(config ...HTTPMetricsConfig) echo.MiddlewareFunc {
	// Ensure meter is initialized
	ensureHTTPMeterInitialized()

	// Apply config
	var cfg HTTPMetricsConfig
	if len(config) > 0 {
		cfg = config[0]
	}

	handler := &httpMetricsHandler{config: cfg}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return handler.handle(c, next)
		}
	}
}

// normalizeRoute ensures the route is never empty, providing a fallback value.
func normalizeRoute(route string) string {
	if route == "" {
		return "unknown"
	}
	return route
}

// buildBaseAttributes creates attributes available before handler execution.
// These are used for active request tracking.
func buildBaseAttributes(method, scheme string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(attrHTTPRequestMethod, method),
		attribute.String(attrURLScheme, scheme),
	}
}

// buildDurationAttributes creates full attributes for the duration histogram.
// This includes response data and error classification.
func buildDurationAttributes(method, scheme string, statusCode int, route string, err error) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrHTTPRequestMethod, method),
		attribute.String(attrURLScheme, scheme),
		attribute.Int(attrHTTPResponseStatus, statusCode),
		attribute.String(attrHTTPRoute, normalizeRoute(route)),
	}
	if errorType := classifyHTTPError(statusCode, err); errorType != "" {
		attrs = append(attrs, attribute.String(attrErrorType, errorType))
	}
	return attrs
}

// extractScheme determines the URL scheme from the request.
// It checks X-Forwarded-Proto header first (for proxy setups), then falls back to TLS status.
func extractScheme(c echo.Context) string {
	// Check X-Forwarded-Proto header (set by reverse proxies)
	if proto := c.Request().Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	// Check if TLS is present
	if c.Request().TLS != nil {
		return "https"
	}
	return "http"
}

// classifyHTTPError returns an error type string based on status code and handler error.
// Returns empty string for successful responses (2xx, 3xx) without handler errors.
//
// Classification per OTel semantic conventions:
//   - 4xx errors: status code as string (e.g., "404", "400")
//   - 5xx errors: status code as string (e.g., "500", "503")
//   - Handler errors: error type or "handler_error"
func classifyHTTPError(statusCode int, err error) string {
	// For 4xx and 5xx, use status code as error type
	if statusCode >= 400 {
		return fmt.Sprintf("%d", statusCode)
	}

	// If handler returned error but status is OK, classify as handler error
	if err != nil {
		return "handler_error"
	}

	// No error for successful responses
	return ""
}

// IsInitialized returns true if HTTP metrics have been initialized.
// This is primarily useful for testing.
func IsInitialized() bool {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()
	return metricsInited
}

// ResetForTesting resets the metric state for testing purposes.
// This should only be called in tests.
func ResetForTesting() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	httpMeter = nil
	httpDurationHistogram = nil
	httpActiveRequestsGauge = nil
	metricsInited = false
	meterOnce = sync.Once{}
}
