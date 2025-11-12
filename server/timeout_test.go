package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// TestTimeoutHandling tests that the enhanced handler properly detects and handles timeouts
func TestTimeoutHandling(t *testing.T) {
	tests := []struct {
		name           string
		contextTimeout time.Duration
		delayBefore    time.Duration // Delay before calling handler
		expectTimeout  bool
		expectedStatus int
	}{
		{
			name:           "no timeout - quick response",
			contextTimeout: 100 * time.Millisecond,
			delayBefore:    0,
			expectTimeout:  false,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "timeout before handler execution",
			contextTimeout: 10 * time.Millisecond,
			delayBefore:    20 * time.Millisecond,
			expectTimeout:  true,
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "timeout at edge case",
			contextTimeout: 100 * time.Millisecond,
			delayBefore:    50 * time.Millisecond,
			expectTimeout:  false, // Should complete with reasonable buffer
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			e := echo.New()
			e.Validator = NewValidator()
			cfg := &config.Config{
				App: config.AppConfig{
					Env: config.EnvDevelopment,
				},
			}

			// Create handler registry
			hr := NewHandlerRegistry(cfg)
			registrar := newRouteGroup(e.Group(""), "")

			// Define a simple handler
			handler := func(_ EmptyRequest, _ HandlerContext) (Response, IAPIError) {
				return Response{Message: "success"}, nil
			}

			// Register handler
			GET(hr, registrar, "/test", handler)

			// Create request
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), tt.contextTimeout)
			defer cancel()

			// If delay is specified, wait before making request
			if tt.delayBefore > 0 {
				time.Sleep(tt.delayBefore)
			}

			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			// Execute
			e.ServeHTTP(rec, req)

			// Assert
			if !assert.Equal(t, tt.expectedStatus, rec.Code) {
				t.Logf("Response body: %s", rec.Body.String())
			}

			if tt.expectTimeout {
				assert.Contains(t, rec.Body.String(), "timeout")
			}
		})
	}
}

// TestContextDeadlineDetectionBeforeBinding tests early context deadline detection
func TestContextDeadlineDetectionBeforeBinding(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	hr := NewHandlerRegistry(cfg)
	registrar := newRouteGroup(e.Group(""), "")

	// Handler that should never be called
	handlerCalled := false
	handler := func(_ EmptyRequest, _ HandlerContext) (Response, IAPIError) {
		handlerCalled = true
		return Response{Message: "success"}, nil
	}

	GET(hr, registrar, "/test", handler)

	// Create request with already-cancelled context
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	// Execute
	e.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, handlerCalled, "Handler should not be called when context is already cancelled")
	assert.Contains(t, rec.Body.String(), "timeout")
}

// TestCustomErrorHandlerTimeoutDetection tests that the custom error handler properly handles context.DeadlineExceeded
func TestCustomErrorHandlerTimeoutDetection(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	// Configure custom error handler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, cfg)
	}

	// Create a handler that returns context.DeadlineExceeded
	e.GET("/timeout", func(_ echo.Context) error {
		return context.DeadlineExceeded
	})

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/timeout", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "timed out")
}

// TestTimeoutDuringValidation tests timeout detection after binding but before handler execution
func TestTimeoutDuringValidation(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	// Set up validator
	e.Validator = NewValidator()

	hr := NewHandlerRegistry(cfg)
	registrar := newRouteGroup(e.Group(""), "")

	type SlowValidationRequest struct {
		Name string `json:"name" validate:"required,min=3"`
	}

	handlerCalled := false
	handler := func(_ SlowValidationRequest, _ HandlerContext) (Response, IAPIError) {
		handlerCalled = true
		return Response{Message: "success"}, nil
	}

	POST(hr, registrar, "/test", handler)

	// Create request with very short timeout and valid JSON body
	// Use valid JSON so binding succeeds, allowing us to test the post-validation timeout checkpoint
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Wait for context to expire
	time.Sleep(5 * time.Millisecond)

	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	// Execute
	e.ServeHTTP(rec, req)

	// Assert
	assert.False(t, handlerCalled, "Handler should not be called after timeout")
	// Should get timeout error, not validation error
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestContextCancellationDuringHandlerExecution verifies that context cancellation
// during handler execution returns a structured 503 ServiceUnavailable response
// instead of a raw context error.
func TestContextCancellationDuringHandlerExecution(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	hr := NewHandlerRegistry(cfg)
	registrar := newRouteGroup(e.Group(""), "")

	// Handler that simulates work and allows context cancellation
	handler := func(_ EmptyRequest, _ HandlerContext) (Response, IAPIError) {
		// Simulate some work
		time.Sleep(50 * time.Millisecond)
		return Response{Message: "success"}, nil
	}

	GET(hr, registrar, "/test", handler)

	// Create request with short timeout that expires during handler execution
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	// Execute
	e.ServeHTTP(rec, req)

	// Assert structured 503 response
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error"`)
	assert.Contains(t, rec.Body.String(), `"meta"`)
	assert.Contains(t, rec.Body.String(), "timeout")

	// Verify it's valid JSON with proper structure
	var response map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(t, err, "Response should be valid JSON")
	assert.NotNil(t, response["error"], "Response should have error field")
	assert.NotNil(t, response["meta"], "Response should have meta field")
}

// TestTimeoutWithLoggerMiddleware tests the specific scenario that caused production panics:
// timeout + logger middleware attempting to log response status after deadline exceeded.
// This test ensures the custom Timeout middleware prevents response invalidation.
func TestTimeoutWithLoggerMiddleware(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 50 * time.Millisecond,
			},
		},
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	// Set up custom error handler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, cfg)
	}

	// Apply timeout middleware
	e.Use(Timeout(cfg.Server.Timeout.Middleware))

	// Apply logger middleware (this is where the panic occurred in production)
	// Use a thread-safe no-op logger for concurrency tests
	log := &noopLogger{}
	e.Use(LoggerWithConfig(log, LoggerConfig{
		HealthPath:           "/health",
		ReadyPath:            "/ready",
		SlowRequestThreshold: 1 * time.Second,
	}))

	// Slow handler that exceeds timeout and respects context cancellation
	e.GET("/slow", func(c echo.Context) error {
		// Simulate slow operation that checks context
		select {
		case <-time.After(100 * time.Millisecond): // Exceeds 50ms timeout
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		case <-c.Request().Context().Done():
			// Context cancelled - return the error to trigger timeout handling
			return c.Request().Context().Err()
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/slow", http.NoBody)
	rec := httptest.NewRecorder()

	// Should not panic, should return 503 Service Unavailable
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "Expected 503 for timeout")
	assert.Contains(t, rec.Body.String(), "timed out", "Response should mention timeout")

	// Verify the response is a properly formatted API error
	var response map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(t, err, "Response should be valid JSON")
	assert.NotNil(t, response["error"], "Response should have error field")
	assert.NotNil(t, response["meta"], "Response should have meta field")
}

// TestTimeoutWithLoggerHighConcurrency simulates the production load test scenario
// with multiple concurrent requests timing out to ensure no panics occur.
func TestTimeoutWithLoggerHighConcurrency(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 20 * time.Millisecond,
			},
		},
		App: config.AppConfig{
			Env: config.EnvDevelopment,
		},
	}

	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, cfg)
	}

	e.Use(Timeout(cfg.Server.Timeout.Middleware))
	log := &noopLogger{}
	e.Use(LoggerWithConfig(log, LoggerConfig{
		HealthPath:           "/health",
		ReadyPath:            "/ready",
		SlowRequestThreshold: 1 * time.Second,
	}))

	e.GET("/endpoint", func(c echo.Context) error {
		// Simulate slow operation that checks context
		select {
		case <-time.After(50 * time.Millisecond): // Always times out
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		}
	})

	// Simulate 50 concurrent requests (scaled down from 200 VUs for test speed)
	concurrency := 50
	done := make(chan bool, concurrency)
	panicOccurred := false

	for i := 0; i < concurrency; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Panic occurred: %v", r)
					panicOccurred = true
				}
				done <- true
			}()

			req := httptest.NewRequest(http.MethodGet, "/endpoint", http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// All requests should timeout gracefully
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("Expected 503, got %d", rec.Code)
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < concurrency; i++ {
		<-done
	}

	assert.False(t, panicOccurred, "No panics should occur during concurrent timeouts")
}

// EmptyRequest is a request type with no fields for simple handlers
type EmptyRequest struct{}

// Response is a simple response type for testing
type Response struct {
	Message string `json:"message"`
}

// noopLogger is a thread-safe no-op logger for concurrency tests
type noopLogger struct{}

type noopLogEvent struct{}

func (n *noopLogger) Info() logger.LogEvent                     { return &noopLogEvent{} }
func (n *noopLogger) Error() logger.LogEvent                    { return &noopLogEvent{} }
func (n *noopLogger) Debug() logger.LogEvent                    { return &noopLogEvent{} }
func (n *noopLogger) Warn() logger.LogEvent                     { return &noopLogEvent{} }
func (n *noopLogger) Fatal() logger.LogEvent                    { return &noopLogEvent{} }
func (n *noopLogger) WithContext(_ any) logger.Logger           { return n }
func (n *noopLogger) WithFields(_ map[string]any) logger.Logger { return n }
func (e *noopLogEvent) Msg(_ string) {
	// No-op
}
func (e *noopLogEvent) Msgf(_ string, _ ...any) {
	// No-op
}
func (e *noopLogEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *noopLogEvent) Str(_, _ string) logger.LogEvent               { return e }
func (e *noopLogEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *noopLogEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *noopLogEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *noopLogEvent) Float64(_ string, _ float64) logger.LogEvent   { return e }
func (e *noopLogEvent) Bool(_ string, _ bool) logger.LogEvent         { return e }
func (e *noopLogEvent) Any(_ string, _ any) logger.LogEvent           { return e }
func (e *noopLogEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *noopLogEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *noopLogEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
