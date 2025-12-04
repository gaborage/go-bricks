package httpclient

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test constants to avoid string duplication
const (
	testConnectionFailed = "connection failed"
)

// TestErrorTypeFormatting tests the Error() method behavior per error type
func TestErrorTypeFormatting(t *testing.T) {
	tests := []struct {
		name     string
		error    ClientError
		contains []string // Strings that should be present in the error message
	}{
		{
			name:     "network error without wrapped error",
			error:    NewNetworkError(testConnectionFailed, nil),
			contains: []string{"network error", testConnectionFailed},
		},
		{
			name:     "network error with wrapped error",
			error:    NewNetworkError(testConnectionFailed, errors.New("underlying issue")),
			contains: []string{"network error", testConnectionFailed, "underlying issue"},
		},
		{
			name:     "timeout error",
			error:    NewTimeoutError("request timeout", 30*time.Second),
			contains: []string{"timeout error", "request timeout", "30s"},
		},
		{
			name:     "http error",
			error:    NewHTTPError("bad request", 400, []byte("invalid input")),
			contains: []string{"HTTP error", "bad request", "400"},
		},
		{
			name:     "validation error with field",
			error:    NewValidationError("invalid email format", "email"),
			contains: []string{"validation error", "invalid email format", "email"},
		},
		{
			name:     "validation error without field",
			error:    NewValidationError("invalid request", ""),
			contains: []string{"validation error", "invalid request"},
		},
		{
			name:     "interceptor error",
			error:    NewInterceptorError("processing failed", "request", errors.New("parsing error")),
			contains: []string{"interceptor error", "processing failed", "request", "parsing error"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorMsg := tt.error.Error()
			for _, expected := range tt.contains {
				assert.Contains(t, errorMsg, expected, "Error message should contain: %s", expected)
			}
		})
	}
}

// TestErrorTypeIdentification tests the Type() method for each error type
func TestErrorTypeIdentification(t *testing.T) {
	tests := []struct {
		name     string
		error    ClientError
		expected ErrorType
	}{
		{
			name:     "network error type",
			error:    NewNetworkError("test", nil),
			expected: NetworkError,
		},
		{
			name:     "timeout error type",
			error:    NewTimeoutError("test", time.Second),
			expected: TimeoutError,
		},
		{
			name:     "http error type",
			error:    NewHTTPError("test", 500, nil),
			expected: HTTPError,
		},
		{
			name:     "validation error type",
			error:    NewValidationError("test", "field"),
			expected: ValidationError,
		},
		{
			name:     "interceptor error type",
			error:    NewInterceptorError("test", "stage", nil),
			expected: InterceptorError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.error.Type())
		})
	}
}

// TestErrorUnwrapping tests Unwrap() implementations and error chaining
func TestErrorUnwrapping(t *testing.T) {
	t.Run("network error unwrapping", func(t *testing.T) {
		underlyingErr := errors.New("connection refused")
		netErr := NewNetworkError("failed to connect", underlyingErr)

		// Test direct unwrapping
		if unwrapper, ok := netErr.(interface{ Unwrap() error }); ok {
			assert.Equal(t, underlyingErr, unwrapper.Unwrap())
		} else {
			t.Fatal("networkError should implement Unwrap()")
		}

		// Test errors.Is functionality
		assert.True(t, errors.Is(netErr, underlyingErr))

		// Test errors.As functionality
		var target *networkError
		assert.True(t, errors.As(netErr, &target))
		assert.Equal(t, "failed to connect", target.message)
	})

	t.Run("network error without wrapped error", func(t *testing.T) {
		netErr := NewNetworkError("no connection", nil)

		if unwrapper, ok := netErr.(interface{ Unwrap() error }); ok {
			assert.Nil(t, unwrapper.Unwrap())
		}
	})

	t.Run("interceptor error unwrapping", func(t *testing.T) {
		underlyingErr := errors.New("parsing failed")
		intErr := NewInterceptorError("interceptor failed", "request", underlyingErr)

		// Test direct unwrapping
		if unwrapper, ok := intErr.(interface{ Unwrap() error }); ok {
			assert.Equal(t, underlyingErr, unwrapper.Unwrap())
		} else {
			t.Fatal("interceptorError should implement Unwrap()")
		}

		// Test errors.Is functionality
		assert.True(t, errors.Is(intErr, underlyingErr))

		// Test errors.As functionality
		var target *interceptorError
		assert.True(t, errors.As(intErr, &target))
		assert.Equal(t, "interceptor failed", target.message)
		assert.Equal(t, "request", target.stage)
	})

	t.Run("interceptor error without wrapped error", func(t *testing.T) {
		intErr := NewInterceptorError("failed", "response", nil)

		if unwrapper, ok := intErr.(interface{ Unwrap() error }); ok {
			assert.Nil(t, unwrapper.Unwrap())
		}
	})
}

// TestHTTPErrorBodyAccess tests the Body() method of httpError
func TestHTTPErrorBodyAccess(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "empty body",
			body: []byte{},
		},
		{
			name: "nil body",
			body: nil,
		},
		{
			name: "json body",
			body: []byte(`{"error": "invalid request"}`),
		},
		{
			name: "text body",
			body: []byte("Something went wrong"),
		},
		{
			name: "binary body",
			body: []byte{0x89, 0x50, 0x4E, 0x47}, // PNG header
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpErr := NewHTTPError("test error", 500, tt.body)

			// Test Body() method
			if bodyAccessor, ok := httpErr.(interface{ Body() []byte }); ok {
				returnedBody := bodyAccessor.Body()
				assert.Equal(t, tt.body, returnedBody)
			} else {
				t.Fatal("httpError should implement Body() method")
			}

			// Test StatusCode() method while we're here
			if statusAccessor, ok := httpErr.(interface{ StatusCode() int }); ok {
				assert.Equal(t, 500, statusAccessor.StatusCode())
			} else {
				t.Fatal("httpError should implement StatusCode() method")
			}
		})
	}
}

// TestErrorTypeUtilities tests the utility functions for error type checking
func TestErrorTypeUtilities(t *testing.T) {
	t.Run("IsErrorType function", func(t *testing.T) {
		tests := []struct {
			name      string
			error     error
			errorType ErrorType
			expected  bool
		}{
			{
				name:      "nil error",
				error:     nil,
				errorType: NetworkError,
				expected:  false,
			},
			{
				name:      "network error matches",
				error:     NewNetworkError("test", nil),
				errorType: NetworkError,
				expected:  true,
			},
			{
				name:      "network error doesn't match timeout",
				error:     NewNetworkError("test", nil),
				errorType: TimeoutError,
				expected:  false,
			},
			{
				name:      "standard error doesn't match",
				error:     errors.New("standard error"),
				errorType: NetworkError,
				expected:  false,
			},
			{
				name:      "wrapped client error matches",
				error:     errors.New("wrapper: " + NewHTTPError("test", 400, nil).Error()),
				errorType: HTTPError,
				expected:  false, // Because it's not actually wrapped, just string concatenated
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := IsErrorType(tt.error, tt.errorType)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("IsHTTPStatusError function", func(t *testing.T) {
		tests := []struct {
			name       string
			error      error
			statusCode int
			expected   bool
		}{
			{
				name:       "nil error",
				error:      nil,
				statusCode: 404,
				expected:   false,
			},
			{
				name:       "http error with matching status",
				error:      NewHTTPError("not found", 404, nil),
				statusCode: 404,
				expected:   true,
			},
			{
				name:       "http error with different status",
				error:      NewHTTPError("server error", 500, nil),
				statusCode: 404,
				expected:   false,
			},
			{
				name:       "non-http error",
				error:      NewNetworkError(testConnectionFailed, nil),
				statusCode: 404,
				expected:   false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := IsHTTPStatusError(tt.error, tt.statusCode)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("IsSuccessStatus function", func(t *testing.T) {
		tests := []struct {
			statusCode int
			expected   bool
		}{
			{199, false}, // Below 2xx range
			{200, true},  // Start of 2xx range
			{204, true},  // Within 2xx range
			{299, true},  // End of 2xx range
			{300, false}, // Above 2xx range
			{404, false}, // Well above 2xx range
			{500, false}, // Server error range
		}

		for _, tt := range tests {
			t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
				result := IsSuccessStatus(tt.statusCode)
				assert.Equal(t, tt.expected, result, "Status %d success check failed", tt.statusCode)
			})
		}
	})
}

// TestErrorChaining tests complex error chaining scenarios
func TestErrorChaining(t *testing.T) {
	t.Run("nested error unwrapping", func(t *testing.T) {
		// Create a chain: interceptor -> network -> underlying
		underlying := errors.New("socket closed")
		network := NewNetworkError("connection lost", underlying)
		interceptor := NewInterceptorError("request processing failed", "pre-request", network)

		// Test that we can find the underlying error through the chain
		assert.True(t, errors.Is(interceptor, underlying))
		assert.True(t, errors.Is(interceptor, network))

		// Test that we can extract specific error types from the chain
		var netErr *networkError
		assert.True(t, errors.As(interceptor, &netErr))
		assert.Equal(t, "connection lost", netErr.message)

		var intErr *interceptorError
		assert.True(t, errors.As(interceptor, &intErr))
		assert.Equal(t, "pre-request", intErr.stage)
	})

	t.Run("error type checking with wrapped errors", func(t *testing.T) {
		underlying := errors.New("root cause")
		network := NewNetworkError("network issue", underlying)

		// Should identify as network error even with wrapped content
		assert.True(t, IsErrorType(network, NetworkError))
		assert.False(t, IsErrorType(network, TimeoutError))
	})
}
