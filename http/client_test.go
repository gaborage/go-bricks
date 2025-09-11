package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// createTestLogger creates a logger that outputs to a buffer for testing
func createTestLogger() logger.Logger {
	return logger.New("info", false)
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
			WithDefaultHeader("X-API-Key", "test-key").
			WithDefaultHeader("User-Agent", "test-agent").
			Build()
		assert.NotNil(t, client)
	})

	t.Run("with interceptors", func(t *testing.T) {
		reqInterceptor := func(_ context.Context, req *nethttp.Request) error {
			req.Header.Set("X-Intercepted", "true")
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
			server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "test-value", r.Header.Get("X-Custom-Header"))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewClient(log)
		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				"Content-Type":    "application/json",
				"X-Custom-Header": "test-value",
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("default headers", func(t *testing.T) {
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "test-agent", r.Header.Get("User-Agent"))
			assert.Equal(t, "test-key", r.Header.Get("X-API-Key"))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithDefaultHeader("User-Agent", "test-agent").
			WithDefaultHeader("X-API-Key", "test-key").
			Build()

		req := &Request{URL: server.URL}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})

	t.Run("request headers override defaults", func(t *testing.T) {
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "custom-agent", r.Header.Get("User-Agent"))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		client := NewBuilder(log).
			WithDefaultHeader("User-Agent", "default-agent").
			Build()

		req := &Request{
			URL: server.URL,
			Headers: map[string]string{
				"User-Agent": "custom-agent",
			},
		}

		_, err := client.Get(context.Background(), req)
		require.NoError(t, err)
	})
}

func TestClientBasicAuth(t *testing.T) {
	log := createTestLogger()

	t.Run("client-level auth", func(t *testing.T) {
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
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
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		// Content-Type should default to application/json when body is present
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			assert.Equal(t, "intercepted", r.Header.Get("X-Intercepted"))
			w.WriteHeader(nethttp.StatusOK)
		}))
		defer server.Close()

		reqInterceptor := func(_ context.Context, req *nethttp.Request) error {
			req.Header.Set("X-Intercepted", "intercepted")
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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

	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
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
		var calls int
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			calls++
			if calls == 1 {
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
		assert.Equal(t, 2, calls)
	})

	t.Run("does not retry on 4xx", func(t *testing.T) {
		var calls int
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			calls++
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
		assert.Equal(t, 1, calls)
	})

	t.Run("retries on timeout then fails", func(t *testing.T) {
		var calls int
		server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			calls++
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
		assert.Equal(t, 2, calls) // initial + one retry
	})
}
