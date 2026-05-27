package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	obtest "github.com/gaborage/go-bricks/observability/testing"

	"github.com/gaborage/go-bricks/httpclient/internal/tracking"
	"github.com/gaborage/go-bricks/logger"
)

// Test constants to avoid string duplication
const (
	testAPIKey         = "X-API-Key"
	testAPIValue       = "test-key"
	testUserAgent      = "User-Agent"
	testAgentValue     = "test-agent"
	testIntercepted    = "X-Intercepted"
	testCustomTrace    = "custom-trace-123"
	testContentTypeHdr = "Content-Type"
	testJSONType       = "application/json"
)

// createTestLogger creates a logger that outputs to a buffer for testing
func createTestLogger() logger.Logger {
	return logger.New("info", false)
}

func newIPv4TestServer(t *testing.T, handler nethttp.Handler) *httptest.Server {
	t.Helper()
	lc := net.ListenConfig{}
	listener, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping test: unable to bind IPv4 listener: %v", err)
		return &httptest.Server{}
	}

	server := &httptest.Server{
		Listener: listener,
		Config:   &nethttp.Server{Handler: handler},
	}
	server.Start()
	return server
}

type roundTripperFunc func(*nethttp.Request) (*nethttp.Response, error)

func (f roundTripperFunc) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	return f(req)
}

type stubRoundTripper struct {
	name string
}

func (s *stubRoundTripper) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	return nil, fmt.Errorf("blocked request %s via %s", req.URL, s.name)
}

func TestNewClient(t *testing.T) {
	log := createTestLogger()
	client := NewClient(log)

	assert.NotNil(t, client)
}

func TestBuilder(t *testing.T) {
	log := createTestLogger()

	t.Run("default configuration", func(t *testing.T) {
		client := NewBuilder(log).Build()
		assert.NotNil(t, client)
	})

	t.Run("with timeout", func(t *testing.T) {
		timeout := 10 * time.Second
		client := NewBuilder(log).
			WithTimeout(timeout).
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with retries", func(t *testing.T) {
		client := NewBuilder(log).
			WithRetries(3, 2*time.Second).
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with basic auth", func(t *testing.T) {
		client := NewBuilder(log).
			WithBasicAuth("user", "pass").
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with default headers", func(t *testing.T) {
		client := NewBuilder(log).
			WithDefaultHeader(testAPIKey, testAPIValue).
			WithDefaultHeader(testUserAgent, testAgentValue).
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with interceptors", func(t *testing.T) {
		reqInterceptor := func(_ context.Context, req *nethttp.Request) error {
			req.Header.Set(testIntercepted, "true")
			return nil
		}

		respInterceptor := func(_ context.Context, _ *nethttp.Request, resp *nethttp.Response) error {
			resp.Header.Set("X-Response-Intercepted", "true")
			return nil
		}

		client := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			WithResponseInterceptor(respInterceptor).
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with custom http client", func(t *testing.T) {
		customTransport := roundTripperFunc(func(req *nethttp.Request) (*nethttp.Response, error) {
			return nil, fmt.Errorf("not implemented: %s", req.URL)
		})
		custom := &nethttp.Client{Timeout: 123 * time.Millisecond, Transport: customTransport}
		built := NewBuilder(log).
			WithHTTPClient(custom).
			WithTimeout(5 * time.Second).
			Build()

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.Equal(t, custom, clientImpl.httpClient)
		assert.Equal(t, 123*time.Millisecond, clientImpl.httpClient.Timeout)
	})

	t.Run("with custom http client zero timeout uses builder timeout", func(t *testing.T) {
		custom := &nethttp.Client{}
		built := NewBuilder(log).
			WithHTTPClient(custom).
			WithTimeout(2 * time.Second).
			Build()

		clientImpl := built.(*client)
		assert.Equal(t, 2*time.Second, clientImpl.httpClient.Timeout)
	})

	t.Run("with custom transport", func(t *testing.T) {
		transport := &stubRoundTripper{name: "stub"}
		built := NewBuilder(log).
			WithTransport(transport).
			Build()

		clientImpl := built.(*client)
		assert.Equal(t, transport, clientImpl.httpClient.Transport)
	})

	t.Run("with trace ID header", func(t *testing.T) {
		customHeader := "X-Custom-Trace-ID"
		builtClient := NewBuilder(log).
			WithTraceIDHeader(customHeader).
			Build()

		// Assert against the client's config since tests are in the same package
		clientImpl := builtClient.(*client)
		assert.Equal(t, customHeader, clientImpl.config.TraceIDHeader)
	})

	t.Run("with trace ID header empty string", func(t *testing.T) {
		builtClient := NewBuilder(log).
			WithTraceIDHeader("").
			Build()

		// Empty string should not change the default
		clientImpl := builtClient.(*client)
		assert.Equal(t, HeaderXRequestID, clientImpl.config.TraceIDHeader)
	})

	t.Run("with custom trace ID generator", func(t *testing.T) {
		var generatorCallCount int32
		customGenerator := func() string {
			atomic.AddInt32(&generatorCallCount, 1)
			return testCustomTrace
		}

		builtClient := NewBuilder(log).
			WithTraceIDGenerator(customGenerator).
			Build()

		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.NewTraceID)

		// Test that the custom generator is actually used
		traceID := clientImpl.config.NewTraceID()
		assert.Equal(t, testCustomTrace, traceID)
		assert.Equal(t, int32(1), atomic.LoadInt32(&generatorCallCount))
	})

	t.Run("with nil trace ID generator", func(t *testing.T) {
		builtClient := NewBuilder(log).
			WithTraceIDGenerator(nil).
			Build()

		// nil generator should not change the default
		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.NewTraceID)
	})

	t.Run("with custom trace ID extractor", func(t *testing.T) {
		type contextKey string
		const customTraceKey contextKey = "custom-trace"

		customExtractor := func(ctx context.Context) (string, bool) {
			if val := ctx.Value(customTraceKey); val != nil {
				return val.(string), true
			}
			return "", false
		}

		builtClient := NewBuilder(log).
			WithTraceIDExtractor(customExtractor).
			Build()

		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.TraceIDExtractor)

		// Test the custom extractor logic
		ctx := context.WithValue(context.Background(), customTraceKey, "extracted-123")
		traceID, found := clientImpl.config.TraceIDExtractor(ctx)
		assert.True(t, found)
		assert.Equal(t, "extracted-123", traceID)

		// Test fallback behavior
		emptyCtx := context.Background()
		_, found = clientImpl.config.TraceIDExtractor(emptyCtx)
		assert.False(t, found)
	})

	t.Run("with nil trace ID extractor", func(t *testing.T) {
		builtClient := NewBuilder(log).
			WithTraceIDExtractor(nil).
			Build()

		// nil extractor should not change the default
		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.TraceIDExtractor)
	})

	t.Run("with W3C trace enabled", func(t *testing.T) {
		builtClient := NewBuilder(log).
			WithW3CTrace(true).
			Build()

		clientImpl := builtClient.(*client)
		assert.True(t, clientImpl.config.EnableW3CTrace)
	})

	t.Run("with W3C trace disabled", func(t *testing.T) {
		builtClient := NewBuilder(log).
			WithW3CTrace(false).
			Build()

		clientImpl := builtClient.(*client)
		assert.False(t, clientImpl.config.EnableW3CTrace)
	})

	t.Run("combined trace configuration", func(t *testing.T) {
		var generatorCalls int32
		customGenerator := func() string {
			atomic.AddInt32(&generatorCalls, 1)
			return fmt.Sprintf("trace-%d", atomic.LoadInt32(&generatorCalls))
		}

		customExtractor := func(_ context.Context) (string, bool) {
			return "extracted-from-ctx", true
		}

		builtClient := NewBuilder(log).
			WithTraceIDHeader("X-My-Trace").
			WithTraceIDGenerator(customGenerator).
			WithTraceIDExtractor(customExtractor).
			WithW3CTrace(false).
			Build()

		clientImpl := builtClient.(*client)
		assert.Equal(t, "X-My-Trace", clientImpl.config.TraceIDHeader)
		assert.False(t, clientImpl.config.EnableW3CTrace)

		// Test that extractor takes precedence over generator
		traceID, found := clientImpl.config.TraceIDExtractor(context.Background())
		assert.True(t, found)
		assert.Equal(t, "extracted-from-ctx", traceID)

		// Generator should still work when called directly
		generatedID := clientImpl.config.NewTraceID()
		assert.Equal(t, "trace-1", generatedID)
		assert.Equal(t, int32(1), atomic.LoadInt32(&generatorCalls))
	})
}

func TestClientHTTPMethods(t *testing.T) {
	log := createTestLogger()

	tests := []struct {
		name           string
		method         string
		expectedMethod string
	}{
		{"GET", "GET", "GET"},
		{"POST", "POST", "POST"},
		{"PUT", "PUT", "PUT"},
		{"PATCH", "PATCH", "PATCH"},
		{"DELETE", "DELETE", "DELETE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				assert.Equal(t, tt.expectedMethod, r.Method)
				w.WriteHeader(nethttp.StatusOK)
				w.Write([]byte(`{"status": "ok"}`))
			}))
			defer server.Close()

			client := NewClient(log)
			req := &Request{
				URL: server.URL,
			}

			ctx := context.Background()
			var resp *Response
			var err error

			switch tt.method {
			case "GET":
				resp, err = client.Get(ctx, req)
			case "POST":
				resp, err = client.Post(ctx, req)
			case "PUT":
				resp, err = client.Put(ctx, req)
			case "PATCH":
				resp, err = client.Patch(ctx, req)
			case "DELETE":
				resp, err = client.Delete(ctx, req)
			}

			require.NoError(t, err)
			assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
			assert.Equal(t, `{"status": "ok"}`, string(resp.Body))
			// Note: Real HTTP requests typically have measurable overhead,
			// but use >= 0 for robustness across all platforms
			assert.GreaterOrEqual(t, resp.Stats.ElapsedTime, time.Duration(0))
			assert.Equal(t, int64(1), resp.Stats.CallCount)
		})
	}
}

func TestClientRequestValidation(t *testing.T) {
	log := createTestLogger()
	client := NewClient(log)
	ctx := context.Background()

	t.Run("nil request", func(t *testing.T) {
		_, err := client.Get(ctx, nil)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, ValidationError))
	})

	t.Run("empty URL", func(t *testing.T) {
		req := &Request{URL: ""}
		_, err := client.Get(ctx, req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, ValidationError))
	})
}

func TestClientHeaders(t *testing.T) {
	log := createTestLogger()

	t.Run("request headers", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, testJSONType, r.Header.Get(testContentTypeHdr))
			assert.Equal(t, "test-value", r.Header.Get("X-Custom-Header"))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				testContentTypeHdr: testJSONType,
				"X-Custom-Header":  "test-value",
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("default headers", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, testAgentValue, r.Header.Get(testUserAgent))
			assert.Equal(t, testAPIValue, r.Header.Get(testAPIKey))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithDefaultHeader(testUserAgent, testAgentValue).
			WithDefaultHeader(testAPIKey, testAPIValue).
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("request headers override defaults", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "custom-agent", r.Header.Get(testUserAgent))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithDefaultHeader(testUserAgent, "default-agent").
			Build()

		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				testUserAgent: "custom-agent",
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})
}

func TestClientBasicAuth(t *testing.T) {
	log := createTestLogger()

	t.Run("client-level auth", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			username, password, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "user", username)
			assert.Equal(t, "pass", password)
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithBasicAuth("user", "pass").
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("request-level auth overrides client auth", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			username, password, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "request-user", username)
			assert.Equal(t, "request-pass", password)
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithBasicAuth("client-user", "client-pass").
			Build()

		req := &Request{
			URL: server.URL,
			Auth: &BasicAuth{
				Username: "request-user",
				Password: "request-pass",
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})
}

func TestDefaultContentTypeWhenBodyPresent(t *testing.T) {
	log := createTestLogger()
	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		// Content-Type should default to application/json when body is present
		assert.Equal(t, testJSONType, r.Header.Get(testContentTypeHdr))
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	client := NewClient(log)
	req := &Request{
		URL:  server.URL,
		Body: []byte(`{"a":1}`),
		// No Content-Type header provided
	}

	_, err := client.Post(context.Background(), req)
	require.NoError(t, err)
}

func TestClientInterceptors(t *testing.T) {
	log := createTestLogger()

	t.Run("request interceptor", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "intercepted", r.Header.Get(testIntercepted))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		reqInterceptor := func(_ context.Context, req *nethttp.Request) error {
			req.Header.Set(testIntercepted, "intercepted")
			return nil
		}

		client := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("response interceptor", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		interceptorCalled := false
		respInterceptor := func(_ context.Context, _ *nethttp.Request, _ *nethttp.Response) error {
			interceptorCalled = true
			return nil
		}

		client := NewBuilder(log).
			WithResponseInterceptor(respInterceptor).
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
		assert.True(t, interceptorCalled)
	})
}

func TestInterceptorErrors(t *testing.T) {
	log := createTestLogger()

	t.Run("request interceptor error", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		reqInterceptor := func(_ context.Context, _ *nethttp.Request) error {
			return fmt.Errorf("boom")
		}

		client := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			Build()

		req := &Request{URL: server.URL}
		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, InterceptorError))
	})

	t.Run("response interceptor error", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		respInterceptor := func(_ context.Context, _ *nethttp.Request, _ *nethttp.Response) error {
			return fmt.Errorf("boom resp")
		}

		client := NewBuilder(log).
			WithResponseInterceptor(respInterceptor).
			Build()

		req := &Request{URL: server.URL}
		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, InterceptorError))
	})
}

func TestClientErrorHandling(t *testing.T) {
	log := createTestLogger()
	client := NewClient(log)

	t.Run("HTTP error status", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(nethttp.StatusNotFound)
			w.Write([]byte(`{"error": "not found"}`))
		}))
		defer server.Close()

		req := &Request{URL: server.URL}

		resp, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, HTTPError))
		assert.True(t, IsHTTPStatusError(err, nethttp.StatusNotFound))

		// Response should still be available even with error
		assert.NotNil(t, resp)
		assert.Equal(t, nethttp.StatusNotFound, resp.StatusCode)
		assert.Equal(t, `{"error": "not found"}`, string(resp.Body))
	})

	t.Run("network error", func(t *testing.T) {
		req := &Request{URL: "http://invalid-url-that-does-not-exist"}

		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, NetworkError))
	})

	t.Run("timeout error", func(t *testing.T) {
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithTimeout(10 * time.Millisecond).
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, TimeoutError))
	})
}

func TestClientStats(t *testing.T) {
	log := createTestLogger()
	client := NewClient(log)

	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		time.Sleep(10 * time.Millisecond) // Small delay to measure
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	req := &Request{URL: server.URL}

	// First request
	resp1, err := client.Get(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp1.Stats.CallCount)
	assert.Greater(t, resp1.Stats.ElapsedTime, 10*time.Millisecond)

	// Second request
	resp2, err := client.Get(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp2.Stats.CallCount)
	assert.Greater(t, resp2.Stats.ElapsedTime, 10*time.Millisecond)
}

func TestClientRetries(t *testing.T) {
	log := createTestLogger()

	t.Run("retries on 5xx then succeeds", func(t *testing.T) {
		var calls atomic.Int32
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(nethttp.StatusInternalServerError)
				w.Write([]byte("fail"))
				return
			}
			w.WriteHeader(nethttp.StatusOK)
			w.Write([]byte("ok"))
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithRetries(2, 5*time.Millisecond).
			Build()

		req := &Request{URL: server.URL}
		resp, err := client.Get(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "ok", string(resp.Body))
		assert.Equal(t, int32(2), calls.Load())
	})

	t.Run("does not retry on 4xx", func(t *testing.T) {
		var calls atomic.Int32
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			calls.Add(1)
			w.WriteHeader(nethttp.StatusBadRequest)
			w.Write([]byte("bad"))
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithRetries(3, 5*time.Millisecond).
			Build()

		req := &Request{URL: server.URL}
		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("retries on timeout then fails", func(t *testing.T) {
		var calls atomic.Int32
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithTimeout(10*time.Millisecond).
			WithRetries(1, 5*time.Millisecond).
			Build()

		req := &Request{URL: server.URL}
		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, TimeoutError))
		assert.Equal(t, int32(2), calls.Load()) // initial + one retry
	})
}

func TestTraceIDPropagation(t *testing.T) {
	log := createTestLogger()

	t.Run("automatically adds trace ID when none present", func(t *testing.T) {
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
			w.Write([]byte("ok"))
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)

		// Should have automatically added X-Request-ID header
		traceID := requestHeaders.Get(HeaderXRequestID)
		assert.NotEmpty(t, traceID)
		assert.Len(t, traceID, 36) // UUID format
	})

	t.Run("preserves existing X-Request-ID header", func(t *testing.T) {
		expectedTraceID := testCustomTrace
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				HeaderXRequestID: expectedTraceID,
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)

		// Should preserve the existing trace ID
		assert.Equal(t, expectedTraceID, requestHeaders.Get(HeaderXRequestID))
	})

	t.Run("extracts trace ID from context", func(t *testing.T) {
		expectedTraceID := "context-trace-456"
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{URL: server.URL}

		// Add trace ID to context
		ctx := WithTraceID(context.Background(), expectedTraceID)

		_, err := client.Get(ctx, req)
		require.NoError(t, err)

		// Should use trace ID from context
		assert.Equal(t, expectedTraceID, requestHeaders.Get(HeaderXRequestID))
	})

	t.Run("request header takes precedence over context", func(t *testing.T) {
		contextTraceID := "context-trace"
		headerTraceID := "header-trace"
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				HeaderXRequestID: headerTraceID,
			},
		}

		// Add different trace ID to context
		ctx := WithTraceID(context.Background(), contextTraceID)

		_, err := client.Get(ctx, req)
		require.NoError(t, err)

		// Request header should take precedence
		assert.Equal(t, headerTraceID, requestHeaders.Get(HeaderXRequestID))
	})

	t.Run("trace ID interceptor works correctly", func(t *testing.T) {
		expectedTraceID := "interceptor-trace-789"
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		// Create client with trace ID interceptor
		client := NewBuilder(log).
			WithRequestInterceptor(NewTraceIDInterceptor()).
			Build()

		req := &Request{URL: server.URL}
		ctx := WithTraceID(context.Background(), expectedTraceID)

		_, err := client.Get(ctx, req)
		require.NoError(t, err)

		// Should use trace ID from interceptor
		assert.Equal(t, expectedTraceID, requestHeaders.Get(HeaderXRequestID))
	})

	t.Run("adds W3C traceparent when enabled", func(t *testing.T) {
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)

		tp := requestHeaders.Get(HeaderTraceParent)
		assert.NotEmpty(t, tp)
		// Basic shape: 2-32-16-2 hex groups separated by '-'
		parts := strings.Split(tp, "-")
		require.Len(t, parts, 4)
		assert.Len(t, parts[0], 2)
		assert.Len(t, parts[1], 32)
		assert.Len(t, parts[2], 16)
		assert.Len(t, parts[3], 2)
	})

	t.Run("propagates traceparent and tracestate from context", func(t *testing.T) {
		var requestHeaders nethttp.Header
		server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestHeaders = r.Header.Clone()
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{URL: server.URL}

		ctx := context.Background()
		ctx = WithTraceParent(ctx, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		ctx = WithTraceState(ctx, "vendor=k:v")

		_, err := client.Get(ctx, req)
		require.NoError(t, err)

		assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", requestHeaders.Get(HeaderTraceParent))
		assert.Equal(t, "vendor=k:v", requestHeaders.Get(HeaderTraceState))
	})
}

func TestTraceIDUtilities(t *testing.T) {
	t.Run("WithTraceID and EnsureTraceID", func(t *testing.T) {
		expectedTraceID := "test-trace-123"
		ctx := WithTraceID(context.Background(), expectedTraceID)

		actualTraceID := EnsureTraceID(ctx)
		assert.Equal(t, expectedTraceID, actualTraceID)
	})

	t.Run("EnsureTraceID generates UUID when no trace ID", func(t *testing.T) {
		traceID := EnsureTraceID(context.Background())
		assert.NotEmpty(t, traceID)
		assert.Len(t, traceID, 36) // UUID format
	})

	t.Run("NewTraceIDInterceptor creates valid interceptor", func(t *testing.T) {
		interceptor := NewTraceIDInterceptor()
		assert.NotNil(t, interceptor)

		// Test that it adds header when missing
		ctx := WithTraceID(context.Background(), "test-trace")
		req, err := nethttp.NewRequestWithContext(ctx, "GET", "http://example.com", nethttp.NoBody)
		require.NoError(t, err)

		err = interceptor(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "test-trace", req.Header.Get(HeaderXRequestID))

		// Test that it doesn't override existing header
		req.Header.Set(HeaderXRequestID, "existing-trace")
		err = interceptor(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "existing-trace", req.Header.Get(HeaderXRequestID))
	})
}

// setupClientTestMeterProvider creates a TestMeterProvider, sets it as the global OTel
// provider, resets tracking meter state, and initialises the instruments. Returns the
// provider for metric collection and a cleanup function that must be deferred.
func setupClientTestMeterProvider(t *testing.T) (mp *obtest.TestMeterProvider, cleanup func()) {
	t.Helper()
	mp = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()
	tracking.InitHTTPMeter()
	return mp, func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}
}

// hasStringAttr returns true when an attribute with the given key and value appears in attrs.
func hasStringAttr(attrs []attribute.KeyValue, key, val string) bool {
	for _, a := range attrs {
		if string(a.Key) == key && a.Value.AsString() == val {
			return true
		}
	}
	return false
}

// hasAttrKey returns true when any attribute with the given key appears in attrs.
func hasAttrKey(attrs []attribute.KeyValue, key string) bool {
	for _, a := range attrs {
		if string(a.Key) == key {
			return true
		}
	}
	return false
}

// TestHTTPClientMetricsSuccessPath verifies that a successful request emits the
// duration histogram with peer.service and status_code attributes, no error.type
// attribute, and that active_requests returns to 0 after the call completes.
func TestHTTPClientMetricsSuccessPath(t *testing.T) {
	mp, cleanup := setupClientTestMeterProvider(t)
	defer cleanup()

	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("server write failed: %v", err)
		}
	}))
	defer server.Close()

	log := createTestLogger()
	c := NewBuilder(log).
		WithPeerName("test-peer").
		Build()

	resp, err := c.Get(context.Background(), &Request{URL: server.URL})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)

	rm := mp.Collect(t)

	// Duration histogram must have exactly 1 datapoint.
	durationMetric := obtest.FindMetric(rm, "http.client.request.duration")
	require.NotNil(t, durationMetric, "http.client.request.duration metric must be emitted")
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")

	var totalCount uint64
	for _, dp := range histData.DataPoints {
		totalCount += dp.Count
	}
	assert.Equal(t, uint64(1), totalCount, "expected 1 histogram observation for a single successful request")

	// Verify peer.service and status_code attributes, absent error.type.
	require.NotEmpty(t, histData.DataPoints)
	dp0 := histData.DataPoints[0]
	attrs := dp0.Attributes.ToSlice()
	assert.True(t, hasStringAttr(attrs, "peer.service", "test-peer"), "peer.service attribute should be 'test-peer'")
	assert.True(t, hasAttrKey(attrs, "http.response.status_code"), "http.response.status_code attribute must be present")
	assert.False(t, hasAttrKey(attrs, "error.type"), "error.type attribute must be absent on success")

	// Active requests must be net 0 after the call returns (defer fired before we reach here).
	activeMetric := obtest.FindMetric(rm, "http.client.active_requests")
	require.NotNil(t, activeMetric, "http.client.active_requests must be emitted")
	sumData, ok := activeMetric.Data.(metricdata.Sum[int64])
	if ok {
		var netTotal int64
		for _, dp := range sumData.DataPoints {
			netTotal += dp.Value
		}
		assert.Equal(t, int64(0), netTotal, "net active requests must be 0 after the call completes")
	}
}

// TestHTTPClientMetricsRetryOn503 verifies that when the server returns 503 then 200
// the duration histogram records one observation per attempt and the retries counter
// is incremented with retry.reason="5xx".
func TestHTTPClientMetricsRetryOn503(t *testing.T) {
	mp, cleanup := setupClientTestMeterProvider(t)
	defer cleanup()

	var callCount atomic.Int32
	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		if callCount.Add(1) == 1 {
			w.WriteHeader(nethttp.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(nethttp.StatusOK)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("server write failed: %v", err)
		}
	}))
	defer server.Close()

	log := createTestLogger()
	c := NewBuilder(log).
		WithPeerName("retry-peer").
		WithRetries(2, 1*time.Millisecond).
		Build()

	resp, err := c.Get(context.Background(), &Request{URL: server.URL})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)

	rm := mp.Collect(t)

	// Duration histogram must have 2 total observations (one per attempt).
	durationMetric := obtest.FindMetric(rm, "http.client.request.duration")
	require.NotNil(t, durationMetric, "http.client.request.duration must be emitted")
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")

	var totalCount uint64
	for _, dp := range histData.DataPoints {
		totalCount += dp.Count
	}
	assert.Equal(t, uint64(2), totalCount, "expected 2 histogram observations (one per attempt)")

	// Retries counter must be 1 with retry.reason="5xx".
	retryMetric := obtest.FindMetric(rm, "http.client.retries.total")
	require.NotNil(t, retryMetric, "http.client.retries.total must be emitted")
	sumData, ok := retryMetric.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] for retries.total")

	var totalRetries int64
	foundFiveXX := false
	for _, dp := range sumData.DataPoints {
		totalRetries += dp.Value
		if hasStringAttr(dp.Attributes.ToSlice(), "retry.reason", "5xx") {
			foundFiveXX = true
		}
	}
	assert.Equal(t, int64(1), totalRetries, "expected exactly 1 retry")
	assert.True(t, foundFiveXX, "retry.reason='5xx' attribute must be present on retry counter")
}

// TestHTTPClientMetricsTimeoutClassification verifies that when the server delays
// beyond the client timeout the duration histogram datapoint carries error.type="timeout"
// and no http.response.status_code attribute.
func TestHTTPClientMetricsTimeoutClassification(t *testing.T) {
	mp, cleanup := setupClientTestMeterProvider(t)
	defer cleanup()

	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	log := createTestLogger()
	c := NewBuilder(log).
		WithPeerName("timeout-peer").
		WithTimeout(10 * time.Millisecond).
		Build()

	_, err := c.Get(context.Background(), &Request{URL: server.URL})
	require.Error(t, err)
	assert.True(t, IsErrorType(err, TimeoutError))

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, "http.client.request.duration")
	require.NotNil(t, durationMetric, "http.client.request.duration must be emitted on timeout")
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")
	require.NotEmpty(t, histData.DataPoints)

	// Find the datapoint that has error.type="timeout".
	foundTimeout := false
	for _, dp := range histData.DataPoints {
		attrs := dp.Attributes.ToSlice()
		if hasStringAttr(attrs, "error.type", "timeout") {
			foundTimeout = true
			assert.False(t, hasAttrKey(attrs, "http.response.status_code"),
				"http.response.status_code must be absent when status is 0 (transport error)")
		}
	}
	assert.True(t, foundTimeout, "at least one datapoint must have error.type='timeout'")
}

// TestHTTPClientMetricsBuildResponseFailureRecorded verifies that when a response
// interceptor returns an error — a post-roundtrip failure where the wire was hit and
// the server returned a status — the duration histogram still records exactly one
// observation. The datapoint must carry the wire status code (200) and
// error.type="interceptor_failed", and active_requests must be net 0 after the call.
func TestHTTPClientMetricsBuildResponseFailureRecorded(t *testing.T) {
	mp, cleanup := setupClientTestMeterProvider(t)
	defer cleanup()

	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("server write failed: %v", err)
		}
	}))
	defer server.Close()

	respInterceptor := func(_ context.Context, _ *nethttp.Request, _ *nethttp.Response) error {
		return fmt.Errorf("interceptor boom")
	}

	log := createTestLogger()
	c := NewBuilder(log).
		WithPeerName("interceptor-fail-peer").
		WithResponseInterceptor(respInterceptor).
		Build()

	_, err := c.Get(context.Background(), &Request{URL: server.URL})
	require.Error(t, err)
	assert.True(t, IsErrorType(err, InterceptorError))

	rm := mp.Collect(t)

	// Duration histogram must have exactly 1 datapoint — the build-response failure.
	durationMetric := obtest.FindMetric(rm, "http.client.request.duration")
	require.NotNil(t, durationMetric, "http.client.request.duration must be emitted on buildResponse failure")
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")

	var totalCount uint64
	for _, dp := range histData.DataPoints {
		totalCount += dp.Count
	}
	assert.Equal(t, uint64(1), totalCount, "expected 1 histogram observation for the build-response failure attempt")

	// The datapoint must carry the wire status code (200) and error.type="interceptor_failed".
	require.NotEmpty(t, histData.DataPoints)
	dp0 := histData.DataPoints[0]
	attrs := dp0.Attributes.ToSlice()
	assert.True(t, hasAttrKey(attrs, "http.response.status_code"),
		"http.response.status_code must be present — server returned 200 before interceptor failed")
	assert.True(t, hasStringAttr(attrs, "error.type", "interceptor_failed"),
		"error.type must be 'interceptor_failed' for response interceptor errors")

	// Active requests must be net 0 after the call returns.
	activeMetric := obtest.FindMetric(rm, "http.client.active_requests")
	require.NotNil(t, activeMetric, "http.client.active_requests must be emitted")
	sumData, ok := activeMetric.Data.(metricdata.Sum[int64])
	if ok {
		var netTotal int64
		for _, dp := range sumData.DataPoints {
			netTotal += dp.Value
		}
		assert.Equal(t, int64(0), netTotal, "net active requests must be 0 after the call completes")
	}
}

// TestBackoffDelayFallbacks covers the three defensive fallback branches in
// backoffDelay that the existing retry-path tests don't reach: zero RetryDelay
// (uses defaultBackoffBase), attempt exceeding maxBackoffAttempt (clamped),
// and computed delay exceeding maxBackoffDuration (capped). Without explicit
// coverage of these, the W4-D constants extraction would drop SonarCloud's
// new-code coverage below the 80% gate.
func TestBackoffDelayFallbacks(t *testing.T) {
	t.Run("zero_retry_delay_uses_default_base", func(t *testing.T) {
		c := &client{config: &Config{RetryDelay: 0}}
		// With base=50ms and attempt=0, mult=1, so d=50ms. Jitter returns [0, 50ms).
		got := c.backoffDelay(0)
		if got < 0 || got >= defaultBackoffBase {
			t.Fatalf("expected backoff in [0, %v), got %v", defaultBackoffBase, got)
		}
	})

	t.Run("attempt_exceeding_max_is_clamped", func(t *testing.T) {
		c := &client{config: &Config{RetryDelay: 1 * time.Millisecond}}
		// Attempt 1000 should clamp to maxBackoffAttempt; then 1ms * 2^20 = ~17min,
		// which exceeds maxBackoffDuration (30s), so the cap kicks in. Result must
		// fall in [0, maxBackoffDuration).
		got := c.backoffDelay(1000)
		if got < 0 || got >= maxBackoffDuration {
			t.Fatalf("expected backoff in [0, %v) after attempt clamp + duration cap, got %v",
				maxBackoffDuration, got)
		}
	})

	t.Run("computed_delay_exceeds_max_is_capped", func(t *testing.T) {
		// Large base + moderate attempt → product exceeds maxBackoffDuration → cap.
		c := &client{config: &Config{RetryDelay: 10 * time.Second}}
		// 10s * 2^5 = 320s > 30s → cap. Jitter then samples [0, 30s).
		got := c.backoffDelay(5)
		if got < 0 || got >= maxBackoffDuration {
			t.Fatalf("expected backoff in [0, %v) after duration cap, got %v",
				maxBackoffDuration, got)
		}
	})
}

// netTimeoutErr is a minimal net.Error implementation used to exercise the
// generic net.Error.Timeout() branch in classifyError without matching any of
// the more specific error types (DNSError, OpError, etc.).
type netTimeoutErr struct{}

func (netTimeoutErr) Error() string   { return "simulated net timeout" }
func (netTimeoutErr) Timeout() bool   { return true }
func (netTimeoutErr) Temporary() bool { return true }

// TestClassifyError is a white-box table-driven test that verifies every branch
// of classifyError, including the DNS-timeout regression (a timed-out
// *net.DNSError must yield "name_resolution_error", not "timeout").
func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil_error",
			err:      nil,
			expected: "",
		},
		{
			name:     "context_canceled",
			err:      context.Canceled,
			expected: errorTypeContextCanceled,
		},
		{
			name:     "context_deadline_exceeded",
			err:      context.DeadlineExceeded,
			expected: errorTypeTimeout,
		},
		{
			name:     "framework_timeout",
			err:      NewTimeoutError("request timed out", 5*time.Second),
			expected: errorTypeTimeout,
		},
		{
			name:     "dns_error_nxdomain",
			err:      &net.DNSError{Err: "no such host", IsNotFound: true},
			expected: errorTypeNameResolution,
		},
		{
			// Regression: a timed-out DNS lookup must be errorTypeNameResolution,
			// not errorTypeTimeout. Previously the generic net.Error.Timeout() check
			// fired first because *net.DNSError implements net.Error with Timeout()==true.
			name:     "dns_error_timeout_regression",
			err:      &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			expected: errorTypeNameResolution,
		},
		{
			name:     "tls_record_header_error",
			err:      &tls.RecordHeaderError{Msg: "bad record header"},
			expected: errorTypeTLS,
		},
		{
			name:     "tls_cert_verification_error",
			err:      &tls.CertificateVerificationError{Err: errors.New("cert expired")},
			expected: errorTypeTLS,
		},
		{
			name:     "tcp_dial_failure",
			err:      &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			expected: errorTypeConnection,
		},
		{
			// A read-deadline net.Error (not a DNSError or dial OpError) must fall
			// through to the generic net.Error.Timeout() branch → errorTypeTimeout.
			name:     "generic_net_timeout",
			err:      netTimeoutErr{},
			expected: errorTypeTimeout,
		},
		{
			name:     "interceptor_failure",
			err:      NewInterceptorError("interceptor failed", "request", errors.New("upstream")),
			expected: errorTypeInterceptorFailed,
		},
		{
			name:     "generic_network_error",
			err:      NewNetworkError("network failure", errors.New("connection reset")),
			expected: errorTypeOther,
		},
		{
			name:     "unknown_error",
			err:      errors.New("mystery error"),
			expected: errorTypeOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError(tc.err)
			assert.Equal(t, tc.expected, got)
		})
	}
}
