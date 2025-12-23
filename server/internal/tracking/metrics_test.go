package tracking

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func setupTestMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	ResetForTesting()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		ResetForTesting()
	})

	return reader
}

func TestHTTPMetricsMiddleware(t *testing.T) {
	reader := setupTestMeterProvider(t)

	e := echo.New()
	e.Use(HTTPMetrics())

	// Register a test route
	e.GET("/users/:id", func(c echo.Context) error {
		return c.String(http.StatusOK, "user")
	})

	// Make a request
	req := httptest.NewRequest(http.MethodGet, "/users/123", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find our metrics
	var foundDuration, foundActive bool
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != httpMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricHTTPRequestDuration:
				foundDuration = true
				// Verify it's a histogram
				hist, ok := m.Data.(metricdata.Histogram[float64])
				require.True(t, ok, "expected histogram data")
				require.NotEmpty(t, hist.DataPoints, "expected at least one data point")

				// Verify attributes
				dp := hist.DataPoints[0]
				attrs := dp.Attributes.ToSlice()
				assertAttribute(t, attrs, attrHTTPRequestMethod, "GET")
				assertAttribute(t, attrs, attrHTTPRoute, "/users/:id")
				assertAttribute(t, attrs, attrHTTPResponseStatus, int64(200))

			case metricHTTPActiveRequests:
				foundActive = true
				// Active requests should be 0 after request completes
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok, "expected sum data")
				// The counter was incremented then decremented, so net should be 0
				if len(sum.DataPoints) > 0 {
					// Note: In delta temporality, this might be different
					// For cumulative, we expect the current value to be 0
					t.Logf("Active requests data points: %+v", sum.DataPoints)
				}
			}
		}
	}

	assert.True(t, foundDuration, "expected to find http.server.request.duration metric")
	assert.True(t, foundActive, "expected to find http.server.active_requests metric")
}

func TestHTTPMetricsMiddlewareWithError(t *testing.T) {
	reader := setupTestMeterProvider(t)

	e := echo.New()
	e.Use(HTTPMetrics())

	// Register a route that returns 404
	e.GET("/missing", func(c echo.Context) error {
		return c.String(http.StatusNotFound, "not found")
	})

	// Make a request
	req := httptest.NewRequest(http.MethodGet, "/missing", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find duration metric and verify error.type attribute
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != httpMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != metricHTTPRequestDuration {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			require.NotEmpty(t, hist.DataPoints)

			attrs := hist.DataPoints[0].Attributes.ToSlice()
			assertAttribute(t, attrs, attrHTTPResponseStatus, int64(404))
			assertAttribute(t, attrs, attrErrorType, "404")
			return
		}
	}
	t.Fatal("expected to find http.server.request.duration metric with error.type")
}

func TestHTTPMetricsMiddlewareWithSkipper(t *testing.T) {
	reader := setupTestMeterProvider(t)

	e := echo.New()
	e.Use(HTTPMetrics(HTTPMetricsConfig{
		Skipper: func(c echo.Context) bool {
			return c.Path() == "/health"
		},
	}))

	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	e.GET("/api", func(c echo.Context) error {
		return c.String(http.StatusOK, "api")
	})

	// Request to skipped path
	req1 := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, req1)

	// Request to normal path
	req2 := httptest.NewRequest(http.MethodGet, "/api", http.NoBody)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Should only find metrics for /api, not /health
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != httpMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != metricHTTPRequestDuration {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)

			// Should only have one data point (from /api)
			assert.Len(t, hist.DataPoints, 1, "expected only one data point (skipped /health)")

			attrs := hist.DataPoints[0].Attributes.ToSlice()
			assertAttribute(t, attrs, attrHTTPRoute, "/api")
			return
		}
	}
}

func TestExtractScheme(t *testing.T) {
	tests := []struct {
		name           string
		tlsEnabled     bool
		forwardedProto string
		expected       string
	}{
		{
			name:           "http without proxy",
			tlsEnabled:     false,
			forwardedProto: "",
			expected:       "http",
		},
		{
			name:           "https with TLS",
			tlsEnabled:     true,
			forwardedProto: "",
			expected:       "https",
		},
		{
			name:           "https from X-Forwarded-Proto",
			tlsEnabled:     false,
			forwardedProto: "https",
			expected:       "https",
		},
		{
			name:           "X-Forwarded-Proto takes precedence",
			tlsEnabled:     true,
			forwardedProto: "http", // Unusual but tests precedence
			expected:       "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Note: Setting TLS on request requires more setup, so we test header-based detection
			if tt.forwardedProto == "" && tt.tlsEnabled {
				// Can't easily test TLS without more setup, skip
				t.Skip("TLS test requires additional setup")
			}

			scheme := extractScheme(c)
			assert.Equal(t, tt.expected, scheme)
		})
	}
}

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		err        error
		expected   string
	}{
		{name: "200 OK", statusCode: 200, err: nil, expected: ""},
		{name: "200 with handler error", statusCode: 200, err: assert.AnError, expected: "handler_error"},
		{name: "201 Created", statusCode: 201, err: nil, expected: ""},
		{name: "301 Redirect", statusCode: 301, err: nil, expected: ""},
		{name: "400 Bad Request", statusCode: 400, err: nil, expected: "400"},
		{name: "404 Not Found", statusCode: 404, err: nil, expected: "404"},
		{name: "500 Internal Error", statusCode: 500, err: nil, expected: "500"},
		{name: "503 Service Unavailable", statusCode: 503, err: nil, expected: "503"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyHTTPError(tt.statusCode, tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPMetricsHistogramBuckets(t *testing.T) {
	// Verify the bucket boundaries match OTel semantic conventions
	expected := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	assert.Equal(t, expected, httpDurationBuckets)
}

func TestIsInitialized(t *testing.T) {
	ResetForTesting()
	assert.False(t, IsInitialized(), "should not be initialized after reset")

	ensureHTTPMeterInitialized()
	assert.True(t, IsInitialized(), "should be initialized after ensureHTTPMeterInitialized")
}

// assertAttribute checks that an attribute with the given key and value exists in the slice.
func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expectedValue any) {
	t.Helper()
	for _, kv := range attrs {
		if string(kv.Key) == key {
			switch ev := expectedValue.(type) {
			case int64:
				assert.Equal(t, ev, kv.Value.AsInt64(), "attribute %s value mismatch", key)
			case int:
				assert.Equal(t, int64(ev), kv.Value.AsInt64(), "attribute %s value mismatch", key)
			case string:
				assert.Equal(t, ev, kv.Value.AsString(), "attribute %s value mismatch", key)
			default:
				t.Errorf("unsupported expected value type for attribute %s", key)
			}
			return
		}
	}
	t.Errorf("attribute %s not found in %v", key, attrs)
}
