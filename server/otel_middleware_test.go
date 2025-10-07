package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
)

const (
	testServiceName     = "test-service"
	testUserAPIEndpoint = "/api/users/:id"
	testAPIEndpoint     = "/api/test"
)

// setupTestServerWithTracing creates a test Echo server with OTel middleware and an in-memory span exporter.
// It properly saves and restores global OTel state to prevent test pollution.
func setupTestServerWithTracing(t *testing.T) (*echo.Echo, *tracetest.InMemoryExporter) {
	t.Helper()

	// Save original global state to restore after test
	originalTP := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()

	// Create in-memory exporter to capture spans
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)

	// Set W3C trace context propagator (same as observability provider does)
	// This is required for proper trace context propagation
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Cleanup: restore original global state after test completes
	t.Cleanup(func() {
		// Shutdown the test tracer provider to flush any pending spans
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}

		// Restore original global state
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalPropagator)
	})

	// Create Echo instance with middleware
	e := echo.New()
	cfg := &config.Config{
		App: config.AppConfig{
			Name: testServiceName,
		},
	}
	log := logger.New("disabled", false)

	// Setup middlewares (including OTel)
	healthPath := testHealthPath
	readyPath := testReadyPath
	SetupMiddlewares(e, log, cfg, healthPath, readyPath)

	return e, exporter
}

func TestOTelMiddlewareSpanCreation(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	// Register test route
	e.GET(testUserAPIEndpoint, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/api/users/123", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "Expected exactly one span to be created")

	span := spans[0]
	assert.Equal(t, "GET /api/users/:id", span.Name, "Span name should follow semantic convention")
	assert.Equal(t, trace.SpanKindServer, span.SpanKind, "Span kind should be Server")
}

func TestOTelMiddlewareSpanAttributes(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	// Register test route
	e.POST("/api/resources", func(c echo.Context) error {
		return c.JSON(http.StatusCreated, map[string]string{"id": "new-resource"})
	})

	// Make request
	req := httptest.NewRequest(http.MethodPost, "/api/resources", http.NoBody)
	req.Header.Set("User-Agent", "test-client/1.0")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify span attributes
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrs := spans[0].Attributes
	attrMap := make(map[string]interface{})
	for _, attr := range attrs {
		attrMap[string(attr.Key)] = attr.Value.AsInterface()
	}

	// Verify standard HTTP semantic attributes (v1.32.0+ uses different attribute names)
	// Check for http.request.method (new) or http.method (old)
	hasMethod := attrMap["http.request.method"] != nil || attrMap["http.method"] != nil
	assert.True(t, hasMethod, "Should have HTTP method attribute")

	// Check for http.route attribute
	assert.Contains(t, attrMap, "http.route", "Should have http.route attribute")

	// Check for http.response.status_code (new) or http.status_code (old)
	hasStatus := attrMap["http.response.status_code"] != nil || attrMap["http.status_code"] != nil
	assert.True(t, hasStatus, "Should have HTTP status code attribute")

	// Verify span status is OK for 2xx responses
	assert.Equal(t, codes.Unset, spans[0].Status.Code, "2xx responses should have Unset status (success)")
}

func TestOTelMiddlewareTraceContextPropagation(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	e.GET(testAPIEndpoint, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Send request with existing traceparent header
	incomingTraceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest(http.MethodGet, testAPIEndpoint, http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, incomingTraceparent)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	// OTel middleware should propagate the trace context
	// The trace ID should match the incoming traceparent's trace ID
	expectedTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	actualTraceID := span.SpanContext.TraceID().String()

	// Note: The trace provider we set up uses the incoming trace context
	// If propagation is working, the trace ID will match
	assert.Equal(t, expectedTraceID, actualTraceID, "Span should be part of incoming trace")

	// Verify span has a valid parent (remote span context from incoming traceparent)
	assert.True(t, span.Parent.IsValid(), "Span should have a valid parent")
	assert.True(t, span.Parent.IsRemote(), "Parent should be marked as remote")
}

func TestOTelMiddlewareErrorRecording(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		expectedStatus codes.Code
	}{
		{
			name:           "2xx_success",
			statusCode:     http.StatusOK,
			expectedStatus: codes.Unset,
		},
		{
			name:           "3xx_redirect",
			statusCode:     http.StatusMovedPermanently,
			expectedStatus: codes.Unset,
		},
		{
			name:           "4xx_client_error",
			statusCode:     http.StatusBadRequest,
			expectedStatus: codes.Unset, // 4xx is client error, not traced as error per OTel spec
		},
		{
			name:           "404_not_found",
			statusCode:     http.StatusNotFound,
			expectedStatus: codes.Unset, // 404 is normal response, not server error
		},
		{
			name:           "5xx_server_error",
			statusCode:     http.StatusInternalServerError,
			expectedStatus: codes.Error, // 5xx indicates server error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, exporter := setupTestServerWithTracing(t)

			e.GET(testAPIEndpoint, func(c echo.Context) error {
				return c.JSON(tt.statusCode, map[string]string{"status": "test"})
			})

			req := httptest.NewRequest(http.MethodGet, testAPIEndpoint, http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)

			span := spans[0]
			assert.Equal(t, tt.expectedStatus, span.Status.Code,
				"Span status code should be %v for HTTP %d", tt.expectedStatus, tt.statusCode)

			// Verify HTTP status code is recorded as attribute
			attrs := span.Attributes
			hasStatusCode := false
			for _, attr := range attrs {
				key := string(attr.Key)
				if key == "http.response.status_code" || key == "http.status_code" {
					hasStatusCode = true
					assert.Equal(t, int64(tt.statusCode), attr.Value.AsInt64())
				}
			}
			assert.True(t, hasStatusCode, "HTTP status code should be recorded as span attribute")
		})
	}
}

func TestOTelMiddlewareHealthProbeExclusion(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	// Register health and ready endpoints (done by server.New in real usage)
	e.GET(testHealthPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.GET(testReadyPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	// Also register a normal endpoint
	e.GET("/api/data", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"data": "test"})
	})

	tests := []struct {
		name          string
		path          string
		shouldTrace   bool
		expectedSpans int
	}{
		{
			name:          "health_endpoint_not_traced",
			path:          testHealthPath,
			shouldTrace:   false,
			expectedSpans: 0,
		},
		{
			name:          "ready_endpoint_not_traced",
			path:          testReadyPath,
			shouldTrace:   false,
			expectedSpans: 0,
		},
		{
			name:          "normal_endpoint_traced",
			path:          "/api/data",
			shouldTrace:   true,
			expectedSpans: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset exporter
			exporter.Reset()

			req := httptest.NewRequest(http.MethodGet, tt.path, http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)

			spans := exporter.GetSpans()
			assert.Len(t, spans, tt.expectedSpans,
				"Endpoint %s should create %d spans", tt.path, tt.expectedSpans)
		})
	}
}

func TestOTelMiddlewareSpanNaming(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	routes := []struct {
		method       string
		path         string
		expectedName string
	}{
		{http.MethodGet, testUserAPIEndpoint, "GET /api/users/:id"},
		{http.MethodPost, "/api/users", "POST /api/users"},
		{http.MethodPut, testUserAPIEndpoint, "PUT /api/users/:id"},
		{http.MethodDelete, testUserAPIEndpoint, "DELETE /api/users/:id"},
		{http.MethodPatch, "/api/users/:id/status", "PATCH /api/users/:id/status"},
	}

	for _, route := range routes {
		e.Add(route.method, route.path, func(c echo.Context) error {
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})
	}

	for i, route := range routes {
		t.Run(route.expectedName, func(t *testing.T) {
			exporter.Reset()

			// Build actual request path (replace :id with actual value)
			requestPath := route.path
			if route.method != http.MethodPost {
				requestPath = "/api/users/123"
				if route.method == http.MethodPatch {
					requestPath = "/api/users/123/status"
				}
			}

			req := httptest.NewRequest(route.method, requestPath, http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1, "Request %d should create exactly one span", i)

			assert.Equal(t, route.expectedName, spans[0].Name,
				"Span name should follow 'METHOD /route' convention")
		})
	}
}

func TestOTelMiddlewareConcurrentRequests(t *testing.T) {
	e, exporter := setupTestServerWithTracing(t)

	e.GET(testAPIEndpoint, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Send multiple concurrent requests
	const numRequests = 10
	done := make(chan bool, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, testAPIEndpoint, http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			done <- true
		}()
	}

	// Wait for all requests to complete
	for i := 0; i < numRequests; i++ {
		<-done
	}

	// Verify we got the right number of spans
	spans := exporter.GetSpans()
	assert.Len(t, spans, numRequests, "Should create one span per request")

	// Verify all spans have unique span IDs
	spanIDs := make(map[string]bool)
	for _, span := range spans {
		spanID := span.SpanContext.SpanID().String()
		assert.False(t, spanIDs[spanID], "Span IDs should be unique")
		spanIDs[spanID] = true
	}
}

func TestOTelMiddlewareIntegrationWithTraceContext(t *testing.T) {
	// This test verifies that otelecho middleware and TraceContext middleware work together
	e, exporter := setupTestServerWithTracing(t)

	var capturedContext context.Context

	e.GET(testAPIEndpoint, func(c echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	incomingTraceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	incomingTracestate := "congo=t61rcWkgMzE"

	req := httptest.NewRequest(http.MethodGet, testAPIEndpoint, http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, incomingTraceparent)
	req.Header.Set(gobrickshttp.HeaderTraceState, incomingTracestate)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Verify TraceContext middleware still injects headers into context
	require.NotNil(t, capturedContext)

	// TraceContext middleware should have preserved the trace headers
	contextTraceparent, ok := gobrickshttp.TraceParentFromContext(capturedContext)
	assert.True(t, ok, "TraceContext middleware should inject traceparent into context")
	assert.Equal(t, incomingTraceparent, contextTraceparent)

	contextTracestate, ok := gobrickshttp.TraceStateFromContext(capturedContext)
	assert.True(t, ok, "TraceContext middleware should inject tracestate into context")
	assert.Equal(t, incomingTracestate, contextTracestate)

	// TraceID should also be available
	traceID, ok := gobrickshttp.TraceIDFromContext(capturedContext)
	assert.True(t, ok, "TraceID should be available in context")
	assert.NotEmpty(t, traceID)
}

func TestOTelMiddlewareWithCustomBasePath(t *testing.T) {
	// Test that spans are created correctly when server uses a custom base path

	// Save original global state
	originalTP := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Cleanup: restore original global state
	t.Cleanup(func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalPropagator)
	})

	e := echo.New()
	cfg := &config.Config{
		App: config.AppConfig{
			Name: testServiceName,
		},
		Server: config.ServerConfig{
			Path: config.PathConfig{
				Base:   "/api/v1",
				Health: testHealthPath,
				Ready:  testReadyPath,
			},
		},
	}
	log := logger.New("disabled", false)

	// Setup middlewares with custom base path
	healthPath := "/api/v1/health"
	readyPath := "/api/v1/ready"
	SetupMiddlewares(e, log, cfg, healthPath, readyPath)

	// Register route under base path
	group := e.Group("/api/v1")
	group.GET("/users/:id", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/123", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify span was created with correct route
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "GET /api/v1/users/:id", spans[0].Name)
}
