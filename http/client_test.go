package http

import (
	"context"
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
			assert.Greater(t, resp.Stats.ElapsedTime, time.Duration(0))
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
	t.Run("WithTraceID and GetTraceIDFromContext", func(t *testing.T) {
		expectedTraceID := "test-trace-123"
		ctx := WithTraceID(context.Background(), expectedTraceID)

		actualTraceID := GetTraceIDFromContext(ctx)
		assert.Equal(t, expectedTraceID, actualTraceID)
	})

	t.Run("GetTraceIDFromContext generates UUID when no trace ID", func(t *testing.T) {
		traceID := GetTraceIDFromContext(context.Background())
		assert.NotEmpty(t, traceID)
		assert.Len(t, traceID, 36) // UUID format
	})

	t.Run("NewTraceIDInterceptor creates valid interceptor", func(t *testing.T) {
		interceptor := NewTraceIDInterceptor()
		assert.NotNil(t, interceptor)

		// Test that it adds header when missing
		ctx := WithTraceID(context.Background(), "test-trace")
		req, _ := nethttp.NewRequestWithContext(ctx, "GET", "http://example.com", nethttp.NoBody)

		err := interceptor(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "test-trace", req.Header.Get(HeaderXRequestID))

		// Test that it doesn't override existing header
		req.Header.Set(HeaderXRequestID, "existing-trace")
		err = interceptor(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "existing-trace", req.Header.Get(HeaderXRequestID))
	})
}
