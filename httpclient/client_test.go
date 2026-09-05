package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gaborage/go-bricks/httpclient/internal/tracking"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
	obtest "github.com/gaborage/go-bricks/observability/testing"
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

// TestBuilderBareBuildNeverErrors pins that a bare NewBuilder(log).Build()
// chain never errors — NewClient relies on this to discard the error unchecked.
func TestBuilderBareBuildNeverErrors(t *testing.T) {
	_, err := NewBuilder(createTestLogger()).Build()
	require.NoError(t, err)
}

func TestBuilder(t *testing.T) {
	log := createTestLogger()

	t.Run("default configuration", func(t *testing.T) {
		client, err := NewBuilder(log).Build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("with timeout", func(t *testing.T) {
		timeout := 10 * time.Second
		client, err := NewBuilder(log).
			WithTimeout(timeout).
			Build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("with retries", func(t *testing.T) {
		client, err := NewBuilder(log).
			WithRetries(3, 2*time.Second).
			Build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("with basic auth", func(t *testing.T) {
		client, err := NewBuilder(log).
			WithBasicAuth("user", "pass").
			Build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("with default headers", func(t *testing.T) {
		client, err := NewBuilder(log).
			WithDefaultHeader(testAPIKey, testAPIValue).
			WithDefaultHeader(testUserAgent, testAgentValue).
			Build()
		require.NoError(t, err)
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

		client, err := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			WithResponseInterceptor(respInterceptor).
			Build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("with custom http client", func(t *testing.T) {
		customTransport := &stubRoundTripper{name: "custom"}
		custom := &nethttp.Client{Timeout: 123 * time.Millisecond, Transport: customTransport}
		built, err := NewBuilder(log).
			WithHTTPClient(custom).
			WithTimeout(5 * time.Second).
			Build()
		require.NoError(t, err)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.NotSame(t, custom, clientImpl.httpClient, "Build must copy, not alias, the provided client")
		assert.Equal(t, 123*time.Millisecond, clientImpl.httpClient.Timeout)
		assert.Same(t, customTransport, clientImpl.httpClient.Transport, "copy must keep the caller's transport when no override is set")
	})

	t.Run("with custom http client zero timeout uses builder timeout", func(t *testing.T) {
		custom := &nethttp.Client{}
		built, err := NewBuilder(log).
			WithHTTPClient(custom).
			WithTimeout(2 * time.Second).
			Build()
		require.NoError(t, err)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.Equal(t, 2*time.Second, clientImpl.httpClient.Timeout)
		assert.Equal(t, time.Duration(0), custom.Timeout, "original client must not be mutated")
	})

	t.Run("build does not mutate provided client transport", func(t *testing.T) {
		sentinel := &stubRoundTripper{name: "sentinel"}
		tuned := &nethttp.Client{Timeout: 5 * time.Second, Transport: sentinel}
		override := &stubRoundTripper{name: "override"}

		built, err := NewBuilder(log).
			WithHTTPClient(tuned).
			WithTransport(override).
			Build()
		require.NoError(t, err)

		assert.Same(t, sentinel, tuned.Transport, "caller's client transport must be untouched by Build")

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.Same(t, override, clientImpl.httpClient.Transport)
	})

	t.Run("two builders sharing one client keep independent transports", func(t *testing.T) {
		sentinel := &stubRoundTripper{name: "sentinel"}
		tuned := &nethttp.Client{Timeout: 5 * time.Second, Transport: sentinel}
		transportA := &stubRoundTripper{name: "a"}
		transportB := &stubRoundTripper{name: "b"}

		builtA, errA := NewBuilder(log).WithHTTPClient(tuned).WithTransport(transportA).Build()
		require.NoError(t, errA)
		builtB, errB := NewBuilder(log).WithHTTPClient(tuned).WithTransport(transportB).Build()
		require.NoError(t, errB)

		clientImplA, ok := builtA.(*client)
		require.True(t, ok)
		clientImplB, ok := builtB.(*client)
		require.True(t, ok)

		assert.Same(t, transportA, clientImplA.httpClient.Transport)
		assert.Same(t, transportB, clientImplB.httpClient.Transport)
		assert.Same(t, sentinel, tuned.Transport, "shared client transport must remain the original sentinel")
	})

	t.Run("with JOSE does not mutate provided client", func(t *testing.T) {
		sentinel := &stubRoundTripper{name: "sentinel"}
		tuned := &nethttp.Client{Timeout: 5 * time.Second, Transport: sentinel}

		// WithTransport(sentinel) avoids the "wrapper without WithTransport" hazard.
		built, err := NewBuilder(log).
			WithHTTPClient(tuned).
			WithTransport(sentinel).
			WithJOSE(JOSEConfig{}).
			Build()
		require.NoError(t, err)

		assert.Same(t, sentinel, tuned.Transport, "caller's client must be untouched by WithJOSE+Build")

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
		require.True(t, ok, "built client must carry the JOSE transport")
		assert.Same(t, sentinel, joseTransport.Inner, "explicit base must survive under JOSE wrapping")
	})

	t.Run("with custom transport", func(t *testing.T) {
		transport := &stubRoundTripper{name: "stub"}
		built, err := NewBuilder(log).
			WithTransport(transport).
			Build()
		require.NoError(t, err)

		clientImpl := built.(*client)
		assert.Equal(t, transport, clientImpl.httpClient.Transport)
	})

	t.Run("with trace ID header", func(t *testing.T) {
		customHeader := "X-Custom-Trace-ID"
		builtClient, err := NewBuilder(log).
			WithTraceIDHeader(customHeader).
			Build()
		require.NoError(t, err)

		// Assert against the client's config since tests are in the same package
		clientImpl := builtClient.(*client)
		assert.Equal(t, customHeader, clientImpl.config.TraceIDHeader)
	})

	t.Run("with trace ID header empty string", func(t *testing.T) {
		builtClient, err := NewBuilder(log).
			WithTraceIDHeader("").
			Build()
		require.NoError(t, err)

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

		builtClient, err := NewBuilder(log).
			WithTraceIDGenerator(customGenerator).
			Build()
		require.NoError(t, err)

		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.NewTraceID)

		// Test that the custom generator is actually used
		traceID := clientImpl.config.NewTraceID()
		assert.Equal(t, testCustomTrace, traceID)
		assert.Equal(t, int32(1), atomic.LoadInt32(&generatorCallCount))
	})

	t.Run("with nil trace ID generator", func(t *testing.T) {
		builtClient, err := NewBuilder(log).
			WithTraceIDGenerator(nil).
			Build()
		require.NoError(t, err)

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

		builtClient, err := NewBuilder(log).
			WithTraceIDExtractor(customExtractor).
			Build()
		require.NoError(t, err)

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
		builtClient, err := NewBuilder(log).
			WithTraceIDExtractor(nil).
			Build()
		require.NoError(t, err)

		// nil extractor should not change the default
		clientImpl := builtClient.(*client)
		assert.NotNil(t, clientImpl.config.TraceIDExtractor)
	})

	t.Run("with W3C trace enabled", func(t *testing.T) {
		builtClient, err := NewBuilder(log).
			WithW3CTrace(true).
			Build()
		require.NoError(t, err)

		clientImpl := builtClient.(*client)
		assert.True(t, clientImpl.config.EnableW3CTrace)
	})

	t.Run("with W3C trace disabled", func(t *testing.T) {
		builtClient, err := NewBuilder(log).
			WithW3CTrace(false).
			Build()
		require.NoError(t, err)

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

		builtClient, err := NewBuilder(log).
			WithTraceIDHeader("X-My-Trace").
			WithTraceIDGenerator(customGenerator).
			WithTraceIDExtractor(customExtractor).
			WithW3CTrace(false).
			Build()
		require.NoError(t, err)

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

		client, buildErr := NewBuilder(log).
			WithDefaultHeader(testUserAgent, testAgentValue).
			WithDefaultHeader(testAPIKey, testAPIValue).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithDefaultHeader(testUserAgent, "default-agent").
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithBasicAuth("user", "pass").
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithBasicAuth("client-user", "client-pass").
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithResponseInterceptor(respInterceptor).
			Build()
		require.NoError(t, buildErr)

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
			return errors.New("boom")
		}

		client, buildErr := NewBuilder(log).
			WithRequestInterceptor(reqInterceptor).
			Build()
		require.NoError(t, buildErr)

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
			return errors.New("boom resp")
		}

		client, buildErr := NewBuilder(log).
			WithResponseInterceptor(respInterceptor).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithTimeout(10 * time.Millisecond).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithRetries(2, 5*time.Millisecond).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithRetries(3, 5*time.Millisecond).
			Build()
		require.NoError(t, buildErr)

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

		client, buildErr := NewBuilder(log).
			WithTimeout(10*time.Millisecond).
			WithRetries(1, 5*time.Millisecond).
			Build()
		require.NoError(t, buildErr)

		req := &Request{URL: server.URL}
		_, err := client.Get(context.Background(), req)
		require.Error(t, err)
		assert.True(t, IsErrorType(err, TimeoutError))
		assert.Equal(t, int32(2), calls.Load()) // initial + one retry
	})
}

// TestRequestInterceptorRunsPerAttempt pins that buildRequest re-runs request
// interceptors on every retry attempt rather than replaying a stale request.
// Consumers that sign requests through WithRequestInterceptor (e.g. OAuth 1.0a,
// whose nonces are single-use per RFC 5849) depend on getting a fresh request
// object per attempt; hoisting interceptor execution out of the per-attempt
// path would break them with every other test still green.
func TestRequestInterceptorRunsPerAttempt(t *testing.T) {
	const headerInterceptorSeq = "X-Interceptor-Seq"
	log := createTestLogger()

	var serverHits atomic.Int32
	var mu sync.Mutex
	var seenValues []string

	server := newIPv4TestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n := serverHits.Add(1)
		mu.Lock()
		seenValues = append(seenValues, r.Header.Get(headerInterceptorSeq))
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(nethttp.StatusInternalServerError)
			return
		}
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	var interceptorCalls atomic.Int32
	reqInterceptor := func(_ context.Context, req *nethttp.Request) error {
		n := interceptorCalls.Add(1)
		req.Header.Set(headerInterceptorSeq, fmt.Sprintf("attempt-%d", n))
		return nil
	}

	client, buildErr := NewBuilder(log).
		WithRetries(3, 5*time.Millisecond).
		WithRequestInterceptor(reqInterceptor).
		Build()
	require.NoError(t, buildErr)

	req := &Request{URL: server.URL}
	resp, err := client.Get(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)

	assert.Equal(t, int32(3), serverHits.Load())
	assert.Equal(t, serverHits.Load(), interceptorCalls.Load())

	mu.Lock()
	defer mu.Unlock()
	seen := make(map[string]bool, len(seenValues))
	for _, v := range seenValues {
		assert.False(t, seen[v], "duplicate interceptor value observed: %s", v)
		seen[v] = true
	}
}

func TestTraceIDPropagation(t *testing.T) {
	log := createTestLogger()

	// Force a non-recording span for these subtests. Process-wide otel state can
	// carry a real, still-live TracerProvider installed by an earlier test in
	// this binary (the global delegate is permanently bound to the first
	// installed provider — internal/global/state.go sync.Once, #1093), which
	// would otherwise route ensureTraceContextHeaders down the "real span"
	// branch instead of the legacy synthetic path these subtests exercise.
	originalTP := otel.GetTracerProvider()
	originalProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracking.ResetTracerForTesting()
	t.Cleanup(func() {
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalProp)
		tracking.ResetTracerForTesting()
	})

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
		client, buildErr := NewBuilder(log).
			WithRequestInterceptor(NewTraceIDInterceptor()).
			Build()
		require.NoError(t, buildErr)

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
// provider, resets tracking meter state, and initializes the instruments. Returns the
// provider for metric collection and a cleanup function that must be deferred.
func setupClientTestMeterProvider(t *testing.T) (mp *obtest.TestMeterProvider, cleanup func()) {
	t.Helper()
	prev := otel.GetMeterProvider()
	mp = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()
	tracking.InitHTTPMeter()
	return mp, func() {
		// no Shutdown: the first-installed provider is otel's permanent delegate (internal/global/state.go sync.Once, #1093)
		otel.SetMeterProvider(prev)
		tracking.ResetMeterForTesting()
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
	c, err := NewBuilder(log).
		WithPeerName("test-peer").
		Build()
	require.NoError(t, err)

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
	require.True(t, ok, "http.client.active_requests data must be Sum[int64]")
	var netTotal int64
	for _, dp := range sumData.DataPoints {
		netTotal += dp.Value
	}
	assert.Equal(t, int64(0), netTotal, "net active requests must be 0 after the call completes")
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
	c, err := NewBuilder(log).
		WithPeerName("retry-peer").
		WithRetries(2, 1*time.Millisecond).
		Build()
	require.NoError(t, err)

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
	c, buildErr := NewBuilder(log).
		WithPeerName("timeout-peer").
		WithTimeout(10 * time.Millisecond).
		Build()
	require.NoError(t, buildErr)

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
		return errors.New("interceptor boom")
	}

	log := createTestLogger()
	c, buildErr := NewBuilder(log).
		WithPeerName("interceptor-fail-peer").
		WithResponseInterceptor(respInterceptor).
		Build()
	require.NoError(t, buildErr)

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
	require.True(t, ok, "http.client.active_requests data must be Sum[int64]")
	var netTotal int64
	for _, dp := range sumData.DataPoints {
		netTotal += dp.Value
	}
	assert.Equal(t, int64(0), netTotal, "net active requests must be 0 after the call completes")
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

// TestRawBackoffDelaySequence pins the pre-jitter HTTP retry series.
func TestRawBackoffDelaySequence(t *testing.T) {
	tests := []struct {
		name    string
		delay   time.Duration
		attempt int
		want    time.Duration
	}{
		{name: "attempt_0_is_base", delay: 50 * time.Millisecond, attempt: 0, want: 50 * time.Millisecond},
		{name: "attempt_1_is_2x_base", delay: 50 * time.Millisecond, attempt: 1, want: 100 * time.Millisecond},
		{name: "attempt_2_is_4x_base", delay: 50 * time.Millisecond, attempt: 2, want: 200 * time.Millisecond},
		{name: "zero_retry_delay_uses_default_base", delay: 0, attempt: 0, want: defaultBackoffBase},
		{name: "large_base_clamps_to_max_duration", delay: 10 * time.Second, attempt: 5, want: maxBackoffDuration},
		{name: "attempt_over_max_stays_at_the_attempt_cap", delay: time.Nanosecond, attempt: 40, want: time.Nanosecond << maxBackoffAttempt},
		{name: "attempt_over_max_then_duration_cap", delay: time.Millisecond, attempt: 1000, want: maxBackoffDuration},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &client{config: &Config{RetryDelay: tc.delay}}
			assert.Equal(t, tc.want, c.rawBackoff(tc.attempt))
		})
	}
}

// netTimeoutError is a minimal net.Error implementation used to exercise the
// generic net.Error.Timeout() branch in classifyError without matching any of
// the more specific error types (DNSError, OpError, etc.).
type netTimeoutError struct{}

func (netTimeoutError) Error() string   { return "simulated net timeout" }
func (netTimeoutError) Timeout() bool   { return true }
func (netTimeoutError) Temporary() bool { return true }

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
			err:      &net.OpError{Op: netOpDial, Err: errors.New("connection refused")},
			expected: errorTypeConnection,
		},
		{
			// A read-deadline net.Error (not a DNSError or dial OpError) must fall
			// through to the generic net.Error.Timeout() branch → errorTypeTimeout.
			name:     "generic_net_timeout",
			err:      netTimeoutError{},
			expected: errorTypeTimeout,
		},
		{
			name:     "interceptor_failure",
			err:      NewInterceptorError("interceptor failed", "request", errors.New("upstream")),
			expected: errorTypeInterceptorFailed,
		},
		{
			// Regression: an interceptor wrapping a *net.DNSError must be
			// "interceptor_failed", not "name_resolution_error". Without the
			// InterceptorError guard before the errors.As chains, errors.As
			// would traverse Unwrap() and match the wrapped *net.DNSError.
			name:     "interceptor_wrapping_dns_error",
			err:      NewInterceptorError("validate", "response", &net.DNSError{Err: "no such host", IsNotFound: true}),
			expected: errorTypeInterceptorFailed,
		},
		{
			// Regression: an interceptor wrapping a *net.OpError (dial) must be
			// "interceptor_failed", not "connection_error".
			name:     "interceptor_wrapping_dial_error",
			err:      NewInterceptorError("validate", "response", &net.OpError{Op: netOpDial, Err: errors.New("connection refused")}),
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

// TestClassifyErrorRetryReasonCoherence verifies that the retry.reason derived
// from classifyError agrees with the error.type label — i.e., errors that are
// NOT classified as timeout or context_canceled must produce retryReasonNetwork,
// not retryReasonTimeout. This is the regression guard for Bug 2: a dial timeout
// previously produced error.type="connection_error" but retry.reason="timeout"
// because handleExecutionError called isTimeout separately from classifyError.
func TestClassifyErrorRetryReasonCoherence(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantErrType     string
		wantRetryReason string
	}{
		{
			name:            "dial_timeout_connection_not_timeout",
			err:             &net.OpError{Op: netOpDial, Err: errors.New("connection refused")},
			wantErrType:     errorTypeConnection,
			wantRetryReason: retryReasonNetwork,
		},
		{
			name:            "dns_timeout_name_resolution_not_timeout",
			err:             &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			wantErrType:     errorTypeNameResolution,
			wantRetryReason: retryReasonNetwork,
		},
		{
			name:            "context_deadline_exceeded_is_timeout",
			err:             context.DeadlineExceeded,
			wantErrType:     errorTypeTimeout,
			wantRetryReason: retryReasonTimeout,
		},
		{
			name:            "framework_timeout_is_timeout",
			err:             NewTimeoutError("request timed out", 5*time.Second),
			wantErrType:     errorTypeTimeout,
			wantRetryReason: retryReasonTimeout,
		},
		{
			name:            "interceptor_wrapping_dns_is_network",
			err:             NewInterceptorError("validate", "response", &net.DNSError{Err: "no such host", IsNotFound: true}),
			wantErrType:     errorTypeInterceptorFailed,
			wantRetryReason: retryReasonNetwork,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errType := classifyError(tc.err)
			assert.Equal(t, tc.wantErrType, errType, "classifyError mismatch")

			// Mirror the retry-reason logic from handleExecutionError.
			reason := retryReasonNetwork
			if errType == errorTypeTimeout || errType == errorTypeContextCanceled {
				reason = retryReasonTimeout
			}
			assert.Equal(t, tc.wantRetryReason, reason, "retry.reason mismatch for error.type=%q", errType)
		})
	}
}

// setupTestTracerForClient installs an in-memory test trace provider as the
// global tracer + propagator, returning a cleanup that restores both.
func setupTestTracerForClient(t *testing.T) (tp *obtest.TestTraceProvider, cleanup func()) {
	t.Helper()
	tp = obtest.NewTestTraceProvider()
	originalTP := otel.GetTracerProvider()
	originalProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp.TracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracking.ResetTracerForTesting()
	cleanup = func() {
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalProp)
		tracking.ResetTracerForTesting()
	}
	return tp, cleanup
}

// partitionSpans splits the captured spans into the single parent (no parent
// SpanContext) and the children (parent SpanContext valid).
func partitionSpans(t *testing.T, spans tracetest.SpanStubs) (parent tracetest.SpanStub, children []tracetest.SpanStub) {
	t.Helper()
	for i := range spans {
		if spans[i].Parent.IsValid() {
			children = append(children, spans[i])
		} else {
			parent = spans[i]
		}
	}
	return parent, children
}

func TestClientDoEmitsParentAndChildSpansOnSuccess(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c, err := NewBuilder(createTestLogger()).WithPeerName("test-peer").Build()
	require.NoError(t, err)
	resp, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2, "expected one parent Do span + one child attempt span")

	parent, children := partitionSpans(t, spans)
	require.Len(t, children, 1)
	assert.Equal(t, "GET test-peer", parent.Name, "parent span name should use peer template")
	assert.Equal(t, parent.SpanContext.SpanID(), children[0].Parent.SpanID(),
		"attempt span must reference the Do span as its parent")
	obtest.AssertSpanStatus(t, &parent, codes.Unset)
	obtest.AssertSpanAttribute(t, &parent, "http.response.status_code", int64(200))
	obtest.AssertSpanAttribute(t, &children[0], "peer.service", "test-peer")
}

func TestClientDoEmitsParentAndChildSpansWithoutPeer(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	c, buildErr := NewBuilder(createTestLogger()).Build()
	require.NoError(t, buildErr)
	_, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2)
	parent, _ := partitionSpans(t, spans)
	assert.Equal(t, "HTTP GET", parent.Name, "parent span name should use HTTP-METHOD template when no peer")
}

func TestClientDoInjectsRealTraceparentWhenSpanActive(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	var receivedTP string
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		receivedTP = r.Header.Get("traceparent")
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	c, buildErr := NewBuilder(createTestLogger()).WithW3CTrace(true).Build()
	require.NoError(t, buildErr)
	_, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2)
	_, children := partitionSpans(t, spans)
	require.Len(t, children, 1)
	require.True(t, children[0].SpanContext.IsValid(), "child attempt span should have a valid span context")
	require.NotEmpty(t, receivedTP, "server should have received a traceparent header")
	traceID := children[0].SpanContext.TraceID().String()
	assert.Containsf(t, receivedTP, traceID,
		"traceparent header should carry the attempt span's trace ID, got %q", receivedTP)
}

// TestClientDoSyntheticTraceparentWhenNoTracerActive exercises the legacy
// synthetic-traceparent fallback in ensureTraceContextHeaders — the path
// taken when no recording span exists on the request context. We install a
// noop TracerProvider so StartHTTPClientSpan returns a non-recording span
// whose SpanContext is invalid; ensureTraceContextHeaders's IsValid() branch
// then fails over to GenerateTraceParent().
func TestClientDoSyntheticTraceparentWhenNoTracerActive(t *testing.T) {
	originalTP := otel.GetTracerProvider()
	originalProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracking.ResetTracerForTesting()
	t.Cleanup(func() {
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalProp)
		tracking.ResetTracerForTesting()
	})

	var receivedTP string
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		receivedTP = r.Header.Get("traceparent")
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	c, buildErr := NewBuilder(createTestLogger()).WithW3CTrace(true).Build()
	require.NoError(t, buildErr)
	_, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)
	require.NotEmpty(t, receivedTP,
		"synthetic-traceparent fallback should write a header even without a recording span")
	// The synthetic generator's traceparent format is "00-<32hex>-<16hex>-01".
	// Spot-check the prefix and shape so a future regression that returns "" or
	// the propagator's empty value is caught.
	assert.True(t, strings.HasPrefix(receivedTP, "00-"),
		"synthetic traceparent should start with version 00, got %q", receivedTP)
	assert.Len(t, receivedTP, 55, "W3C traceparent length should be 55 chars (00-trace-span-flags)")
}

// TestClientDoPreservesCallerSuppliedTraceparent codifies F2 from the
// pre-push review: when a caller pins a traceparent via req.Headers (e.g.
// a vendor SDK that wants the upstream trace ID to flow through), the OTel
// propagator path must NOT overwrite it. Pre-fix behavior: with a real SDK
// active, the attempt span's trace ID would clobber the caller value.
func TestClientDoPreservesCallerSuppliedTraceparent(t *testing.T) {
	_, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	const pinned = "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01"
	var receivedTP string
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		receivedTP = r.Header.Get("traceparent")
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	c, buildErr := NewBuilder(createTestLogger()).WithW3CTrace(true).Build()
	require.NoError(t, buildErr)
	_, err := c.Get(context.Background(), &Request{
		URL:     server.URL + "/foo",
		Headers: map[string]string{"traceparent": pinned},
	})
	require.NoError(t, err)
	assert.Equal(t, pinned, receivedTP,
		"caller-supplied traceparent must survive — propagator must not overwrite an explicit header value")
}

func TestClientDoRetrySequenceEmitsParentPlusChildrenWithResendCounts(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	var attempts atomic.Int32
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(nethttp.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(nethttp.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c, err := NewBuilder(createTestLogger()).
		WithRetries(2, time.Millisecond).
		WithPeerName("flaky-svc").
		Build()
	require.NoError(t, err)
	resp, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 4, "expected one parent Do span + three attempt spans (2x 503 + 1x 200)")
	parent, children := partitionSpans(t, spans)
	require.Len(t, children, 3)

	// Every attempt span has the Do span as its parent.
	for i := range children {
		assert.Equalf(t, parent.SpanContext.SpanID(), children[i].Parent.SpanID(),
			"child span %d must reference the Do span as its parent", i)
	}

	// Resend counts: attempt 0 omits resend_count, attempts 1 and 2 set 1 and 2.
	resendCounts := map[int64]int{}
	var firstAttemptHasResend bool
	for i := range children {
		found := int64(-1)
		for _, kv := range children[i].Attributes {
			if string(kv.Key) == "http.request.resend_count" {
				found = kv.Value.AsInt64()
			}
		}
		if found == -1 {
			// resend_count omitted → first attempt
			require.False(t, firstAttemptHasResend, "only one attempt should omit resend_count (attempt 0)")
			firstAttemptHasResend = true
			continue
		}
		resendCounts[found]++
	}
	assert.Equal(t, 1, resendCounts[1], "exactly one attempt should have resend_count=1")
	assert.Equal(t, 1, resendCounts[2], "exactly one attempt should have resend_count=2")

	// Parent Do span ends with the final 2xx → status unset, status_code=200.
	obtest.AssertSpanStatus(t, &parent, codes.Unset)
	obtest.AssertSpanAttribute(t, &parent, "http.response.status_code", int64(200))

	// First two attempts (503) → Error status, last (200) → Unset.
	// codes.Ok is unused for client spans per OTel HTTP semconv (4xx-as-OK
	// is signaled by Unset, not Ok).
	errorCount, unsetCount := 0, 0
	for i := range children {
		switch children[i].Status.Code {
		case codes.Error:
			errorCount++
		case codes.Unset:
			unsetCount++
		case codes.Ok:
			t.Fatalf("attempt span %d unexpectedly has codes.Ok status (client spans should never set Ok)", i)
		}
	}
	assert.Equal(t, 2, errorCount, "first two 5xx attempts should have Error status")
	assert.Equal(t, 1, unsetCount, "final 2xx attempt should have Unset status")
}

// TestClientDoPanicInResponseInterceptorEndsBothSpansWithErrorType locks in
// F3: a panic in user-supplied code that runs AFTER the attempt span opens
// (here, a response interceptor that fires post-roundtrip) must not leak the
// attempt span or silently classify the Do span as success. Both spans must
// end with error.type="panic" and codes.Error; the panic must propagate.
func TestClientDoPanicInResponseInterceptorEndsBothSpansWithErrorType(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))
	defer server.Close()

	c, buildErr := NewBuilder(createTestLogger()).
		WithResponseInterceptor(func(_ context.Context, _ *nethttp.Request, _ *nethttp.Response) error {
			panic("user-supplied response interceptor panicked")
		}).
		Build()
	require.NoError(t, buildErr)

	require.Panics(t, func() {
		_, _ = c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	})

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2, "panic must still produce one parent Do span + one attempt span")
	parent, children := partitionSpans(t, spans)
	require.Len(t, children, 1)

	obtest.AssertSpanStatus(t, &parent, codes.Error)
	obtest.AssertSpanAttribute(t, &parent, "error.type", "panic")
	obtest.AssertSpanStatus(t, &children[0], codes.Error)
	obtest.AssertSpanAttribute(t, &children[0], "error.type", "panic")
}

func TestClientDoTransportErrorSpan(t *testing.T) {
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	// Address that should fail to connect: closed loopback port.
	c, buildErr := NewBuilder(createTestLogger()).WithPeerName("dead-svc").Build()
	require.NoError(t, buildErr)
	_, err := c.Get(context.Background(), &Request{URL: "http://127.0.0.1:1/foo"})
	require.Error(t, err)

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2)
	parent, children := partitionSpans(t, spans)
	require.Len(t, children, 1)

	// Both spans should carry Error status; the child's exception event records the error.
	obtest.AssertSpanStatus(t, &parent, codes.Error)
	obtest.AssertSpanStatus(t, &children[0], codes.Error)
	hasException := false
	for _, ev := range children[0].Events {
		if ev.Name == "exception" {
			hasException = true
			break
		}
	}
	assert.True(t, hasException, "transport error should produce an exception event on the attempt span")
	// error.type should be present on the attempt span.
	foundErrType := false
	for _, kv := range children[0].Attributes {
		if string(kv.Key) == "error.type" {
			foundErrType = true
			break
		}
	}
	assert.True(t, foundErrType, "transport error should set error.type on the attempt span")
}

// Builder must stay a comparable type: apidiff reports a comparable-to-not-comparable
// change on an exported type as INCOMPATIBLE, which would gate the PR behind an ADR.
func TestBuilderTransportChainKeepsBuilderComparable(_ *testing.T) {
	var a, b Builder
	_ = a == b
}

func TestBuilderTransportChainOrderIndependence(t *testing.T) {
	log := createTestLogger()

	cases := []struct {
		name  string
		build func(t *testing.T, base nethttp.RoundTripper) Client
	}{
		{
			// Regression: WithTransport after WithJOSE used to discard the JOSE layer.
			name: "jose_then_transport",
			build: func(t *testing.T, base nethttp.RoundTripper) Client {
				built, err := NewBuilder(log).WithJOSE(JOSEConfig{}).WithTransport(base).Build()
				require.NoError(t, err)
				return built
			},
		},
		{
			name: "transport_then_jose",
			build: func(t *testing.T, base nethttp.RoundTripper) Client {
				built, err := NewBuilder(log).WithTransport(base).WithJOSE(JOSEConfig{}).Build()
				require.NoError(t, err)
				return built
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &stubRoundTripper{name: "base"}
			built := tc.build(t, base)

			clientImpl, ok := built.(*client)
			require.True(t, ok)
			joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
			require.True(t, ok, "JOSE layer must survive regardless of builder call order")
			assert.Same(t, base, joseTransport.Inner)
		})
	}
}

func TestBuilderTransportChainLayerOrdering(t *testing.T) {
	log := createTestLogger()

	assertNesting := func(t *testing.T, built Client, base *stubRoundTripper) {
		t.Helper()
		clientImpl, ok := built.(*client)
		require.True(t, ok)
		joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
		require.True(t, ok, "body-transform layer must be outermost")
		signer, ok := joseTransport.Inner.(*wrappingTransport)
		require.True(t, ok, "signer layer must sit between JOSE and the base")
		assert.Same(t, base, signer.inner)
	}

	wrapSigner := func(inner nethttp.RoundTripper) nethttp.RoundTripper {
		return &wrappingTransport{inner: inner}
	}

	t.Run("signer_registered_first", func(t *testing.T) {
		base := &stubRoundTripper{name: "base"}
		b := NewBuilder(log).WithTransport(base)
		b.addTransportWrapper(layerSigner, wrapSigner)
		b.WithJOSE(JOSEConfig{})
		built, err := b.Build()
		require.NoError(t, err)
		assertNesting(t, built, base)
	})

	t.Run("signer_registered_second", func(t *testing.T) {
		base := &stubRoundTripper{name: "base"}
		b := NewBuilder(log).WithTransport(base).WithJOSE(JOSEConfig{})
		b.addTransportWrapper(layerSigner, wrapSigner)
		built, err := b.Build()
		require.NoError(t, err)
		assertNesting(t, built, base)
	})
}

func TestBuilderTransportChainNeverPassesNilInner(t *testing.T) {
	log := createTestLogger()
	var gotInner nethttp.RoundTripper
	applied := false
	b := NewBuilder(log)
	b.addTransportWrapper(layerSigner, func(inner nethttp.RoundTripper) nethttp.RoundTripper {
		gotInner, applied = inner, true
		return &wrappingTransport{inner: inner}
	})
	_, err := b.Build()
	require.NoError(t, err)
	require.True(t, applied, "wrapper must have been applied")
	assert.IsType(t, defaultTransportShim{}, gotInner, "chain must seed the DefaultTransport shim, never a nil inner")
}

func TestBuilderTransportChainResolvesDefaultTransportPerRequest(t *testing.T) {
	log := createTestLogger()
	var captured nethttp.RoundTripper
	b := NewBuilder(log)
	b.addTransportWrapper(layerSigner, func(inner nethttp.RoundTripper) nethttp.RoundTripper {
		captured = inner
		return inner
	})
	_, buildErr := b.Build() // Build happens BEFORE the global is swapped — that is the point.
	require.NoError(t, buildErr)
	require.NotNil(t, captured)

	// Non-parallel on purpose: the package has no t.Parallel() calls, so swapping
	// the global here cannot race another test.
	orig := nethttp.DefaultTransport
	t.Cleanup(func() { nethttp.DefaultTransport = orig })
	stub := &recordingRoundTripper{}
	nethttp.DefaultTransport = stub

	//nolint:gocritic // literal nil, not http.NoBody, matches a real GET's nil req.Body
	req, err := nethttp.NewRequestWithContext(context.Background(), nethttp.MethodGet, "http://example.invalid", nil)
	require.NoError(t, err)
	_, _ = captured.RoundTrip(req) //nolint:bodyclose // recordingRoundTripper returns no body

	assert.True(t, stub.called, "chain must resolve DefaultTransport per request, not capture it at Build")
}

func TestBuilderTransportChainErrorsWhenDefaultTransportIsNil(t *testing.T) {
	log := createTestLogger()
	var captured nethttp.RoundTripper
	b := NewBuilder(log)
	b.addTransportWrapper(layerSigner, func(inner nethttp.RoundTripper) nethttp.RoundTripper {
		captured = inner
		return inner
	})
	_, buildErr := b.Build()
	require.NoError(t, buildErr)
	require.NotNil(t, captured)

	// Non-parallel on purpose: the package has no t.Parallel() calls, so swapping
	// the global here cannot race another test.
	orig := nethttp.DefaultTransport
	t.Cleanup(func() { nethttp.DefaultTransport = orig })
	nethttp.DefaultTransport = nil

	//nolint:gocritic // literal nil, not http.NoBody, matches a real GET's nil req.Body
	req, err := nethttp.NewRequestWithContext(context.Background(), nethttp.MethodGet, "http://example.invalid", nil)
	require.NoError(t, err)
	_, err = captured.RoundTrip(req) //nolint:bodyclose // shim errors before returning a response; no body to close

	require.Error(t, err, "shim must error, not panic, when net/http.DefaultTransport is nil")
	assert.Contains(t, err.Error(), "DefaultTransport")
}

func TestBuilderTransportChainDiscardsClientTransport(t *testing.T) {
	log := createTestLogger()
	base := &stubRoundTripper{name: "base"}
	withClient := func() *nethttp.Client { return &nethttp.Client{Transport: base} }

	cases := []struct {
		name    string
		want    bool
		builder func() *Builder
	}{
		{
			// The hazard: no WithTransport base, so the caller's own Transport is replaced.
			name:    "wrapper_over_client_transport_without_base",
			want:    true,
			builder: func() *Builder { return NewBuilder(log).WithHTTPClient(withClient()).WithJOSE(JOSEConfig{}) },
		},
		{
			name: "wrapper_with_explicit_base",
			want: false,
			builder: func() *Builder {
				return NewBuilder(log).WithHTTPClient(withClient()).WithTransport(base).WithJOSE(JOSEConfig{})
			},
		},
		{
			name:    "no_wrapper_leaves_client_transport_alone",
			want:    false,
			builder: func() *Builder { return NewBuilder(log).WithHTTPClient(withClient()) },
		},
		{
			name:    "wrapper_without_caller_client",
			want:    false,
			builder: func() *Builder { return NewBuilder(log).WithJOSE(JOSEConfig{}) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.builder().discardsClientTransport())
		})
	}

	t.Run("hazard_fails_build", func(t *testing.T) {
		_, err := NewBuilder(log).WithHTTPClient(withClient()).WithJOSE(JOSEConfig{}).Build()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "net/http.DefaultTransport")
	})

	t.Run("explicit_base_builds_successfully", func(t *testing.T) {
		built, err := NewBuilder(log).WithHTTPClient(withClient()).WithTransport(base).WithJOSE(JOSEConfig{}).Build()
		require.NoError(t, err)
		clientImpl, ok := built.(*client)
		require.True(t, ok)
		joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
		require.True(t, ok, "JOSE wrapper must still apply on top of the explicit base")
		assert.Same(t, base, joseTransport.Inner, "explicit WithTransport base must survive, not be replaced by the DefaultTransport shim")
	})
}

func TestBuilderBaseSlotDiscards(t *testing.T) {
	log := createTestLogger()
	stub := &stubRoundTripper{name: "stub"}
	caPEM, _, _ := newTestCA(t, "discards-tls-config-ca")
	newCfg := func() *tls.Config {
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
		require.NoError(t, err)
		return cfg
	}

	cases := []struct {
		name          string
		wantTLS       bool
		wantTransport bool
		builder       func() *Builder
	}{
		{
			name:          "tls_then_transport_discards_tls",
			wantTLS:       true,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(stub) },
		},
		{
			name:          "transport_then_tls_discards_transport",
			wantTLS:       false,
			wantTransport: true,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(stub).WithTLSConfig(newCfg()) },
		},
		{
			name:          "tls_only_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTLSConfig(newCfg()) },
		},
		{
			name:          "transport_only_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(stub) },
		},
		{
			name:          "tls_retaken_clears_tls_but_discards_transport",
			wantTLS:       false,
			wantTransport: true,
			builder: func() *Builder {
				return NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(stub).WithTLSConfig(newCfg())
			},
		},
		{
			name:          "transport_retaken_clears_transport_but_discards_tls",
			wantTLS:       true,
			wantTransport: false,
			builder: func() *Builder {
				return NewBuilder(log).WithTransport(stub).WithTLSConfig(newCfg()).WithTransport(stub)
			},
		},
		{
			name:          "repeated_transport_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(stub).WithTransport(stub) },
		},
		{
			name:          "repeated_tls_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTLSConfig(newCfg()).WithTLSConfig(newCfg()) },
		},
		{
			name:          "nil_tls_config_is_inert",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(stub).WithTLSConfig(nil) },
		},
		{
			name:          "nil_transport_after_tls_still_discards_tls",
			wantTLS:       true,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(nil) },
		},
		{
			name:          "nil_transport_then_tls_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(nil).WithTLSConfig(newCfg()) },
		},
		{
			name:          "transport_then_nil_transport_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder:       func() *Builder { return NewBuilder(log).WithTransport(stub).WithTransport(nil) },
		},
		{
			name:          "tls_vacated_then_transport_refill_still_discards_tls",
			wantTLS:       true,
			wantTransport: false,
			builder: func() *Builder {
				return NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(nil).WithTransport(stub)
			},
		},
		{
			name:          "tls_reloaded_after_vacating_clears_the_loss",
			wantTLS:       false,
			wantTransport: false,
			builder: func() *Builder {
				return NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(nil).WithTLSConfig(newCfg())
			},
		},
		{
			name:          "transport_reloaded_after_vacating_discards_nothing",
			wantTLS:       false,
			wantTransport: false,
			builder: func() *Builder {
				return NewBuilder(log).WithTransport(stub).WithTransport(nil).WithTransport(stub)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.builder()
			assert.Equal(t, tc.wantTLS, b.discardsTLSConfig())
			assert.Equal(t, tc.wantTransport, b.discardsProvidedTransport())
		})
	}

	t.Run("hazard_fails_build", func(t *testing.T) {
		_, err := NewBuilder(log).WithTLSConfig(newCfg()).WithTransport(stub).Build()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "WithTransport was called after WithTLSConfig")
	})

	t.Run("mirror_hazard_fails_build", func(t *testing.T) {
		_, err := NewBuilder(log).WithTransport(stub).WithTLSConfig(newCfg()).Build()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "WithTLSConfig was called after WithTransport")
	})

	t.Run("no_collision_builds_successfully", func(t *testing.T) {
		_, err := NewBuilder(log).WithTLSConfig(newCfg()).Build()
		require.NoError(t, err)
	})

	t.Run("both_hazards_reported_together", func(t *testing.T) {
		withClient := func() *nethttp.Client { return &nethttp.Client{Transport: stub} }
		_, err := NewBuilder(log).WithTLSConfig(newCfg()).WithHTTPClient(withClient()).WithTransport(nil).WithJOSE(JOSEConfig{}).Build()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "WithTransport was called after WithTLSConfig")
		assert.Contains(t, err.Error(), "net/http.DefaultTransport")
	})
}

// TestBuilderTLSConfigComposesWithIncumbentTransport pins that WithTLSConfig
// composes onto an incumbent *nethttp.Transport rather than colliding with
// it; an opaque incumbent can't be cloned and remains a genuine discard.
func TestBuilderTLSConfigComposesWithIncumbentTransport(t *testing.T) {
	log := createTestLogger()
	caPEM, _, _ := newTestCA(t, "compose-ca")
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
	require.NoError(t, err)

	t.Run("nethttp_transport_incumbent_composes", func(t *testing.T) {
		custom := &nethttp.Transport{MaxIdleConns: 7}
		built, buildErr := NewBuilder(log).WithTransport(custom).WithTLSConfig(cfg).Build()
		require.NoError(t, buildErr)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		transport, ok := clientImpl.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok)
		assert.NotSame(t, custom, transport, "the base must be a clone, not the caller's live transport")
		assert.Equal(t, 7, transport.MaxIdleConns, "the incumbent's distinguishing field must survive the clone")
		require.NotNil(t, transport.TLSClientConfig)
		assert.Same(t, cfg.RootCAs, transport.TLSClientConfig.RootCAs, "the transport must carry cfg's own RootCAs (Clone is shallow), not merely a non-nil pool")
	})

	// fillBaseSlot's nil check is an interface check, so WithTransport with a
	// typed-nil *nethttp.Transport fills the slot with a value that satisfies the
	// type assertion and panics on Clone(). It must take the empty-slot path.
	t.Run("typed_nil_incumbent_does_not_panic", func(t *testing.T) {
		var typedNil *nethttp.Transport
		built, buildErr := NewBuilder(log).WithTransport(typedNil).WithTLSConfig(cfg).Build()
		require.NoError(t, buildErr, "a typed nil carries no material, so there is nothing to report")

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		transport, ok := clientImpl.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok, "the typed nil must be replaced by a real base, not installed as the transport")
		require.NotNil(t, transport)
		assert.Same(t, cfg.RootCAs, transport.TLSClientConfig.RootCAs)
	})

	t.Run("opaque_incumbent_still_errors", func(t *testing.T) {
		stub := &stubRoundTripper{name: "opaque"}
		_, buildErr := NewBuilder(log).WithTransport(stub).WithTLSConfig(cfg).Build()
		require.Error(t, buildErr, "an opaque RoundTripper cannot be cloned, so this is still a discard")
		assert.Contains(t, buildErr.Error(), "WithTLSConfig was called after WithTransport")
	})
}

// TestBuilderTLSConfigCompositionNeverDropsIncumbentTLSMaterial pins that
// composition never silently drops the incumbent's own TLS material
// (certificate, pinned roots, or TLS dialer) and stays deterministic
// regardless of ALPN-only Clone() side effects or how many builders have
// already cloned a shared incumbent. See ADR-044.
func TestBuilderTLSConfigCompositionNeverDropsIncumbentTLSMaterial(t *testing.T) {
	log := createTestLogger()
	caPEM, _, _ := newTestCA(t, "no-drop-ca")
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
	require.NoError(t, err)

	t.Run("incumbent_with_client_cert_is_not_silently_replaced", func(t *testing.T) {
		incumbent := &nethttp.Transport{
			MaxIdleConns: 55,
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{{Certificate: [][]byte{[]byte("fake-client-cert")}}},
			},
		}
		_, buildErr := NewBuilder(log).WithTransport(incumbent).WithTLSConfig(cfg).Build()
		require.Error(t, buildErr, "an incumbent carrying its own client certificate must not be silently replaced by a CA-only config")
		assert.Contains(t, buildErr.Error(), "TLSClientConfig", "the error must name the TLS-material replacement, not just the generic call-order collision")
		assert.Contains(t, buildErr.Error(), "client certificate")
	})

	// A custom TLS dialer is material of the same class as TLSClientConfig
	// (pinning, a TLS tunnel) and must not be silently cleared.
	t.Run("incumbent_with_tls_dialer_is_not_silently_cleared", func(t *testing.T) {
		pinnedDialContext := func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("must never be dialed by this test")
		}
		incumbent := &nethttp.Transport{MaxIdleConns: 55, DialTLSContext: pinnedDialContext}
		_, buildErr := NewBuilder(log).WithTransport(incumbent).WithTLSConfig(cfg).Build()
		require.Error(t, buildErr, "an incumbent carrying its own TLS dialer must not be silently cleared by a CA-only config")
		assert.Contains(t, buildErr.Error(), "WithTLSConfig was called after WithTransport")
		// The message is static and does not identify which field fired; what this
		// pins is that it mentions the dialer at all, so a dialer-only caller is not
		// left reading advice about a TLSClientConfig they never set.
		assert.Contains(t, buildErr.Error(), "DialTLSContext",
			"the error must mention the TLS-dialer class, not only TLSClientConfig")
	})

	t.Run("incumbent_with_deprecated_dial_tls_is_not_silently_cleared", func(t *testing.T) {
		pinnedDialTLS := func(string, string) (net.Conn, error) {
			return nil, errors.New("must never be dialed by this test")
		}
		incumbent := &nethttp.Transport{MaxIdleConns: 55}
		//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil; this test exercises exactly that fallback field.
		incumbent.DialTLS = pinnedDialTLS
		_, buildErr := NewBuilder(log).WithTransport(incumbent).WithTLSConfig(cfg).Build()
		require.Error(t, buildErr, "an incumbent carrying its own deprecated DialTLS dialer must not be silently cleared by a CA-only config")
		assert.Contains(t, buildErr.Error(), "WithTLSConfig was called after WithTransport")
		assert.Contains(t, buildErr.Error(), "DialTLS",
			"the error must mention the TLS-dialer class; it is one static message, so this does not distinguish which field fired")
	})

	t.Run("incumbent_without_tls_config_composes_cleanly", func(t *testing.T) {
		incumbent := &nethttp.Transport{MaxIdleConns: 55}
		built, buildErr := NewBuilder(log).WithTransport(incumbent).WithTLSConfig(cfg).Build()
		require.NoError(t, buildErr)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		transport, ok := clientImpl.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok)
		assert.Equal(t, 55, transport.MaxIdleConns, "the incumbent's tuning must survive the clone")
		require.NotNil(t, transport.TLSClientConfig)
		assert.Same(t, cfg.RootCAs, transport.TLSClientConfig.RootCAs, "the transport must carry cfg's own RootCAs")
	})

	// Composing from a shared incumbent must be deterministic regardless of
	// how many builders have already cloned it.
	t.Run("same_incumbent_across_two_builders_is_deterministic", func(t *testing.T) {
		shared := &nethttp.Transport{MaxIdleConns: 66}

		_, errA := NewBuilder(log).WithTransport(shared).WithTLSConfig(cfg).Build()
		require.NoError(t, errA, "first builder must compose cleanly")

		builtB, errB := NewBuilder(log).WithTransport(shared).WithTLSConfig(cfg).Build()
		require.NoError(t, errB, "a second builder cloning the SAME shared incumbent must see the identical result — composition must not depend on construction order")

		clientImplB, ok := builtB.(*client)
		require.True(t, ok)
		transportB, ok := clientImplB.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok)
		assert.Equal(t, 66, transportB.MaxIdleConns, "the incumbent's tuning must still survive on the second build")
	})

	// ALPN-only defaults from a forced Clone() must not be treated as material.
	t.Run("incumbent_carrying_only_alpn_defaults_still_composes", func(t *testing.T) {
		incumbent := &nethttp.Transport{MaxIdleConns: 77}
		_ = incumbent.Clone() // forces onceSetNextProtoDefaults to populate an ALPN-only TLSClientConfig
		require.NotNil(t, incumbent.TLSClientConfig, "the forced Clone must have populated TLSClientConfig, or this test proves nothing")

		built, buildErr := NewBuilder(log).WithTransport(incumbent).WithTLSConfig(cfg).Build()
		require.NoError(t, buildErr, "ALPN-only defaults are not security material and must not block composition")

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		transport, ok := clientImpl.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok)
		assert.Equal(t, 77, transport.MaxIdleConns)
	})
}

// TestBuilderTLSConfigDiscardSuppressedByExplicitTLSClientConfig pins that
// discardsTLSConfig is suppressed only when the replacement transport carries
// real TLS material of its own, not merely a non-nil TLSClientConfig.
func TestBuilderTLSConfigDiscardSuppressedByExplicitTLSClientConfig(t *testing.T) {
	log := createTestLogger()
	caPEM, _, _ := newTestCA(t, "suppress-ca")
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
	require.NoError(t, err)

	t.Run("replacement_with_tls_client_config_succeeds", func(t *testing.T) {
		replacement := &nethttp.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		built, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(replacement).Build()
		require.NoError(t, buildErr)
		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.Same(t, replacement, clientImpl.httpClient.Transport)
	})

	t.Run("replacement_without_tls_client_config_still_errors", func(t *testing.T) {
		replacement := &nethttp.Transport{}
		_, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(replacement).Build()
		require.Error(t, buildErr)
		assert.Contains(t, buildErr.Error(), "WithTransport was called after WithTLSConfig")
	})

	// A TLS dialer makes net/http ignore TLSClientConfig outright, so a
	// replacement carrying only one still decides its own TLS — the compose
	// direction already treats it as material and both must agree.
	t.Run("replacement_with_only_a_tls_dialer_succeeds", func(t *testing.T) {
		replacement := &nethttp.Transport{
			DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("must never be dialed by this test")
			},
		}
		_, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(replacement).Build()
		require.NoError(t, buildErr, "a replacement that performs its own TLS handshake is not an accidental discard")
	})

	// The deprecated field is a separate branch of transportCarriesTLSMaterial and
	// net/http still honors it when DialTLSContext is nil, so it needs its own case:
	// gremlins does not mutate ||, so the mutation gate cannot cover this one.
	t.Run("replacement_with_only_deprecated_dial_tls_succeeds", func(t *testing.T) {
		replacement := &nethttp.Transport{}
		//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil; this test exercises exactly that fallback field.
		replacement.DialTLS = func(string, string) (net.Conn, error) {
			return nil, errors.New("must never be dialed by this test")
		}
		_, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(replacement).Build()
		require.NoError(t, buildErr, "a replacement carrying only the deprecated dialer still performs its own handshake")
	})

	// Mirror of incumbent_carrying_only_alpn_defaults_still_composes: an
	// ALPN-only TLSClientConfig must not suppress the discard either.
	t.Run("replacement_with_alpn_only_tls_config_still_errors", func(t *testing.T) {
		replacement := &nethttp.Transport{}
		_ = replacement.Clone() // forces onceSetNextProtoDefaults to populate an ALPN-only TLSClientConfig
		require.NotNil(t, replacement.TLSClientConfig, "the forced Clone must have populated TLSClientConfig, or this test proves nothing")

		_, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(replacement).Build()
		require.Error(t, buildErr, "an ALPN-only TLSClientConfig carries no security material and must not suppress the discard")
		assert.Contains(t, buildErr.Error(), "WithTransport was called after WithTLSConfig")
	})
}

// TestBuilderTLSSuppressionDoesNotSurviveRetake pins that discardsTLSConfig
// must stay evaluated at Build() time against the final slot occupant, not
// decided eagerly when a displacing WithTransport call happens — an eager
// version would miss a later retake that drops the TLS material again.
func TestBuilderTLSSuppressionDoesNotSurviveRetake(t *testing.T) {
	log := createTestLogger()
	caPEM, _, _ := newTestCA(t, "retake-ca")
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
	require.NoError(t, err)

	t.Run("suppressed_when_replacement_carries_tls", func(t *testing.T) {
		transportWithTLSClientConfig := &nethttp.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		_, buildErr := NewBuilder(log).WithTLSConfig(cfg).WithTransport(transportWithTLSClientConfig).Build()
		require.NoError(t, buildErr)
	})

	t.Run("not_suppressed_when_a_later_retake_drops_tls", func(t *testing.T) {
		transportWithTLSClientConfig := &nethttp.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		_, buildErr := NewBuilder(log).
			WithTLSConfig(cfg).
			WithTransport(transportWithTLSClientConfig).
			WithTransport(&nethttp.Transport{}).
			Build()
		require.Error(t, buildErr)
		assert.Contains(t, buildErr.Error(), "WithTransport was called after WithTLSConfig")
	})
}

// TestBuildAllowsExplicitTransportOverride pins the deliberate override
// carve-out ADR-044 documents: WithTransport/WithTLSConfig after
// WithHTTPClient always wins, even over a cert-carrying WithHTTPClient client.
func TestBuildAllowsExplicitTransportOverride(t *testing.T) {
	log := createTestLogger()

	t.Run("explicit_transport_after_http_client", func(t *testing.T) {
		certCarrier := &stubRoundTripper{name: "carries-client-cert"}
		clientCarryingCert := &nethttp.Client{Transport: certCarrier}
		override := &stubRoundTripper{name: "override"}

		built, err := NewBuilder(log).WithHTTPClient(clientCarryingCert).WithTransport(override).Build()
		require.NoError(t, err)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		assert.Same(t, override, clientImpl.httpClient.Transport, "explicit WithTransport must win over WithHTTPClient's own transport")
		assert.NotSame(t, certCarrier, clientImpl.httpClient.Transport, "the caller's original (cert-carrying) transport must not survive an explicit override")
	})

	t.Run("explicit_tls_config_after_http_client", func(t *testing.T) {
		certCarrier := &stubRoundTripper{name: "carries-client-cert"}
		clientCarryingCert := &nethttp.Client{Transport: certCarrier}

		caPEM, _, _ := newTestCA(t, "override-ca")
		cfg, cfgErr := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
		require.NoError(t, cfgErr)

		built, err := NewBuilder(log).WithHTTPClient(clientCarryingCert).WithTLSConfig(cfg).Build()
		require.NoError(t, err)

		clientImpl, ok := built.(*client)
		require.True(t, ok)
		transport, ok := clientImpl.httpClient.Transport.(*nethttp.Transport)
		require.True(t, ok, "WithTLSConfig must install its own *http.Transport in place of the caller's original")
		require.NotNil(t, transport.TLSClientConfig)
		assert.Same(t, cfg.RootCAs, transport.TLSClientConfig.RootCAs, "the transport must carry cfg's own RootCAs (Clone is shallow), not merely a non-nil pool")
	})
}

// TestBuildErrorIsUnsafeTransportComposition pins that Build's error stays
// classifiable with errors.Is after an Init()-style wrap, not just as text.
func TestBuildErrorIsUnsafeTransportComposition(t *testing.T) {
	log := createTestLogger()

	t.Run("discarding_composition_is_classifiable", func(t *testing.T) {
		stub := &stubRoundTripper{name: "opaque"}
		_, err := NewBuilder(log).WithHTTPClient(&nethttp.Client{Transport: stub}).WithJOSE(JOSEConfig{}).Build()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnsafeTransportComposition))

		// The sentinel still classifies after an Init()-style wrap.
		wrapped := fmt.Errorf("module init: %w", err)
		assert.True(t, errors.Is(wrapped, ErrUnsafeTransportComposition))
	})

	t.Run("successful_build_returns_nil_error", func(t *testing.T) {
		_, err := NewBuilder(log).Build()
		require.NoError(t, err)
	})
}

// wrappingTransport stands in for a signer-layer wrapper; it exposes the
// RoundTripper it wraps so tests can assert nesting depth.
type wrappingTransport struct {
	inner nethttp.RoundTripper
}

func (r *wrappingTransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	return r.inner.RoundTrip(req)
}

// recordingRoundTripper records whether RoundTrip was invoked, so a test that swaps
// nethttp.DefaultTransport can observe late binding rather than capture-at-Build.
type recordingRoundTripper struct {
	called bool
}

func (r *recordingRoundTripper) RoundTrip(_ *nethttp.Request) (*nethttp.Response, error) {
	r.called = true
	return nil, errors.New("recordingRoundTripper: no response configured")
}

func TestNewBuilderAndBuildRejectNilLogger(t *testing.T) {
	const builderMsg = "httpclient: NewBuilder requires a non-nil logger (pass deps.Logger)"
	const buildMsg = "httpclient: Build requires a Builder created by NewBuilder"

	t.Run("new_builder", func(t *testing.T) {
		assert.PanicsWithValue(t, builderMsg, func() { NewBuilder(nil) })
	})

	t.Run("new_client_forwards_its_argument", func(t *testing.T) {
		assert.PanicsWithValue(t, builderMsg, func() { NewClient(nil) })
	})

	t.Run("zero_value_builder_has_no_config", func(t *testing.T) {
		assert.PanicsWithValue(t, buildMsg, func() { _, _ = (&Builder{}).Build() })
	})

	t.Run("builder_literal_has_no_logger", func(t *testing.T) {
		assert.PanicsWithValue(t, buildMsg, func() {
			_, _ = (&Builder{config: &Config{Timeout: time.Second}}).Build()
		})
	})

	t.Run("typed_nil_logger", func(t *testing.T) {
		assert.PanicsWithValue(t, builderMsg, func() {
			var zl *logger.ZeroLogger
			NewBuilder(zl)
		})
	})

	t.Run("typed_nil_logger_in_build", func(t *testing.T) {
		assert.PanicsWithValue(t, buildMsg, func() {
			_, _ = (&Builder{config: &Config{Timeout: time.Second}, logger: (*logger.ZeroLogger)(nil)}).Build()
		})
	})

	t.Run("nil_builder_receiver", func(t *testing.T) {
		assert.PanicsWithValue(t, buildMsg, func() {
			var b *Builder
			_, _ = b.Build()
		})
	})
}

// capturingRoundTripper records the body and Content-Type the chain actually put on
// the wire, then answers 200 with plaintext JSON.
type capturingRoundTripper struct {
	body        string
	contentType string
}

func (c *capturingRoundTripper) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	if req.Body != nil {
		// A RoundTripper owns the request body once it reads it — close it on every
		// path, the same obligation addTransportWrapper documents for real layers.
		defer req.Body.Close()
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		c.body = string(raw)
	}
	c.contentType = req.Header.Get(testContentTypeHdr)
	return &nethttp.Response{
		StatusCode: nethttp.StatusOK,
		Header:     nethttp.Header{testContentTypeHdr: []string{testJSONType}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

// outboundKidsOnly is an outbound policy carrying only the two kids — every algorithm
// left at its zero value, the shape Build must fill from the jose package defaults.
func outboundKidsOnly(f *jositest.BidirectionalFixture) *jose.Policy {
	return &jose.Policy{
		Direction:  jose.DirectionOutbound,
		SignKid:    f.ClientOutbound.SignKid,
		EncryptKid: f.ClientOutbound.EncryptKid,
	}
}

// A policy whose algorithms sit outside the jose allowlist used to seal every request
// and fail per-request in production. Build now refuses it, the ADR-044 posture.
func TestBuildRejectsJOSEPolicyWithDisallowedAlgorithm(t *testing.T) {
	log := createTestLogger()
	f := jositest.NewBidirectionalFixture(t)

	cases := []struct {
		name    string
		mutate  func(p *jose.Policy)
		wantAlg string
	}{
		{name: "symmetric_signature", mutate: func(p *jose.Policy) { p.SigAlg = "HS256" }, wantAlg: "HS256"},
		{name: "pkcs1v15_key_wrapping", mutate: func(p *jose.Policy) { p.KeyAlg = "RSA1_5" }, wantAlg: "RSA1_5"},
		{name: "non_aead_content_encryption", mutate: func(p *jose.Policy) { p.Enc = "A256CBC-HS512" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := outboundKidsOnly(f)
			tc.mutate(policy)

			_, err := NewBuilder(log).
				WithTransport(&stubRoundTripper{name: "base"}).
				WithJOSE(JOSEConfig{Outbound: policy, Resolver: f.Resolver}).
				Build()

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid JOSE policy")
			assert.NotErrorIs(t, err, ErrUnsafeTransportComposition,
				"a policy failure is its own error path, not a transport-slot displacement")

			var jerr *jose.Error
			require.ErrorAs(t, err, &jerr)
			require.ErrorIs(t, err, jose.ErrAlgorithmDisallowed)
			assert.Equal(t, "JOSE_ALGORITHM_DISALLOWED", jerr.Code)
			if tc.wantAlg != "" {
				assert.Equal(t, tc.wantAlg, jerr.Alg)
			}
		})
	}
}

// A policy that names only its kids must build and seal: Build fills the algorithms
// from the jose defaults, exactly as the server's tag parser does.
func TestBuildAppliesJOSEDefaultsToZeroValuePolicy(t *testing.T) {
	log := createTestLogger()
	f := jositest.NewBidirectionalFixture(t)
	inner := &capturingRoundTripper{}

	built, err := NewBuilder(log).
		WithTransport(inner).
		WithJOSE(JOSEConfig{Outbound: outboundKidsOnly(f), Resolver: f.Resolver}).
		Build()
	require.NoError(t, err)

	resp, err := built.Post(context.Background(), &Request{
		URL:  "http://example.invalid/tokens",
		Body: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)

	assert.Equal(t, jose.ContentType, inner.contentType, "the defaulted policy must still seal the body")
	plaintext, _, hdr, err := jose.Open(inner.body, f.PeerInbound, f.Resolver)
	require.NoError(t, err, "the sealed payload must parse under the peer's inbound policy")
	assert.JSONEq(t, `{"hello":"world"}`, string(plaintext))
	assert.Equal(t, string(jose.DefaultSigAlg), hdr.JWS.Alg, "an unset SigAlg must land on the package default")
	assert.Equal(t, string(jose.DefaultKeyAlg), hdr.JWE.Alg)
	assert.Equal(t, string(jose.DefaultEnc), hdr.JWE.Enc)
	// Asserted explicitly because nothing else here would notice: Policy.Validate
	// ignores Cty, and Open's cty check is permissive by design — it accepts a token
	// that declares no cty at all, so an undefaulted Cty would reach the wire silently.
	assert.Equal(t, jose.DefaultCty, hdr.JWS.Cty, "an unset Cty must land on the package default")
}

// The mirror of the defaulting above: a Cty the caller set explicitly must survive
// Build untouched. Cty is the one normalized field Policy.Validate never inspects,
// so only the emitted header can show which way the default went.
func TestBuildKeepsExplicitJOSECty(t *testing.T) {
	const customCty = "application/vnd.test+json"

	log := createTestLogger()
	f := jositest.NewBidirectionalFixture(t)
	inner := &capturingRoundTripper{}

	outbound := outboundKidsOnly(f)
	outbound.Cty = customCty

	built, err := NewBuilder(log).
		WithTransport(inner).
		WithJOSE(JOSEConfig{Outbound: outbound, Resolver: f.Resolver}).
		Build()
	require.NoError(t, err)

	_, err = built.Post(context.Background(), &Request{
		URL:  "http://example.invalid/tokens",
		Body: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)

	// The peer declares the same cty, so a clobbered one is rejected outright
	// (JOSE_CTY_REJECTED) as well as being visible in the header below.
	peerInbound := *f.PeerInbound
	peerInbound.Cty = customCty

	plaintext, _, hdr, err := jose.Open(inner.body, &peerInbound, f.Resolver)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hello":"world"}`, string(plaintext))
	assert.Equal(t, customCty, hdr.JWS.Cty, "an explicitly-set Cty must not be overwritten by the default")
}

// A nil Resolver used to fail per-request as JOSE_KEYSTORE_UNAVAILABLE.
func TestBuildRejectsJOSEWithoutResolver(t *testing.T) {
	log := createTestLogger()
	f := jositest.NewBidirectionalFixture(t)

	cases := []struct {
		name string
		cfg  JOSEConfig
	}{
		{name: "outbound_without_resolver", cfg: JOSEConfig{Outbound: f.ClientOutbound}},
		{name: "inbound_without_resolver", cfg: JOSEConfig{Inbound: f.ClientInbound}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewBuilder(log).WithTransport(&stubRoundTripper{name: "base"}).WithJOSE(tc.cfg).Build()
			require.Error(t, err)
			require.ErrorIs(t, err, jose.ErrKeyResolution)

			var jerr *jose.Error
			require.ErrorAs(t, err, &jerr, "every Build JOSE failure must be matchable as a *jose.Error")
			assert.Equal(t, "JOSE_KEYSTORE_UNAVAILABLE", jerr.Code)
		})
	}

	t.Run("no_policy_needs_no_resolver", func(t *testing.T) {
		_, err := NewBuilder(log).WithTransport(&stubRoundTripper{name: "base"}).WithJOSE(JOSEConfig{}).Build()
		require.NoError(t, err, "an all-nil JOSEConfig registers an inert layer and must still build")
	})
}

// Build normalizes copies: the caller may reuse its jose.Policy across builders, so
// defaulting must not write algorithms back into the struct it was handed.
func TestBuildDoesNotMutateCallerJOSEPolicy(t *testing.T) {
	log := createTestLogger()
	f := jositest.NewBidirectionalFixture(t)

	caller := outboundKidsOnly(f)
	before := *caller
	cfg := JOSEConfig{Outbound: caller, Resolver: f.Resolver}

	built, err := NewBuilder(log).WithTransport(&capturingRoundTripper{}).WithJOSE(cfg).Build()
	require.NoError(t, err)

	assert.Equal(t, before, *caller, "Build must normalize a copy, never the caller's policy")
	assert.Empty(t, string(caller.SigAlg), "the caller's unset algorithm must stay unset")
	assert.Same(t, caller, cfg.Outbound, "the caller's JOSEConfig must not be repointed either")

	// The normalized copy is what reached the transport — proving the copy is not a
	// discarded intermediate the request path bypasses.
	clientImpl, ok := built.(*client)
	require.True(t, ok)
	joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
	require.True(t, ok)
	assert.NotSame(t, caller, joseTransport.Outbound)
	assert.Equal(t, jose.DefaultSigAlg, joseTransport.Outbound.SigAlg)
}

// Two WithJOSE calls must not stack two body-transform layers: the wrapper closure
// reads the builder's config, so a second layer would seal the first layer's JWE
// again. Last call wins, and the body is sealed exactly once.
func TestWithJOSECalledTwiceSealsOnce(t *testing.T) {
	log := createTestLogger()
	// Two fixtures reuse the same kid names but hold different keys, so which config
	// sealed the body is decided by whose resolver can open it.
	first := jositest.NewBidirectionalFixture(t)
	last := jositest.NewBidirectionalFixture(t)
	inner := &capturingRoundTripper{}

	built, err := NewBuilder(log).
		WithTransport(inner).
		WithJOSE(JOSEConfig{Outbound: first.ClientOutbound, Inbound: first.ClientInbound, Resolver: first.Resolver}).
		WithJOSE(JOSEConfig{Outbound: last.ClientOutbound, Resolver: last.Resolver}).
		Build()
	require.NoError(t, err)

	clientImpl, ok := built.(*client)
	require.True(t, ok)
	joseTransport, ok := clientImpl.httpClient.Transport.(*JOSETransport)
	require.True(t, ok)
	assert.Same(t, inner, joseTransport.Inner,
		"the single JOSE layer must sit directly on the base transport, not on a second JOSE layer")
	assert.Nil(t, joseTransport.Inbound,
		"the last call wins outright, including the Inbound policy it did not carry")

	_, err = built.Post(context.Background(), &Request{
		URL:  "http://example.invalid/tokens",
		Body: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)

	// Double sealing would yield the inner compact JWE here, not the payload — and
	// only the last config's peer can decrypt at all.
	plaintext, _, _, err := jose.Open(inner.body, last.PeerInbound, last.Resolver)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hello":"world"}`, string(plaintext))
}
