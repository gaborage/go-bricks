package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
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

// EmptyRequest is a request type with no fields for simple handlers
type EmptyRequest struct{}

// Response is a simple response type for testing
type Response struct {
	Message string `json:"message"`
}
