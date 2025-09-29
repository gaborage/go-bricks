package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func TestAuthMiddleware(t *testing.T) {
	// Create test app and handlers with bearer token
	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  "/_debug",
		BearerToken: "test-secret-token",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	// Create a test handler that returns success if auth passes
	testHandler := func(c echo.Context) error {
		return c.String(http.StatusOK, "success")
	}

	// Get the auth middleware
	authMiddleware := debugHandlers.authMiddleware()
	wrappedHandler := authMiddleware(testHandler)

	tests := []struct {
		name               string
		authHeader         string
		expectedStatusCode int
		expectedBody       string
	}{
		{
			name:               "valid bearer token",
			authHeader:         "Bearer test-secret-token",
			expectedStatusCode: http.StatusOK,
			expectedBody:       "success",
		},
		{
			name:               "invalid bearer token",
			authHeader:         "Bearer wrong-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "missing bearer prefix",
			authHeader:         "test-secret-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "empty authorization header",
			authHeader:         "",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "bearer with no token",
			authHeader:         "Bearer ",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "case sensitive bearer",
			authHeader:         "bearer test-secret-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test request
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			// Set a test remote IP for logging
			req.RemoteAddr = "127.0.0.1:12345"

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Execute the wrapped handler
			err := wrappedHandler(c)

			if tt.expectedStatusCode == http.StatusOK {
				// Should succeed
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatusCode, rec.Code)
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			} else {
				// Should return an HTTP error
				assert.Error(t, err)

				// Check if it's an echo.HTTPError
				if httpErr, ok := err.(*echo.HTTPError); ok {
					assert.Equal(t, tt.expectedStatusCode, httpErr.Code)
				}
			}
		})
	}
}

func TestAuthMiddlewareConstantTimeComparison(t *testing.T) {
	// This test ensures that the constant-time comparison is working
	// We can't easily test timing attacks, but we can verify the behavior

	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  "/_debug",
		BearerToken: "secret123",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	testHandler := func(c echo.Context) error {
		return c.String(http.StatusOK, "success")
	}

	authMiddleware := debugHandlers.authMiddleware()
	wrappedHandler := authMiddleware(testHandler)

	// Test tokens that are similar but not exact
	tokens := []struct {
		token    string
		expected bool
	}{
		{"secret123", true},   // exact match
		{"secret124", false},  // one char different
		{"secret12", false},   // shorter
		{"secret1234", false}, // longer
		{"SECRET123", false},  // different case
		{"", false},           // empty
	}

	for _, tt := range tokens {
		t.Run("token_"+tt.token, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := wrappedHandler(c)

			if tt.expected {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err)
				if httpErr, ok := err.(*echo.HTTPError); ok {
					assert.Equal(t, http.StatusUnauthorized, httpErr.Code)
				}
			}
		})
	}
}
