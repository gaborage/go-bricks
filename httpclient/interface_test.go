package httpclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test constants to avoid string duplication
const (
	testExampleURL    = "http://example.com"
	testMultiTrace    = "multi-trace-123"
	testPriorityTrace = "X-Priority-Trace"
)

// TestNewTraceIDInterceptor tests the basic trace ID interceptor
func TestNewTraceIDInterceptor(t *testing.T) {
	t.Run("adds trace ID when header is missing", func(t *testing.T) {
		interceptor := NewTraceIDInterceptor()

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		ctx := WithTraceID(context.Background(), "test-trace-123")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		assert.Equal(t, "test-trace-123", req.Header.Get(HeaderXRequestID))
	})

	t.Run("preserves existing trace ID header", func(t *testing.T) {
		interceptor := NewTraceIDInterceptor()

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Set an existing trace ID
		req.Header.Set(HeaderXRequestID, "existing-trace-456")

		ctx := WithTraceID(context.Background(), "new-trace-789")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		// Should preserve the existing header
		assert.Equal(t, "existing-trace-456", req.Header.Get(HeaderXRequestID))
	})

	t.Run("generates trace ID when none in context", func(t *testing.T) {
		interceptor := NewTraceIDInterceptor()

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Use empty context - should generate a new trace ID
		err = interceptor(context.Background(), req)
		assert.NoError(t, err)

		traceID := req.Header.Get(HeaderXRequestID)
		assert.NotEmpty(t, traceID)
		assert.NotEqual(t, "", traceID)
	})
}

// TestNewTraceIDInterceptorFor tests the custom header interceptor
func TestNewTraceIDInterceptorFor(t *testing.T) {
	t.Run("uses custom header name", func(t *testing.T) {
		customHeader := "X-Custom-Trace-ID"
		interceptor := NewTraceIDInterceptorFor(customHeader)

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		ctx := WithTraceID(context.Background(), "custom-trace-123")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		assert.Equal(t, "custom-trace-123", req.Header.Get(customHeader))
		assert.Empty(t, req.Header.Get(HeaderXRequestID)) // Default header should not be set
	})

	t.Run("falls back to default header when empty string provided", func(t *testing.T) {
		interceptor := NewTraceIDInterceptorFor("")

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		ctx := WithTraceID(context.Background(), "fallback-trace-456")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		assert.Equal(t, "fallback-trace-456", req.Header.Get(HeaderXRequestID))
	})

	t.Run("preserves existing custom header", func(t *testing.T) {
		customHeader := "X-My-Trace"
		interceptor := NewTraceIDInterceptorFor(customHeader)

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Set existing value in custom header
		req.Header.Set(customHeader, "existing-custom-789")

		ctx := WithTraceID(context.Background(), "new-trace-000")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		// Should preserve existing custom header
		assert.Equal(t, "existing-custom-789", req.Header.Get(customHeader))
	})

	t.Run("works with different header names", func(t *testing.T) {
		testCases := []struct {
			name   string
			header string
		}{
			{"x-request-id", "X-Request-ID"},
			{"x-correlation-id", "X-Correlation-ID"},
			{"x-trace-id", "X-Trace-ID"},
			{"trace-id", "Trace-ID"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				interceptor := NewTraceIDInterceptorFor(tc.header)

				req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
				assert.NoError(t, err)

				expectedTraceID := "trace-for-" + tc.name
				ctx := WithTraceID(context.Background(), expectedTraceID)

				err = interceptor(ctx, req)
				assert.NoError(t, err)

				assert.Equal(t, expectedTraceID, req.Header.Get(tc.header))
			})
		}
	})

	t.Run("handles multiple interceptors with different headers", func(t *testing.T) {
		interceptor1 := NewTraceIDInterceptorFor("X-Trace-A")
		interceptor2 := NewTraceIDInterceptorFor("X-Trace-B")

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		ctx := WithTraceID(context.Background(), testMultiTrace)

		// Apply both interceptors
		err = interceptor1(ctx, req)
		assert.NoError(t, err)

		err = interceptor2(ctx, req)
		assert.NoError(t, err)

		// Both headers should be set
		assert.Equal(t, testMultiTrace, req.Header.Get("X-Trace-A"))
		assert.Equal(t, testMultiTrace, req.Header.Get("X-Trace-B"))
	})

	t.Run("generates trace ID when none in context for custom header", func(t *testing.T) {
		customHeader := "X-Generated-Trace"
		interceptor := NewTraceIDInterceptorFor(customHeader)

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Use empty context - should generate a new trace ID
		err = interceptor(context.Background(), req)
		assert.NoError(t, err)

		traceID := req.Header.Get(customHeader)
		assert.NotEmpty(t, traceID)
		assert.NotEqual(t, "", traceID)
	})
}

// TestTraceIDInterceptorIntegration tests integration scenarios
func TestTraceIDInterceptorIntegration(t *testing.T) {
	t.Run("interceptor respects header priority", func(t *testing.T) {
		interceptor := NewTraceIDInterceptorFor(testPriorityTrace)

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Pre-populate header with existing value
		req.Header.Set(testPriorityTrace, "priority-value")

		// Context has different trace ID
		ctx := WithTraceID(context.Background(), "context-value")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		// Header value should remain unchanged (priority to existing header)
		assert.Equal(t, "priority-value", req.Header.Get(testPriorityTrace))
	})

	t.Run("interceptor works with W3C trace context", func(t *testing.T) {
		interceptor := NewTraceIDInterceptorFor("X-W3C-Trace")

		req, err := http.NewRequestWithContext(context.Background(), "GET", testExampleURL, http.NoBody)
		assert.NoError(t, err)

		// Set up context with both trace ID and W3C traceparent
		ctx := WithTraceID(context.Background(), "w3c-trace-123")
		ctx = WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

		err = interceptor(ctx, req)
		assert.NoError(t, err)

		// Should use the trace ID from context
		assert.Equal(t, "w3c-trace-123", req.Header.Get("X-W3C-Trace"))
	})
}
