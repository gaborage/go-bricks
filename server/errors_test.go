package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestBaseAPIError(t *testing.T) {
	t.Run("new_base_api_error", func(t *testing.T) {
		err := NewBaseAPIError("TEST_ERROR", "Test error message", http.StatusBadRequest)

		assert.Equal(t, "TEST_ERROR", err.ErrorCode())
		assert.Equal(t, "Test error message", err.Message())
		assert.Equal(t, http.StatusBadRequest, err.HTTPStatus())
		assert.NotNil(t, err.Details())
		assert.Empty(t, err.Details())
		assert.Equal(t, "TEST_ERROR: Test error message", err.Error())
	})

	t.Run("with_details", func(t *testing.T) {
		err := NewBaseAPIError("TEST_ERROR", "Test error message", http.StatusBadRequest)
		err.WithDetails("key1", "value1")
		err.WithDetails("key2", 123)

		details := err.Details()
		assert.Len(t, details, 2)
		assert.Equal(t, "value1", details["key1"])
		assert.Equal(t, 123, details["key2"])
	})

	t.Run("details_are_copied", func(t *testing.T) {
		err := NewBaseAPIError("TEST_ERROR", "Test error message", http.StatusBadRequest)
		err.WithDetails("key1", "value1")

		details1 := err.Details()
		details2 := err.Details()

		// Modify one copy
		details1["key2"] = "value2"

		// Original and second copy should not be affected
		assert.Len(t, err.Details(), 1)
		assert.Len(t, details2, 1)
		assert.NotEqual(t, details1, details2)
	})

	t.Run("nil_error_string", func(t *testing.T) {
		var err *BaseAPIError
		assert.Equal(t, "", err.Error())
	})

	t.Run("empty_code_error_string", func(t *testing.T) {
		err := NewBaseAPIError("", "Test message", http.StatusBadRequest)
		assert.Equal(t, "Test message", err.Error())
	})
}

func TestSpecificErrorTypes(t *testing.T) {
	tests := []struct {
		name           string
		createError    func() IAPIError
		expectedCode   string
		expectedStatus int
		expectedMsg    string
	}{
		{
			name: "not_found_error",
			createError: func() IAPIError {
				return NewNotFoundError("User")
			},
			expectedCode:   "NOT_FOUND",
			expectedStatus: http.StatusNotFound,
			expectedMsg:    "User not found",
		},
		{
			name: "conflict_error",
			createError: func() IAPIError {
				return NewConflictError("Email already exists")
			},
			expectedCode:   "CONFLICT",
			expectedStatus: http.StatusConflict,
			expectedMsg:    "Email already exists",
		},
		{
			name: "unauthorized_error_with_message",
			createError: func() IAPIError {
				return NewUnauthorizedError("Invalid credentials")
			},
			expectedCode:   "UNAUTHORIZED",
			expectedStatus: http.StatusUnauthorized,
			expectedMsg:    "Invalid credentials",
		},
		{
			name: "unauthorized_error_default",
			createError: func() IAPIError {
				return NewUnauthorizedError("")
			},
			expectedCode:   "UNAUTHORIZED",
			expectedStatus: http.StatusUnauthorized,
			expectedMsg:    "Authentication required",
		},
		{
			name: "forbidden_error_with_message",
			createError: func() IAPIError {
				return NewForbiddenError("Insufficient permissions")
			},
			expectedCode:   "FORBIDDEN",
			expectedStatus: http.StatusForbidden,
			expectedMsg:    "Insufficient permissions",
		},
		{
			name: "forbidden_error_default",
			createError: func() IAPIError {
				return NewForbiddenError("")
			},
			expectedCode:   "FORBIDDEN",
			expectedStatus: http.StatusForbidden,
			expectedMsg:    "Access denied",
		},
		{
			name: "internal_server_error_with_message",
			createError: func() IAPIError {
				return NewInternalServerError("Database connection failed")
			},
			expectedCode:   "INTERNAL_ERROR",
			expectedStatus: http.StatusInternalServerError,
			expectedMsg:    "Database connection failed",
		},
		{
			name: "internal_server_error_default",
			createError: func() IAPIError {
				return NewInternalServerError("")
			},
			expectedCode:   "INTERNAL_ERROR",
			expectedStatus: http.StatusInternalServerError,
			expectedMsg:    "An internal error occurred",
		},
		{
			name: "bad_request_error",
			createError: func() IAPIError {
				return NewBadRequestError("Invalid input format")
			},
			expectedCode:   "BAD_REQUEST",
			expectedStatus: http.StatusBadRequest,
			expectedMsg:    "Invalid input format",
		},
		{
			name: "service_unavailable_error_with_message",
			createError: func() IAPIError {
				return NewServiceUnavailableError("Database maintenance")
			},
			expectedCode:   "SERVICE_UNAVAILABLE",
			expectedStatus: http.StatusServiceUnavailable,
			expectedMsg:    "Database maintenance",
		},
		{
			name: "service_unavailable_error_default",
			createError: func() IAPIError {
				return NewServiceUnavailableError("")
			},
			expectedCode:   "SERVICE_UNAVAILABLE",
			expectedStatus: http.StatusServiceUnavailable,
			expectedMsg:    "Service temporarily unavailable",
		},
		{
			name: "too_many_requests_error_with_message",
			createError: func() IAPIError {
				return NewTooManyRequestsError("API quota exceeded")
			},
			expectedCode:   "TOO_MANY_REQUESTS",
			expectedStatus: http.StatusTooManyRequests,
			expectedMsg:    "API quota exceeded",
		},
		{
			name: "too_many_requests_error_default",
			createError: func() IAPIError {
				return NewTooManyRequestsError("")
			},
			expectedCode:   "TOO_MANY_REQUESTS",
			expectedStatus: http.StatusTooManyRequests,
			expectedMsg:    "Rate limit exceeded",
		},
		{
			name: "business_logic_error",
			createError: func() IAPIError {
				return NewBusinessLogicError("INSUFFICIENT_BALANCE", "Account balance is too low")
			},
			expectedCode:   "INSUFFICIENT_BALANCE",
			expectedStatus: http.StatusUnprocessableEntity,
			expectedMsg:    "Account balance is too low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.createError()

			assert.Equal(t, tt.expectedCode, err.ErrorCode())
			assert.Equal(t, tt.expectedStatus, err.HTTPStatus())
			assert.Equal(t, tt.expectedMsg, err.Message())
			assert.NotNil(t, err.Details())
		})
	}
}

func TestCustomErrorHandler(t *testing.T) {
	tests := []struct {
		name           string
		config         *config.Config
		error          error
		expectedStatus int
		expectedCode   string
		expectDetails  bool
	}{
		{
			name: "api_error_in_development",
			config: &config.Config{
				App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
			},
			error:          NewBadRequestError("Invalid input"),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "BAD_REQUEST",
			expectDetails:  true,
		},
		{
			name: "api_error_in_production",
			config: &config.Config{
				App: config.AppConfig{Env: config.EnvProduction, Debug: false},
			},
			error:          NewInternalServerError("Database error"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_ERROR",
			expectDetails:  true,
		},
		{
			name: "echo_http_error_string",
			config: &config.Config{
				App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
			},
			error:          echo.NewHTTPError(http.StatusNotFound, "Route not found"),
			expectedStatus: http.StatusNotFound,
			expectedCode:   "NOT_FOUND",
			expectDetails:  true,
		},
		{
			name: "echo_http_error_error_type",
			config: &config.Config{
				App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
			},
			error:          echo.NewHTTPError(http.StatusBadRequest, fmt.Errorf("validation failed")),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "BAD_REQUEST",
			expectDetails:  true,
		},
		{
			name: "generic_error_development",
			config: &config.Config{
				App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
			},
			error:          fmt.Errorf("generic error"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_ERROR",
			expectDetails:  true,
		},
		{
			name: "generic_error_production",
			config: &config.Config{
				App: config.AppConfig{Env: "production", Debug: false},
			},
			error:          fmt.Errorf("generic error"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_ERROR",
			expectDetails:  false,
		},
		{
			name: "internal_error_production_sanitized",
			config: &config.Config{
				App: config.AppConfig{Env: "production", Debug: false},
			},
			error:          echo.NewHTTPError(http.StatusInternalServerError, "Database connection failed"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_ERROR",
			expectDetails:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Call the custom error handler
			customErrorHandler(tt.error, c, tt.config)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			// Parse response
			var response APIResponse
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			require.NoError(t, err)

			// Verify error structure
			require.NotNil(t, response.Error)
			assert.Equal(t, tt.expectedCode, response.Error.Code)
			assert.NotEmpty(t, response.Error.Message)

			// Verify details presence based on environment
			if tt.expectDetails && (tt.config.App.Env == config.EnvDevelopment) {
				if tt.expectedStatus >= http.StatusInternalServerError && !tt.config.App.Debug {
					// Production 5xx errors should not have details
					assert.Nil(t, response.Error.Details)
				} else if response.Error.Details != nil {
					// Development or non-5xx errors may have details
					assert.NotEmpty(t, response.Error.Details)
				}
			}

			// Verify no data in error response
			assert.Nil(t, response.Data)

			// Verify metadata
			assert.NotNil(t, response.Meta)
			assert.Contains(t, response.Meta, "timestamp")
		})
	}
}

func TestStatusToErrorCode(t *testing.T) {
	tests := []struct {
		status       int
		expectedCode string
	}{
		{http.StatusBadRequest, "BAD_REQUEST"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusConflict, "CONFLICT"},
		{http.StatusTooManyRequests, "TOO_MANY_REQUESTS"},
		{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{http.StatusInternalServerError, "INTERNAL_ERROR"},
		{http.StatusTeapot, "INTERNAL_ERROR"}, // Unknown status
		{999, "INTERNAL_ERROR"},               // Invalid status
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			code := statusToErrorCode(tt.status)
			assert.Equal(t, tt.expectedCode, code)
		})
	}
}

func TestErrorResponseFormatting(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, &config.Config{
			App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
		})
	}

	e.GET("/test", func(_ echo.Context) error {
		return NewBadRequestError("Test validation error").
			WithDetails("field", "email").
			WithDetails("value", "invalid-email")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var response APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	// Verify response structure
	assert.Nil(t, response.Data)
	require.NotNil(t, response.Error)
	assert.Equal(t, "BAD_REQUEST", response.Error.Code)
	assert.Equal(t, "Test validation error", response.Error.Message)

	// Verify details
	require.NotNil(t, response.Error.Details)
	assert.Equal(t, "email", response.Error.Details["field"])
	assert.Equal(t, "invalid-email", response.Error.Details["value"])

	// Verify metadata
	require.NotNil(t, response.Meta)
	assert.Contains(t, response.Meta, "timestamp")
}

func TestErrorHandlerLogging(t *testing.T) {
	e := echo.New()

	// Capture logs for testing
	var loggedErrors []string
	e.Logger.SetOutput(&testWriter{logs: &loggedErrors})

	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, &config.Config{
			App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
		})
	}

	tests := []struct {
		name         string
		error        error
		expectLogged bool
	}{
		{
			name:         "4xx_error_not_logged",
			error:        NewBadRequestError("Bad input"),
			expectLogged: false,
		},
		{
			name:         "5xx_error_logged",
			error:        echo.NewHTTPError(http.StatusInternalServerError, "Server error"),
			expectLogged: true,
		},
		{
			name:         "generic_error_logged",
			error:        fmt.Errorf("unexpected error"),
			expectLogged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loggedErrors = []string{} // Reset logs

			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			customErrorHandler(tt.error, c, &config.Config{
				App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
			})

			if tt.expectLogged {
				assert.NotEmpty(t, loggedErrors, "Expected error to be logged")
			} else {
				assert.Empty(t, loggedErrors, "Expected error not to be logged")
			}
		})
	}
}

// testWriter captures log output for testing
type testWriter struct {
	logs *[]string
}

func (w *testWriter) Write(p []byte) (n int, err error) {
	*w.logs = append(*w.logs, string(p))
	return len(p), nil
}

func TestErrorChaining(t *testing.T) {
	// Test that errors can be properly chained and wrapped
	originalErr := fmt.Errorf("database connection failed")
	wrappedErr := fmt.Errorf("failed to create user: %w", originalErr)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	customErrorHandler(wrappedErr, c, &config.Config{
		App: config.AppConfig{Env: config.EnvDevelopment, Debug: true},
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var response APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	require.NotNil(t, response.Error)
	assert.Equal(t, "INTERNAL_ERROR", response.Error.Code)

	// In development, the error details should include the wrapped error
	if response.Error.Details != nil {
		errorDetail, exists := response.Error.Details["error"]
		if exists {
			assert.Contains(t, errorDetail, "failed to create user")
		}
	}
}
