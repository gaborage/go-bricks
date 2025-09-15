package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestTiming(t *testing.T) {
	e := echo.New()
	e.Use(Timing())

	tests := []struct {
		name         string
		handlerDelay time.Duration
		expectHeader bool
	}{
		{
			name:         "fast_handler",
			handlerDelay: 1 * time.Millisecond,
			expectHeader: true,
		},
		{
			name:         "slow_handler",
			handlerDelay: 100 * time.Millisecond,
			expectHeader: true,
		},
		{
			name:         "instant_handler",
			handlerDelay: 0,
			expectHeader: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.GET("/test", func(c echo.Context) error {
				if tt.handlerDelay > 0 {
					time.Sleep(tt.handlerDelay)
				}
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()

			start := time.Now()
			e.ServeHTTP(rec, req)
			actualDuration := time.Since(start)

			// Verify response time header is present
			responseTimeHeader := rec.Header().Get("X-Response-Time")
			if tt.expectHeader {
				assert.NotEmpty(t, responseTimeHeader, "X-Response-Time header should be present")

				// Parse the duration from header
				headerDuration, err := time.ParseDuration(responseTimeHeader)
				assert.NoError(t, err, "X-Response-Time should be a valid duration")

				// The header duration should be reasonable (within actual request time)
				assert.True(t, headerDuration <= actualDuration,
					"Header duration (%v) should not exceed actual duration (%v)",
					headerDuration, actualDuration)

				// For slow handlers, verify the timing is approximately correct
				if tt.handlerDelay >= 50*time.Millisecond {
					assert.True(t, headerDuration >= tt.handlerDelay-10*time.Millisecond,
						"Header duration (%v) should be close to handler delay (%v)",
						headerDuration, tt.handlerDelay)
				}
			} else {
				assert.Empty(t, responseTimeHeader, "X-Response-Time header should not be present")
			}

			// Verify response is successful
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestTimingErrorHandler(t *testing.T) {
	e := echo.New()
	e.Use(Timing())

	// Handler that returns an error
	e.GET("/error", func(_ echo.Context) error {
		time.Sleep(50 * time.Millisecond)
		return echo.NewHTTPError(http.StatusBadRequest, "test error")
	})

	req := httptest.NewRequest(http.MethodGet, "/error", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify timing header is still added even when handler returns error
	responseTimeHeader := rec.Header().Get("X-Response-Time")
	assert.NotEmpty(t, responseTimeHeader, "X-Response-Time header should be present even on error")

	// Parse and verify duration
	headerDuration, err := time.ParseDuration(responseTimeHeader)
	assert.NoError(t, err, "X-Response-Time should be a valid duration")
	assert.True(t, headerDuration >= 40*time.Millisecond,
		"Duration should reflect the actual processing time including the sleep")

	// Verify error response
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTimingPanicHandler(t *testing.T) {
	e := echo.New()

	e.Use(Timing())

	// Add recovery middleware AFTER timing to handle panics gracefully
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			defer func() {
				if r := recover(); r != nil {
					// Convert panic to HTTP error
					c.JSON(http.StatusInternalServerError, map[string]string{"error": "panic occurred"})
				}
			}()
			return next(c)
		}
	})

	// Handler that panics
	e.GET("/panic", func(_ echo.Context) error {
		time.Sleep(30 * time.Millisecond)
		panic("test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", http.NoBody)
	rec := httptest.NewRecorder()

	// This should not crash
	e.ServeHTTP(rec, req)

	// Verify timing header is present (timing middleware should execute and add header)
	responseTimeHeader := rec.Header().Get("X-Response-Time")
	assert.NotEmpty(t, responseTimeHeader, "X-Response-Time header should be present even when panic occurs")

	// Parse duration
	headerDuration, err := time.ParseDuration(responseTimeHeader)
	assert.NoError(t, err, "X-Response-Time should be a valid duration")
	assert.True(t, headerDuration >= 20*time.Millisecond,
		"Duration should reflect processing time before panic")
}

func TestTimingConcurrentRequests(t *testing.T) {
	e := echo.New()
	e.Use(Timing())

	e.GET("/test", func(c echo.Context) error {
		// Simulate different processing times based on query parameter
		delayStr := c.QueryParam("delay")
		if delayStr != "" {
			if delayMs, err := strconv.Atoi(delayStr); err == nil {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test concurrent requests with different delays
	delays := []int{10, 50, 100, 20, 5}

	type result struct {
		delay    int
		duration time.Duration
	}

	results := make(chan result, len(delays))

	// Launch concurrent requests
	for _, delay := range delays {
		go func(d int) {
			req := httptest.NewRequest(http.MethodGet, "/test?delay="+strconv.Itoa(d), http.NoBody)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			// Parse timing header
			responseTimeHeader := rec.Header().Get("X-Response-Time")
			duration, err := time.ParseDuration(responseTimeHeader)

			assert.NoError(t, err, "Should have valid duration for delay %d", d)
			assert.Equal(t, http.StatusOK, rec.Code, "Should have successful response for delay %d", d)

			results <- result{delay: d, duration: duration}
		}(delay)
	}

	// Collect results
	for i := 0; i < len(delays); i++ {
		res := <-results

		// Verify timing is approximately correct (within tolerance)
		expectedDuration := time.Duration(res.delay) * time.Millisecond
		tolerance := 20 * time.Millisecond

		assert.True(t, res.duration >= expectedDuration-tolerance,
			"Duration (%v) should be at least expected delay (%v) minus tolerance",
			res.duration, expectedDuration)

		assert.True(t, res.duration <= expectedDuration+tolerance+50*time.Millisecond,
			"Duration (%v) should not exceed expected delay (%v) plus reasonable overhead",
			res.duration, expectedDuration)
	}
}

func TestTimingHeaderFormat(t *testing.T) {
	e := echo.New()
	e.Use(Timing())

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	responseTimeHeader := rec.Header().Get("X-Response-Time")
	assert.NotEmpty(t, responseTimeHeader)

	// Verify the header format is a valid Go duration string
	_, err := time.ParseDuration(responseTimeHeader)
	assert.NoError(t, err, "X-Response-Time should be in valid Go duration format")

	// Verify it contains common duration suffixes
	validSuffixes := []string{"ns", "µs", "ms", "s"}
	hasValidSuffix := false
	for _, suffix := range validSuffixes {
		if strings.HasSuffix(responseTimeHeader, suffix) {
			hasValidSuffix = true
			break
		}
	}
	assert.True(t, hasValidSuffix,
		"X-Response-Time (%s) should end with a valid duration suffix", responseTimeHeader)
}

func TestTimingWithOtherMiddleware(t *testing.T) {
	e := echo.New()

	// Add timing with other middleware to test interaction
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("X-Custom-Header", "test")
			return next(c)
		}
	})

	e.Use(Timing())

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			time.Sleep(25 * time.Millisecond) // Add processing time in middleware
			return next(c)
		}
	})

	e.GET("/test", func(c echo.Context) error {
		time.Sleep(25 * time.Millisecond) // Add processing time in handler
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify both custom header and timing header are present
	assert.Equal(t, "test", rec.Header().Get("X-Custom-Header"))

	responseTimeHeader := rec.Header().Get("X-Response-Time")
	assert.NotEmpty(t, responseTimeHeader)

	// Verify timing includes all middleware processing
	duration, err := time.ParseDuration(responseTimeHeader)
	assert.NoError(t, err)

	// Should be at least 40ms (25ms middleware + 25ms handler) minus some tolerance
	assert.True(t, duration >= 35*time.Millisecond,
		"Duration (%v) should include all middleware and handler processing time", duration)
}
